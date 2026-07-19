package networkplan

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
)

const (
	testInstallationID       = identity.InstallationID("installation-alpha")
	testAuthorityFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// TestPlatformValuesPinVersionedProfiles prevents persisted product-profile identities from following runtime labels.
func TestPlatformValuesPinVersionedProfiles(t *testing.T) {
	profiles := map[Platform]string{
		PlatformMacOS:      "macos-v1",
		PlatformUbuntu2404: "ubuntu-24.04-v1",
		PlatformWindows11:  "windows-11-v1",
	}
	if len(profiles) != 3 {
		t.Fatalf("profile count = %d, want 3", len(profiles))
	}
	for profile, want := range profiles {
		if got := string(profile); got != want {
			t.Errorf("profile = %q, want %q", got, want)
		}
	}
}

// TestBuildConstructsEveryProductProfile pins the exact topology, port vector, and policy fingerprint for each profile.
func TestBuildConstructsEveryProductProfile(t *testing.T) {
	pool := mustPool(t, "127.77.0.8/29", "127.77.0.10", "127.77.0.11")
	tests := []struct {
		name        string
		platform    Platform
		mechanisms  networkpolicy.Mechanisms
		dns         networkpolicy.Listener
		http        networkpolicy.Listener
		https       networkpolicy.Listener
		fingerprint string
	}{
		{
			name:        "macOS",
			platform:    PlatformMacOS,
			mechanisms:  networkpolicy.MacOSMechanisms(),
			dns:         directListener("127.0.0.1:22656"),
			http:        redirectedListener("127.0.0.1:80", "127.0.0.1:22657"),
			https:       redirectedListener("127.0.0.1:443", "127.0.0.1:22658"),
			fingerprint: "6e555b731b7868fab8870e2c437ed454ddf687126cd779b0ad4478b8a14de483",
		},
		{
			name:        "Ubuntu 24.04",
			platform:    PlatformUbuntu2404,
			mechanisms:  networkpolicy.UbuntuMechanisms(),
			dns:         directListener("127.0.0.1:22656"),
			http:        redirectedListener("127.0.0.1:80", "127.0.0.1:22657"),
			https:       redirectedListener("127.0.0.1:443", "127.0.0.1:22658"),
			fingerprint: "cf1ac62d7e3eb4839114dc862486e7da432c35d9c75cb0a994b09b6dc656a78d",
		},
		{
			name:        "Windows 11",
			platform:    PlatformWindows11,
			mechanisms:  networkpolicy.WindowsMechanisms(),
			dns:         directListener("127.0.0.2:53"),
			http:        directListener("127.0.0.1:80"),
			https:       directListener("127.0.0.1:443"),
			fingerprint: "94979ccd290a8c7e77243a9776fd0413b377808f0b30670053bed87f19ffc21a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := Build(Request{
				Platform:             test.platform,
				InstallationID:       testInstallationID,
				Pool:                 pool,
				AuthorityFingerprint: testAuthorityFingerprint,
			})
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}

			want := networkpolicy.Policy{
				Suffix:               networkpolicy.TestSuffix,
				AuthorityFingerprint: testAuthorityFingerprint,
				Mechanisms:           test.mechanisms,
				DNS:                  test.dns,
				HTTP:                 test.http,
				HTTPS:                test.https,
			}
			if policy != want {
				t.Fatalf("Build() = %#v, want %#v", policy, want)
			}
			if err := policy.Validate(); err != nil {
				t.Fatalf("Policy.Validate() error = %v", err)
			}
			fingerprint, err := policy.Fingerprint()
			if err != nil {
				t.Fatalf("Policy.Fingerprint() error = %v", err)
			}
			if fingerprint != test.fingerprint {
				t.Fatalf("Policy.Fingerprint() = %q, want %q", fingerprint, test.fingerprint)
			}
		})
	}
}

// TestBuildKeepsPortsStableAcrossAuthorityRotation proves certificate lifecycle does not move daemon sockets.
func TestBuildKeepsPortsStableAcrossAuthorityRotation(t *testing.T) {
	request := validRequest(t, PlatformMacOS)
	first, err := Build(request)
	if err != nil {
		t.Fatalf("first Build() error = %v", err)
	}

	request.AuthorityFingerprint = strings.Repeat("b", 64)
	rotated, err := Build(request)
	if err != nil {
		t.Fatalf("rotated Build() error = %v", err)
	}
	if first.DNS != rotated.DNS || first.HTTP != rotated.HTTP || first.HTTPS != rotated.HTTPS {
		t.Fatalf("rotated listeners = %#v/%#v/%#v, want %#v/%#v/%#v", rotated.DNS, rotated.HTTP, rotated.HTTPS, first.DNS, first.HTTP, first.HTTPS)
	}
	if first.AuthorityFingerprint == rotated.AuthorityFingerprint {
		t.Fatal("authority rotation did not reach the policy")
	}
	firstFingerprint, err := first.Fingerprint()
	if err != nil {
		t.Fatalf("first Policy.Fingerprint() error = %v", err)
	}
	rotatedFingerprint, err := rotated.Fingerprint()
	if err != nil {
		t.Fatalf("rotated Policy.Fingerprint() error = %v", err)
	}
	if firstFingerprint == rotatedFingerprint {
		t.Fatalf("rotated Policy.Fingerprint() = original %q", firstFingerprint)
	}

	repeated, err := Build(request)
	if err != nil || repeated != rotated {
		t.Fatalf("repeated Build() = (%#v, %v), want (%#v, nil)", repeated, err, rotated)
	}
}

