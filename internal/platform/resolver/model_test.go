package resolver

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
)

const testAuthorityFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const testNativeAttributesFingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

var (
	testLocalhost  = netip.MustParseAddr("127.0.0.1")
	testWindowsDNS = netip.MustParseAddr("127.0.0.2")
)

// TestNewRequestAcceptsExactPolicies proves all supported resolver profiles derive immutable authority.
func TestNewRequestAcceptsExactPolicies(t *testing.T) {
	tests := []struct {
		name      string
		mechanism networkpolicy.ResolverMechanism
		endpoint  netip.AddrPort
	}{
		{name: "macOS", mechanism: networkpolicy.DarwinResolverFile, endpoint: netip.AddrPortFrom(testLocalhost, 25000)},
		{name: "Ubuntu", mechanism: networkpolicy.UbuntuSystemdResolved, endpoint: netip.AddrPortFrom(testLocalhost, 25000)},
		{name: "Windows", mechanism: networkpolicy.WindowsNRPT, endpoint: netip.AddrPortFrom(testWindowsDNS, 53)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := resolverTestPolicy(t, test.mechanism)
			request, err := NewRequest("installation-test", policy)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			wantFingerprint, err := policy.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint() policy error = %v", err)
			}
			if request.Policy() != policy || request.InstallationID() != "installation-test" ||
				request.PolicyFingerprint() != wantFingerprint || request.Mechanism() != test.mechanism ||
				request.Suffix() != networkpolicy.TestSuffix || request.Endpoint() != test.endpoint {
				t.Fatalf("NewRequest() = %#v, want exact derived authority", request)
			}
			if err := request.OwnerMarker().Validate(); err != nil {
				t.Fatalf("OwnerMarker().Validate() error = %v", err)
			}
		})
	}
}

// TestNewRequestRejectsInvalidAuthority covers installation, policy, and internal fingerprint validation.
func TestNewRequestRejectsInvalidAuthority(t *testing.T) {
	policy := resolverTestPolicy(t, networkpolicy.DarwinResolverFile)
	if _, err := NewRequest(" bad ", policy); err == nil {
		t.Fatal("NewRequest() accepted an invalid installation ID")
	}
	invalidPolicy := policy
	invalidPolicy.Suffix = ".invalid"
	if _, err := NewRequest("installation-test", invalidPolicy); err == nil {
		t.Fatal("NewRequest() accepted an invalid policy")
	}

	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	request.policyFingerprint = strings.Repeat("b", canonicalFingerprintLength)
	if err := request.Validate(); err == nil {
		t.Fatal("Request.Validate() accepted an inconsistent policy fingerprint")
	}
	request = resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	request.policy.Suffix = ".invalid"
	if err := request.Validate(); err == nil {
		t.Fatal("Request.Validate() accepted an invalid embedded policy")
	}
}

