package certificates

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/trust/localca"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

var managerTestTime = time.Date(2032, time.March, 4, 12, 0, 0, 0, time.UTC)

// TestOpenRequiresPersistedValidAuthority verifies startup never creates or replaces trust identity.
func TestOpenRequiresPersistedValidAuthority(t *testing.T) {
	t.Parallel()
	config := testManagerConfig(newTestClock(managerTestTime))
	missing := &stubStore{loadAuthorityError: materialstore.ErrAuthorityNotInitialized}
	if _, err := Open(context.Background(), missing, config); !errors.Is(err, materialstore.ErrAuthorityNotInitialized) {
		t.Fatalf("Open(missing) error = %v", err)
	}
	if missing.createAuthorityCalls.Load() != 0 {
		t.Fatal("Open(missing) attempted authority creation")
	}
	corruption := &materialstore.CorruptionError{Component: "authority generation", Cause: errors.New("bad pair")}
	broken := &stubStore{loadAuthorityError: corruption}
	if _, err := Open(context.Background(), broken, config); !errors.Is(err, corruption) {
		t.Fatalf("Open(corrupt) error = %v", err)
	}
	if broken.createAuthorityCalls.Load() != 0 {
		t.Fatal("Open(corrupt) attempted authority creation")
	}
}

// TestBootstrapCreatesOnceAndRestartRetainsIdentity verifies first-run creation and restart reuse.
func TestBootstrapCreatesOnceAndRestartRetainsIdentity(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	store := openTestMaterialStore(t)

	first, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap(first) error = %v", err)
	}
	firstRoot, err := first.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot(first) error = %v", err)
	}
	created, err := first.EnsureLeaf(context.Background(), "ORDERS.TEST.")
	if err != nil {
		t.Fatalf("EnsureLeaf(created) error = %v", err)
	}
	if created.Disposition != LeafCreated || created.Host != "orders.test" {
		t.Fatalf("EnsureLeaf(created) = %#v", created)
	}

	second, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap(second) error = %v", err)
	}
	secondRoot, err := second.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot(second) error = %v", err)
	}
	if secondRoot.Fingerprint != firstRoot.Fingerprint {
		t.Fatalf("root fingerprint changed across restart: %q != %q", secondRoot.Fingerprint, firstRoot.Fingerprint)
	}
	reused, err := second.EnsureLeaf(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(reused) error = %v", err)
	}
	if reused.Disposition != LeafReused || reused.Fingerprint != created.Fingerprint {
		t.Fatalf("EnsureLeaf(reused) = %#v, created = %#v", reused, created)
	}
	certificate, err := second.Certificate(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("Certificate() error = %v", err)
	}
	if certificate.Leaf == nil || len(certificate.Leaf.DNSNames) != 1 || certificate.Leaf.DNSNames[0] != "orders.test" {
		t.Fatalf("Certificate() names = %#v", certificate.Leaf)
	}
}

// TestBootstrapNeverReplacesCorruptOrExpiredAuthority covers both unsafe recovery classes.
func TestBootstrapNeverReplacesCorruptOrExpiredAuthority(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	corruption := &materialstore.CorruptionError{Component: "authority manifest", Cause: errors.New("invalid")}
	broken := &stubStore{loadAuthorityError: corruption}
	if _, err := Bootstrap(context.Background(), broken, config); !errors.Is(err, corruption) {
		t.Fatalf("Bootstrap(corrupt) error = %v", err)
	}
	if broken.createAuthorityCalls.Load() != 0 {
		t.Fatal("Bootstrap(corrupt) attempted authority replacement")
	}

	store := openTestMaterialStore(t)
	shortConfig := config
	shortConfig.Authority.CAValidity = time.Hour
	shortConfig.Authority.LeafValidity = 30 * time.Minute
	shortConfig.RenewalWindow = 10 * time.Minute
	if _, err := Bootstrap(context.Background(), store, shortConfig); err != nil {
		t.Fatalf("Bootstrap(short root) error = %v", err)
	}
	clock.Set(managerTestTime.Add(2 * time.Hour))
	if _, err := Bootstrap(context.Background(), store, shortConfig); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("Bootstrap(expired root) error = %v", err)
	}
}

