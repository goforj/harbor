package hostconflict

import (
	"net/netip"
	"testing"
)

// TestClassifyRoutesCoversSafeConflictAndIndeterminateShapes exercises every route admission class.
func TestClassifyRoutesCoversSafeConflictAndIndeterminateShapes(t *testing.T) {
	tests := []struct {
		name   string
		base   func(*testing.T) Observation
		mutate func(*Observation)
		want   State
	}{
		{name: "ordinary baseline", base: safeLinuxObservation, mutate: func(*Observation) {}, want: StateSafe},
		{name: "selected foreign interface", base: safeLinuxObservation, mutate: func(observation *Observation) {
			selected := RouteFact{
				Destination:   netip.MustParsePrefix("127.0.0.0/8"),
				Interface:     InterfaceIdentity{Name: "eth0", Index: 2},
				Normalization: RouteNormalizationDirect,
			}
			observation.Routes.Matching = []RouteFact{selected}
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "selected gateway", base: safeLinuxObservation, mutate: func(observation *Observation) {
			selected := baselineRoute(observation.Loopback.Interface)
			selected.Gateway = netip.MustParseAddr("192.0.2.1")
			observation.Routes.Matching = []RouteFact{selected}
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "unexplained host route", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Matching = append(observation.Routes.Matching, RouteFact{
				Destination:    netip.MustParsePrefix("127.77.0.10/32"),
				Interface:      observation.Loopback.Interface,
				NativeLoopback: true,
				Normalization:  RouteNormalizationDirect,
			})
		}, want: StateConflict},
		{name: "selected unexplained host route", base: safeLinuxObservation, mutate: func(observation *Observation) {
			selected := RouteFact{
				Destination:    netip.MustParsePrefix("127.77.0.10/32"),
				Interface:      observation.Loopback.Interface,
				NativeLoopback: true,
				Normalization:  RouteNormalizationDirect,
			}
			observation.Routes.Matching = append(observation.Routes.Matching, selected)
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "multiple baselines", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Matching = append(observation.Routes.Matching, baselineRoute(observation.Loopback.Interface))
		}, want: StateConflict},
		{name: "missing baseline complete", base: safeLinuxObservation, mutate: func(observation *Observation) {
			selected := foreignDefaultRoute()
			observation.Routes.Matching = []RouteFact{selected}
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "duplicate selected default", base: safeLinuxObservation, mutate: func(observation *Observation) {
			selected := foreignDefaultRoute()
			observation.Routes.Matching = []RouteFact{baselineRoute(observation.Loopback.Interface), selected, selected}
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "incomplete", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Complete = false
		}, want: StateIndeterminate},
		{name: "truncated", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Complete = false
			observation.Routes.Truncated = true
		}, want: StateIndeterminate},
		{name: "incomplete without selection", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Complete = false
			observation.Routes.Selected = nil
			observation.Routes.Matching = nil
		}, want: StateIndeterminate},
		{name: "incomplete still proves conflict", base: safeLinuxObservation, mutate: func(observation *Observation) {
			observation.Routes.Complete = false
			selected := foreignDefaultRoute()
			observation.Routes.Matching = append(observation.Routes.Matching, selected)
			observation.Routes.Selected = &selected
		}, want: StateConflict},
		{name: "unresolved macOS clone", base: safeMacOSObservation, mutate: func(observation *Observation) {
			clone := RouteFact{
				Destination:    netip.MustParsePrefix("127.77.0.10/32"),
				Interface:      observation.Loopback.Interface,
				NativeLoopback: true,
				Normalization:  RouteNormalizationMacOSCloneUnresolved,
			}
			observation.Routes.Matching = append(observation.Routes.Matching, clone)
			observation.Routes.Selected = &clone
		}, want: StateIndeterminate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := test.base(t)
			test.mutate(&observation)
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.Routes != test.want {
				t.Fatalf("Classify().Routes = %q, want %q", assessment.Routes, test.want)
			}
		})
	}
}

