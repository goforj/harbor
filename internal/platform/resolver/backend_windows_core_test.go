package resolver

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// windowsNRPTFakeStore records backend preconditions and applies deterministic in-memory NRPT effects.
type windowsNRPTFakeStore struct {
	rules           []windowsNRPTRule
	snapshotErr     error
	ensureErr       error
	releaseErr      error
	ensureExpected  []windowsNRPTExpectedRule
	ensureGuard     windowsNRPTGuard
	releaseExpected []windowsNRPTExpectedRule
	releaseGuard    windowsNRPTGuard
	ensureCalls     int
	releaseCalls    int
}

// snapshot returns independent native rule slices so adapter code cannot mutate the fixture by alias.
func (store *windowsNRPTFakeStore) snapshot(ctx context.Context, _ Request) ([]windowsNRPTRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store.snapshotErr != nil {
		return nil, store.snapshotErr
	}
	return cloneWindowsNRPTTestRules(store.rules), nil
}

// ensure records complete preconditions and converges the selected deterministic destination.
func (store *windowsNRPTFakeStore) ensure(
	_ context.Context,
	request Request,
	expected []windowsNRPTExpectedRule,
	guard windowsNRPTGuard,
) error {
	store.ensureCalls++
	store.ensureExpected = append([]windowsNRPTExpectedRule(nil), expected...)
	store.ensureGuard = guard
	if store.ensureErr != nil {
		return store.ensureErr
	}
	exact := windowsNRPTTestExactRule(request)
	if guard.Exists {
		for index := range store.rules {
			if store.rules[index].Name == guard.Name {
				exact.Name = guard.Name
				store.rules[index] = exact
				return nil
			}
		}
		return errors.New("guarded fake NRPT rule is absent")
	}
	store.rules = append(store.rules, exact)
	return nil
}

// release records complete preconditions and removes only the selected deterministic destination.
func (store *windowsNRPTFakeStore) release(
	_ context.Context,
	_ Request,
	expected []windowsNRPTExpectedRule,
	guard windowsNRPTGuard,
) error {
	store.releaseCalls++
	store.releaseExpected = append([]windowsNRPTExpectedRule(nil), expected...)
	store.releaseGuard = guard
	if store.releaseErr != nil {
		return store.releaseErr
	}
	for index := range store.rules {
		if store.rules[index].Name == guard.Name {
			store.rules = append(store.rules[:index], store.rules[index+1:]...)
			return nil
		}
	}
	return errors.New("guarded fake NRPT rule is absent")
}

// TestWindowsNRPTAdapterLifecycle proves absent, exact, drift repair, and release use the shared Adapter state machine.
func TestWindowsNRPTAdapterLifecycle(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	store := &windowsNRPTFakeStore{}
	adapter := newAdapter(newWindowsNRPTBackend(store))

	absent, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(absent) error = %v", err)
	}
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(absent) error = %v", err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, absentFingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved(absent) error = %v", err)
	}
	if !change.Attempted || !change.Changed || store.ensureCalls != 1 || store.ensureGuard != (windowsNRPTGuard{}) || len(store.ensureExpected) != 0 {
		t.Fatalf("absent ensure = %#v, calls/guard/expected = %d / %#v / %#v", change, store.ensureCalls, store.ensureGuard, store.ensureExpected)
	}

	exact := change.After
	assessment, err := exact.Classify()
	if err != nil || assessment.State != StateExact {
		t.Fatalf("Classify(exact) = %#v, %v", assessment, err)
	}
	exactFingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(exact) error = %v", err)
	}
	change, err = adapter.ReleaseIfObserved(t.Context(), request, exactFingerprint)
	if err != nil {
		t.Fatalf("ReleaseIfObserved(exact) error = %v", err)
	}
	if !change.Attempted || !change.Changed || store.releaseCalls != 1 ||
		!store.releaseGuard.Exists || store.releaseGuard.Name != windowsNRPTTestRuleName ||
		!reflect.DeepEqual(store.releaseExpected, []windowsNRPTExpectedRule{{
			Name:                   windowsNRPTTestRuleName,
			NativeAttributesSHA256: store.releaseGuard.NativeAttributesSHA256,
		}}) {
		t.Fatalf("release = %#v, calls/guard/expected = %d / %#v / %#v", change, store.releaseCalls, store.releaseGuard, store.releaseExpected)
	}
	if assessment, err := change.After.Classify(); err != nil || assessment.State != StateAbsent {
		t.Fatalf("Classify(released) = %#v, %v", assessment, err)
	}
}

