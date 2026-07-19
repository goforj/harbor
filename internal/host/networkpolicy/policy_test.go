package networkpolicy

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

const testAuthorityFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestMechanismsValidateAcceptsOnlyCompleteProfiles proves profile parts cannot be mixed across operating systems.
func TestMechanismsValidateAcceptsOnlyCompleteProfiles(t *testing.T) {
	valid := []Mechanisms{MacOSMechanisms(), UbuntuMechanisms(), WindowsMechanisms()}
	for _, mechanisms := range valid {
		if err := mechanisms.Validate(); err != nil {
			t.Fatalf("Mechanisms.Validate() for %+v error = %v", mechanisms, err)
		}
	}

	invalid := []Mechanisms{
		{},
		{Resolver: DarwinResolverFile, LowPorts: DarwinPFAnchor},
		{Resolver: DarwinResolverFile, LowPorts: UbuntuNFTables, Trust: DarwinCurrentUserTrust},
		{Resolver: UbuntuSystemdResolved, LowPorts: UbuntuNFTables, Trust: DarwinCurrentUserTrust},
		{Resolver: WindowsNRPT, LowPorts: WindowsDirectLowPorts, Trust: UbuntuSystemTrust},
		{Resolver: "unknown", LowPorts: WindowsDirectLowPorts, Trust: WindowsCurrentUserTrust},
	}
	for _, mechanisms := range invalid {
		if err := mechanisms.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("Mechanisms.Validate() for %+v error = %v, want ErrInvalidPolicy", mechanisms, err)
		}
	}
}

// TestPolicyValidateAcceptsSupportedTopologies covers every complete host-integration profile.
func TestPolicyValidateAcceptsSupportedTopologies(t *testing.T) {
	for name, policy := range map[string]Policy{
		"macOS":   validMacOSPolicy(),
		"Ubuntu":  validUbuntuPolicy(),
		"Windows": validWindowsPolicy(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.Validate(); err != nil {
				t.Fatalf("Policy.Validate() error = %v", err)
			}
			fingerprint, err := policy.Fingerprint()
			if err != nil {
				t.Fatalf("Policy.Fingerprint() error = %v", err)
			}
			if len(fingerprint) != 64 || fingerprint != strings.ToLower(fingerprint) {
				t.Fatalf("Policy.Fingerprint() = %q, want lowercase SHA-256", fingerprint)
			}
		})
	}
}

// TestNewBuildsCanonicalPolicy proves construction fixes the owned suffix and rejects invalid input atomically.
func TestNewBuildsCanonicalPolicy(t *testing.T) {
	want := validMacOSPolicy()
	got, err := New(want.AuthorityFingerprint, want.Mechanisms, want.DNS, want.HTTP, want.HTTPS)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got != want {
		t.Fatalf("New() = %+v, want %+v", got, want)
	}

	if got, err = New("not-a-fingerprint", want.Mechanisms, want.DNS, want.HTTP, want.HTTPS); !errors.Is(err, ErrInvalidPolicy) || got != (Policy{}) {
		t.Fatalf("invalid New() = (%+v, %v), want zero Policy and ErrInvalidPolicy", got, err)
	}
}

// TestPolicyValidateRejectsCanonicalContractViolations covers common fields and socket identity rules.
func TestPolicyValidateRejectsCanonicalContractViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "suffix without leading dot", mutate: func(policy *Policy) { policy.Suffix = "test" }},
		{name: "suffix with uppercase", mutate: func(policy *Policy) { policy.Suffix = ".TEST" }},
		{name: "short authority fingerprint", mutate: func(policy *Policy) { policy.AuthorityFingerprint = "aa" }},
		{name: "uppercase authority fingerprint", mutate: func(policy *Policy) { policy.AuthorityFingerprint = strings.Repeat("A", 64) }},
		{name: "nonhex authority fingerprint", mutate: func(policy *Policy) { policy.AuthorityFingerprint = strings.Repeat("g", 64) }},
		{name: "partial mechanisms", mutate: func(policy *Policy) { policy.Mechanisms.Trust = "" }},
		{name: "invalid advertised socket", mutate: func(policy *Policy) { policy.DNS.Advertised = netip.AddrPort{} }},
		{name: "IPv6 bind socket", mutate: func(policy *Policy) { policy.DNS.Bind = mustAddrPort("[::1]:53535") }},
		{name: "IPv4-mapped bind socket", mutate: func(policy *Policy) { policy.DNS.Bind = mustAddrPort("[::ffff:127.0.0.1]:53535") }},
		{name: "nonloopback bind socket", mutate: func(policy *Policy) { policy.DNS.Bind = mustAddrPort("192.0.2.1:53535") }},
		{name: "zero bind port", mutate: func(policy *Policy) { policy.DNS.Bind = netip.AddrPortFrom(localhost, 0) }},
		{name: "HTTP advertised address", mutate: func(policy *Policy) { policy.HTTP.Advertised = mustAddrPort("127.0.0.2:80") }},
		{name: "HTTP advertised port", mutate: func(policy *Policy) { policy.HTTP.Advertised = mustAddrPort("127.0.0.1:8080") }},
		{name: "HTTPS advertised address", mutate: func(policy *Policy) { policy.HTTPS.Advertised = mustAddrPort("127.0.0.2:443") }},
		{name: "HTTPS advertised port", mutate: func(policy *Policy) { policy.HTTPS.Advertised = mustAddrPort("127.0.0.1:8443") }},
		{name: "DNS and HTTP share socket", mutate: func(policy *Policy) { policy.HTTP.Bind = policy.DNS.Bind }},
		{name: "HTTP and HTTPS share socket", mutate: func(policy *Policy) { policy.HTTPS.Bind = policy.HTTP.Bind }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := validMacOSPolicy()
			test.mutate(&policy)
			assertInvalidPolicy(t, policy)
		})
	}
}

