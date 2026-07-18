package certificates

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/harbor/internal/trust/localca"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

const (
	defaultRenewalWindow = 7 * 24 * time.Hour
	defaultLeafValidity  = 30 * 24 * time.Hour
	minimumRenewalWindow = time.Minute
)

var (
	// ErrNotOpen reports use of a zero-value or otherwise unopened Manager.
	ErrNotOpen = errors.New("certificate manager is not open")
	// ErrCertificateNotReady reports that no ready certificate is published for an exact host.
	ErrCertificateNotReady = errors.New("certificate is not ready")
)

// MaterialStore is the durable boundary required by Manager.
type MaterialStore interface {
	LoadAuthority(context.Context, localca.Config) (*localca.Authority, error)
	CreateAuthority(context.Context, *localca.Authority) error
	LoadLeaf(context.Context, *localca.Authority, []string) (localca.Leaf, error)
	PutLeaf(context.Context, *localca.Authority, localca.Leaf) error
}

// Config controls certificate policy and proactive leaf renewal.
type Config struct {
	// Authority controls root and leaf issuance through localca.
	Authority localca.Config
	// RenewalWindow renews a valid leaf when this much lifetime or less remains.
	// Zero selects Harbor's seven-day default.
	RenewalWindow time.Duration
}

// LeafDisposition describes how EnsureLeaf reached a ready certificate.
type LeafDisposition string

const (
	// LeafCreated means no persisted certificate existed and a new one was published.
	LeafCreated LeafDisposition = "created"
	// LeafReused means the persisted certificate remains outside its renewal window.
	LeafReused LeafDisposition = "reused"
	// LeafRenewed means a valid persisted certificate was proactively replaced.
	LeafRenewed LeafDisposition = "renewed"
	// LeafRepaired means corrupt or no-longer-valid leaf material was replaced.
	LeafRepaired LeafDisposition = "repaired"
)

// LeafResult reports the exact public identity made ready by EnsureLeaf.
type LeafResult struct {
	// Disposition describes whether the leaf was created, reused, renewed, or repaired.
	Disposition LeafDisposition
	// Host is the canonical exact DNS name carried by the leaf.
	Host string
	// Fingerprint is the lowercase SHA-256 digest of the leaf certificate.
	Fingerprint string
	// NotAfter is the leaf certificate's UTC expiration time.
	NotAfter time.Time
}

// Root is the public-only representation passed to platform trust installation.
type Root struct {
	// CertificatePEM contains one CA certificate and no private key.
	CertificatePEM []byte
	// Fingerprint is the lowercase SHA-256 digest of the CA certificate.
	Fingerprint string
	// NotBefore is the CA certificate's UTC activation time.
	NotBefore time.Time
	// NotAfter is the CA certificate's UTC expiration time.
	NotAfter time.Time
}

// Manager serializes certificate mutations and serves immutable ready snapshots.
type Manager struct {
	store         MaterialStore
	authority     *localca.Authority
	root          Root
	now           func() time.Time
	renewalWindow time.Duration

	mutations sync.Mutex
	state     atomic.Uint32
	ready     atomic.Pointer[readySnapshot]
}

const managerStateOpen uint32 = 1

// readyCertificate retains one manager-owned TLS pair and its public lifecycle metadata.
type readyCertificate struct {
	certificate *tls.Certificate
	notBefore   time.Time
	notAfter    time.Time
}

// readySnapshot is replaced as a whole so TLS handshakes never observe partial rotation.
type readySnapshot struct {
	certificates map[string]*readyCertificate
}

// Open reloads an existing root without generating or replacing trust material.
func Open(ctx context.Context, store MaterialStore, config Config) (*Manager, error) {
	ctx = normalizeContext(ctx)
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if isNilStore(store) {
		return nil, fmt.Errorf("open certificate manager: material store is required")
	}
	authority, err := store.LoadAuthority(ctx, normalized.Authority)
	if err != nil {
		return nil, fmt.Errorf("open certificate manager: %w", err)
	}
	if authority == nil {
		return nil, fmt.Errorf("open certificate manager: material store returned no authority")
	}
	return newManager(store, authority, normalized), nil
}

// Bootstrap opens an existing root or creates one only when persistence is truly uninitialized.
func Bootstrap(ctx context.Context, store MaterialStore, config Config) (*Manager, error) {
	ctx = normalizeContext(ctx)
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	if isNilStore(store) {
		return nil, fmt.Errorf("bootstrap certificate manager: material store is required")
	}
	authority, err := store.LoadAuthority(ctx, normalized.Authority)
	if err == nil {
		if authority == nil {
			return nil, fmt.Errorf("bootstrap certificate manager: material store returned no authority")
		}
		return newManager(store, authority, normalized), nil
	}
	if !errors.Is(err, materialstore.ErrAuthorityNotInitialized) {
		return nil, fmt.Errorf("bootstrap certificate manager: load authority: %w", err)
	}
	authority, err = localca.New(normalized.Authority)
	if err != nil {
		return nil, fmt.Errorf("bootstrap certificate manager: %w", err)
	}
	if err := store.CreateAuthority(ctx, authority); err != nil {
		if !errors.Is(err, materialstore.ErrAuthorityAlreadyInitialized) {
			return nil, fmt.Errorf("bootstrap certificate manager: %w", err)
		}
	}
	// Reloading makes persisted publication, including a concurrent winner, the only ready identity.
	authority, err = store.LoadAuthority(ctx, normalized.Authority)
	if err != nil {
		return nil, fmt.Errorf("bootstrap certificate manager: reload authority: %w", err)
	}
	if authority == nil {
		return nil, fmt.Errorf("bootstrap certificate manager: material store returned no authority after creation")
	}
	return newManager(store, authority, normalized), nil
}