// TestBootstrapCoversCreationAndConcurrentPublicationFailures verifies every first-run commit boundary.
func TestBootstrapCoversCreationAndConcurrentPublicationFailures(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	winner, err := localca.New(config.Authority)
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}

	invalid := config
	invalid.RenewalWindow = time.Second
	invalidStore := &sequenceStore{}
	if _, err := Bootstrap(context.Background(), invalidStore, invalid); err == nil || !strings.Contains(err.Error(), "renewal window") {
		t.Fatalf("Bootstrap(invalid config) error = %v", err)
	}
	if invalidStore.loadCalls != 0 {
		t.Fatal("Bootstrap(invalid config) touched persistence")
	}

	nilExisting := &sequenceStore{loads: []authorityLoad{{}}}
	if _, err := Bootstrap(context.Background(), nilExisting, config); err == nil || !strings.Contains(err.Error(), "returned no authority") {
		t.Fatalf("Bootstrap(nil existing authority) error = %v", err)
	}

	zeroClock := config
	zeroClock.Authority.Now = func() time.Time { return time.Time{} }
	generation := &sequenceStore{loads: []authorityLoad{{err: materialstore.ErrAuthorityNotInitialized}}}
	if _, err := Bootstrap(context.Background(), generation, zeroClock); err == nil || !strings.Contains(err.Error(), "zero time") {
		t.Fatalf("Bootstrap(generation failure) error = %v", err)
	}
	if generation.createCalls != 0 {
		t.Fatal("Bootstrap(generation failure) touched persistence")
	}

	createError := errors.New("create failed")
	createFailure := &sequenceStore{
		loads:     []authorityLoad{{err: materialstore.ErrAuthorityNotInitialized}},
		createErr: createError,
	}
	if _, err := Bootstrap(context.Background(), createFailure, config); !errors.Is(err, createError) {
		t.Fatalf("Bootstrap(create failure) error = %v", err)
	}

	concurrentWinner := &sequenceStore{
		loads: []authorityLoad{
			{err: materialstore.ErrAuthorityNotInitialized},
			{authority: winner},
		},
		createErr: materialstore.ErrAuthorityAlreadyInitialized,
	}
	manager, err := Bootstrap(context.Background(), concurrentWinner, config)
	if err != nil {
		t.Fatalf("Bootstrap(concurrent winner) error = %v", err)
	}
	root, err := manager.PublicRoot()
	if err != nil || root.Fingerprint != winner.Material().Fingerprint {
		t.Fatalf("PublicRoot(concurrent winner) = %#v, %v", root, err)
	}

	reloadError := errors.New("reload failed")
	reloadFailure := &sequenceStore{
		loads: []authorityLoad{
			{err: materialstore.ErrAuthorityNotInitialized},
			{err: reloadError},
		},
	}
	if _, err := Bootstrap(context.Background(), reloadFailure, config); !errors.Is(err, reloadError) {
		t.Fatalf("Bootstrap(reload failure) error = %v", err)
	}

	reloadNil := &sequenceStore{
		loads: []authorityLoad{
			{err: materialstore.ErrAuthorityNotInitialized},
			{},
		},
	}
	if _, err := Bootstrap(context.Background(), reloadNil, config); err == nil || !strings.Contains(err.Error(), "after creation") {
		t.Fatalf("Bootstrap(nil reload) error = %v", err)
	}
}

// TestEnsureLeafReportsEveryDisposition exercises create, reuse, proactive renewal, and repair.
func TestEnsureLeafReportsEveryDisposition(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	store := openTestMaterialStore(t)
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	created, err := manager.EnsureLeaf(context.Background(), "billing.test")
	if err != nil || created.Disposition != LeafCreated {
		t.Fatalf("EnsureLeaf(created) = %#v, %v", created, err)
	}
	reused, err := manager.EnsureLeaf(context.Background(), "billing.test")
	if err != nil || reused.Disposition != LeafReused || reused.Fingerprint != created.Fingerprint {
		t.Fatalf("EnsureLeaf(reused) = %#v, %v", reused, err)
	}
	clock.Set(managerTestTime.Add(3*time.Hour + 30*time.Minute))
	renewed, err := manager.EnsureLeaf(context.Background(), "billing.test")
	if err != nil || renewed.Disposition != LeafRenewed || renewed.Fingerprint == created.Fingerprint {
		t.Fatalf("EnsureLeaf(renewed) = %#v, %v", renewed, err)
	}
	clock.Set(managerTestTime.Add(8 * time.Hour))
	repaired, err := manager.EnsureLeaf(context.Background(), "billing.test")
	if err != nil || repaired.Disposition != LeafRepaired || repaired.Fingerprint == renewed.Fingerprint {
		t.Fatalf("EnsureLeaf(repaired) = %#v, %v", repaired, err)
	}
}

