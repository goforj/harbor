package hostconflict

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var testCandidate = netip.MustParseAddr("127.77.0.10")

// TestNewPreAssignmentRequestCanonicalizesRequirements verifies TCP and UDP requirements retain same-port capabilities in stable order.
func TestNewPreAssignmentRequestCanonicalizesRequirements(t *testing.T) {
	input := []SocketRequirement{
		{Transport: TransportUDP4, Port: 53},
		{Transport: TransportTCP4, Port: 443},
		{Transport: TransportTCP4, Port: 53},
	}
	request, err := NewPreAssignmentRequest(testCandidate, input)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	want := []SocketRequirement{
		{Transport: TransportTCP4, Port: 53},
		{Transport: TransportTCP4, Port: 443},
		{Transport: TransportUDP4, Port: 53},
	}
	if got := request.Requirements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Requirements() = %#v, want %#v", got, want)
	}
	if request.Candidate() != testCandidate {
		t.Fatalf("Candidate() = %s, want %s", request.Candidate(), testCandidate)
	}
	if request.Purpose() != PurposePreAssignment {
		t.Fatalf("Purpose() = %q, want %q", request.Purpose(), PurposePreAssignment)
	}
	input[0] = SocketRequirement{}
	returned := request.Requirements()
	returned[0] = SocketRequirement{}
	if !reflect.DeepEqual(request.Requirements(), want) {
		t.Fatal("Request retained caller-owned requirement storage")
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestNewPreAssignmentRequestRejectsInvalidCandidates covers every noncanonical candidate family and zero values.
func TestNewPreAssignmentRequestRejectsInvalidCandidates(t *testing.T) {
	tests := []netip.Addr{
		{},
		netip.MustParseAddr("192.0.2.1"),
		netip.IPv6Loopback(),
		netip.MustParseAddr("::ffff:127.77.0.10"),
	}
	for _, candidate := range tests {
		if request, err := NewPreAssignmentRequest(candidate, nil); err == nil {
			t.Fatalf("NewPreAssignmentRequest(%s) = %#v, want error", candidate, request)
		}
	}
}

// TestNewPreAssignmentRequestAcceptsRouteOnlyAdmission makes an empty capability set an explicit pool-admission contract.
func TestNewPreAssignmentRequestAcceptsRouteOnlyAdmission(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	if len(request.Requirements()) != 0 {
		t.Fatalf("Requirements() = %#v, want empty", request.Requirements())
	}
}

// TestNewPreAssignmentRequestRejectsInvalidRequirements proves duplicates and unsupported or zero capabilities cannot disappear during sorting.
func TestNewPreAssignmentRequestRejectsInvalidRequirements(t *testing.T) {
	tests := []struct {
		name         string
		requirements []SocketRequirement
	}{
		{name: "duplicate", requirements: []SocketRequirement{{Transport: TransportTCP4, Port: 80}, {Transport: TransportTCP4, Port: 80}}},
		{name: "unknown transport", requirements: []SocketRequirement{{Transport: "sctp4", Port: 80}}},
		{name: "zero port", requirements: []SocketRequirement{{Transport: TransportUDP4}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if request, err := NewPreAssignmentRequest(testCandidate, test.requirements); err == nil {
				t.Fatalf("NewPreAssignmentRequest() = %#v, want error", request)
			}
		})
	}

	tooMany := make([]SocketRequirement, MaximumSocketRequirements+1)
	for index := range tooMany {
		tooMany[index] = SocketRequirement{Transport: TransportTCP4, Port: uint16(index + 1)}
	}
	if request, err := NewPreAssignmentRequest(testCandidate, tooMany); err == nil {
		t.Fatalf("NewPreAssignmentRequest() = %#v with %d requirements, want error", request, len(tooMany))
	}
}

// TestRequestValidateRejectsNoncanonicalInternalState guards the immutable request invariant against zero or package-local corruption.
func TestRequestValidateRejectsNoncanonicalInternalState(t *testing.T) {
	valid := mustRequest(t)
	tests := []Request{
		{},
		{purpose: PurposePreAssignment, candidate: testCandidate, requirements: []SocketRequirement{{Transport: TransportTCP4}}},
		{purpose: PurposePreAssignment, candidate: testCandidate, requirements: []SocketRequirement{{Transport: TransportUDP4, Port: 53}, {Transport: TransportTCP4, Port: 53}}},
		{purpose: PurposePreAssignment, candidate: testCandidate, requirements: []SocketRequirement{{Transport: TransportTCP4, Port: 53}, {Transport: TransportTCP4, Port: 53}}},
	}
	oversized := valid
	oversized.requirements = make([]SocketRequirement, MaximumSocketRequirements+1)
	tests = append(tests, oversized)
	for index, request := range tests {
		if err := request.Validate(); err == nil {
			t.Errorf("request %d Validate() error = nil", index)
		}
	}
}

// TestNetworkScopeConstructorsAndValidation covers canonical scope identities and cross-platform rejection.
func TestNetworkScopeConstructorsAndValidation(t *testing.T) {
	linux, err := NewLinuxScope(4, 4026531840)
	if err != nil {
		t.Fatalf("NewLinuxScope() error = %v", err)
	}
	windows, err := NewWindowsScope(1)
	if err != nil {
		t.Fatalf("NewWindowsScope() error = %v", err)
	}
	for _, scope := range []NetworkScope{linux, NewMacOSScope(), windows} {
		if err := scope.Validate(); err != nil {
			t.Errorf("%s scope Validate() error = %v", scope.Platform, err)
		}
	}

	linuxID := LinuxNamespaceIdentity{Device: 4, Inode: 9}
	windowsID := WindowsCompartmentIdentity{ID: 1}
	invalid := []NetworkScope{
		{},
		{Platform: PlatformLinux},
		{Platform: PlatformLinux, LinuxNamespace: &LinuxNamespaceIdentity{Inode: 1}},
		{Platform: PlatformLinux, LinuxNamespace: &linuxID, WindowsCompartment: &windowsID},
		{Platform: PlatformMacOS, LinuxNamespace: &linuxID},
		{Platform: PlatformMacOS, WindowsCompartment: &windowsID},
		{Platform: PlatformWindows},
		{Platform: PlatformWindows, WindowsCompartment: &WindowsCompartmentIdentity{}},
		{Platform: PlatformWindows, WindowsCompartment: &windowsID, LinuxNamespace: &linuxID},
	}
	for index, scope := range invalid {
		if err := scope.Validate(); err == nil {
			t.Errorf("invalid scope %d Validate() error = nil", index)
		}
	}
	if scope, err := NewLinuxScope(0, 1); err == nil {
		t.Fatalf("NewLinuxScope() = %#v, want error", scope)
	}
	if scope, err := NewWindowsScope(0); err == nil {
		t.Fatalf("NewWindowsScope() = %#v, want error", scope)
	}
}

// TestInterfaceAndLoopbackValidation covers bounded names and platform-specific loopback proofs.
func TestInterfaceAndLoopbackValidation(t *testing.T) {
	valid := InterfaceIdentity{Name: "Loopback Pseudo-Interface 1", Index: 12}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for _, identity := range []InterfaceIdentity{
		{Name: valid.Name},
		{Index: 1},
		{Name: " lo", Index: 1},
		{Name: "lo\n", Index: 1},
		{Name: string([]byte{0xff}), Index: 1},
		{Name: strings.Repeat("x", maximumInterfaceName+1), Index: 1},
	} {
		if err := identity.Validate(); err == nil {
			t.Errorf("InterfaceIdentity %#v Validate() error = nil", identity)
		}
	}

	tests := []struct {
		platform Platform
		kind     LoopbackKind
		luid     uint64
	}{
		{platform: PlatformLinux, kind: LoopbackKindLinuxNative},
		{platform: PlatformMacOS, kind: LoopbackKindMacOSNative},
		{platform: PlatformWindows, kind: LoopbackKindWindowsSoftware, luid: 48},
	}
	for _, test := range tests {
		platformInterface := valid
		platformInterface.WindowsLUID = test.luid
		identity := LoopbackIdentity{Interface: platformInterface, Kind: test.kind}
		if err := identity.Validate(test.platform); err != nil {
			t.Errorf("Validate(%s) error = %v", test.platform, err)
		}
		identity.Kind = LoopbackKind("wrong")
		if err := identity.Validate(test.platform); err == nil {
			t.Errorf("Validate(%s) with wrong kind error = nil", test.platform)
		}
	}
	if err := (LoopbackIdentity{Interface: valid}).Validate("plan9"); err == nil {
		t.Fatal("Validate() with unsupported platform error = nil")
	}
	if err := (LoopbackIdentity{Interface: valid, Kind: LoopbackKindWindowsSoftware}).Validate(PlatformWindows); err == nil {
		t.Fatal("Validate() with missing Windows LUID error = nil")
	}
	foreign := valid
	foreign.WindowsLUID = 48
	if err := (LoopbackIdentity{Interface: foreign, Kind: LoopbackKindLinuxNative}).Validate(PlatformLinux); err == nil {
		t.Fatal("Validate() with Windows LUID on Linux error = nil")
	}
}

// TestObservationValidateRejectsMalformedRouteFacts exercises every route structural boundary.
func TestObservationValidateRejectsMalformedRouteFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "complete truncated", mutate: func(observation *Observation) { observation.Routes.Truncated = true }},
		{name: "missing selected", mutate: func(observation *Observation) { observation.Routes.Selected = nil }},
		{name: "selected absent", mutate: func(observation *Observation) {
			selected := foreignDefaultRoute()
			selected.Interface = InterfaceIdentity{Name: "ethernet9", Index: 9}
			observation.Routes.Selected = &selected
		}},
		{name: "invalid destination", mutate: func(observation *Observation) { observation.Routes.Matching[0].Destination = netip.Prefix{} }},
		{name: "IPv6 destination", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Destination = netip.MustParsePrefix("::1/128")
		}},
		{name: "mapped destination", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Destination = netip.PrefixFrom(netip.MustParseAddr("::ffff:127.0.0.0"), 120)
		}},
		{name: "unmasked destination", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Destination = netip.PrefixFrom(netip.MustParseAddr("127.77.0.1"), 8)
		}},
		{name: "nonmatching destination", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Destination = netip.MustParsePrefix("126.0.0.0/8")
		}},
		{name: "invalid interface", mutate: func(observation *Observation) { observation.Routes.Matching[0].Interface.Index = 0 }},
		{name: "native mismatch", mutate: func(observation *Observation) { observation.Routes.Matching[0].NativeLoopback = false }},
		{name: "invalid gateway family", mutate: func(observation *Observation) { observation.Routes.Matching[0].Gateway = netip.IPv6Loopback() }},
		{name: "mapped gateway", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Gateway = netip.MustParseAddr("::ffff:192.0.2.1")
		}},
		{name: "unspecified gateway", mutate: func(observation *Observation) { observation.Routes.Matching[0].Gateway = netip.IPv4Unspecified() }},
		{name: "unknown normalization", mutate: func(observation *Observation) { observation.Routes.Matching[0].Normalization = "unknown" }},
		{name: "mac clone on Linux", mutate: func(observation *Observation) {
			observation.Routes.Matching[0].Destination = netip.MustParsePrefix("127.77.0.10/32")
			observation.Routes.Matching[0].Normalization = RouteNormalizationMacOSCloneUnresolved
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := safeLinuxObservation(t)
			test.mutate(&observation)
			if observation.Routes.Selected != nil && len(observation.Routes.Matching) > 0 && test.name != "selected absent" && test.name != "missing selected" {
				selected := observation.Routes.Matching[0]
				observation.Routes.Selected = &selected
			}
			if err := observation.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}

	observation := safeLinuxObservation(t)
	observation.Routes.Matching = make([]RouteFact, maximumRouteFacts+1)
	for index := range observation.Routes.Matching {
		observation.Routes.Matching[index] = baselineRoute(observation.Loopback.Interface)
	}
	selected := observation.Routes.Matching[0]
	observation.Routes.Selected = &selected
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with too many route facts error = nil")
	}

	windowsObservation := safeWindowsObservation(t)
	windowsObservation.Routes.Matching[0].Interface.WindowsLUID = 0
	selected = windowsObservation.Routes.Matching[0]
	windowsObservation.Routes.Selected = &selected
	if err := windowsObservation.Validate(); err == nil {
		t.Fatal("Validate() with missing Windows route LUID error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Routes.Matching[0].Interface.WindowsLUID = 48
	selected = observation.Routes.Matching[0]
	observation.Routes.Selected = &selected
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with Windows route LUID on Linux error = nil")
	}
}