// EnsureLeaf makes one exact canonical host ready before ingress accepts its handshakes.
func (manager *Manager) EnsureLeaf(ctx context.Context, host string) (LeafResult, error) {
	ctx = normalizeContext(ctx)
	if err := manager.validateReady(); err != nil {
		return LeafResult{}, err
	}
	canonical, err := canonicalHost(host)
	if err != nil {
		return LeafResult{}, fmt.Errorf("ensure local certificate: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return LeafResult{}, err
	}

	manager.mutations.Lock()
	defer manager.mutations.Unlock()
	if err := ctx.Err(); err != nil {
		return LeafResult{}, err
	}

	leaf, loadErr := manager.store.LoadLeaf(ctx, manager.authority, []string{canonical})
	if loadErr == nil {
		leaf, err = manager.validateLeaf(leaf, canonical)
		if err != nil {
			loadErr = &materialstore.CorruptionError{Component: "leaf material", Cause: err}
		} else {
			if err := manager.publish(canonical, leaf); err != nil {
				return LeafResult{}, err
			}
			now, err := currentTime(manager.now)
			if err != nil {
				return LeafResult{}, fmt.Errorf("ensure local certificate: %w", err)
			}
			if leaf.Material.NotAfter.Sub(now) > manager.renewalWindow {
				return resultFor(LeafReused, canonical, leaf), nil
			}
			return manager.issueAndPublish(ctx, canonical, LeafRenewed)
		}
	}

	disposition := LeafCreated
	if errors.Is(loadErr, materialstore.ErrLeafNotFound) {
		disposition = LeafCreated
	} else {
		if !isLeafCorruption(loadErr) {
			return LeafResult{}, fmt.Errorf("ensure local certificate: load %q: %w", canonical, loadErr)
		}
		disposition = LeafRepaired
	}
	return manager.issueAndPublish(ctx, canonical, disposition)
}

// Certificate returns the current in-memory certificate for one exact ready host.
func (manager *Manager) Certificate(ctx context.Context, host string) (*tls.Certificate, error) {
	ctx = normalizeContext(ctx)
	if err := manager.validateReady(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := manager.ready.Load()
	certificate, found := snapshot.certificates[host]
	canonical := host
	if !found {
		var err error
		canonical, err = canonicalHost(host)
		if err != nil {
			return nil, fmt.Errorf("serve local certificate: %w", err)
		}
		certificate, found = snapshot.certificates[canonical]
	}
	if !found {
		return nil, fmt.Errorf("%w for %q", ErrCertificateNotReady, canonical)
	}
	now, err := currentTime(manager.now)
	if err != nil {
		return nil, fmt.Errorf("serve local certificate: %w", err)
	}
	if now.Before(certificate.notBefore) || !now.Before(certificate.notAfter) {
		return nil, fmt.Errorf("%w for %q: certificate is outside its validity window", ErrCertificateNotReady, canonical)
	}
	return certificate.certificate, nil
}

// PublicRoot returns a defensive public-only copy of the active trust identity.
func (manager *Manager) PublicRoot() (Root, error) {
	if err := manager.validateReady(); err != nil {
		return Root{}, err
	}
	root := manager.root
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	return root, nil
}

// issueAndPublish creates and persists a leaf before making it visible to handshakes.
func (manager *Manager) issueAndPublish(ctx context.Context, host string, disposition LeafDisposition) (LeafResult, error) {
	leaf, err := manager.authority.Issue(ctx, []string{host})
	if err != nil {
		return LeafResult{}, fmt.Errorf("ensure local certificate: issue %q: %w", host, err)
	}
	leaf, err = manager.validateLeaf(leaf, host)
	if err != nil {
		return LeafResult{}, fmt.Errorf("ensure local certificate: validate issued %q: %w", host, err)
	}
	if err := ctx.Err(); err != nil {
		return LeafResult{}, err
	}
	if err := manager.store.PutLeaf(ctx, manager.authority, leaf); err != nil {
		return LeafResult{}, fmt.Errorf("ensure local certificate: persist %q: %w", host, err)
	}
	if err := manager.publish(host, leaf); err != nil {
		return LeafResult{}, err
	}
	return resultFor(disposition, host, leaf), nil
}

// validateLeaf proves an interface implementation returned material owned by this authority and exact host.
func (manager *Manager) validateLeaf(leaf localca.Leaf, host string) (localca.Leaf, error) {
	validated, err := manager.authority.LoadLeaf(leaf.Material.CertificatePEM, leaf.Material.PrivateKeyPEM, []string{host})
	if err != nil {
		return localca.Leaf{}, err
	}
	if validated.Material.Fingerprint != leaf.Material.Fingerprint {
		return localca.Leaf{}, fmt.Errorf("certificate fingerprint does not match validated material")
	}
	return validated, nil
}

// publish atomically adds or replaces one manager-owned ready certificate.
func (manager *Manager) publish(host string, leaf localca.Leaf) error {
	if len(leaf.Hosts) != 1 || leaf.Hosts[0] != host {
		return fmt.Errorf("publish local certificate: leaf must contain only exact host %q", host)
	}
	certificate := leaf.TLSCertificate
	certificate.Certificate = cloneCertificateChain(certificate.Certificate)
	certificate.OCSPStaple = append([]byte(nil), certificate.OCSPStaple...)
	certificate.SignedCertificateTimestamps = cloneCertificateChain(certificate.SignedCertificateTimestamps)
	certificate.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), certificate.SupportedSignatureAlgorithms...)

	current := manager.ready.Load()
	next := &readySnapshot{certificates: make(map[string]*readyCertificate, len(current.certificates)+1)}
	for existingHost, existing := range current.certificates {
		next.certificates[existingHost] = existing
	}
	next.certificates[host] = &readyCertificate{
		certificate: &certificate,
		notBefore:   leaf.Material.NotBefore,
		notAfter:    leaf.Material.NotAfter,
	}
	manager.ready.Store(next)
	return nil
}