// TestFailedRenewalRetainsOldReadyCertificate verifies persistence failure cannot unpublish usable memory state.
func TestFailedRenewalRetainsOldReadyCertificate(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	created, err := manager.EnsureLeaf(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(created) error = %v", err)
	}
	oldCertificate, err := manager.Certificate(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("Certificate(old) error = %v", err)
	}
	oldFingerprint := tlsFingerprint(oldCertificate)

	clock.Set(managerTestTime.Add(3*time.Hour + 30*time.Minute))
	persistError := errors.New("disk unavailable")
	store.SetPutLeafError(persistError)
	if _, err := manager.EnsureLeaf(context.Background(), "orders.test"); !errors.Is(err, persistError) {
		t.Fatalf("EnsureLeaf(failed renewal) error = %v", err)
	}
	retained, err := manager.Certificate(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("Certificate(retained) error = %v", err)
	}
	if got := tlsFingerprint(retained); got != oldFingerprint || got != created.Fingerprint {
		t.Fatalf("retained fingerprint = %q, want %q", got, oldFingerprint)
	}
}

// TestCorruptLeafRepairRetainsOldCertificateUntilPublication verifies repair follows the same atomic boundary.
func TestCorruptLeafRepairRetainsOldCertificateUntilPublication(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	created, err := manager.EnsureLeaf(context.Background(), "repair.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(created) error = %v", err)
	}
	corruption := &materialstore.CorruptionError{Component: "leaf generation", Cause: errors.New("truncated")}
	store.SetLoadLeafError(corruption)
	store.SetPutLeafError(errors.New("repair write failed"))
	if _, err := manager.EnsureLeaf(context.Background(), "repair.test"); err == nil {
		t.Fatal("EnsureLeaf(failed repair) succeeded")
	}
	retained, err := manager.Certificate(context.Background(), "repair.test")
	if err != nil || tlsFingerprint(retained) != created.Fingerprint {
		t.Fatalf("Certificate(retained repair) = %q, %v", tlsFingerprint(retained), err)
	}
	store.SetPutLeafError(nil)
	repaired, err := manager.EnsureLeaf(context.Background(), "repair.test")
	if err != nil || repaired.Disposition != LeafRepaired || repaired.Fingerprint == created.Fingerprint {
		t.Fatalf("EnsureLeaf(repaired) = %#v, %v", repaired, err)
	}
}

// TestCertificateProviderIsReadyOnly verifies steady-state lookup performs no persistence or issuance work.
func TestCertificateProviderIsReadyOnly(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	entropy := &countingReader{delegate: rand.Reader}
	config := testManagerConfig(clock)
	config.Authority.Random = entropy
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := manager.EnsureLeaf(context.Background(), "ready.test"); err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	storeCalls := store.Calls()
	entropyReads := entropy.reads.Load()
	first, err := manager.Certificate(context.Background(), "ready.test")
	if err != nil {
		t.Fatalf("Certificate(first) error = %v", err)
	}
	for index := 0; index < 1000; index++ {
		certificate, err := manager.Certificate(context.Background(), "ready.test")
		if err != nil {
			t.Fatalf("Certificate(%d) error = %v", index, err)
		}
		if certificate != first {
			t.Fatal("Certificate() did not return the immutable published snapshot entry")
		}
	}
	if store.Calls() != storeCalls {
		t.Fatalf("Certificate() store calls = %d, want unchanged %d", store.Calls(), storeCalls)
	}
	if entropy.reads.Load() != entropyReads {
		t.Fatalf("Certificate() entropy reads = %d, want unchanged %d", entropy.reads.Load(), entropyReads)
	}
}

// TestCertificateProviderSteadyStateAllocatesNothing protects the TLS handshake fast path from reconciliation work.
func TestCertificateProviderSteadyStateAllocatesNothing(t *testing.T) {
	clock := newTestClock(managerTestTime)
	manager, err := Bootstrap(context.Background(), openTestMaterialStore(t), testManagerConfig(clock))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := manager.EnsureLeaf(context.Background(), "allocations.test"); err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	ctx := context.Background()
	var providerError error
	allocations := testing.AllocsPerRun(1000, func() {
		_, providerError = manager.Certificate(ctx, "allocations.test")
	})
	if providerError != nil {
		t.Fatalf("Certificate() error = %v", providerError)
	}
	if allocations != 0 {
		t.Fatalf("Certificate() allocations = %f, want 0", allocations)
	}
}