// TestObservationTreatsInterfaceNamesAsDisplayEvidence keeps native identity authoritative across alias changes.
func TestObservationTreatsInterfaceNamesAsDisplayEvidence(t *testing.T) {
	observation := safeWindowsObservation(t)
	observation.Routes.Matching[0].Interface.Name = "Renamed Loopback"
	selected := observation.Routes.Matching[0]
	observation.Routes.Selected = &selected
	if err := observation.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestObservationValidateRejectsMalformedCloneShapes keeps unresolved macOS normalization explicitly fail-closed.
func TestObservationValidateRejectsMalformedCloneShapes(t *testing.T) {
	for _, mutate := range []func(*RouteFact){
		func(fact *RouteFact) { fact.Destination = netip.MustParsePrefix("127.0.0.0/8") },
		func(fact *RouteFact) {
			fact.Interface = InterfaceIdentity{Name: "en0", Index: 2}
			fact.NativeLoopback = false
		},
		func(fact *RouteFact) { fact.Gateway = netip.MustParseAddr("192.0.2.1") },
	} {
		observation := safeMacOSObservation(t)
		clone := RouteFact{
			Destination:    netip.MustParsePrefix("127.77.0.10/32"),
			Interface:      observation.Loopback.Interface,
			NativeLoopback: true,
			Normalization:  RouteNormalizationMacOSCloneUnresolved,
		}
		mutate(&clone)
		observation.Routes.Matching = append(observation.Routes.Matching, clone)
		observation.Routes.Selected = &clone
		if err := observation.Validate(); err == nil {
			t.Fatal("Validate() with malformed unresolved clone error = nil")
		}
	}
}

// TestObservationValidateRejectsMalformedSocketFacts exercises socket family, protocol, state, and request constraints.
func TestObservationValidateRejectsMalformedSocketFacts(t *testing.T) {
	tests := []struct {
		name string
		fact SocketFact
	}{
		{name: "unknown protocol", fact: SocketFact{Protocol: "sctp", Address: testCandidate, Port: 443, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "zero port", fact: SocketFact{Protocol: SocketProtocolTCP, Address: testCandidate, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "unrequested", fact: SocketFact{Protocol: SocketProtocolTCP, Address: testCandidate, Port: 80, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "invalid address", fact: SocketFact{Protocol: SocketProtocolTCP, Port: 443, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "mapped address", fact: SocketFact{Protocol: SocketProtocolTCP, Address: netip.MustParseAddr("::ffff:127.77.0.10"), Port: 443, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "zoned address", fact: SocketFact{Protocol: SocketProtocolTCP, Address: netip.MustParseAddr("fe80::1%eth0"), Port: 443, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "UDP accepting", fact: SocketFact{Protocol: SocketProtocolUDP, Address: testCandidate, Port: 53, TCPAccepting: true, IPv6Only: IPv6OnlyNotApplicable}},
		{name: "IPv6 wildcard missing state", fact: SocketFact{Protocol: SocketProtocolTCP, Address: netip.IPv6Unspecified(), Port: 443}},
		{name: "IPv6 wildcard invalid state", fact: SocketFact{Protocol: SocketProtocolTCP, Address: netip.IPv6Unspecified(), Port: 443, IPv6Only: "invalid"}},
		{name: "IPv6 state on ordinary address", fact: SocketFact{Protocol: SocketProtocolTCP, Address: netip.IPv6Loopback(), Port: 443, IPv6Only: IPv6OnlyEnabled}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := safeLinuxObservation(t)
			observation.Sockets.Endpoints = []SocketFact{test.fact}
			if err := observation.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}

	observation := safeLinuxObservation(t)
	observation.Sockets.Complete = true
	observation.Sockets.Truncated = true
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with complete truncated sockets error = nil")
	}
	observation = safeLinuxObservation(t)
	observation.Sockets.Endpoints = make([]SocketFact, maximumSocketFacts+1)
	for index := range observation.Sockets.Endpoints {
		observation.Sockets.Endpoints[index] = SocketFact{Protocol: SocketProtocolTCP, Address: netip.IPv6Loopback(), Port: 443, IPv6Only: IPv6OnlyNotApplicable}
	}
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with too many socket facts error = nil")
	}
}

// TestObservationValidateRejectsMalformedPolicyFacts covers completeness, bounds, duplicates, and platform isolation.
func TestObservationValidateRejectsMalformedPolicyFacts(t *testing.T) {
	observation := safeLinuxObservation(t)
	observation.Policy.Linux = nil
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() without Linux policy error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Policy.Linux.Truncated = true
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with complete truncated Linux policy error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Policy.Linux.RouteLocalnet = nil
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with omitted loopback policy error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Policy.Linux.RouteLocalnet = append(observation.Policy.Linux.RouteLocalnet, observation.Policy.Linux.RouteLocalnet[0])
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with duplicate policy interface error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Policy.Linux.RouteLocalnet[0].Interface.Index = 0
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with invalid policy interface error = nil")
	}

	observation = safeLinuxObservation(t)
	observation.Policy.Linux.RouteLocalnet = make([]RouteLocalnetFact, maximumPolicyInterfaces+1)
	for index := range observation.Policy.Linux.RouteLocalnet {
		observation.Policy.Linux.RouteLocalnet[index] = RouteLocalnetFact{Interface: InterfaceIdentity{Name: "eth" + strings.Repeat("x", index%10), Index: uint32(index + 1)}}
	}
	if err := observation.Validate(); err == nil {
		t.Fatal("Validate() with too many policy facts error = nil")
	}

	for _, other := range []Observation{safeMacOSObservation(t), safeWindowsObservation(t)} {
		other.Policy.Linux = &LinuxPolicyFacts{}
		if err := other.Validate(); err == nil {
			t.Errorf("%s Validate() with Linux policy error = nil", other.Scope.Platform)
		}
	}
}

// mustRequest returns a representative dual-protocol request.
func mustRequest(t *testing.T) Request {
	t.Helper()
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{
		{Transport: TransportTCP4, Port: 443},
		{Transport: TransportTCP4, Port: 53},
		{Transport: TransportUDP4, Port: 53},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	return request
}

// safeLinuxObservation returns one complete conflict-free Linux fact set.
func safeLinuxObservation(t *testing.T) Observation {
	t.Helper()
	scope, err := NewLinuxScope(4, 4026531840)
	if err != nil {
		t.Fatalf("NewLinuxScope() error = %v", err)
	}
	loopback := LoopbackIdentity{
		Interface: InterfaceIdentity{Name: "lo", Index: 1},
		Kind:      LoopbackKindLinuxNative,
	}
	baseline := baselineRoute(loopback.Interface)
	return Observation{
		Request:  mustRequest(t),
		Scope:    scope,
		Loopback: loopback,
		Routes: RouteSnapshot{
			Complete: true,
			Selected: &baseline,
			Matching: []RouteFact{baseline, foreignDefaultRoute()},
		},
		Sockets: SocketSnapshot{Complete: true},
		Policy: PolicyFacts{Linux: &LinuxPolicyFacts{
			Complete: true,
			RouteLocalnet: []RouteLocalnetFact{
				{Interface: loopback.Interface, Enabled: true},
				{Interface: InterfaceIdentity{Name: "eth0", Index: 2}},
			},
		}},
	}
}

// safeMacOSObservation returns one complete conflict-free macOS fact set.
func safeMacOSObservation(t *testing.T) Observation {
	t.Helper()
	loopback := LoopbackIdentity{
		Interface: InterfaceIdentity{Name: "lo0", Index: 1},
		Kind:      LoopbackKindMacOSNative,
	}
	baseline := baselineRoute(loopback.Interface)
	return Observation{
		Request:  mustRequest(t),
		Scope:    NewMacOSScope(),
		Loopback: loopback,
		Routes:   RouteSnapshot{Complete: true, Selected: &baseline, Matching: []RouteFact{baseline}},
		Sockets:  SocketSnapshot{Complete: true},
	}
}

// safeWindowsObservation returns one complete conflict-free Windows fact set.
func safeWindowsObservation(t *testing.T) Observation {
	t.Helper()
	scope, err := NewWindowsScope(1)
	if err != nil {
		t.Fatalf("NewWindowsScope() error = %v", err)
	}
	loopback := LoopbackIdentity{
		Interface: InterfaceIdentity{Name: "Loopback Pseudo-Interface 1", Index: 12, WindowsLUID: 48},
		Kind:      LoopbackKindWindowsSoftware,
	}
	baseline := baselineRoute(loopback.Interface)
	return Observation{
		Request:  mustRequest(t),
		Scope:    scope,
		Loopback: loopback,
		Routes:   RouteSnapshot{Complete: true, Selected: &baseline, Matching: []RouteFact{baseline}},
		Sockets:  SocketSnapshot{Complete: true},
	}
}

// baselineRoute returns the accepted ordinary native-loopback route.
func baselineRoute(loopback InterfaceIdentity) RouteFact {
	return RouteFact{
		Destination:    netip.MustParsePrefix("127.0.0.0/8"),
		Interface:      loopback,
		NativeLoopback: true,
		Normalization:  RouteNormalizationDirect,
	}
}

// foreignDefaultRoute returns a harmless less-specific route fact.
func foreignDefaultRoute() RouteFact {
	return RouteFact{
		Destination:   netip.MustParsePrefix("0.0.0.0/0"),
		Interface:     InterfaceIdentity{Name: "eth0", Index: 2},
		Gateway:       netip.MustParseAddr("192.0.2.1"),
		Normalization: RouteNormalizationDirect,
	}
}

// cloneObservation copies every slice and optional fact so table mutations remain isolated.
func cloneObservation(observation Observation) Observation {
	clone := observation
	clone.Request.requirements = slices.Clone(observation.Request.requirements)
	clone.Routes.Matching = slices.Clone(observation.Routes.Matching)
	if observation.Routes.Selected != nil {
		selected := *observation.Routes.Selected
		clone.Routes.Selected = &selected
	}
	clone.Sockets.Endpoints = slices.Clone(observation.Sockets.Endpoints)
	if observation.Scope.LinuxNamespace != nil {
		identity := *observation.Scope.LinuxNamespace
		clone.Scope.LinuxNamespace = &identity
	}
	if observation.Scope.WindowsCompartment != nil {
		identity := *observation.Scope.WindowsCompartment
		clone.Scope.WindowsCompartment = &identity
	}
	if observation.Policy.Linux != nil {
		facts := *observation.Policy.Linux
		facts.RouteLocalnet = slices.Clone(observation.Policy.Linux.RouteLocalnet)
		clone.Policy.Linux = &facts
	}
	return clone
}