// TestWindowsNRPTAdapterRepairsOnlyUniquelyOwnedDrift proves repair carries the exact admitted native identity.
func TestWindowsNRPTAdapterRepairsOnlyUniquelyOwnedDrift(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	drifted := windowsNRPTTestExactRule(request)
	drifted.NameServers = []string{"127.77.1.99"}
	store := &windowsNRPTFakeStore{rules: []windowsNRPTRule{drifted}}
	adapter := newAdapter(newWindowsNRPTBackend(store))

	observation, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(drifted) error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateOwnedDrifted {
		t.Fatalf("Classify(drifted) = %#v, %v", assessment, err)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(drifted) error = %v", err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, fingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved(drifted) error = %v", err)
	}
	wantNative := windowsNRPTRuleFingerprint(drifted)
	if !change.Changed || store.ensureGuard != (windowsNRPTGuard{
		Exists:                 true,
		Name:                   drifted.Name,
		NativeAttributesSHA256: wantNative,
	}) || !reflect.DeepEqual(store.ensureExpected, []windowsNRPTExpectedRule{{
		Name:                   drifted.Name,
		NativeAttributesSHA256: wantNative,
	}}) {
		t.Fatalf("drift repair guard/expected = %#v / %#v", store.ensureGuard, store.ensureExpected)
	}
}

// TestWindowsNRPTBackendNeverMutatesForeignOrAmbiguousRules protects the native boundary even if called without Adapter admission.
func TestWindowsNRPTBackendNeverMutatesForeignOrAmbiguousRules(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	foreign := windowsNRPTTestExactRule(request)
	foreign.DisplayName = "Another resolver"
	foreign.Comment = "another owner"
	firstOwned := windowsNRPTTestExactRule(request)
	secondOwned := windowsNRPTTestExactRule(request)
	secondOwned.Name = "{22222222-2222-2222-2222-222222222222}"

	for _, test := range []struct {
		name  string
		rules []windowsNRPTRule
	}{
		{name: "foreign", rules: []windowsNRPTRule{foreign}},
		{name: "ambiguous", rules: []windowsNRPTRule{firstOwned, secondOwned}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &windowsNRPTFakeStore{rules: cloneWindowsNRPTTestRules(test.rules)}
			backend := newWindowsNRPTBackend(store)
			before, err := windowsNRPTObservationFromRules(t.Context(), request, test.rules)
			if err != nil {
				t.Fatalf("windowsNRPTObservationFromRules() error = %v", err)
			}
			if err := backend.ensure(t.Context(), request, before); err == nil {
				t.Fatal("backend ensure accepted unsafe rules")
			}
			if err := backend.release(t.Context(), request, before); err == nil {
				t.Fatal("backend release accepted unsafe rules")
			}
			if store.ensureCalls != 0 || store.releaseCalls != 0 {
				t.Fatalf("native calls = ensure %d, release %d", store.ensureCalls, store.releaseCalls)
			}
		})
	}
}

// TestWindowsNRPTRuleClassificationRetainsAllRelevantClaims covers suffix, FQDN, any, and destination-occupancy conflicts.
func TestWindowsNRPTRuleClassificationRetainsAllRelevantClaims(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	for _, test := range []struct {
		name       string
		mutate     func(*windowsNRPTRule)
		wantRules  int
		wantState  State
		wantOwned  OwnedState
		wantServer int
	}{
		{name: "exact", mutate: func(*windowsNRPTRule) {}, wantRules: 1, wantState: StateExact, wantOwned: OwnedStateExact, wantServer: 1},
		{
			name: "more specific suffix",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{".api.test"}
			},
			wantRules: 1, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantServer: 1,
		},
		{
			name: "FQDN",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{"api.test"}
			},
			wantRules: 1, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantServer: 1,
		},
		{
			name: "any",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{"."}
			},
			wantRules: 1, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantServer: 1,
		},
		{
			name: "multiple claims",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{".test", "api.test"}
			},
			wantRules: 2, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantServer: 1,
		},
		{
			name: "deterministic display occupancy",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{".example"}
				rule.NameServers = nil
				rule.Comment = "foreign"
			},
			wantRules: 1, wantState: StateForeign, wantOwned: OwnedStateAbsent,
		},
		{
			name: "unrelated",
			mutate: func(rule *windowsNRPTRule) {
				rule.Namespaces = []string{".example"}
				rule.DisplayName = "Another resolver"
				rule.Comment = "foreign"
			},
			wantState: StateAbsent, wantOwned: OwnedStateAbsent,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule := windowsNRPTTestExactRule(request)
			test.mutate(&rule)
			observation, err := windowsNRPTObservationFromRules(t.Context(), request, []windowsNRPTRule{rule})
			if err != nil {
				t.Fatalf("windowsNRPTObservationFromRules() error = %v", err)
			}
			if len(observation.Rules) != test.wantRules {
				t.Fatalf("rule facts = %#v, want %d", observation.Rules, test.wantRules)
			}
			for _, fact := range observation.Rules {
				if len(fact.Servers) != test.wantServer {
					t.Fatalf("fact servers = %v, want %d", fact.Servers, test.wantServer)
				}
			}
			assessment, err := observation.Classify()
			if err != nil || assessment.State != test.wantState || assessment.Owned != test.wantOwned {
				t.Fatalf("Classify() = %#v, %v", assessment, err)
			}
		})
	}
}