// TestConcurrentEnsureAndCertificatePublishesWholeSnapshots verifies mutation serialization and lock-free rotation.
func TestConcurrentEnsureAndCertificatePublishesWholeSnapshots(t *testing.T) {
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base, delay: 2 * time.Millisecond}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	created, err := manager.EnsureLeaf(context.Background(), "race.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(created) error = %v", err)
	}
	clock.Set(managerTestTime.Add(3*time.Hour + 30*time.Minute))

	var readers sync.WaitGroup
	errorsChannel := make(chan error, 64)
	for worker := 0; worker < 32; worker++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for iteration := 0; iteration < 200; iteration++ {
				certificate, err := manager.Certificate(context.Background(), "race.test")
				if err != nil {
					errorsChannel <- err
					return
				}
				if len(certificate.Certificate) != 1 || certificate.Leaf == nil {
					errorsChannel <- fmt.Errorf("observed partial certificate")
					return
				}
			}
		}()
	}
	renewed, err := manager.EnsureLeaf(context.Background(), "race.test")
	if err != nil || renewed.Disposition != LeafRenewed || renewed.Fingerprint == created.Fingerprint {
		t.Fatalf("EnsureLeaf(renewed) = %#v, %v", renewed, err)
	}
	readers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	if store.MaximumActive() != 1 {
		t.Fatalf("maximum concurrent store mutations = %d, want 1", store.MaximumActive())
	}
}

// TestManagerRejectsUnopenedUnknownMalformedAndExpiredLookups covers fail-closed provider boundaries.
func TestManagerRejectsUnopenedUnknownMalformedAndExpiredLookups(t *testing.T) {
	t.Parallel()
	var zero Manager
	if _, err := zero.Certificate(context.Background(), "orders.test"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("zero Certificate() error = %v", err)
	}
	if _, err := zero.EnsureLeaf(context.Background(), "orders.test"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("zero EnsureLeaf() error = %v", err)
	}
	if _, err := zero.PublicRoot(); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("zero PublicRoot() error = %v", err)
	}

	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	manager, err := Bootstrap(context.Background(), openTestMaterialStore(t), config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := manager.Certificate(context.Background(), "unknown.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(unknown) error = %v", err)
	}
	for _, host := range []string{"", "orders.example", "*.orders.test", " orders.test"} {
		if _, err := manager.Certificate(context.Background(), host); err == nil {
			t.Fatalf("Certificate(%q) succeeded", host)
		}
	}
	created, err := manager.EnsureLeaf(context.Background(), "orders.test")
	if err != nil {
		t.Fatalf("EnsureLeaf() error = %v", err)
	}
	certificate, err := manager.Certificate(context.Background(), "ORDERS.TEST.")
	if err != nil || tlsFingerprint(certificate) != created.Fingerprint {
		t.Fatalf("Certificate(canonical equivalent) = %q, %v", tlsFingerprint(certificate), err)
	}
	clock.Set(created.NotAfter)
	if _, err := manager.Certificate(context.Background(), "orders.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(expired) error = %v", err)
	}
}

// TestPublicRootNeverExposesPrivateMaterial verifies the trust-helper boundary is public-only and defensive.
func TestPublicRootNeverExposesPrivateMaterial(t *testing.T) {
	t.Parallel()
	manager, err := Bootstrap(context.Background(), openTestMaterialStore(t), testManagerConfig(newTestClock(managerTestTime)))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	root, err := manager.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot() error = %v", err)
	}
	if strings.Contains(string(root.CertificatePEM), "PRIVATE KEY") || !strings.Contains(string(root.CertificatePEM), "BEGIN CERTIFICATE") {
		t.Fatalf("PublicRoot() PEM is not public certificate material")
	}
	original := append([]byte(nil), root.CertificatePEM...)
	root.CertificatePEM[0] ^= 0xff
	fresh, err := manager.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot(fresh) error = %v", err)
	}
	if string(fresh.CertificatePEM) != string(original) {
		t.Fatal("PublicRoot() exposed manager-owned bytes")
	}
}

