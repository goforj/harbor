package hostconflict

import (
	"encoding/hex"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"testing"
)

// TestFingerprintCanonicalizesNilAndEnumerationOrder proves host enumeration details do not alter evidence.
func TestFingerprintCanonicalizesNilAndEnumerationOrder(t *testing.T) {
	left := safeLinuxObservation(t)
	left.Sockets.Endpoints = []SocketFact{
		tcpFact(netip.MustParseAddr("127.77.0.11"), true),
		udpFact(netip.MustParseAddr("127.77.0.12"), 53),
	}
	right := cloneObservation(left)
	slices.Reverse(right.Routes.Matching)
	slices.Reverse(right.Sockets.Endpoints)
	slices.Reverse(right.Policy.Linux.RouteLocalnet)
	before := cloneObservation(right)
	assertSameFingerprint(t, left, right)
	if !reflect.DeepEqual(right, before) {
		t.Fatal("Fingerprint() reordered caller-owned facts")
	}

	empty := safeLinuxObservation(t)
	empty.Sockets.Endpoints = []SocketFact{}
	nilFacts := safeLinuxObservation(t)
	nilFacts.Sockets.Endpoints = nil
	assertSameFingerprint(t, empty, nilFacts)

	empty.Routes.Matching = empty.Routes.Matching[:0]
	empty.Routes.Complete = false
	empty.Routes.Selected = nil
	nilFacts.Routes.Matching = nil
	nilFacts.Routes.Complete = false
	nilFacts.Routes.Selected = nil
	assertSameFingerprint(t, empty, nilFacts)
}

// TestFingerprintIncludesEveryAuthorityFact checks independent fact sensitivity without process diagnostics.
func TestFingerprintIncludesEveryAuthorityFact(t *testing.T) {
	reference := safeLinuxObservation(t)
	reference.Sockets.Endpoints = []SocketFact{tcpFact(netip.MustParseAddr("127.77.0.11"), true)}
	referenceFingerprint := mustFingerprint(t, reference)
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "candidate", mutate: func(observation *Observation) {
			replaceCandidate(t, observation, netip.MustParseAddr("127.77.0.11"))
			observation.Sockets.Endpoints[0].Address = netip.MustParseAddr("127.77.0.12")
		}},
		{name: "requirements", mutate: func(observation *Observation) {
			replaceRequirements(t, observation, append(observation.Request.Requirements(), SocketRequirement{Transport: TransportUDP4, Port: 5353}))
		}},
		{name: "scope device", mutate: func(observation *Observation) { observation.Scope.LinuxNamespace.Device++ }},
		{name: "scope inode", mutate: func(observation *Observation) { observation.Scope.LinuxNamespace.Inode++ }},
		{name: "loopback name", mutate: func(observation *Observation) {
			replaceLoopback(observation, InterfaceIdentity{Name: "loopback0", Index: 1})
		}},
		{name: "loopback index", mutate: func(observation *Observation) { replaceLoopback(observation, InterfaceIdentity{Name: "lo", Index: 10}) }},
		{name: "route completeness", mutate: func(observation *Observation) { observation.Routes.Complete = false }},
		{name: "route truncation", mutate: func(observation *Observation) {
			observation.Routes.Complete = false
			observation.Routes.Truncated = true
		}},
		{name: "selected route", mutate: func(observation *Observation) {
			selected := foreignDefaultRoute()
			observation.Routes.Matching = append(observation.Routes.Matching, selected)
			observation.Routes.Selected = &selected
		}},
		{name: "matching route multiplicity", mutate: func(observation *Observation) {
			observation.Routes.Matching = append(observation.Routes.Matching, foreignDefaultRoute())
		}},
		{name: "route destination", mutate: func(observation *Observation) {
			observation.Routes.Matching[1].Destination = netip.MustParsePrefix("0.0.0.0/1")
		}},
		{name: "route interface", mutate: func(observation *Observation) { observation.Routes.Matching[1].Interface.Name = "ethernet0" }},
		{name: "route gateway", mutate: func(observation *Observation) {
			observation.Routes.Matching[1].Gateway = netip.MustParseAddr("192.0.2.2")
		}},
		{name: "socket completeness", mutate: func(observation *Observation) { observation.Sockets.Complete = false }},
		{name: "socket truncation", mutate: func(observation *Observation) {
			observation.Sockets.Complete = false
			observation.Sockets.Truncated = true
		}},
		{name: "socket protocol", mutate: func(observation *Observation) {
			observation.Sockets.Endpoints[0] = udpFact(observation.Sockets.Endpoints[0].Address, 53)
		}},
		{name: "socket address", mutate: func(observation *Observation) {
			observation.Sockets.Endpoints[0].Address = netip.MustParseAddr("127.77.0.12")
		}},
		{name: "socket port", mutate: func(observation *Observation) { observation.Sockets.Endpoints[0].Port = 53 }},
		{name: "TCP accepting", mutate: func(observation *Observation) { observation.Sockets.Endpoints[0].TCPAccepting = false }},
		{name: "policy completeness", mutate: func(observation *Observation) { observation.Policy.Linux.Complete = false }},
		{name: "policy truncation", mutate: func(observation *Observation) {
			observation.Policy.Linux.Complete = false
			observation.Policy.Linux.Truncated = true
		}},
		{name: "ip_nonlocal_bind", mutate: func(observation *Observation) { observation.Policy.Linux.IPNonlocalBind = true }},
		{name: "route_localnet interface", mutate: func(observation *Observation) { observation.Policy.Linux.RouteLocalnet[1].Interface.Name = "enp0s1" }},
		{name: "route_localnet enabled", mutate: func(observation *Observation) { observation.Policy.Linux.RouteLocalnet[1].Enabled = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := cloneObservation(reference)
			test.mutate(&observation)
			fingerprint, err := observation.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint() error = %v", err)
			}
			if fingerprint == referenceFingerprint {
				t.Fatalf("Fingerprint() omitted %s", test.name)
			}
		})
	}
}