// TestPolicyValidateRejectsRedirectedTopologyViolations pins macOS and Ubuntu to unprivileged direct DNS and redirected web ports.
func TestPolicyValidateRejectsRedirectedTopologyViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "DNS is redirected", mutate: func(policy *Policy) { policy.DNS.Advertised = mustAddrPort("127.0.0.1:53534") }},
		{name: "DNS uses another loopback", mutate: func(policy *Policy) { policy.DNS = directListener("127.0.0.2:53535") }},
		{name: "DNS uses privileged port", mutate: func(policy *Policy) { policy.DNS = directListener("127.0.0.1:1023") }},
		{name: "HTTP bind uses another loopback", mutate: func(policy *Policy) { policy.HTTP.Bind = mustAddrPort("127.0.0.2:58080") }},
		{name: "HTTP bind uses privileged port", mutate: func(policy *Policy) { policy.HTTP.Bind = mustAddrPort("127.0.0.1:1023") }},
		{name: "HTTP is direct", mutate: func(policy *Policy) { policy.HTTP.Bind = policy.HTTP.Advertised }},
		{name: "HTTPS bind uses another loopback", mutate: func(policy *Policy) { policy.HTTPS.Bind = mustAddrPort("127.0.0.2:58443") }},
		{name: "HTTPS bind uses privileged port", mutate: func(policy *Policy) { policy.HTTPS.Bind = mustAddrPort("127.0.0.1:1023") }},
		{name: "HTTPS is direct", mutate: func(policy *Policy) { policy.HTTPS.Bind = policy.HTTPS.Advertised }},
	}

	for _, mechanisms := range []Mechanisms{MacOSMechanisms(), UbuntuMechanisms()} {
		for _, test := range tests {
			t.Run(string(mechanisms.Resolver)+"/"+test.name, func(t *testing.T) {
				policy := validMacOSPolicy()
				policy.Mechanisms = mechanisms
				test.mutate(&policy)
				assertInvalidPolicy(t, policy)
			})
		}
	}
}

// TestPolicyValidateRejectsWindowsTopologyViolations pins NRPT DNS and direct web listeners to their dedicated addresses.
func TestPolicyValidateRejectsWindowsTopologyViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "DNS is redirected", mutate: func(policy *Policy) { policy.DNS.Advertised = mustAddrPort("127.0.0.2:5353") }},
		{name: "DNS uses localhost", mutate: func(policy *Policy) { policy.DNS = directListener("127.0.0.1:53") }},
		{name: "DNS uses high port", mutate: func(policy *Policy) { policy.DNS = directListener("127.0.0.2:5353") }},
		{name: "HTTP is redirected", mutate: func(policy *Policy) { policy.HTTP.Bind = mustAddrPort("127.0.0.1:58080") }},
		{name: "HTTPS is redirected", mutate: func(policy *Policy) { policy.HTTPS.Bind = mustAddrPort("127.0.0.1:58443") }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := validWindowsPolicy()
			test.mutate(&policy)
			assertInvalidPolicy(t, policy)
		})
	}
}

// TestPolicyFingerprintPinsCanonicalJSON proves stable field order, text encoding, and digest spelling.
func TestPolicyFingerprintPinsCanonicalJSON(t *testing.T) {
	policy := validMacOSPolicy()
	payload, err := policy.canonicalJSON()
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v", err)
	}
	const wantJSON = `{"suffix":".test","authority_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mechanisms":{"resolver":"darwin-resolver-file-v1","low_ports":"darwin-pf-anchor-v1","trust":"darwin-current-user-trust-v1"},"dns":{"advertised":"127.0.0.1:53535","bind":"127.0.0.1:53535"},"http":{"advertised":"127.0.0.1:80","bind":"127.0.0.1:58080"},"https":{"advertised":"127.0.0.1:443","bind":"127.0.0.1:58443"}}`
	if string(payload) != wantJSON {
		t.Fatalf("canonicalJSON() = %s, want %s", payload, wantJSON)
	}

	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() error = %v", err)
	}
	const wantFingerprint = "cae1d7c7bd5c188ac3326e6539643973787cb5491d0b65145c1baed291865a33"
	if fingerprint != wantFingerprint {
		t.Fatalf("Policy.Fingerprint() = %q, want %q", fingerprint, wantFingerprint)
	}
	second, err := policy.Fingerprint()
	if err != nil || second != fingerprint {
		t.Fatalf("second Policy.Fingerprint() = (%q, %v), want (%q, nil)", second, err, fingerprint)
	}
}

