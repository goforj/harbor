package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

var errResolverFake = errors.New("resolver fake failure")

// resolverFakeBackend records logical effects while exposing controlled observation boundaries.
type resolverFakeBackend struct {
	observation       Observation
	observeErr        error
	observeErrCall    int
	ensureErr         error
	releaseErr        error
	skipEnsureEffect  bool
	skipReleaseEffect bool
	afterObserve      func(int)
	observeCalls      int
	ensureCalls       int
	releaseCalls      int
	lastEnsureBefore  Observation
	lastReleaseBefore Observation
}

// observe returns the current injected snapshot without consulting the host.
func (f *resolverFakeBackend) observe(_ context.Context, _ Request) (Observation, error) {
	f.observeCalls++
	if f.afterObserve != nil {
		f.afterObserve(f.observeCalls)
	}
	if f.observeErr != nil && (f.observeErrCall == 0 || f.observeCalls == f.observeErrCall) {
		return Observation{}, f.observeErr
	}
	return cloneObservation(f.observation), nil
}

// ensure records the admitted snapshot and optionally installs one exact owned rule.
func (f *resolverFakeBackend) ensure(_ context.Context, request Request, before Observation) error {
	f.ensureCalls++
	f.lastEnsureBefore = cloneObservation(before)
	if !f.skipEnsureEffect {
		remaining := make([]RuleFact, 0, len(f.observation.Rules)+1)
		for _, rule := range f.observation.Rules {
			if !markerMatchesRequest(rule.Owner, request) {
				remaining = append(remaining, cloneRuleFact(rule))
			}
		}
		remaining = append(remaining, resolverExactRule(request, "owned-native-rule"))
		f.observation = Observation{Request: request, Complete: true, Rules: remaining}
	}
	return f.ensureErr
}

// release records the admitted snapshot and optionally removes only exact-owner marker matches.
func (f *resolverFakeBackend) release(_ context.Context, request Request, before Observation) error {
	f.releaseCalls++
	f.lastReleaseBefore = cloneObservation(before)
	if !f.skipReleaseEffect {
		remaining := make([]RuleFact, 0, len(f.observation.Rules))
		for _, rule := range f.observation.Rules {
			if !markerMatchesRequest(rule.Owner, request) {
				remaining = append(remaining, cloneRuleFact(rule))
			}
		}
		f.observation = Observation{Request: request, Complete: true, Rules: remaining}
	}
	return f.releaseErr
}

// newResolverFakeBackend returns one complete observation containing independent rule storage.
func newResolverFakeBackend(request Request, rules ...RuleFact) *resolverFakeBackend {
	return &resolverFakeBackend{
		observation: Observation{
			Request:  request,
			Complete: true,
			Rules:    cloneObservation(Observation{Rules: rules}).Rules,
		},
	}
}

// TestAdapterObserveValidatesAndCopiesBackendFacts proves native storage cannot mutate returned evidence.
func TestAdapterObserveValidatesAndCopiesBackendFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	platform := newResolverFakeBackend(request, resolverExactRule(request, "owned"))
	observation, err := newAdapter(platform).Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	platform.observation.Rules[0].NativeID = "changed"
	platform.observation.Rules[0].Servers[0] = request.Policy().HTTP.Advertised
	platform.observation.Rules[0].Owner.InstallationID = "installation-other"
	if observation.Rules[0].NativeID != "owned" || observation.Rules[0].Servers[0] != request.Endpoint() ||
		observation.Rules[0].Owner.InstallationID != string(request.InstallationID()) {
		t.Fatalf("Observe() returned backend-owned storage: %#v", observation.Rules[0])
	}
}

// TestAdapterObserveRejectsInvalidRequestsAndFacts covers typed observation boundary failures.
func TestAdapterObserveRejectsInvalidRequestsAndFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	tests := []struct {
		name     string
		request  Request
		platform *resolverFakeBackend
		want     ErrorKind
	}{
		{name: "invalid request", platform: newResolverFakeBackend(request), want: ErrorKindInvalidRequest},
		{name: "backend failure", request: request, platform: func() *resolverFakeBackend {
			platform := newResolverFakeBackend(request)
			platform.observeErr = errResolverFake
			return platform
		}(), want: ErrorKindObserveFailed},
		{name: "wrong request", request: request, platform: func() *resolverFakeBackend {
			other, err := NewRequest("installation-other", request.Policy())
			if err != nil {
				t.Fatalf("NewRequest() alternate fixture error = %v", err)
			}
			return newResolverFakeBackend(other)
		}(), want: ErrorKindInvalidFacts},
		{name: "invalid fact", request: request, platform: func() *resolverFakeBackend {
			rule := resolverExactRule(request, "")
			return newResolverFakeBackend(request, rule)
		}(), want: ErrorKindInvalidFacts},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newAdapter(test.platform).Observe(t.Context(), test.request)
			resolverAssertErrorKind(t, err, test.want)
		})
	}
}

