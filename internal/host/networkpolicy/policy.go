package networkpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
)

const (
	// TestSuffix is the only resolver suffix Harbor owns.
	TestSuffix = ".test"

	sha256HexLength = sha256.Size * 2
	lowestHighPort  = 1024
)

var (
	// ErrInvalidPolicy identifies a policy that cannot safely describe Harbor's host integration.
	ErrInvalidPolicy = errors.New("invalid host network policy")

	localhost = netip.MustParseAddr("127.0.0.1")
)

// ResolverMechanism identifies the operating-system facility used to route the
// Harbor suffix to its DNS listener.
type ResolverMechanism string

const (
	// DarwinResolverFile routes the suffix with Harbor's first macOS resolver-file contract.
	DarwinResolverFile ResolverMechanism = "darwin-resolver-file-v1"
	// UbuntuSystemdResolved routes the suffix with Harbor's first Ubuntu systemd-resolved contract.
	UbuntuSystemdResolved ResolverMechanism = "ubuntu-systemd-resolved-v1"
	// WindowsNRPT routes the suffix with Harbor's first Windows Name Resolution Policy Table contract.
	WindowsNRPT ResolverMechanism = "windows-nrpt-v1"
)

// LowPortMechanism identifies how advertised HTTP and HTTPS ports reach the
// daemon's bind sockets.
type LowPortMechanism string

const (
	// DarwinPFAnchor redirects macOS loopback traffic through Harbor's first owned PF-anchor contract.
	DarwinPFAnchor LowPortMechanism = "darwin-pf-anchor-v1"
	// UbuntuNFTables redirects Linux loopback traffic through Harbor's first owned nftables contract.
	UbuntuNFTables LowPortMechanism = "ubuntu-nftables-v1"
	// WindowsDirectLowPorts binds advertised ports through Harbor's first Windows direct-listener contract.
	WindowsDirectLowPorts LowPortMechanism = "windows-direct-low-ports-v1"
)

// TrustMechanism identifies the trust store that receives Harbor's certificate authority.
type TrustMechanism string

const (
	// DarwinCurrentUserTrust installs trust for only the interactive macOS user.
	DarwinCurrentUserTrust TrustMechanism = "darwin-current-user-trust-v1"
	// UbuntuSystemTrust installs trust through Harbor's first Ubuntu system-store contract.
	UbuntuSystemTrust TrustMechanism = "ubuntu-system-trust-v1"
	// WindowsCurrentUserTrust installs trust for only the interactive Windows user.
	WindowsCurrentUserTrust TrustMechanism = "windows-current-user-trust-v1"
)

// Mechanisms is the indivisible resolver, low-port, and trust-store profile for
// one supported operating system.
type Mechanisms struct {
	Resolver ResolverMechanism `json:"resolver"`
	LowPorts LowPortMechanism  `json:"low_ports"`
	Trust    TrustMechanism    `json:"trust"`
}

// Validate rejects partial and mixed-platform profiles because each supported
// profile is proven and repaired as one host-integration unit.
func (mechanisms Mechanisms) Validate() error {
	if mechanisms == MacOSMechanisms() || mechanisms == UbuntuMechanisms() || mechanisms == WindowsMechanisms() {
		return nil
	}

	return fmt.Errorf("%w: unsupported mechanism profile %q/%q/%q", ErrInvalidPolicy, mechanisms.Resolver, mechanisms.LowPorts, mechanisms.Trust)
}

// MacOSMechanisms returns Harbor's complete supported macOS integration profile.
func MacOSMechanisms() Mechanisms {
	return Mechanisms{Resolver: DarwinResolverFile, LowPorts: DarwinPFAnchor, Trust: DarwinCurrentUserTrust}
}

// UbuntuMechanisms returns Harbor's complete supported Ubuntu integration profile.
func UbuntuMechanisms() Mechanisms {
	return Mechanisms{Resolver: UbuntuSystemdResolved, LowPorts: UbuntuNFTables, Trust: UbuntuSystemTrust}
}

// WindowsMechanisms returns Harbor's complete supported Windows integration profile.
func WindowsMechanisms() Mechanisms {
	return Mechanisms{Resolver: WindowsNRPT, LowPorts: WindowsDirectLowPorts, Trust: WindowsCurrentUserTrust}
}