// TestPolicyFingerprintCoversEveryMutableCanonicalComponent proves no operational field is omitted from evidence.
func TestPolicyFingerprintCoversEveryMutableCanonicalComponent(t *testing.T) {
	baseline := validMacOSPolicy()
	want, err := baseline.Fingerprint()
	if err != nil {
		t.Fatalf("baseline Policy.Fingerprint() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "authority", mutate: func(policy *Policy) { policy.AuthorityFingerprint = strings.Repeat("b", 64) }},
		{name: "mechanisms", mutate: func(policy *Policy) { policy.Mechanisms = UbuntuMechanisms() }},
		{name: "DNS", mutate: func(policy *Policy) { policy.DNS = directListener("127.0.0.1:53536") }},
		{name: "HTTP bind", mutate: func(policy *Policy) { policy.HTTP.Bind = mustAddrPort("127.0.0.1:58081") }},
		{name: "HTTPS bind", mutate: func(policy *Policy) { policy.HTTPS.Bind = mustAddrPort("127.0.0.1:58444") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := baseline
			test.mutate(&candidate)
			got, fingerprintErr := candidate.Fingerprint()
			if fingerprintErr != nil {
				t.Fatalf("mutated Policy.Fingerprint() error = %v", fingerprintErr)
			}
			if got == want {
				t.Fatalf("mutated Policy.Fingerprint() = baseline %q", got)
			}
		})
	}

	invalid := baseline
	invalid.Suffix = "test"
	if _, err := invalid.Fingerprint(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid Policy.Fingerprint() error = %v, want ErrInvalidPolicy", err)
	}
}

// validMacOSPolicy returns the representative redirected profile used by tests.
func validMacOSPolicy() Policy {
	return Policy{
		Suffix:               TestSuffix,
		AuthorityFingerprint: testAuthorityFingerprint,
		Mechanisms:           MacOSMechanisms(),
		DNS:                  directListener("127.0.0.1:53535"),
		HTTP:                 redirectedListener("127.0.0.1:80", "127.0.0.1:58080"),
		HTTPS:                redirectedListener("127.0.0.1:443", "127.0.0.1:58443"),
	}
}

// validUbuntuPolicy returns a distinct valid Ubuntu policy for profile coverage.
func validUbuntuPolicy() Policy {
	policy := validMacOSPolicy()
	policy.Mechanisms = UbuntuMechanisms()
	policy.DNS = directListener("127.0.0.1:53536")
	policy.HTTP.Bind = mustAddrPort("127.0.0.1:58081")
	policy.HTTPS.Bind = mustAddrPort("127.0.0.1:58444")

	return policy
}

// validWindowsPolicy returns the direct-listener profile used by Windows tests.
func validWindowsPolicy() Policy {
	return Policy{
		Suffix:               TestSuffix,
		AuthorityFingerprint: testAuthorityFingerprint,
		Mechanisms:           WindowsMechanisms(),
		DNS:                  directListener("127.0.0.2:53"),
		HTTP:                 directListener("127.0.0.1:80"),
		HTTPS:                directListener("127.0.0.1:443"),
	}
}

// directListener returns a listener whose advertised socket is its bind socket.
func directListener(socket string) Listener {
	parsed := mustAddrPort(socket)
	return Listener{Advertised: parsed, Bind: parsed}
}

// redirectedListener returns a listener whose public and daemon sockets differ.
func redirectedListener(advertised, bind string) Listener {
	return Listener{Advertised: mustAddrPort(advertised), Bind: mustAddrPort(bind)}
}

// mustAddrPort parses test literals and panics only when the fixture itself is malformed.
func mustAddrPort(value string) netip.AddrPort {
	return netip.MustParseAddrPort(value)
}

// assertInvalidPolicy requires both validation and fingerprinting to reject a policy.
func assertInvalidPolicy(t *testing.T, policy Policy) {
	t.Helper()
	if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Policy.Validate() error = %v, want ErrInvalidPolicy", err)
	}
	if _, err := policy.Fingerprint(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Policy.Fingerprint() error = %v, want ErrInvalidPolicy", err)
	}
}