// TestConfigAndDependencyValidationCoversFailureBranches verifies policy errors fail before persistence access.
func TestConfigAndDependencyValidationCoversFailureBranches(t *testing.T) {
	t.Parallel()
	var typedNil *stubStore
	if _, err := Open(context.Background(), typedNil, testManagerConfig(newTestClock(managerTestTime))); err == nil || !strings.Contains(err.Error(), "store is required") {
		t.Fatalf("Open(typed nil) error = %v", err)
	}
	if _, err := Bootstrap(context.Background(), nil, testManagerConfig(newTestClock(managerTestTime))); err == nil || !strings.Contains(err.Error(), "store is required") {
		t.Fatalf("Bootstrap(nil) error = %v", err)
	}
	tests := []Config{
		{RenewalWindow: time.Second},
		{Authority: localca.Config{LeafValidity: time.Hour}, RenewalWindow: time.Hour},
		{Authority: localca.Config{LeafValidity: time.Hour}, RenewalWindow: 2 * time.Hour},
	}
	for index, config := range tests {
		store := &stubStore{}
		if _, err := Open(context.Background(), store, config); err == nil || !strings.Contains(err.Error(), "renewal window") {
			t.Fatalf("Open(invalid %d) error = %v", index, err)
		}
		if store.loadAuthorityCalls.Load() != 0 {
			t.Fatalf("Open(invalid %d) touched persistence", index)
		}
	}
	noAuthority := &stubStore{}
	if _, err := Open(context.Background(), noAuthority, testManagerConfig(newTestClock(managerTestTime))); err == nil || !strings.Contains(err.Error(), "returned no authority") {
		t.Fatalf("Open(nil authority) error = %v", err)
	}
}

// TestDefaultConfigBootstrapsUsablePolicy verifies zero values select compatible localca and renewal defaults.
func TestDefaultConfigBootstrapsUsablePolicy(t *testing.T) {
	t.Parallel()
	manager, err := Bootstrap(nil, openTestMaterialStore(t), Config{})
	if err != nil {
		t.Fatalf("Bootstrap(defaults) error = %v", err)
	}
	result, err := manager.EnsureLeaf(nil, "defaults.test")
	if err != nil || result.Disposition != LeafCreated {
		t.Fatalf("EnsureLeaf(defaults) = %#v, %v", result, err)
	}
}

// TestCancellationAndEntropyFailuresDoNotPublish verifies aborts remain outside the ready set.
func TestCancellationAndEntropyFailuresDoNotPublish(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	entropy := &switchReader{delegate: rand.Reader}
	config := testManagerConfig(clock)
	config.Authority.Random = entropy
	manager, err := Bootstrap(context.Background(), openTestMaterialStore(t), config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.EnsureLeaf(cancelled, "cancelled.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureLeaf(cancelled) error = %v", err)
	}
	if _, err := manager.Certificate(context.Background(), "cancelled.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(cancelled host) error = %v", err)
	}
	entropy.SetError(errors.New("entropy unavailable"))
	if _, err := manager.EnsureLeaf(context.Background(), "entropy.test"); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("EnsureLeaf(entropy failure) error = %v", err)
	}
	if _, err := manager.Certificate(context.Background(), "entropy.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(entropy host) error = %v", err)
	}
}

// TestEnsureAndProviderFailureBoundariesRejectInvalidStateWithoutPublication covers non-persistence branches.
func TestEnsureAndProviderFailureBoundariesRejectInvalidStateWithoutPublication(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	config := testManagerConfig(clock)
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := manager.EnsureLeaf(context.Background(), "*.bad.test"); err == nil {
		t.Fatal("EnsureLeaf(malformed) succeeded")
	}
	loadError := errors.New("read failed")
	store.SetLoadLeafError(loadError)
	if _, err := manager.EnsureLeaf(context.Background(), "read.test"); !errors.Is(err, loadError) {
		t.Fatalf("EnsureLeaf(load failure) error = %v", err)
	}
	rootCorruption := &materialstore.CorruptionError{Component: "authority generation", Cause: errors.New("invalid root")}
	store.SetLoadLeafError(rootCorruption)
	rootCorruptionCalls := store.Calls()
	if _, err := manager.EnsureLeaf(context.Background(), "root-corruption.test"); !errors.Is(err, rootCorruption) {
		t.Fatalf("EnsureLeaf(root corruption) error = %v", err)
	}
	if store.Calls() != rootCorruptionCalls+1 {
		t.Fatal("EnsureLeaf(root corruption) attempted leaf repair")
	}
	store.SetLoadLeafError(nil)
	store.SetLoadLeafValue(&localca.Leaf{})
	repaired, err := manager.EnsureLeaf(context.Background(), "invalid-return.test")
	if err != nil || repaired.Disposition != LeafRepaired {
		t.Fatalf("EnsureLeaf(invalid returned leaf) = %#v, %v", repaired, err)
	}
	store.SetLoadLeafValue(nil)
	_, err = manager.EnsureLeaf(nil, "clock.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(nil context) error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Certificate(cancelled, "clock.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Certificate(cancelled) error = %v", err)
	}
	manager.now = func() time.Time { return time.Time{} }
	if _, err := manager.EnsureLeaf(context.Background(), "clock.test"); err == nil || !strings.Contains(err.Error(), "zero time") {
		t.Fatalf("EnsureLeaf(zero clock) error = %v", err)
	}
	if _, err := manager.Certificate(context.Background(), "clock.test"); err == nil || !strings.Contains(err.Error(), "zero time") {
		t.Fatalf("Certificate(zero clock) error = %v", err)
	}

	manager.now = clock.Now
	leaf, err := manager.authority.Issue(context.Background(), []string{"direct.test"})
	if err != nil {
		t.Fatalf("Issue(direct) error = %v", err)
	}
	badEncoding := leaf
	badEncoding.Material.CertificatePEM = []byte("invalid")
	if _, err := manager.validateLeaf(badEncoding, "direct.test"); err == nil {
		t.Fatal("validateLeaf(bad encoding) succeeded")
	}
	badFingerprint := leaf
	badFingerprint.Material.Fingerprint = strings.Repeat("0", 64)
	if _, err := manager.validateLeaf(badFingerprint, "direct.test"); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("validateLeaf(bad fingerprint) error = %v", err)
	}
	badHosts := leaf
	badHosts.Hosts = []string{"other.test"}
	if err := manager.publish("direct.test", badHosts); err == nil || !strings.Contains(err.Error(), "only exact host") {
		t.Fatalf("publish(bad hosts) error = %v", err)
	}
	clock.Set(managerTestTime.Add(-30 * time.Minute))
	if _, err := manager.Certificate(context.Background(), "clock.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(not yet valid) error = %v", err)
	}
}