// newManager owns the public root view and publishes an initially empty ready set.
func newManager(store MaterialStore, authority *localca.Authority, config Config) *Manager {
	material := authority.Material()
	manager := &Manager{
		store:     store,
		authority: authority,
		root: Root{
			CertificatePEM: append([]byte(nil), material.CertificatePEM...),
			Fingerprint:    material.Fingerprint,
			NotBefore:      material.NotBefore,
			NotAfter:       material.NotAfter,
		},
		now:           config.Authority.Now,
		renewalWindow: config.RenewalWindow,
	}
	manager.ready.Store(&readySnapshot{certificates: make(map[string]*readyCertificate)})
	manager.state.Store(managerStateOpen)
	return manager
}

// normalizeConfig gives localca and renewal decisions one shared clock and bounded policy.
func normalizeConfig(config Config) (Config, error) {
	if config.Authority.Now == nil {
		config.Authority.Now = time.Now
	}
	leafValidity := config.Authority.LeafValidity
	if leafValidity == 0 {
		leafValidity = defaultLeafValidity
	}
	if config.RenewalWindow == 0 {
		config.RenewalWindow = defaultRenewalWindow
	}
	if config.RenewalWindow < minimumRenewalWindow || config.RenewalWindow >= leafValidity {
		return Config{}, fmt.Errorf("certificate renewal window must be between %s and less than the leaf validity %s", minimumRenewalWindow, leafValidity)
	}
	return config, nil
}

// canonicalHost enforces the V1 one-host leaf policy through localca's canonical identity contract.
func canonicalHost(host string) (string, error) {
	hosts, err := localca.CanonicalHosts([]string{host})
	if err != nil {
		return "", err
	}
	return hosts[0], nil
}

// currentTime rejects clocks that cannot support fail-closed expiration decisions.
func currentTime(now func() time.Time) (time.Time, error) {
	current := now().UTC().Round(0)
	if current.IsZero() {
		return time.Time{}, fmt.Errorf("certificate clock returned zero time")
	}
	return current, nil
}

// validateReady fails closed for zero-value managers before any dependency access.
func (manager *Manager) validateReady() error {
	if manager == nil || manager.state.Load() != managerStateOpen {
		return ErrNotOpen
	}
	return nil
}

// isNilStore catches typed-nil interface dependencies at the constructor boundary.
func isNilStore(store MaterialStore) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// isLeafCorruption restricts automatic repair to persisted leaf objects, never root identity.
func isLeafCorruption(err error) bool {
	var corruption *materialstore.CorruptionError
	return errors.As(err, &corruption) && corruption != nil && strings.HasPrefix(corruption.Component, "leaf ")
}

// cloneCertificateChain gives each published snapshot ownership of its encoded certificate slices.
func cloneCertificateChain(chain [][]byte) [][]byte {
	cloned := make([][]byte, len(chain))
	for index, encoded := range chain {
		cloned[index] = append([]byte(nil), encoded...)
	}
	return cloned
}

// resultFor exposes only public leaf lifecycle metadata.
func resultFor(disposition LeafDisposition, host string, leaf localca.Leaf) LeafResult {
	return LeafResult{
		Disposition: disposition,
		Host:        host,
		Fingerprint: leaf.Material.Fingerprint,
		NotAfter:    leaf.Material.NotAfter,
	}
}

// normalizeContext gives nil callers the same cancellation-free semantics as other Harbor boundaries.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