// TestFingerprintIncludesPlatformSpecificScopeAndIPv6Facts covers encodings not present in the Linux reference.
func TestFingerprintIncludesPlatformSpecificScopeAndIPv6Facts(t *testing.T) {
	windows := safeWindowsObservation(t)
	reference := mustFingerprint(t, windows)
	windows.Scope.WindowsCompartment.ID++
	if fingerprint := mustFingerprint(t, windows); fingerprint == reference {
		t.Fatal("Fingerprint() omitted Windows compartment")
	}
	windows = safeWindowsObservation(t)
	reference = mustFingerprint(t, windows)
	windows.Loopback.Interface.WindowsLUID++
	windows.Routes.Matching[0].Interface.WindowsLUID++
	selected := windows.Routes.Matching[0]
	windows.Routes.Selected = &selected
	if fingerprint := mustFingerprint(t, windows); fingerprint == reference {
		t.Fatal("Fingerprint() omitted Windows interface LUID")
	}

	macOS := safeMacOSObservation(t)
	macOS.Sockets.Endpoints = []SocketFact{tcpIPv6Wildcard(IPv6OnlyEnabled, true)}
	reference = mustFingerprint(t, macOS)
	macOS.Sockets.Endpoints[0].IPv6Only = IPv6OnlyDisabled
	if fingerprint := mustFingerprint(t, macOS); fingerprint == reference {
		t.Fatal("Fingerprint() omitted IPv6-only state")
	}

	clone := RouteFact{
		Destination:    netip.MustParsePrefix("127.77.0.10/32"),
		Interface:      macOS.Loopback.Interface,
		NativeLoopback: true,
		Normalization:  RouteNormalizationMacOSCloneUnresolved,
	}
	macOS.Routes.Matching = append(macOS.Routes.Matching, clone)
	macOS.Routes.Selected = &clone
	if fingerprint := mustFingerprint(t, macOS); fingerprint == reference {
		t.Fatal("Fingerprint() omitted route normalization")
	}
}

