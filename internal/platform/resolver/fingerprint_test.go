package resolver

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestObservationFingerprintCanonicalizesOrderWithoutCollapsingMultiplicity proves stable set encoding.
func TestObservationFingerprintCanonicalizesOrderWithoutCollapsingMultiplicity(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	first := resolverExactRule(request, "owned")
	second := cloneRuleFact(first)
	second.NativeID = "foreign"
	second.Owner = nil
	second.Namespace = ".child.test"

	left := Observation{Request: request, Complete: true, Rules: []RuleFact{first, second}}
	right := Observation{Request: request, Complete: true, Rules: []RuleFact{second, first}}
	leftFingerprint := resolverFingerprint(t, left)
	rightFingerprint := resolverFingerprint(t, right)
	if leftFingerprint != rightFingerprint {
		t.Fatalf("rule order changed fingerprint: %q != %q", leftFingerprint, rightFingerprint)
	}

	duplicate := Observation{Request: request, Complete: true, Rules: []RuleFact{first, second, second}}
	if resolverFingerprint(t, duplicate) == leftFingerprint {
		t.Fatal("duplicate rule did not change fingerprint")
	}
}

// TestObservationFingerprintCanonicalizesServerSetOrder makes native enumeration order irrelevant.
func TestObservationFingerprintCanonicalizesServerSetOrder(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	firstServer := netip.AddrPortFrom(testLocalhost, 26000)
	secondServer := netip.AddrPortFrom(testWindowsDNS, 53)
	rule := resolverExactRule(request, "foreign")
	rule.Owner = nil
	rule.Servers = []netip.AddrPort{firstServer, secondServer}
	reordered := cloneRuleFact(rule)
	reordered.Servers[0], reordered.Servers[1] = reordered.Servers[1], reordered.Servers[0]

	first := Observation{Request: request, Complete: true, Rules: []RuleFact{rule}}
	second := Observation{Request: request, Complete: true, Rules: []RuleFact{reordered}}
	if resolverFingerprint(t, first) != resolverFingerprint(t, second) {
		t.Fatal("server set order changed observation fingerprint")
	}
}

// TestObservationFingerprintBindsDistinctNativeDrift proves CAS sees changes hidden by the normalized verdict.
func TestObservationFingerprintBindsDistinctNativeDrift(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	firstRule := resolverExactRule(request, "owned")
	firstRule.NativeExact = false
	secondRule := cloneRuleFact(firstRule)
	secondRule.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)

	first := Observation{Request: request, Complete: true, Rules: []RuleFact{firstRule}}
	second := Observation{Request: request, Complete: true, Rules: []RuleFact{secondRule}}
	firstAssessment, err := first.Classify()
	if err != nil {
		t.Fatalf("first Classify() error = %v", err)
	}
	secondAssessment, err := second.Classify()
	if err != nil {
		t.Fatalf("second Classify() error = %v", err)
	}
	if firstAssessment != secondAssessment || firstAssessment.State != StateOwnedDrifted {
		t.Fatalf("drift assessments = %#v and %#v, want matching owned drift", firstAssessment, secondAssessment)
	}
	if resolverFingerprint(t, first) == resolverFingerprint(t, second) {
		t.Fatal("distinct native drift produced the same observation fingerprint")
	}
}

// TestObservationFingerprintTreatsNilAndEmptySlicesEqually preserves canonical empty representations.
func TestObservationFingerprintTreatsNilAndEmptySlicesEqually(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	nilRules := Observation{Request: request, Complete: true}
	emptyRules := Observation{Request: request, Complete: true, Rules: []RuleFact{}}
	if resolverFingerprint(t, nilRules) != resolverFingerprint(t, emptyRules) {
		t.Fatal("nil and empty rule slices produced different fingerprints")
	}

	foreignNil := RuleFact{
		Mechanism:              request.Mechanism(),
		NativeID:               "foreign",
		Namespace:              request.Suffix(),
		RouteOnly:              true,
		NativeExact:            true,
		NativeAttributesSHA256: testNativeAttributesFingerprint,
	}
	foreignEmpty := cloneRuleFact(foreignNil)
	foreignEmpty.Servers = []netip.AddrPort{}
	if resolverFingerprint(t, Observation{Request: request, Complete: true, Rules: []RuleFact{foreignNil}}) !=
		resolverFingerprint(t, Observation{Request: request, Complete: true, Rules: []RuleFact{foreignEmpty}}) {
		t.Fatal("nil and empty server slices produced different fingerprints")
	}
}

// TestObservationFingerprintBindsCompletenessRequestAndEveryRuleField prevents stale evidence reuse.
func TestObservationFingerprintBindsCompletenessRequestAndEveryRuleField(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	baseRule := resolverExactRule(request, "owned")
	base := Observation{Request: request, Complete: true, Rules: []RuleFact{baseRule}}
	wantDifferent := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "completeness", mutate: func(observation *Observation) { observation.Complete = false }},
		{name: "truncation", mutate: func(observation *Observation) { observation.Complete = false; observation.Truncated = true }},
		{name: "installation", mutate: func(observation *Observation) {
			other, err := NewRequest("installation-other", observation.Request.Policy())
			if err != nil {
				t.Fatalf("NewRequest() alternate fixture error = %v", err)
			}
			observation.Request = other
		}},
		{name: "mechanism", mutate: func(observation *Observation) {
			observation.Rules[0].Mechanism = networkpolicy.UbuntuSystemdResolved
		}},
		{name: "native ID", mutate: func(observation *Observation) { observation.Rules[0].NativeID = "owned-other" }},
		{name: "namespace", mutate: func(observation *Observation) { observation.Rules[0].Namespace = ".child.test" }},
		{name: "server", mutate: func(observation *Observation) {
			observation.Rules[0].Servers[0] = netip.AddrPortFrom(testLocalhost, 26000)
		}},
		{name: "route only", mutate: func(observation *Observation) { observation.Rules[0].RouteOnly = false }},
		{name: "native exact", mutate: func(observation *Observation) { observation.Rules[0].NativeExact = false }},
		{name: "native attributes", mutate: func(observation *Observation) {
			observation.Rules[0].NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
		}},
		{name: "owner absent", mutate: func(observation *Observation) { observation.Rules[0].Owner = nil }},
		{name: "owner version", mutate: func(observation *Observation) { observation.Rules[0].Owner.Version++ }},
		{name: "owner installation", mutate: func(observation *Observation) {
			observation.Rules[0].Owner.InstallationID = "installation-other"
		}},
		{name: "owner policy", mutate: func(observation *Observation) {
			observation.Rules[0].Owner.PolicyFingerprint = strings.Repeat("b", canonicalFingerprintLength)
		}},
	}
	baseFingerprint := resolverFingerprint(t, base)
	for _, test := range wantDifferent {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneObservation(base)
			test.mutate(&candidate)
			if got := resolverFingerprint(t, candidate); got == baseFingerprint {
				t.Fatalf("%s did not change observation fingerprint", test.name)
			}
		})
	}
}

// TestObservationFingerprintRejectsInvalidFacts prevents malformed native data from becoming CAS authority.
func TestObservationFingerprintRejectsInvalidFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	rule := resolverExactRule(request, "owned")
	rule.NativeID = ""
	if _, err := (Observation{Request: request, Complete: true, Rules: []RuleFact{rule}}).Fingerprint(); err == nil {
		t.Fatal("Fingerprint() accepted invalid rule facts")
	}
}

// resolverFingerprint returns one valid observation fingerprint or fails the current test.
func resolverFingerprint(t *testing.T, observation Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Observation.Fingerprint() error = %v", err)
	}
	return fingerprint
}