// TestEnsureLeafRechecksCancellationAfterWaitingForMutationOwnership verifies queued work never touches persistence.
func TestEnsureLeafRechecksCancellationAfterWaitingForMutationOwnership(t *testing.T) {
	t.Parallel()
	manager, err := Bootstrap(context.Background(), openTestMaterialStore(t), testManagerConfig(newTestClock(managerTestTime)))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	manager.mutations.Lock()
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedContext{Context: base, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := manager.EnsureLeaf(ctx, "queued.test")
		done <- err
	}()
	select {
	case <-ctx.observed:
	case <-time.After(5 * time.Second):
		manager.mutations.Unlock()
		t.Fatal("EnsureLeaf() did not reach its initial cancellation check")
	}
	cancel()
	manager.mutations.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureLeaf(queued cancellation) error = %v", err)
	}
	if _, err := manager.Certificate(context.Background(), "queued.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(queued host) error = %v", err)
	}
}

// TestCancellationAfterIssuanceDoesNotPersist verifies the context boundary immediately before durable publication.
func TestCancellationAfterIssuanceDoesNotPersist(t *testing.T) {
	t.Parallel()
	clock := newTestClock(managerTestTime)
	entropy := &cancelingReader{delegate: rand.Reader}
	config := testManagerConfig(clock)
	config.Authority.Random = entropy
	base := openTestMaterialStore(t)
	store := &instrumentedStore{delegate: base}
	manager, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entropy.SetCancel(cancel)
	putCalls := store.Calls()
	if _, err := manager.EnsureLeaf(ctx, "cancel-after-issue.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureLeaf(cancel after issue) error = %v", err)
	}
	if store.Calls() != putCalls+1 {
		// LoadLeaf is allowed; PutLeaf must remain outside the cancelled transaction.
		t.Fatalf("persistence calls after cancellation = %d, want %d", store.Calls(), putCalls+1)
	}
	if _, err := manager.Certificate(context.Background(), "cancel-after-issue.test"); !errors.Is(err, ErrCertificateNotReady) {
		t.Fatalf("Certificate(cancelled issue) error = %v", err)
	}
}

// testClock provides one race-safe certificate clock shared by localca and Manager.
type testClock struct {
	mutex sync.RWMutex
	now   time.Time
}

// newTestClock constructs a mutable deterministic clock.
func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

// Now returns the current deterministic instant.
func (clock *testClock) Now() time.Time {
	clock.mutex.RLock()
	defer clock.mutex.RUnlock()
	return clock.now
}

// Set advances or rewinds the deterministic instant for lifecycle tests.
func (clock *testClock) Set(now time.Time) {
	clock.mutex.Lock()
	clock.now = now
	clock.mutex.Unlock()
}