// Listener records the public socket clients use and the socket the daemon binds.
type Listener struct {
	Advertised netip.AddrPort `json:"advertised"`
	Bind       netip.AddrPort `json:"bind"`
}

// Validate requires usable canonical IPv4 loopback sockets on both sides of a listener.
func (listener Listener) Validate() error {
	if err := validateSocket("advertised", listener.Advertised); err != nil {
		return err
	}
	if err := validateSocket("bind", listener.Bind); err != nil {
		return err
	}

	return nil
}

// Policy is the canonical host-network contract shared by setup, repair, and runtime startup.
type Policy struct {
	Suffix               string     `json:"suffix"`
	AuthorityFingerprint string     `json:"authority_fingerprint"`
	Mechanisms           Mechanisms `json:"mechanisms"`
	DNS                  Listener   `json:"dns"`
	HTTP                 Listener   `json:"http"`
	HTTPS                Listener   `json:"https"`
}

// New constructs and validates the exact .test policy Harbor may apply to a host.
func New(authorityFingerprint string, mechanisms Mechanisms, dns, http, https Listener) (Policy, error) {
	policy := Policy{
		Suffix:               TestSuffix,
		AuthorityFingerprint: authorityFingerprint,
		Mechanisms:           mechanisms,
		DNS:                  dns,
		HTTP:                 http,
		HTTPS:                https,
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}

	return policy, nil
}

// Validate proves that the policy is canonical and matches one complete supported topology.
func (policy Policy) Validate() error {
	if policy.Suffix != TestSuffix {
		return fmt.Errorf("%w: suffix must be exactly %q", ErrInvalidPolicy, TestSuffix)
	}
	if err := validateFingerprint(policy.AuthorityFingerprint); err != nil {
		return err
	}
	if err := policy.Mechanisms.Validate(); err != nil {
		return err
	}
	listeners := []struct {
		name     string
		listener Listener
	}{
		{name: "dns", listener: policy.DNS},
		{name: "http", listener: policy.HTTP},
		{name: "https", listener: policy.HTTPS},
	}
	for _, named := range listeners {
		if err := named.listener.Validate(); err != nil {
			return fmt.Errorf("%s listener: %w", named.name, err)
		}
	}
	if policy.HTTP.Advertised != netip.AddrPortFrom(localhost, 80) {
		return fmt.Errorf("%w: HTTP must be advertised at 127.0.0.1:80", ErrInvalidPolicy)
	}
	if policy.HTTPS.Advertised != netip.AddrPortFrom(localhost, 443) {
		return fmt.Errorf("%w: HTTPS must be advertised at 127.0.0.1:443", ErrInvalidPolicy)
	}
	if err := validateDistinctProtocolSockets(policy); err != nil {
		return err
	}

	if policy.Mechanisms == WindowsMechanisms() {
		return validateWindowsPolicy(policy)
	}

	return validateRedirectedPolicy(policy)
}

// Fingerprint returns the lowercase SHA-256 digest of the validated canonical JSON policy.
func (policy Policy) Fingerprint() (string, error) {
	payload, err := policy.canonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)

	return hex.EncodeToString(digest[:]), nil
}

// canonicalPolicy pins field order and socket spelling independently from Policy's public representation.
type canonicalPolicy struct {
	Suffix               string              `json:"suffix"`
	AuthorityFingerprint string              `json:"authority_fingerprint"`
	Mechanisms           canonicalMechanisms `json:"mechanisms"`
	DNS                  canonicalListener   `json:"dns"`
	HTTP                 canonicalListener   `json:"http"`
	HTTPS                canonicalListener   `json:"https"`
}

// canonicalMechanisms pins backend field order independently from Mechanisms' public representation.
type canonicalMechanisms struct {
	Resolver ResolverMechanism `json:"resolver"`
	LowPorts LowPortMechanism  `json:"low_ports"`
	Trust    TrustMechanism    `json:"trust"`
}

// canonicalListener serializes netip values with their canonical text representation.
type canonicalListener struct {
	Advertised string `json:"advertised"`
	Bind       string `json:"bind"`
}

// canonicalJSON returns the stable evidence payload used by Fingerprint.
func (policy Policy) canonicalJSON() ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(canonicalPolicy{
		Suffix:               policy.Suffix,
		AuthorityFingerprint: policy.AuthorityFingerprint,
		Mechanisms: canonicalMechanisms{
			Resolver: policy.Mechanisms.Resolver,
			LowPorts: policy.Mechanisms.LowPorts,
			Trust:    policy.Mechanisms.Trust,
		},
		DNS:   canonicalizeListener(policy.DNS),
		HTTP:  canonicalizeListener(policy.HTTP),
		HTTPS: canonicalizeListener(policy.HTTPS),
	})
}