// TestRuleFactValidateRejectsUnsafeFacts covers every bounded native rule boundary.
func TestRuleFactValidateRejectsUnsafeFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	valid := resolverExactRule(request, "native-rule")
	tests := []struct {
		name   string
		mutate func(*RuleFact)
	}{
		{name: "unknown mechanism", mutate: func(rule *RuleFact) { rule.Mechanism = "unknown" }},
		{name: "empty native ID", mutate: func(rule *RuleFact) { rule.NativeID = "" }},
		{name: "whitespace native ID", mutate: func(rule *RuleFact) { rule.NativeID = " native-rule" }},
		{name: "invalid UTF-8 native ID", mutate: func(rule *RuleFact) { rule.NativeID = string([]byte{0xff}) }},
		{name: "large native ID", mutate: func(rule *RuleFact) { rule.NativeID = strings.Repeat("a", maximumNativeIDLength+1) }},
		{name: "control native ID", mutate: func(rule *RuleFact) { rule.NativeID = "native\x00rule" }},
		{name: "namespace without suffix marker", mutate: func(rule *RuleFact) { rule.Namespace = "test" }},
		{name: "uppercase namespace", mutate: func(rule *RuleFact) { rule.Namespace = ".Test" }},
		{name: "unrelated namespace", mutate: func(rule *RuleFact) { rule.Namespace = ".example" }},
		{name: "leading hyphen label", mutate: func(rule *RuleFact) { rule.Namespace = ".-child.test" }},
		{name: "trailing hyphen label", mutate: func(rule *RuleFact) { rule.Namespace = ".child-.test" }},
		{name: "empty label", mutate: func(rule *RuleFact) { rule.Namespace = ".child..test" }},
		{name: "too many servers", mutate: func(rule *RuleFact) {
			rule.Servers = make([]netip.AddrPort, maximumServersPerRule+1)
			for index := range rule.Servers {
				rule.Servers[index] = request.Endpoint()
			}
		}},
		{name: "zero server", mutate: func(rule *RuleFact) { rule.Servers = []netip.AddrPort{{}} }},
		{name: "mapped server", mutate: func(rule *RuleFact) {
			rule.Servers = []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("::ffff:127.0.0.1"), 53)}
		}},
		{name: "duplicate server", mutate: func(rule *RuleFact) { rule.Servers = append(rule.Servers, rule.Servers[0]) }},
		{name: "native attribute fingerprint", mutate: func(rule *RuleFact) { rule.NativeAttributesSHA256 = "bad" }},
		{name: "invalid owner", mutate: func(rule *RuleFact) { rule.Owner.InstallationID = " bad " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := cloneRuleFact(valid)
			test.mutate(&rule)
			if err := rule.Validate(request); err == nil {
				t.Fatalf("RuleFact.Validate() accepted %#v", rule)
			}
		})
	}
	if err := valid.Validate(Request{}); err == nil {
		t.Fatal("RuleFact.Validate() accepted a zero request")
	}
	hyphenated := cloneRuleFact(valid)
	hyphenated.Namespace = ".child-name.test"
	hyphenated.Owner = nil
	if err := hyphenated.Validate(request); err != nil {
		t.Fatalf("RuleFact.Validate() rejected a canonical internal hyphen: %v", err)
	}
}

// TestObservationValidateEnforcesCompletenessAndBounds rejects contradictory or oversized snapshots.
func TestObservationValidateEnforcesCompletenessAndBounds(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	contradictory := Observation{Request: request, Complete: true, Truncated: true}
	if err := contradictory.Validate(); err == nil {
		t.Fatal("Observation.Validate() accepted complete truncated facts")
	}
	if _, err := contradictory.Classify(); err == nil {
		t.Fatal("Observation.Classify() accepted complete truncated facts")
	}
	rules := make([]RuleFact, maximumRuleFacts+1)
	for index := range rules {
		rules[index] = resolverExactRule(request, "native-rule")
	}
	if err := (Observation{Request: request, Rules: rules}).Validate(); err == nil {
		t.Fatal("Observation.Validate() accepted too many rules")
	}
}