// TestWindowsNRPTObservationRejectsAmbiguousNativeEvidence covers duplicate identities, malformed ownership, and unsafe fields.
func TestWindowsNRPTObservationRejectsAmbiguousNativeEvidence(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	duplicate := windowsNRPTTestExactRule(request)
	badMarker := windowsNRPTTestExactRule(request)
	badMarker.Comment = windowsNRPTOwnerPrefix + "version=1 installation=missing-policy"
	badServer := windowsNRPTTestExactRule(request)
	badServer.NameServers = []string{"127.0.0.01"}
	duplicateNamespace := windowsNRPTTestExactRule(request)
	duplicateNamespace.Namespaces = []string{".test", "test"}
	controlText := windowsNRPTTestExactRule(request)
	controlText.Comment += "\nunsafe"

	for _, test := range []struct {
		name  string
		rules []windowsNRPTRule
	}{
		{name: "duplicate native name", rules: []windowsNRPTRule{duplicate, duplicate}},
		{name: "malformed marker", rules: []windowsNRPTRule{badMarker}},
		{name: "noncanonical server", rules: []windowsNRPTRule{badServer}},
		{name: "duplicate semantic namespace", rules: []windowsNRPTRule{duplicateNamespace}},
		{name: "control text", rules: []windowsNRPTRule{controlText}},
		{name: "too many", rules: slices.Repeat([]windowsNRPTRule{duplicate}, maximumRuleFacts+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := windowsNRPTObservationFromRules(t.Context(), request, test.rules); err == nil {
				t.Fatal("windowsNRPTObservationFromRules() error = nil")
			}
		})
	}
}

// TestWindowsNRPTNativeFingerprintBindsEveryField prevents an unreviewed native drift from satisfying a mutation guard.
func TestWindowsNRPTNativeFingerprintBindsEveryField(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	baseline := windowsNRPTTestExactRule(request)
	want := windowsNRPTRuleFingerprint(baseline)
	for _, mutate := range []func(*windowsNRPTRule){
		func(rule *windowsNRPTRule) { rule.Version++ },
		func(rule *windowsNRPTRule) { rule.Name += "x" },
		func(rule *windowsNRPTRule) { rule.Namespaces = append(rule.Namespaces, ".api.test") },
		func(rule *windowsNRPTRule) { rule.IPsecCARestriction = "CA" },
		func(rule *windowsNRPTRule) { rule.DirectAccessDNSServers = []string{"127.0.0.2"} },
		func(rule *windowsNRPTRule) { rule.DirectAccessEnabled = true },
		func(rule *windowsNRPTRule) { rule.DirectAccessProxyType = "NoProxy" },
		func(rule *windowsNRPTRule) { rule.DirectAccessProxyName = "proxy" },
		func(rule *windowsNRPTRule) { rule.DirectAccessQueryIPsecEncryption = "High" },
		func(rule *windowsNRPTRule) { rule.DirectAccessQueryIPsecRequired = true },
		func(rule *windowsNRPTRule) { rule.NameServers = []string{"127.0.0.3"} },
		func(rule *windowsNRPTRule) { rule.DNSSecEnabled = true },
		func(rule *windowsNRPTRule) { rule.DNSSecQueryIPsecEncryption = "High" },
		func(rule *windowsNRPTRule) { rule.DNSSecQueryIPsecRequired = true },
		func(rule *windowsNRPTRule) { rule.DNSSecValidationRequired = true },
		func(rule *windowsNRPTRule) { rule.NameEncoding = "Punycode" },
		func(rule *windowsNRPTRule) { rule.DisplayName += " drift" },
		func(rule *windowsNRPTRule) { rule.Comment += " drift" },
	} {
		candidate := windowsNRPTTestExactRule(request)
		mutate(&candidate)
		if got := windowsNRPTRuleFingerprint(candidate); got == want {
			t.Fatalf("fingerprint ignored mutation: %#v", candidate)
		}
	}
}