// canonicalizeListener removes any dependency on netip's JSON encoding contract.
func canonicalizeListener(listener Listener) canonicalListener {
	return canonicalListener{Advertised: listener.Advertised.String(), Bind: listener.Bind.String()}
}

// validateFingerprint requires the exact lowercase hexadecimal spelling used by SHA-256 evidence.
func validateFingerprint(fingerprint string) error {
	if len(fingerprint) != sha256HexLength {
		return fmt.Errorf("%w: authority fingerprint must be 64 lowercase hexadecimal characters", ErrInvalidPolicy)
	}
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || hex.EncodeToString(decoded) != fingerprint {
		return fmt.Errorf("%w: authority fingerprint must be 64 lowercase hexadecimal characters", ErrInvalidPolicy)
	}

	return nil
}

// validateSocket excludes ambiguous IPv4-mapped addresses and sockets that cannot be bound.
func validateSocket(name string, socket netip.AddrPort) error {
	address := socket.Addr()
	if !socket.IsValid() || !address.Is4() || !address.IsLoopback() || address != address.Unmap() || socket.Port() == 0 {
		return fmt.Errorf("%w: %s socket must be a canonical IPv4 loopback address with a nonzero port", ErrInvalidPolicy, name)
	}

	return nil
}

// validateDistinctProtocolSockets permits direct advertised/bind pairs but never shares a socket across protocols.
func validateDistinctProtocolSockets(policy Policy) error {
	type socketOwner struct {
		name   string
		socket netip.AddrPort
	}
	owners := []socketOwner{
		{name: "dns", socket: policy.DNS.Advertised},
		{name: "dns", socket: policy.DNS.Bind},
		{name: "http", socket: policy.HTTP.Advertised},
		{name: "http", socket: policy.HTTP.Bind},
		{name: "https", socket: policy.HTTPS.Advertised},
		{name: "https", socket: policy.HTTPS.Bind},
	}
	seen := make(map[netip.AddrPort]string, len(owners))
	for _, owner := range owners {
		if previous, exists := seen[owner.socket]; exists && previous != owner.name {
			return fmt.Errorf("%w: %s and %s share socket %s", ErrInvalidPolicy, previous, owner.name, owner.socket)
		}
		seen[owner.socket] = owner.name
	}

	return nil
}

// validateRedirectedPolicy enforces the unprivileged listener topology used by macOS and Ubuntu.
func validateRedirectedPolicy(policy Policy) error {
	if policy.DNS.Advertised != policy.DNS.Bind || policy.DNS.Bind.Addr() != localhost || policy.DNS.Bind.Port() < lowestHighPort {
		return fmt.Errorf("%w: redirected profiles require direct DNS on a high 127.0.0.1 port", ErrInvalidPolicy)
	}
	listeners := []struct {
		name     string
		listener Listener
	}{
		{name: "HTTP", listener: policy.HTTP},
		{name: "HTTPS", listener: policy.HTTPS},
	}
	for _, named := range listeners {
		if named.listener.Bind.Addr() != localhost || named.listener.Bind.Port() < lowestHighPort || named.listener.Bind == named.listener.Advertised {
			return fmt.Errorf("%w: redirected profiles require %s to bind a distinct high 127.0.0.1 port", ErrInvalidPolicy, named.name)
		}
	}

	return nil
}

// validateWindowsPolicy enforces direct low ports and the dedicated loopback needed by NRPT DNS.
func validateWindowsPolicy(policy Policy) error {
	if policy.DNS.Advertised != policy.DNS.Bind || policy.DNS.Bind.Port() != 53 || policy.DNS.Bind.Addr() == localhost {
		return fmt.Errorf("%w: Windows requires direct DNS on port 53 at a dedicated loopback address", ErrInvalidPolicy)
	}
	if policy.HTTP.Bind != policy.HTTP.Advertised || policy.HTTPS.Bind != policy.HTTPS.Advertised {
		return fmt.Errorf("%w: Windows requires direct HTTP and HTTPS low ports", ErrInvalidPolicy)
	}

	return nil
}