// TestObservationClassifySeparatesOwnedAndForeignState covers the complete two-axis state matrix.
func TestObservationClassifySeparatesOwnedAndForeignState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	exact := resolverExactRule(request, "owned")
	drifted := cloneRuleFact(exact)
	drifted.Servers = []netip.AddrPort{netip.AddrPortFrom(testLocalhost, 26000)}
	foreign := cloneRuleFact(exact)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	otherOwner := cloneRuleFact(exact)
	otherOwner.NativeID = "other-owner"
	otherOwner.Owner.InstallationID = "installation-other"
	descendant := cloneRuleFact(foreign)
	descendant.NativeID = "foreign-descendant"
	descendant.Namespace = ".child.test"

	tests := []struct {
		name         string
		complete     bool
		truncated    bool
		rules        []RuleFact
		wantState    State
		wantOwned    OwnedState
		wantForeigns int
	}{
		{name: "absent", complete: true, wantState: StateAbsent, wantOwned: OwnedStateAbsent},
		{name: "exact", complete: true, rules: []RuleFact{exact}, wantState: StateExact, wantOwned: OwnedStateExact},
		{name: "owned drifted", complete: true, rules: []RuleFact{drifted}, wantState: StateOwnedDrifted, wantOwned: OwnedStateDrifted},
		{name: "foreign", complete: true, rules: []RuleFact{foreign}, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantForeigns: 1},
		{name: "other owner", complete: true, rules: []RuleFact{otherOwner}, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantForeigns: 1},
		{name: "mixed", complete: true, rules: []RuleFact{exact, descendant}, wantState: StateForeign, wantOwned: OwnedStateExact, wantForeigns: 1},
		{name: "ambiguous owned", complete: true, rules: []RuleFact{exact, exact}, wantState: StateAmbiguous, wantOwned: OwnedStateAmbiguous},
		{name: "incomplete", rules: []RuleFact{exact}, wantState: StateIndeterminate, wantOwned: OwnedStateExact},
		{name: "truncated", truncated: true, rules: []RuleFact{foreign}, wantState: StateIndeterminate, wantOwned: OwnedStateAbsent, wantForeigns: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := Observation{
				Request:   request,
				Complete:  test.complete,
				Truncated: test.truncated,
				Rules:     test.rules,
			}
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.State != test.wantState || assessment.Owned != test.wantOwned || assessment.ForeignCount != test.wantForeigns {
				t.Fatalf("Classify() = %#v, want state %q, owned %q, foreign %d", assessment, test.wantState, test.wantOwned, test.wantForeigns)
			}
		})
	}
}

// resolverTestRequest constructs one validated request for focused resolver tests.
func resolverTestRequest(t *testing.T, mechanism networkpolicy.ResolverMechanism) Request {
	t.Helper()
	request, err := NewRequest("installation-test", resolverTestPolicy(t, mechanism))
	if err != nil {
		t.Fatalf("NewRequest() fixture error = %v", err)
	}
	return request
}

// resolverTestPolicy constructs one exact supported policy without relying on platform selection.
func resolverTestPolicy(t *testing.T, mechanism networkpolicy.ResolverMechanism) networkpolicy.Policy {
	t.Helper()
	redirectedDNS := netip.AddrPortFrom(testLocalhost, 25000)
	redirectedHTTP := networkpolicy.Listener{
		Advertised: netip.AddrPortFrom(testLocalhost, 80),
		Bind:       netip.AddrPortFrom(testLocalhost, 25001),
	}
	redirectedHTTPS := networkpolicy.Listener{
		Advertised: netip.AddrPortFrom(testLocalhost, 443),
		Bind:       netip.AddrPortFrom(testLocalhost, 25002),
	}
	mechanisms := networkpolicy.MacOSMechanisms()
	dns := networkpolicy.Listener{Advertised: redirectedDNS, Bind: redirectedDNS}
	http := redirectedHTTP
	https := redirectedHTTPS
	switch mechanism {
	case networkpolicy.DarwinResolverFile:
	case networkpolicy.UbuntuSystemdResolved:
		mechanisms = networkpolicy.UbuntuMechanisms()
	case networkpolicy.WindowsNRPT:
		mechanisms = networkpolicy.WindowsMechanisms()
		windowsDNS := netip.AddrPortFrom(testWindowsDNS, 53)
		dns = networkpolicy.Listener{Advertised: windowsDNS, Bind: windowsDNS}
		http = networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(testLocalhost, 80),
			Bind:       netip.AddrPortFrom(testLocalhost, 80),
		}
		https = networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(testLocalhost, 443),
			Bind:       netip.AddrPortFrom(testLocalhost, 443),
		}
	default:
		t.Fatalf("unsupported resolver test mechanism %q", mechanism)
	}
	policy, err := networkpolicy.New(testAuthorityFingerprint, mechanisms, dns, http, https)
	if err != nil {
		t.Fatalf("networkpolicy.New() fixture error = %v", err)
	}
	return policy
}