// TestWindowsNRPTNativeExactRejectsLatentSecurityState keeps unrequested DirectAccess and DNSSEC behavior from appearing healthy.
func TestWindowsNRPTNativeExactRejectsLatentSecurityState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	baseline := windowsNRPTTestExactRule(request)
	if !windowsNRPTRuleNativeExact(baseline, request) {
		t.Fatal("windowsNRPTRuleNativeExact() rejected the canonical baseline")
	}
	for _, mutate := range []func(*windowsNRPTRule){
		func(rule *windowsNRPTRule) { rule.IPsecCARestriction = "CA" },
		func(rule *windowsNRPTRule) { rule.DirectAccessDNSServers = []string{"127.0.0.2"} },
		func(rule *windowsNRPTRule) { rule.DirectAccessProxyType = "NoProxy" },
		func(rule *windowsNRPTRule) { rule.DirectAccessProxyName = "proxy" },
		func(rule *windowsNRPTRule) { rule.DirectAccessQueryIPsecEncryption = "High" },
		func(rule *windowsNRPTRule) { rule.DNSSecQueryIPsecEncryption = "High" },
	} {
		candidate := windowsNRPTTestExactRule(request)
		mutate(&candidate)
		if windowsNRPTRuleNativeExact(candidate, request) {
			t.Fatalf("windowsNRPTRuleNativeExact() accepted latent state %#v", candidate)
		}
	}
}

// TestWindowsNRPTExpectedRulesDeduplicatesMultiNamespaceFacts keeps one complete native CAS identity per rule.
func TestWindowsNRPTExpectedRulesDeduplicatesMultiNamespaceFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	rule := windowsNRPTTestExactRule(request)
	rule.Namespaces = []string{".test", ".api.test"}
	rule.DisplayName = "Foreign"
	rule.Comment = "Foreign"
	observation, err := windowsNRPTObservationFromRules(t.Context(), request, []windowsNRPTRule{rule})
	if err != nil {
		t.Fatalf("windowsNRPTObservationFromRules() error = %v", err)
	}
	expected, err := windowsNRPTExpectedRules(observation)
	if err != nil {
		t.Fatalf("windowsNRPTExpectedRules() error = %v", err)
	}
	if !reflect.DeepEqual(expected, []windowsNRPTExpectedRule{{
		Name:                   rule.Name,
		NativeAttributesSHA256: windowsNRPTRuleFingerprint(rule),
	}}) {
		t.Fatalf("expected rules = %#v", expected)
	}

	corrupt := observation
	corrupt.Rules = append([]RuleFact(nil), observation.Rules...)
	corrupt.Rules[1].NativeAttributesSHA256 = strings.Repeat("a", 64)
	if _, err := windowsNRPTExpectedRules(corrupt); err == nil {
		t.Fatal("windowsNRPTExpectedRules() accepted inconsistent native identities")
	}
}

const windowsNRPTTestRuleName = "{11111111-1111-1111-1111-111111111111}"

// windowsNRPTTestExactRule constructs the exact local rule Harbor expects Add-DnsClientNrptRule to produce.
func windowsNRPTTestExactRule(request Request) windowsNRPTRule {
	return windowsNRPTRule{
		Version:                2,
		Name:                   windowsNRPTTestRuleName,
		Namespaces:             []string{request.Suffix()},
		DirectAccessDNSServers: []string{},
		NameServers:            []string{request.Endpoint().Addr().String()},
		NameEncoding:           "Disable",
		DisplayName:            windowsNRPTDisplayName(request),
		Comment:                windowsNRPTOwnerComment(request),
	}
}

// cloneWindowsNRPTTestRules deep-copies native arrays retained by fake stores.
func cloneWindowsNRPTTestRules(rules []windowsNRPTRule) []windowsNRPTRule {
	cloned := append([]windowsNRPTRule(nil), rules...)
	for index := range cloned {
		cloned[index].Namespaces = slices.Clone(cloned[index].Namespaces)
		cloned[index].DirectAccessDNSServers = slices.Clone(cloned[index].DirectAccessDNSServers)
		cloned[index].NameServers = slices.Clone(cloned[index].NameServers)
	}
	return cloned
}