// TestAdapterObserveHonorsCanceledAndNilContexts covers both context normalization paths.
func TestAdapterObserveHonorsCanceledAndNilContexts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	platform := newResolverFakeBackend(request)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newAdapter(platform).Observe(ctx, request)
	resolverAssertErrorKind(t, err, ErrorKindObserveFailed)
	if platform.observeCalls != 0 {
		t.Fatalf("Observe() called backend %d times after cancellation", platform.observeCalls)
	}
	if _, err := newAdapter(platform).Observe(nil, request); err != nil {
		t.Fatalf("Observe(nil) error = %v", err)
	}
}

// TestEnsureIfObservedCreatesAndRepairsOwnedRule verifies both admitted mutation states.
func TestEnsureIfObservedCreatesAndRepairsOwnedRule(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	drifted := resolverExactRule(request, "owned-drifted")
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	tests := []struct {
		name  string
		rules []RuleFact
	}{
		{name: "absent"},
		{name: "owned drifted", rules: []RuleFact{drifted}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request, test.rules...)
			before := cloneObservation(platform.observation)
			change, err := newAdapter(platform).EnsureIfObserved(t.Context(), request, resolverFingerprint(t, before))
			if err != nil {
				t.Fatalf("EnsureIfObserved() error = %v", err)
			}
			afterAssessment, err := change.After.Classify()
			if err != nil {
				t.Fatalf("Classify() after ensure error = %v", err)
			}
			if platform.ensureCalls != 1 || !change.Attempted || !change.Changed || change.Indeterminate || afterAssessment.State != StateExact {
				t.Fatalf("EnsureIfObserved() change = %#v, assessment = %#v, calls = %d", change, afterAssessment, platform.ensureCalls)
			}
			if resolverFingerprint(t, platform.lastEnsureBefore) != resolverFingerprint(t, before) {
				t.Fatal("EnsureIfObserved() backend did not receive the guarded observation")
			}
		})
	}
}

// TestEnsureIfObservedIsIdempotentForExactState proves no native effect occurs when already satisfied.
func TestEnsureIfObservedIsIdempotentForExactState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	platform := newResolverFakeBackend(request, resolverExactRule(request, "owned"))
	change, err := newAdapter(platform).EnsureIfObserved(t.Context(), request, resolverFingerprint(t, platform.observation))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if platform.ensureCalls != 0 || change.Attempted || change.Changed || change.Indeterminate {
		t.Fatalf("EnsureIfObserved() exact change = %#v, calls = %d", change, platform.ensureCalls)
	}
}

// TestEnsureIfObservedRefusesUnsafeClassifications covers foreign, ambiguous, and incomplete facts.
func TestEnsureIfObservedRefusesUnsafeClassifications(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	exact := resolverExactRule(request, "owned")
	foreign := cloneRuleFact(exact)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	tests := []struct {
		name      string
		rules     []RuleFact
		complete  bool
		truncated bool
		want      ErrorKind
	}{
		{name: "foreign", rules: []RuleFact{foreign}, complete: true, want: ErrorKindConflict},
		{name: "mixed foreign", rules: []RuleFact{exact, foreign}, complete: true, want: ErrorKindConflict},
		{name: "ambiguous owned", rules: []RuleFact{exact, exact}, complete: true, want: ErrorKindConflict},
		{name: "incomplete", complete: false, want: ErrorKindIndeterminate},
		{name: "truncated", complete: false, truncated: true, want: ErrorKindIndeterminate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request, test.rules...)
			platform.observation.Complete = test.complete
			platform.observation.Truncated = test.truncated
			change, err := newAdapter(platform).EnsureIfObserved(
				t.Context(),
				request,
				resolverFingerprint(t, platform.observation),
			)
			resolverAssertErrorKind(t, err, test.want)
			if platform.ensureCalls != 0 || change.Attempted {
				t.Fatalf("EnsureIfObserved() unsafe change = %#v, calls = %d", change, platform.ensureCalls)
			}
		})
	}
}