// TestClassifySocketsCoversExactAndWildcardConflicts exercises TCP accept state, every UDP bind, and IPv6-only proof.
func TestClassifySocketsCoversExactAndWildcardConflicts(t *testing.T) {
	tests := []struct {
		name     string
		fact     SocketFact
		complete bool
		truncate bool
		want     State
	}{
		{name: "no endpoints", complete: true, want: StateSafe},
		{name: "TCP exact listener", complete: true, fact: tcpFact(testCandidate, true), want: StateConflict},
		{name: "TCP exact nonlistener", complete: true, fact: tcpFact(testCandidate, false), want: StateSafe},
		{name: "TCP IPv4 wildcard listener", complete: true, fact: tcpFact(netip.IPv4Unspecified(), true), want: StateConflict},
		{name: "TCP other IPv4 listener", complete: true, fact: tcpFact(netip.MustParseAddr("127.77.0.11"), true), want: StateSafe},
		{name: "TCP IPv6 wildcard v6 only", complete: true, fact: tcpIPv6Wildcard(IPv6OnlyEnabled, true), want: StateSafe},
		{name: "TCP IPv6 wildcard dual stack", complete: true, fact: tcpIPv6Wildcard(IPv6OnlyDisabled, true), want: StateConflict},
		{name: "TCP IPv6 wildcard unknown", complete: true, fact: tcpIPv6Wildcard(IPv6OnlyUnknown, true), want: StateConflict},
		{name: "TCP IPv6 wildcard nonlistener", complete: true, fact: tcpIPv6Wildcard(IPv6OnlyUnknown, false), want: StateSafe},
		{name: "UDP exact bind", complete: true, fact: udpFact(testCandidate, 53), want: StateConflict},
		{name: "UDP IPv4 wildcard bind", complete: true, fact: udpFact(netip.IPv4Unspecified(), 53), want: StateConflict},
		{name: "UDP IPv6 wildcard v6 only", complete: true, fact: udpIPv6Wildcard(IPv6OnlyEnabled), want: StateSafe},
		{name: "UDP IPv6 wildcard dual stack", complete: true, fact: udpIPv6Wildcard(IPv6OnlyDisabled), want: StateConflict},
		{name: "UDP IPv6 wildcard unknown", complete: true, fact: udpIPv6Wildcard(IPv6OnlyUnknown), want: StateConflict},
		{name: "UDP other bind", complete: true, fact: udpFact(netip.MustParseAddr("127.77.0.11"), 53), want: StateSafe},
		{name: "incomplete", want: StateIndeterminate},
		{name: "truncated", truncate: true, want: StateIndeterminate},
		{name: "incomplete definite conflict", fact: tcpFact(testCandidate, true), want: StateConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := safeLinuxObservation(t)
			observation.Sockets.Complete = test.complete
			observation.Sockets.Truncated = test.truncate
			if test.fact.Address.IsValid() {
				observation.Sockets.Endpoints = []SocketFact{test.fact}
			}
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.Sockets != test.want {
				t.Fatalf("Classify().Sockets = %q, want %q", assessment.Sockets, test.want)
			}
		})
	}
}

// TestClassifyLinuxPolicyCoversIsolationAndCompleteness proves only non-loopback route_localnet enablement is a conflict.
func TestClassifyLinuxPolicyCoversIsolationAndCompleteness(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LinuxPolicyFacts)
		want   State
	}{
		{name: "default policy", mutate: func(*LinuxPolicyFacts) {}, want: StateSafe},
		{name: "ip_nonlocal_bind bound but safe", mutate: func(facts *LinuxPolicyFacts) { facts.IPNonlocalBind = true }, want: StateSafe},
		{name: "loopback route_localnet", mutate: func(facts *LinuxPolicyFacts) { facts.RouteLocalnet[0].Enabled = true }, want: StateSafe},
		{name: "non-loopback route_localnet", mutate: func(facts *LinuxPolicyFacts) { facts.RouteLocalnet[1].Enabled = true }, want: StateConflict},
		{name: "incomplete", mutate: func(facts *LinuxPolicyFacts) { facts.Complete = false }, want: StateIndeterminate},
		{name: "truncated", mutate: func(facts *LinuxPolicyFacts) { facts.Complete = false; facts.Truncated = true }, want: StateIndeterminate},
		{name: "incomplete definite conflict", mutate: func(facts *LinuxPolicyFacts) { facts.Complete = false; facts.RouteLocalnet[1].Enabled = true }, want: StateConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := safeLinuxObservation(t)
			test.mutate(observation.Policy.Linux)
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.Policy != test.want {
				t.Fatalf("Classify().Policy = %q, want %q", assessment.Policy, test.want)
			}
		})
	}

	for _, observation := range []Observation{safeMacOSObservation(t), safeWindowsObservation(t)} {
		assessment, err := observation.Classify()
		if err != nil {
			t.Fatalf("Classify(%s) error = %v", observation.Scope.Platform, err)
		}
		if assessment.Policy != StateSafe {
			t.Fatalf("Classify(%s).Policy = %q, want safe", observation.Scope.Platform, assessment.Policy)
		}
	}
}