// TestFingerprintDoesNotAuthorizeUnsafeEvidence proves a stable matching digest cannot replace classification.
func TestFingerprintDoesNotAuthorizeUnsafeEvidence(t *testing.T) {
	observation := safeLinuxObservation(t)
	observation.Sockets.Endpoints = []SocketFact{tcpFact(testCandidate, true)}
	first := mustFingerprint(t, observation)
	second := mustFingerprint(t, cloneObservation(observation))
	if first != second {
		t.Fatalf("Fingerprint() = %q and %q, want stable", first, second)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if assessment.State != StateConflict {
		t.Fatalf("Classify().State = %q, want conflict", assessment.State)
	}
}

// TestFingerprintRejectsInvalidFacts ensures malformed observations cannot acquire compare-and-swap evidence.
func TestFingerprintRejectsInvalidFacts(t *testing.T) {
	observation := safeLinuxObservation(t)
	observation.Sockets.Endpoints = []SocketFact{{Protocol: SocketProtocolTCP, Address: testCandidate, Port: 80, IPv6Only: IPv6OnlyNotApplicable}}
	if fingerprint, err := observation.Fingerprint(); err == nil {
		t.Fatalf("Fingerprint() = %q, want error", fingerprint)
	}
}

// TestFingerprintKnownVector fixes the v2 canonical encoding across platforms and implementations.
func TestFingerprintKnownVector(t *testing.T) {
	fingerprint := mustFingerprint(t, safeLinuxObservation(t))
	const want = "055a2bb2d1895c5e1dfd536cd3360d69a6e8c784110ce6594956814d5ced6016"
	if fingerprint != want {
		t.Fatalf("Fingerprint() = %q, want %q", fingerprint, want)
	}
}

// TestFingerprintOutputGrammar keeps evidence bounded to lowercase raw SHA-256 hexadecimal.
func TestFingerprintOutputGrammar(t *testing.T) {
	fingerprint := mustFingerprint(t, safeWindowsObservation(t))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fingerprint) {
		t.Fatalf("Fingerprint() = %q, want lowercase SHA-256 hex", fingerprint)
	}
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("hex.DecodeString() length = %d, error = %v", len(decoded), err)
	}
}

// assertSameFingerprint compares canonical digests with useful failure context.
func assertSameFingerprint(t *testing.T, left Observation, right Observation) {
	t.Helper()
	leftFingerprint := mustFingerprint(t, left)
	rightFingerprint := mustFingerprint(t, right)
	if leftFingerprint != rightFingerprint {
		t.Fatalf("Fingerprint() = %q and %q, want equal", leftFingerprint, rightFingerprint)
	}
}

// mustFingerprint returns a digest or fails the current test.
func mustFingerprint(t *testing.T, observation Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	return fingerprint
}

// replaceCandidate reconstructs the immutable request while preserving requirements.
func replaceCandidate(t *testing.T, observation *Observation, candidate netip.Addr) {
	t.Helper()
	request, err := NewPreAssignmentRequest(candidate, observation.Request.Requirements())
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation.Request = request
}

// replaceRequirements reconstructs the immutable request with a new capability set.
func replaceRequirements(t *testing.T, observation *Observation, requirements []SocketRequirement) {
	t.Helper()
	request, err := NewPreAssignmentRequest(observation.Request.Candidate(), requirements)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	observation.Request = request
}

// replaceLoopback updates every fact whose authority is tied to the selected loopback identity.
func replaceLoopback(observation *Observation, identity InterfaceIdentity) {
	original := observation.Loopback.Interface
	observation.Loopback.Interface = identity
	for index := range observation.Routes.Matching {
		if observation.Routes.Matching[index].Interface == original {
			observation.Routes.Matching[index].Interface = identity
		}
	}
	if observation.Routes.Selected != nil && observation.Routes.Selected.Interface == original {
		observation.Routes.Selected.Interface = identity
	}
	if observation.Policy.Linux != nil {
		for index := range observation.Policy.Linux.RouteLocalnet {
			if observation.Policy.Linux.RouteLocalnet[index].Interface == original {
				observation.Policy.Linux.RouteLocalnet[index].Interface = identity
			}
		}
	}
}