// TestEnsureIfObservedRequiresMatchingCASFingerprint proves stale evidence causes zero effect.
func TestEnsureIfObservedRequiresMatchingCASFingerprint(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	platform := newResolverFakeBackend(request)
	change, err := newAdapter(platform).EnsureIfObserved(
		t.Context(),
		request,
		strings.Repeat("0", canonicalFingerprintLength),
	)
	resolverAssertErrorKind(t, err, ErrorKindObservationChanged)
	if platform.ensureCalls != 0 || change.Attempted || change.Before.Request.PolicyFingerprint() != request.PolicyFingerprint() {
		t.Fatalf("EnsureIfObserved() stale change = %#v, calls = %d", change, platform.ensureCalls)
	}
}

// TestReleaseIfObservedRequiresMatchingCASFingerprint proves stale cleanup evidence causes zero effect.
func TestReleaseIfObservedRequiresMatchingCASFingerprint(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	platform := newResolverFakeBackend(request, resolverExactRule(request, "owned"))
	change, err := newAdapter(platform).ReleaseIfObserved(
		t.Context(),
		request,
		strings.Repeat("0", canonicalFingerprintLength),
	)
	resolverAssertErrorKind(t, err, ErrorKindObservationChanged)
	if platform.releaseCalls != 0 || change.Attempted || change.Before.Request.PolicyFingerprint() != request.PolicyFingerprint() {
		t.Fatalf("ReleaseIfObserved() stale change = %#v, calls = %d", change, platform.releaseCalls)
	}
}

// TestConditionalMutationRejectsMalformedFingerprintsBeforeObservation keeps CAS mandatory.
func TestConditionalMutationRejectsMalformedFingerprintsBeforeObservation(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	for _, operation := range []struct {
		name string
		call func(*Adapter) error
	}{
		{name: "ensure", call: func(adapter *Adapter) error {
			_, err := adapter.EnsureIfObserved(t.Context(), request, "bad")
			return err
		}},
		{name: "release", call: func(adapter *Adapter) error {
			_, err := adapter.ReleaseIfObserved(t.Context(), request, "bad")
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request)
			resolverAssertErrorKind(t, operation.call(newAdapter(platform)), ErrorKindInvalidRequest)
			if platform.observeCalls != 0 || platform.ensureCalls != 0 || platform.releaseCalls != 0 {
				t.Fatalf("malformed fingerprint reached backend: %#v", platform)
			}
		})
	}
}

// TestConditionalMutationPropagatesInitialObservationFailure prevents effects without fresh facts.
func TestConditionalMutationPropagatesInitialObservationFailure(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	for _, operation := range []struct {
		name string
		call func(*Adapter) error
	}{
		{name: "ensure", call: func(adapter *Adapter) error {
			_, err := adapter.EnsureIfObserved(t.Context(), request, strings.Repeat("0", canonicalFingerprintLength))
			return err
		}},
		{name: "release", call: func(adapter *Adapter) error {
			_, err := adapter.ReleaseIfObserved(t.Context(), request, strings.Repeat("0", canonicalFingerprintLength))
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request)
			platform.observeErr = errResolverFake
			resolverAssertErrorKind(t, operation.call(newAdapter(platform)), ErrorKindObserveFailed)
			if platform.ensureCalls != 0 || platform.releaseCalls != 0 {
				t.Fatalf("initial observation failure reached mutation backend: %#v", platform)
			}
		})
	}
}

// TestEnsureIfObservedReportsMutationAndVerificationFailures preserves uncertain effect evidence.
func TestEnsureIfObservedReportsMutationAndVerificationFailures(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	tests := []struct {
		name        string
		configure   func(*resolverFakeBackend)
		wantKind    ErrorKind
		wantChanged bool
		wantUnknown bool
	}{
		{name: "mutation returned error after effect", configure: func(platform *resolverFakeBackend) {
			platform.ensureErr = errResolverFake
		}, wantKind: ErrorKindMutationFailed, wantChanged: true},
		{name: "effect missing", configure: func(platform *resolverFakeBackend) {
			platform.skipEnsureEffect = true
		}, wantKind: ErrorKindVerificationFailed},
		{name: "post observation failed", configure: func(platform *resolverFakeBackend) {
			platform.observeErr = errResolverFake
			platform.observeErrCall = 2
		}, wantKind: ErrorKindVerificationFailed, wantUnknown: true},
		{name: "mutation and post observation failed", configure: func(platform *resolverFakeBackend) {
			platform.ensureErr = errResolverFake
			platform.observeErr = errResolverFake
			platform.observeErrCall = 2
		}, wantKind: ErrorKindMutationFailed, wantUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request)
			test.configure(platform)
			change, err := newAdapter(platform).EnsureIfObserved(
				t.Context(),
				request,
				resolverFingerprint(t, platform.observation),
			)
			resolverAssertErrorKind(t, err, test.wantKind)
			if !change.Attempted || change.Changed != test.wantChanged || change.Indeterminate != test.wantUnknown {
				t.Fatalf("EnsureIfObserved() failure change = %#v", change)
			}
		})
	}
}