// TestIPNonlocalBindChangesEvidenceWithoutInventingAConflict fixes the policy setting's authority semantics.
func TestIPNonlocalBindChangesEvidenceWithoutInventingAConflict(t *testing.T) {
	disabled := safeLinuxObservation(t)
	enabled := cloneObservation(disabled)
	enabled.Policy.Linux.IPNonlocalBind = true

	for _, observation := range []Observation{disabled, enabled} {
		assessment, err := observation.Classify()
		if err != nil {
			t.Fatalf("Classify() error = %v", err)
		}
		if assessment.State != StateSafe {
			t.Fatalf("Classify().State = %q, want safe", assessment.State)
		}
	}
	if mustFingerprint(t, disabled) == mustFingerprint(t, enabled) {
		t.Fatal("Fingerprint() omitted ip_nonlocal_bind")
	}
}

// TestClassifyOverallUsesConflictPrecedence requires all components to prove safety but preserves definite conflicts.
func TestClassifyOverallUsesConflictPrecedence(t *testing.T) {
	observation := safeLinuxObservation(t)
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateSafe {
		t.Fatalf("Classify() = %#v, %v, want safe", assessment, err)
	}

	observation.Sockets.Complete = false
	assessment, err = observation.Classify()
	if err != nil || assessment.State != StateIndeterminate {
		t.Fatalf("Classify() = %#v, %v, want indeterminate", assessment, err)
	}

	observation.Routes.Matching = append(observation.Routes.Matching, RouteFact{
		Destination:    netip.MustParsePrefix("127.77.0.10/32"),
		Interface:      observation.Loopback.Interface,
		NativeLoopback: true,
		Normalization:  RouteNormalizationDirect,
	})
	assessment, err = observation.Classify()
	if err != nil || assessment.State != StateConflict {
		t.Fatalf("Classify() = %#v, %v, want conflict", assessment, err)
	}

	if got := combineStates(StateSafe, StateSafe); got != StateSafe {
		t.Fatalf("combineStates(safe) = %q", got)
	}
}

// tcpFact creates a requested TCP endpoint fact.
func tcpFact(address netip.Addr, accepting bool) SocketFact {
	return SocketFact{
		Protocol:     SocketProtocolTCP,
		Address:      address,
		Port:         443,
		TCPAccepting: accepting,
		IPv6Only:     IPv6OnlyNotApplicable,
	}
}

// tcpIPv6Wildcard creates a requested TCP IPv6 wildcard fact.
func tcpIPv6Wildcard(ipv6Only IPv6OnlyState, accepting bool) SocketFact {
	fact := tcpFact(netip.IPv6Unspecified(), accepting)
	fact.IPv6Only = ipv6Only
	return fact
}

// udpFact creates a requested UDP endpoint fact.
func udpFact(address netip.Addr, port uint16) SocketFact {
	return SocketFact{
		Protocol: SocketProtocolUDP,
		Address:  address,
		Port:     port,
		IPv6Only: IPv6OnlyNotApplicable,
	}
}

// udpIPv6Wildcard creates a requested UDP IPv6 wildcard fact.
func udpIPv6Wildcard(ipv6Only IPv6OnlyState) SocketFact {
	fact := udpFact(netip.IPv6Unspecified(), 53)
	fact.IPv6Only = ipv6Only
	return fact
}