// testManagerConfig gives roots enough lifetime for several leaf rotations.
func testManagerConfig(clock *testClock) Config {
	return Config{
		Authority: localca.Config{
			CAValidity:   48 * time.Hour,
			LeafValidity: 4 * time.Hour,
			Backdate:     time.Minute,
			Now:          clock.Now,
		},
		RenewalWindow: time.Hour,
	}
}

// openTestMaterialStore opens owner-private persistence under the test's temporary directory.
func openTestMaterialStore(t *testing.T) *materialstore.Store {
	t.Helper()
	store, err := materialstore.Open(filepath.Join(t.TempDir(), "certificates"))
	if err != nil {
		t.Fatalf("materialstore.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return store
}

// tlsFingerprint returns the stable public identity of one ready TLS certificate.
func tlsFingerprint(certificate *tls.Certificate) string {
	if certificate == nil || len(certificate.Certificate) == 0 {
		return ""
	}
	return fingerprintDER(certificate.Certificate[0])
}

// stubStore controls authority startup results without touching durable state.
type stubStore struct {
	loadAuthorityError   error
	loadAuthorityCalls   atomic.Int64
	createAuthorityCalls atomic.Int64
}

// authorityLoad is one scripted root load result.
type authorityLoad struct {
	authority *localca.Authority
	err       error
}

// sequenceStore returns deterministic root lifecycle results in call order.
type sequenceStore struct {
	mutex       sync.Mutex
	loads       []authorityLoad
	loadCalls   int
	createCalls int
	createErr   error
}

// LoadAuthority returns the next scripted root result.
func (store *sequenceStore) LoadAuthority(context.Context, localca.Config) (*localca.Authority, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	index := store.loadCalls
	store.loadCalls++
	if index >= len(store.loads) {
		return nil, fmt.Errorf("unexpected authority load %d", index)
	}
	return store.loads[index].authority, store.loads[index].err
}

// CreateAuthority records first-run publication and returns its scripted result.
func (store *sequenceStore) CreateAuthority(context.Context, *localca.Authority) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.createCalls++
	return store.createErr
}

// LoadLeaf is unavailable because sequenceStore tests only root startup.
func (store *sequenceStore) LoadLeaf(context.Context, *localca.Authority, []string) (localca.Leaf, error) {
	return localca.Leaf{}, materialstore.ErrLeafNotFound
}

// PutLeaf is unavailable because sequenceStore tests only root startup.
func (store *sequenceStore) PutLeaf(context.Context, *localca.Authority, localca.Leaf) error {
	return nil
}

// LoadAuthority returns the configured startup result.
func (store *stubStore) LoadAuthority(context.Context, localca.Config) (*localca.Authority, error) {
	store.loadAuthorityCalls.Add(1)
	return nil, store.loadAuthorityError
}

// CreateAuthority records an unexpected creation attempt.
func (store *stubStore) CreateAuthority(context.Context, *localca.Authority) error {
	store.createAuthorityCalls.Add(1)
	return nil
}

// LoadLeaf is unavailable because startup tests never reach leaf reconciliation.
func (store *stubStore) LoadLeaf(context.Context, *localca.Authority, []string) (localca.Leaf, error) {
	return localca.Leaf{}, materialstore.ErrLeafNotFound
}

// PutLeaf is unavailable because startup tests never reach leaf reconciliation.
func (store *stubStore) PutLeaf(context.Context, *localca.Authority, localca.Leaf) error {
	return nil
}

// instrumentedStore delegates persistence while exposing deterministic failure and concurrency boundaries.
type instrumentedStore struct {
	delegate MaterialStore
	delay    time.Duration

	mutex         sync.RWMutex
	loadLeafError error
	loadLeafValue *localca.Leaf
	putLeafError  error
	calls         atomic.Int64
	active        atomic.Int64
	maximumActive atomic.Int64
}

// enter records one active persistence operation.
func (store *instrumentedStore) enter() func() {
	store.calls.Add(1)
	active := store.active.Add(1)
	for maximum := store.maximumActive.Load(); active > maximum && !store.maximumActive.CompareAndSwap(maximum, active); maximum = store.maximumActive.Load() {
	}
	if store.delay > 0 {
		time.Sleep(store.delay)
	}
	return func() { store.active.Add(-1) }
}

// LoadAuthority delegates one instrumented root load.
func (store *instrumentedStore) LoadAuthority(ctx context.Context, config localca.Config) (*localca.Authority, error) {
	leave := store.enter()
	defer leave()
	return store.delegate.LoadAuthority(ctx, config)
}

// CreateAuthority delegates one instrumented root creation.
func (store *instrumentedStore) CreateAuthority(ctx context.Context, authority *localca.Authority) error {
	leave := store.enter()
	defer leave()
	return store.delegate.CreateAuthority(ctx, authority)
}

// LoadLeaf returns an injected corruption or delegates one leaf load.
func (store *instrumentedStore) LoadLeaf(ctx context.Context, authority *localca.Authority, hosts []string) (localca.Leaf, error) {
	leave := store.enter()
	defer leave()
	store.mutex.RLock()
	err := store.loadLeafError
	leaf := store.loadLeafValue
	store.mutex.RUnlock()
	if err != nil {
		return localca.Leaf{}, err
	}
	if leaf != nil {
		return *leaf, nil
	}
	return store.delegate.LoadLeaf(ctx, authority, hosts)
}

// PutLeaf returns an injected publication failure or delegates one leaf write.
func (store *instrumentedStore) PutLeaf(ctx context.Context, authority *localca.Authority, leaf localca.Leaf) error {
	leave := store.enter()
	defer leave()
	store.mutex.RLock()
	err := store.putLeafError
	store.mutex.RUnlock()
	if err != nil {
		return err
	}
	return store.delegate.PutLeaf(ctx, authority, leaf)
}

// SetLoadLeafError changes the deterministic read result for subsequent calls.
func (store *instrumentedStore) SetLoadLeafError(err error) {
	store.mutex.Lock()
	store.loadLeafError = err
	store.mutex.Unlock()
}

// SetLoadLeafValue changes the deterministic successful read for subsequent calls.
func (store *instrumentedStore) SetLoadLeafValue(leaf *localca.Leaf) {
	store.mutex.Lock()
	store.loadLeafValue = leaf
	store.mutex.Unlock()
}

// SetPutLeafError changes the deterministic publication result for subsequent calls.
func (store *instrumentedStore) SetPutLeafError(err error) {
	store.mutex.Lock()
	store.putLeafError = err
	store.mutex.Unlock()
}

// Calls returns the total number of persistence operations.
func (store *instrumentedStore) Calls() int64 {
	return store.calls.Load()
}

// MaximumActive returns the peak concurrent persistence operation count.
func (store *instrumentedStore) MaximumActive() int64 {
	return store.maximumActive.Load()
}

// countingReader records entropy consumption while delegating secure reads.
type countingReader struct {
	delegate io.Reader
	reads    atomic.Int64
}

// Read delegates one entropy request and records it.
func (reader *countingReader) Read(buffer []byte) (int, error) {
	reader.reads.Add(1)
	return reader.delegate.Read(buffer)
}

// switchReader permits entropy failure only after root bootstrap completes.
type switchReader struct {
	delegate io.Reader
	mutex    sync.RWMutex
	err      error
}

// Read delegates or returns the currently injected entropy failure.
func (reader *switchReader) Read(buffer []byte) (int, error) {
	reader.mutex.RLock()
	err := reader.err
	reader.mutex.RUnlock()
	if err != nil {
		return 0, err
	}
	return reader.delegate.Read(buffer)
}

// SetError changes the entropy result for subsequent reads.
func (reader *switchReader) SetError(err error) {
	reader.mutex.Lock()
	reader.err = err
	reader.mutex.Unlock()
}

// cancelingReader cancels one operation after issuance has begun without failing entropy itself.
type cancelingReader struct {
	delegate io.Reader
	mutex    sync.Mutex
	cancel   context.CancelFunc
	once     sync.Once
}

// observedContext reports when EnsureLeaf completes its pre-lock cancellation check.
type observedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

// Err delegates cancellation state and records the first observation.
func (ctx *observedContext) Err() error {
	err := ctx.Context.Err()
	ctx.once.Do(func() { close(ctx.observed) })
	return err
}

// Read delegates entropy and then triggers the configured cancellation exactly once.
func (reader *cancelingReader) Read(buffer []byte) (int, error) {
	count, err := reader.delegate.Read(buffer)
	reader.mutex.Lock()
	cancel := reader.cancel
	reader.mutex.Unlock()
	if cancel != nil {
		reader.once.Do(cancel)
	}
	return count, err
}

// SetCancel arms cancellation for the next entropy read.
func (reader *cancelingReader) SetCancel(cancel context.CancelFunc) {
	reader.mutex.Lock()
	reader.cancel = cancel
	reader.mutex.Unlock()
}

// fingerprintDER returns the lowercase SHA-256 identity of certificate DER.
func fingerprintDER(der []byte) string {
	digest := sha256.Sum256(der)
	return fmt.Sprintf("%x", digest[:])
}