// TestEnsureIfObservedChecksCancellationAgainBeforeMutation closes the observation-to-effect boundary.
func TestEnsureIfObservedChecksCancellationAgainBeforeMutation(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	ctx, cancel := context.WithCancel(t.Context())
	platform := newResolverFakeBackend(request)
	platform.afterObserve = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	_, err := newAdapter(platform).EnsureIfObserved(ctx, request, resolverFingerprint(t, platform.observation))
	resolverAssertErrorKind(t, err, ErrorKindMutationFailed)
	if platform.ensureCalls != 0 {
		t.Fatalf("EnsureIfObserved() called backend after cancellation %d times", platform.ensureCalls)
	}
}

// TestReleaseIfObservedChecksCancellationAgainBeforeMutation closes the cleanup observation-to-effect boundary.
func TestReleaseIfObservedChecksCancellationAgainBeforeMutation(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	ctx, cancel := context.WithCancel(t.Context())
	platform := newResolverFakeBackend(request, resolverExactRule(request, "owned"))
	platform.afterObserve = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	_, err := newAdapter(platform).ReleaseIfObserved(ctx, request, resolverFingerprint(t, platform.observation))
	resolverAssertErrorKind(t, err, ErrorKindMutationFailed)
	if platform.releaseCalls != 0 {
		t.Fatalf("ReleaseIfObserved() called backend after cancellation %d times", platform.releaseCalls)
	}
}

// TestReleaseIfObservedRemovesOwnedRuleAndPreservesForeignFacts verifies the two-axis cleanup contract.
func TestReleaseIfObservedRemovesOwnedRuleAndPreservesForeignFacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	owned := resolverExactRule(request, "owned")
	foreign := cloneRuleFact(owned)
	foreign.NativeID = "foreign"
	foreign.Namespace = ".child.test"
	foreign.Owner = nil
	platform := newResolverFakeBackend(request, owned, foreign)
	change, err := newAdapter(platform).ReleaseIfObserved(
		t.Context(),
		request,
		resolverFingerprint(t, platform.observation),
	)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	afterAssessment, err := change.After.Classify()
	if err != nil {
		t.Fatalf("Classify() after release error = %v", err)
	}
	if platform.releaseCalls != 1 || !change.Attempted || !change.Changed || change.Indeterminate ||
		afterAssessment.State != StateForeign || afterAssessment.Owned != OwnedStateAbsent || len(change.After.Rules) != 1 ||
		change.After.Rules[0].NativeID != foreign.NativeID {
		t.Fatalf("ReleaseIfObserved() change = %#v, assessment = %#v", change, afterAssessment)
	}
}

// TestReleaseIfObservedLeavesForeignOnlyStateUntouched proves cleanup never adopts another rule.
func TestReleaseIfObservedLeavesForeignOnlyStateUntouched(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	foreign := resolverExactRule(request, "foreign")
	foreign.Owner = nil
	platform := newResolverFakeBackend(request, foreign)
	change, err := newAdapter(platform).ReleaseIfObserved(
		t.Context(),
		request,
		resolverFingerprint(t, platform.observation),
	)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if platform.releaseCalls != 0 || change.Attempted || change.Changed || len(change.After.Rules) != 1 {
		t.Fatalf("ReleaseIfObserved() foreign-only change = %#v, calls = %d", change, platform.releaseCalls)
	}
}

// TestReleaseIfObservedRemovesUniqueOwnedDrift permits cleanup without broadening ownership.
func TestReleaseIfObservedRemovesUniqueOwnedDrift(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	drifted := resolverExactRule(request, "owned-drifted")
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	platform := newResolverFakeBackend(request, drifted)
	change, err := newAdapter(platform).ReleaseIfObserved(
		t.Context(),
		request,
		resolverFingerprint(t, platform.observation),
	)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if platform.releaseCalls != 1 || !change.Attempted || !change.Changed || len(change.After.Rules) != 0 {
		t.Fatalf("ReleaseIfObserved() drifted change = %#v, calls = %d", change, platform.releaseCalls)
	}
}