// TestBuildRejectsInvalidInputsAtomically covers all caller-controlled validation boundaries.
func TestBuildRejectsInvalidInputsAtomically(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Request)
		is       error
		contains string
	}{
		{
			name:     "missing installation ID",
			mutate:   func(request *Request) { request.InstallationID = "" },
			contains: "installation ID",
		},
		{
			name:     "malformed installation ID",
			mutate:   func(request *Request) { request.InstallationID = " installation " },
			contains: "installation ID",
		},
		{
			name:     "invalid pool",
			mutate:   func(request *Request) { request.Pool = identity.Pool{} },
			contains: "pool",
		},
		{
			name:     "invalid authority fingerprint",
			mutate:   func(request *Request) { request.AuthorityFingerprint = "not-a-fingerprint" },
			is:       networkpolicy.ErrInvalidPolicy,
			contains: "authority fingerprint",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest(t, PlatformMacOS)
			test.mutate(&request)
			policy, err := Build(request)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Build() error = %v, want containing %q", err, test.contains)
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("Build() error = %v, want errors.Is(%v)", err, test.is)
			}
			if policy != (networkpolicy.Policy{}) {
				t.Fatalf("invalid Build() policy = %#v, want zero value", policy)
			}
		})
	}
}

// TestBuildReturnsTypedUnsupportedPlatformError preserves both programmatic matching forms and the rejected value.
func TestBuildReturnsTypedUnsupportedPlatformError(t *testing.T) {
	request := validRequest(t, Platform("macos"))
	policy, err := Build(request)
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Build() error = %v, want ErrUnsupportedPlatform", err)
	}
	var unsupported *UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Build() error = %v, want UnsupportedPlatformError", err)
	}
	if got, want := err.Error(), `unsupported host network plan platform "macos"`; got != want {
		t.Fatalf("Build() error = %q, want %q", got, want)
	}
	if unsupported.Platform != request.Platform {
		t.Fatalf("UnsupportedPlatformError.Platform = %q, want %q", unsupported.Platform, request.Platform)
	}
	if policy != (networkpolicy.Policy{}) {
		t.Fatalf("unsupported Build() policy = %#v, want zero value", policy)
	}
}

// TestBuildRejectsWindowsDNSPoolCollision requires the dedicated NRPT address to remain outside project allocation.
func TestBuildRejectsWindowsDNSPoolCollision(t *testing.T) {
	request := validRequest(t, PlatformWindows11)
	request.Pool = mustPool(t, "127.0.0.0/8", "127.0.0.2", "127.77.0.10")
	policy, err := Build(request)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.2") || !strings.Contains(err.Error(), "project-pool candidate") {
		t.Fatalf("Build() error = %v, want Windows DNS pool collision", err)
	}
	if policy != (networkpolicy.Policy{}) {
		t.Fatalf("collision Build() policy = %#v, want zero value", policy)
	}

	request.Pool = mustPool(t, "127.0.0.0/8", "127.0.0.3", "127.77.0.10")
	if _, err := Build(request); err != nil {
		t.Fatalf("Build() with prefix-only overlap error = %v", err)
	}
}

// TestRedirectedPortBlocksStayInsidePlanningWindow samples enough identities to catch overflow and socket-order drift.
func TestRedirectedPortBlocksStayInsidePlanningWindow(t *testing.T) {
	request := validRequest(t, PlatformUbuntu2404)
	for index := range 10_000 {
		request.InstallationID = identity.InstallationID(fmt.Sprintf("installation-%05d", index))
		policy, err := Build(request)
		if err != nil {
			t.Fatalf("Build(%q) error = %v", request.InstallationID, err)
		}

		ports := [3]uint16{policy.DNS.Bind.Port(), policy.HTTP.Bind.Port(), policy.HTTPS.Bind.Port()}
		if ports[0] < 21000 || ports[2] > 29999 {
			t.Fatalf("Build(%q) ports = %v, want inside 21000..29999", request.InstallationID, ports)
		}
		if ports[1] != ports[0]+1 || ports[2] != ports[0]+2 {
			t.Fatalf("Build(%q) ports = %v, want one contiguous block", request.InstallationID, ports)
		}
		if (ports[0]-21000)%3 != 0 {
			t.Fatalf("Build(%q) first port = %d, want an aligned 3-port block", request.InstallationID, ports[0])
		}
	}
}

// validRequest returns a complete request whose pool cannot collide with a platform listener.
func validRequest(t *testing.T, platform Platform) Request {
	t.Helper()
	return Request{
		Platform:             platform,
		InstallationID:       testInstallationID,
		Pool:                 mustPool(t, "127.77.0.8/29", "127.77.0.10", "127.77.0.11"),
		AuthorityFingerprint: testAuthorityFingerprint,
	}
}

// mustPool constructs a pool and fails when a test fixture violates identity invariants.
func mustPool(t *testing.T, prefix string, candidates ...string) identity.Pool {
	t.Helper()
	addresses := make([]netip.Addr, len(candidates))
	for index, candidate := range candidates {
		addresses[index] = netip.MustParseAddr(candidate)
	}
	pool, err := identity.NewPool(netip.MustParsePrefix(prefix), addresses)
	if err != nil {
		t.Fatalf("identity.NewPool() error = %v", err)
	}
	return pool
}

// directListener constructs a test listener whose public and daemon sockets are identical.
func directListener(socket string) networkpolicy.Listener {
	parsed := netip.MustParseAddrPort(socket)
	return networkpolicy.Listener{Advertised: parsed, Bind: parsed}
}

// redirectedListener constructs a test listener with distinct advertised and bind sockets.
func redirectedListener(advertised string, bind string) networkpolicy.Listener {
	return networkpolicy.Listener{
		Advertised: netip.MustParseAddrPort(advertised),
		Bind:       netip.MustParseAddrPort(bind),
	}
}