// resolverExactRule returns one complete native rule carrying the request's exact owner marker.
func resolverExactRule(request Request, nativeID string) RuleFact {
	owner := request.OwnerMarker()
	return RuleFact{
		Mechanism:              request.Mechanism(),
		NativeID:               nativeID,
		Namespace:              request.Suffix(),
		Servers:                []netip.AddrPort{request.Endpoint()},
		RouteOnly:              true,
		NativeExact:            true,
		NativeAttributesSHA256: testNativeAttributesFingerprint,
		Owner:                  &owner,
	}
}

// cloneRuleFact creates independent slice and marker storage for mutation tests.
func cloneRuleFact(rule RuleFact) RuleFact {
	cloned := rule
	cloned.Servers = append([]netip.AddrPort(nil), rule.Servers...)
	if rule.Owner != nil {
		owner := *rule.Owner
		cloned.Owner = &owner
	}
	return cloned
}

// TestOwnerMarkerRejectsNoncanonicalFields covers every marker validation boundary.
func TestOwnerMarkerRejectsNoncanonicalFields(t *testing.T) {
	marker := resolverTestRequest(t, networkpolicy.DarwinResolverFile).OwnerMarker()
	tests := []struct {
		name   string
		mutate func(*OwnerMarker)
	}{
		{name: "zero version", mutate: func(marker *OwnerMarker) { marker.Version = 0 }},
		{name: "large installation", mutate: func(marker *OwnerMarker) { marker.InstallationID = strings.Repeat("a", maximumMarkerTextLength+1) }},
		{name: "installation", mutate: func(marker *OwnerMarker) { marker.InstallationID = string(identity.InstallationID("-bad")) }},
		{name: "fingerprint length", mutate: func(marker *OwnerMarker) { marker.PolicyFingerprint = "aa" }},
		{name: "fingerprint case", mutate: func(marker *OwnerMarker) { marker.PolicyFingerprint = strings.Repeat("A", canonicalFingerprintLength) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := marker
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("OwnerMarker.Validate() accepted %#v", candidate)
			}
		})
	}
}

// TestRuleFactSupportsBoundedIPv6ServerScopes covers native foreign-server scope facts.
func TestRuleFactSupportsBoundedIPv6ServerScopes(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	rule := resolverExactRule(request, "foreign-ipv6")
	rule.Owner = nil
	rule.Servers = []netip.AddrPort{
		netip.AddrPortFrom(netip.MustParseAddr("fe80::1").WithZone("resolver0"), 53),
	}
	if err := rule.Validate(request); err != nil {
		t.Fatalf("RuleFact.Validate() IPv6 scope error = %v", err)
	}
	if _, err := (Observation{Request: request, Complete: true, Rules: []RuleFact{rule}}).Fingerprint(); err != nil {
		t.Fatalf("Observation.Fingerprint() IPv6 scope error = %v", err)
	}

	rule.Servers[0] = netip.AddrPortFrom(netip.MustParseAddr("fe80::1").WithZone(" invalid "), 53)
	if err := rule.Validate(request); err == nil {
		t.Fatal("RuleFact.Validate() accepted a noncanonical IPv6 scope")
	}
}

// TestClassifyOverallStateFailsClosedOnUnknownOwnedState guards future enum additions.
func TestClassifyOverallStateFailsClosedOnUnknownOwnedState(t *testing.T) {
	observation := Observation{Complete: true}
	if got := classifyOverallState(observation, Assessment{Owned: "future-owned-state"}); got != StateIndeterminate {
		t.Fatalf("classifyOverallState() = %q, want %q", got, StateIndeterminate)
	}
}