// TestReleaseIfObservedRefusesAmbiguousAndIncompleteOwnership covers fail-closed cleanup states.
func TestReleaseIfObservedRefusesAmbiguousAndIncompleteOwnership(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	exact := resolverExactRule(request, "owned")
	tests := []struct {
		name     string
		rules    []RuleFact
		complete bool
		want     ErrorKind
	}{
		{name: "ambiguous", rules: []RuleFact{exact, exact}, complete: true, want: ErrorKindConflict},
		{name: "incomplete", rules: []RuleFact{exact}, want: ErrorKindIndeterminate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request, test.rules...)
			platform.observation.Complete = test.complete
			change, err := newAdapter(platform).ReleaseIfObserved(
				t.Context(),
				request,
				resolverFingerprint(t, platform.observation),
			)
			resolverAssertErrorKind(t, err, test.want)
			if platform.releaseCalls != 0 || change.Attempted {
				t.Fatalf("ReleaseIfObserved() unsafe change = %#v, calls = %d", change, platform.releaseCalls)
			}
		})
	}
}

// TestReleaseIfObservedReportsMutationAndVerificationFailures preserves cleanup uncertainty.
func TestReleaseIfObservedReportsMutationAndVerificationFailures(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	tests := []struct {
		name        string
		configure   func(*resolverFakeBackend)
		wantKind    ErrorKind
		wantChanged bool
		wantUnknown bool
	}{
		{name: "mutation returned error after effect", configure: func(platform *resolverFakeBackend) {
			platform.releaseErr = errResolverFake
		}, wantKind: ErrorKindMutationFailed, wantChanged: true},
		{name: "effect missing", configure: func(platform *resolverFakeBackend) {
			platform.skipReleaseEffect = true
		}, wantKind: ErrorKindVerificationFailed},
		{name: "post observation failed", configure: func(platform *resolverFakeBackend) {
			platform.observeErr = errResolverFake
			platform.observeErrCall = 2
		}, wantKind: ErrorKindVerificationFailed, wantUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newResolverFakeBackend(request, resolverExactRule(request, "owned"))
			test.configure(platform)
			change, err := newAdapter(platform).ReleaseIfObserved(
				t.Context(),
				request,
				resolverFingerprint(t, platform.observation),
			)
			resolverAssertErrorKind(t, err, test.wantKind)
			if !change.Attempted || change.Changed != test.wantChanged || change.Indeterminate != test.wantUnknown {
				t.Fatalf("ReleaseIfObserved() failure change = %#v", change)
			}
		})
	}
}

// TestResolverErrorKeepsDisplayBoundedAndCauseDiscoverable verifies typed diagnostics do not print native output.
func TestResolverErrorKeepsDisplayBoundedAndCauseDiscoverable(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	observation := Observation{Request: request, Complete: true}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Classify() fixture error = %v", err)
	}
	nativeCause := errors.New("unbounded native diagnostic")
	err = operationError(ErrorKindConflict, "ensure", observation, assessment, nativeCause)
	if !errors.Is(err, nativeCause) {
		t.Fatal("resolver error did not preserve its cause")
	}
	if strings.Contains(err.Error(), nativeCause.Error()) {
		t.Fatalf("resolver error exposed native cause: %v", err)
	}
	if !strings.Contains(err.Error(), string(ErrorKindConflict)) || !strings.Contains(err.Error(), string(StateAbsent)) {
		t.Fatalf("resolver error omitted bounded classification: %v", err)
	}
}

// TestResolverErrorFormatsWithoutAssessment covers pre-observation typed failures.
func TestResolverErrorFormatsWithoutAssessment(t *testing.T) {
	err := (&Error{Kind: ErrorKindObserveFailed, Operation: "observe"}).Error()
	if err != "resolver observe: observe-failed" {
		t.Fatalf("Error() = %q, want bounded pre-observation summary", err)
	}
}

// resolverAssertErrorKind requires one bounded resolver error with the expected classification.
func resolverAssertErrorKind(t *testing.T, err error, want ErrorKind) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want resolver kind %q", want)
	}
	var resolverError *Error
	if !errors.As(err, &resolverError) {
		t.Fatalf("error = %T %v, want *resolver.Error", err, err)
	}
	if resolverError.Kind != want {
		t.Fatalf("error kind = %q, want %q: %v", resolverError.Kind, want, err)
	}
	return resolverError
}
