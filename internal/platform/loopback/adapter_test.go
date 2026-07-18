package loopback

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

var (
	testAddress   = netip.MustParseAddr("127.77.0.10")
	testLoopback  = InterfaceFact{Name: "native-loopback", Index: 1, Kind: InterfaceKindLinuxNative, NativeLoopback: true}
	testForeign   = InterfaceFact{Name: "foreign", Index: 2}
	testExactFact = AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: testLoopback.Index}
)

// fakeBackend records exact effects while allowing each observation boundary to be controlled.
type fakeBackend struct {
	interfaceFacts     []InterfaceFact
	assignmentFacts    []AssignmentFact
	interfaceErr       error
	assignmentErr      error
	assignmentErrCall  int
	ensureErr          error
	releaseErr         error
	ensureBeforeError  bool
	releaseBeforeError bool
	skipEnsureEffect   bool
	skipReleaseEffect  bool
	assignmentCalls    int
	ensureCalls        int
	releaseCalls       int
	lastPrefix         netip.Prefix
	lastInterface      InterfaceFact
}

// interfaces returns the injected interface facts without consulting the host.
func (f *fakeBackend) interfaces(context.Context) ([]InterfaceFact, error) {
	return append([]InterfaceFact(nil), f.interfaceFacts...), f.interfaceErr
}

// assignments returns the injected exact-address facts without consulting the host.
func (f *fakeBackend) assignments(context.Context, netip.Addr) ([]AssignmentFact, error) {
	f.assignmentCalls++
	if f.assignmentErr != nil && (f.assignmentErrCall == 0 || f.assignmentErrCall == f.assignmentCalls) {
		return nil, f.assignmentErr
	}
	return append([]AssignmentFact(nil), f.assignmentFacts...), nil
}

// ensure records the bounded prefix and optionally simulates the resulting assignment.
func (f *fakeBackend) ensure(_ context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	f.ensureCalls++
	f.lastInterface = interf
	f.lastPrefix = prefix
	if f.ensureErr != nil && !f.ensureBeforeError {
		return f.ensureErr
	}
	if !f.skipEnsureEffect {
		f.assignmentFacts = []AssignmentFact{{Address: prefix.Addr(), PrefixLength: prefix.Bits(), InterfaceIndex: interf.Index}}
	}
	return f.ensureErr
}

// release records the bounded prefix and optionally simulates removing the assignment.
func (f *fakeBackend) release(_ context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	f.releaseCalls++
	f.lastInterface = interf
	f.lastPrefix = prefix
	if f.releaseErr != nil && !f.releaseBeforeError {
		return f.releaseErr
	}
	if !f.skipReleaseEffect {
		f.assignmentFacts = nil
	}
	return f.releaseErr
}

// newFakeBackend returns a host with one verified loopback and one ordinary interface.
func newFakeBackend(assignments ...AssignmentFact) *fakeBackend {
	return &fakeBackend{
		interfaceFacts:  []InterfaceFact{testLoopback, testForeign},
		assignmentFacts: assignments,
	}
}

// TestObserveClassifiesBoundedFacts covers every state exposed to higher policy layers.
func TestObserveClassifiesBoundedFacts(t *testing.T) {
	tests := []struct {
		name        string
		assignments []AssignmentFact
		want        State
	}{
		{name: "absent", want: StateAbsent},
		{name: "exact", assignments: []AssignmentFact{testExactFact}, want: StateExact},
		{name: "foreign", assignments: []AssignmentFact{{Address: testAddress, PrefixLength: 32, InterfaceIndex: testForeign.Index}}, want: StateForeign},
		{name: "non host prefix", assignments: []AssignmentFact{{Address: testAddress, PrefixLength: 8, InterfaceIndex: testLoopback.Index}}, want: StateNonHostPrefix},
		{name: "ambiguous", assignments: []AssignmentFact{testExactFact, {Address: testAddress, PrefixLength: 32, InterfaceIndex: testForeign.Index}}, want: StateAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, err := newAdapter(newFakeBackend(test.assignments...)).Observe(context.Background(), testAddress)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.State != test.want {
				t.Fatalf("Observe() state = %q, want %q", observation.State, test.want)
			}
			if observation.Address != testAddress || observation.Loopback != testLoopback {
				t.Fatalf("Observe() identity = %+v", observation)
			}
			for _, assignment := range observation.Assignments {
				if assignment.InterfaceName == "" {
					t.Fatalf("Observe() assignment lacks bounded interface facts: %+v", assignment)
				}
			}
		})
	}
}

// TestObserveRejectsUnsafeAddresses proves the adapter cannot escape canonical IPv4 loopback.
func TestObserveRejectsUnsafeAddresses(t *testing.T) {
	tests := []netip.Addr{
		{},
		netip.MustParseAddr("192.0.2.1"),
		netip.IPv6Loopback(),
		netip.MustParseAddr("::ffff:127.77.0.10"),
	}
	for _, address := range tests {
		_, err := newAdapter(newFakeBackend()).Observe(context.Background(), address)
		assertErrorKind(t, err, ErrorKindInvalidAddress)
	}
}

// TestObserveRejectsUnsafeInterfaceFacts proves selection is exact and bounded.
func TestObserveRejectsUnsafeInterfaceFacts(t *testing.T) {
	duplicate := testLoopback
	duplicate.Name = "duplicate"
	tests := []struct {
		name       string
		interfaces []InterfaceFact
		want       ErrorKind
	}{
		{name: "missing", interfaces: []InterfaceFact{testForeign}, want: ErrorKindLoopbackMissing},
		{name: "ambiguous", interfaces: []InterfaceFact{testLoopback, {Name: "other-native", Index: 3, Kind: InterfaceKindLinuxNative, NativeLoopback: true}}, want: ErrorKindLoopbackAmbiguous},
		{name: "duplicate index", interfaces: []InterfaceFact{testLoopback, duplicate}, want: ErrorKindInvalidFacts},
		{name: "empty name", interfaces: []InterfaceFact{{Index: 1, Kind: InterfaceKindLinuxNative, NativeLoopback: true}}, want: ErrorKindInvalidFacts},
		{name: "invalid index", interfaces: []InterfaceFact{{Name: "native", Index: 0, Kind: InterfaceKindLinuxNative, NativeLoopback: true}}, want: ErrorKindInvalidFacts},
		{name: "missing kind", interfaces: []InterfaceFact{{Name: "native", Index: 1, NativeLoopback: true}}, want: ErrorKindInvalidFacts},
		{name: "unknown kind", interfaces: []InterfaceFact{{Name: "native", Index: 1, Kind: "unknown", NativeLoopback: true}}, want: ErrorKindInvalidFacts},
		{name: "kind on ordinary interface", interfaces: []InterfaceFact{testLoopback, {Name: "ordinary", Index: 3, Kind: InterfaceKindLinuxNative}}, want: ErrorKindInvalidFacts},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := newFakeBackend()
			platform.interfaceFacts = test.interfaces
			_, err := newAdapter(platform).Observe(context.Background(), testAddress)
			assertErrorKind(t, err, test.want)
		})
	}

	platform := newFakeBackend()
	platform.interfaceFacts = make([]InterfaceFact, maximumInterfaceFacts+1)
	_, err := newAdapter(platform).Observe(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindInvalidFacts)
}

// TestObserveRejectsUnsafeAssignmentFacts proves platform matches cannot escape the selected address and interfaces.
func TestObserveRejectsUnsafeAssignmentFacts(t *testing.T) {
	tests := []struct {
		name       string
		assignment AssignmentFact
	}{
		{name: "wrong address", assignment: AssignmentFact{Address: netip.MustParseAddr("127.77.0.11"), PrefixLength: 32, InterfaceIndex: 1}},
		{name: "negative prefix", assignment: AssignmentFact{Address: testAddress, PrefixLength: -1, InterfaceIndex: 1}},
		{name: "large prefix", assignment: AssignmentFact{Address: testAddress, PrefixLength: 33, InterfaceIndex: 1}},
		{name: "unknown interface", assignment: AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: 99}},
		{name: "wrong interface name", assignment: AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: 1, InterfaceName: "wrong"}},
		{name: "Windows facts on Linux", assignment: AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: 1, Windows: exactWindowsFact()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newAdapter(newFakeBackend(test.assignment)).Observe(context.Background(), testAddress)
			assertErrorKind(t, err, ErrorKindInvalidFacts)
		})
	}

	assignments := make([]AssignmentFact, maximumAssignmentFacts+1)
	for index := range assignments {
		assignments[index] = testExactFact
	}
	_, err := newAdapter(newFakeBackend(assignments...)).Observe(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindInvalidFacts)
}

// TestObserveRequiresWindowsActiveAssignmentAttributes keeps externally shaped addresses out of StateExact.
func TestObserveRequiresWindowsActiveAssignmentAttributes(t *testing.T) {
	loopback := InterfaceFact{Name: "Loopback Pseudo-Interface 1", Index: 1, Kind: InterfaceKindWindowsSoftware, NativeLoopback: true}
	exact := AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: loopback.Index, Windows: exactWindowsFact()}
	tests := []struct {
		name   string
		mutate func(*WindowsAssignmentFact)
		want   State
	}{
		{name: "exact", want: StateExact},
		{name: "source candidate", mutate: func(fact *WindowsAssignmentFact) { fact.SkipAsSource = false }, want: StateAttributeConflict},
		{name: "automatic prefix", mutate: func(fact *WindowsAssignmentFact) { fact.PrefixOrigin = AddressOriginDHCP }, want: StateAttributeConflict},
		{name: "automatic suffix", mutate: func(fact *WindowsAssignmentFact) { fact.SuffixOrigin = AddressOriginRandom }, want: StateAttributeConflict},
		{name: "finite valid lifetime", mutate: func(fact *WindowsAssignmentFact) { fact.ValidLifetimeSeconds = 60 }, want: StateAttributeConflict},
		{name: "finite preferred lifetime", mutate: func(fact *WindowsAssignmentFact) { fact.PreferredLifetimeSeconds = 60 }, want: StateAttributeConflict},
		{name: "tentative remains exact assignment evidence", mutate: func(fact *WindowsAssignmentFact) { fact.DADState = AddressStateTentative }, want: StateExact},
		{name: "duplicate remains exact assignment evidence", mutate: func(fact *WindowsAssignmentFact) { fact.DADState = AddressStateDuplicate }, want: StateExact},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := &fakeBackend{interfaceFacts: []InterfaceFact{loopback}}
			fact := exact
			attributes := *exact.Windows
			fact.Windows = &attributes
			if test.mutate != nil {
				test.mutate(fact.Windows)
			}
			platform.assignmentFacts = []AssignmentFact{fact}
			observation, err := newAdapter(platform).Observe(context.Background(), testAddress)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.State != test.want {
				t.Fatalf("Observe() state = %q, want %q", observation.State, test.want)
			}
		})
	}

	platform := &fakeBackend{
		interfaceFacts:  []InterfaceFact{loopback},
		assignmentFacts: []AssignmentFact{{Address: testAddress, PrefixLength: 32, InterfaceIndex: loopback.Index}},
	}
	_, err := newAdapter(platform).Observe(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindInvalidFacts)
}

// TestMutationsRejectWindowsAttributeConflicts prevents manual-looking foreign state from being adopted or removed.
func TestMutationsRejectWindowsAttributeConflicts(t *testing.T) {
	loopback := InterfaceFact{Name: "Loopback Pseudo-Interface 1", Index: 1, Kind: InterfaceKindWindowsSoftware, NativeLoopback: true}
	attributes := exactWindowsFact()
	attributes.SkipAsSource = false
	assignment := AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceIndex: loopback.Index, Windows: attributes}
	platform := &fakeBackend{interfaceFacts: []InterfaceFact{loopback}, assignmentFacts: []AssignmentFact{assignment}}

	_, err := newAdapter(platform).Ensure(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindConflict)
	_, err = newAdapter(platform).Release(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindConflict)
	if platform.ensureCalls != 0 || platform.releaseCalls != 0 {
		t.Fatalf("attribute-conflict mutations = ensure %d, release %d", platform.ensureCalls, platform.releaseCalls)
	}
}

// TestObserveWrapsPlatformFailures verifies callers receive typed failures with an inspectable cause.
func TestObserveWrapsPlatformFailures(t *testing.T) {
	platform := newFakeBackend()
	platform.interfaceErr = errors.New("interface failure")
	_, err := newAdapter(platform).Observe(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindObserveFailed)
	if !errors.Is(err, platform.interfaceErr) {
		t.Fatalf("Observe() error = %v, want wrapped platform cause", err)
	}

	platform = newFakeBackend()
	platform.assignmentErr = errors.New("assignment failure")
	_, err = newAdapter(platform).Observe(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindObserveFailed)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newAdapter(newFakeBackend()).Observe(canceled, testAddress)
	assertErrorKind(t, err, ErrorKindObserveFailed)
}

// TestEnsureCreatesOnlyAbsentExactAssignments proves ensure's idempotence and mutation boundary.
func TestEnsureCreatesOnlyAbsentExactAssignments(t *testing.T) {
	platform := newFakeBackend()
	change, err := newAdapter(platform).Ensure(nil, testAddress)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !change.Attempted || !change.Changed || change.Indeterminate || change.Before.State != StateAbsent || change.After.State != StateExact {
		t.Fatalf("Ensure() change = %+v", change)
	}
	assertExactMutation(t, platform, 1, 0)

	change, err = newAdapter(platform).Ensure(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("idempotent Ensure() error = %v", err)
	}
	if change.Attempted || change.Changed || change.Indeterminate || platform.ensureCalls != 1 {
		t.Fatalf("idempotent Ensure() change = %+v, calls = %d", change, platform.ensureCalls)
	}
}

// TestConditionalMutationsRequireFreshObservation proves signed helper preconditions cannot survive host-state drift.
func TestConditionalMutationsRequireFreshObservation(t *testing.T) {
	t.Run("ensure", func(t *testing.T) {
		platform := newFakeBackend()
		adapter := newAdapter(platform)
		before, err := adapter.Observe(context.Background(), testAddress)
		if err != nil {
			t.Fatalf("Observe(absent) error = %v", err)
		}
		fingerprint, err := before.Fingerprint()
		if err != nil {
			t.Fatalf("Fingerprint(absent) error = %v", err)
		}

		platform.assignmentFacts = []AssignmentFact{testExactFact}
		change, err := adapter.EnsureIfObserved(context.Background(), testAddress, fingerprint)
		assertErrorKind(t, err, ErrorKindObservationChanged)
		if change.Before.State != StateExact || change.After.State != StateExact || platform.ensureCalls != 0 {
			t.Fatalf("EnsureIfObserved(drift) change = %+v, calls = %d", change, platform.ensureCalls)
		}
	})

	t.Run("release", func(t *testing.T) {
		platform := newFakeBackend(testExactFact)
		adapter := newAdapter(platform)
		before, err := adapter.Observe(context.Background(), testAddress)
		if err != nil {
			t.Fatalf("Observe(exact) error = %v", err)
		}
		fingerprint, err := before.Fingerprint()
		if err != nil {
			t.Fatalf("Fingerprint(exact) error = %v", err)
		}

		platform.assignmentFacts = nil
		change, err := adapter.ReleaseIfObserved(context.Background(), testAddress, fingerprint)
		assertErrorKind(t, err, ErrorKindObservationChanged)
		if change.Before.State != StateAbsent || change.After.State != StateAbsent || platform.releaseCalls != 0 {
			t.Fatalf("ReleaseIfObserved(drift) change = %+v, calls = %d", change, platform.releaseCalls)
		}
	})
}

// TestConditionalMutationsApplyExactMatchingObservations covers both admitted state transitions and fingerprint grammar.
func TestConditionalMutationsApplyExactMatchingObservations(t *testing.T) {
	platform := newFakeBackend()
	adapter := newAdapter(platform)
	for _, fingerprint := range []string{"bad", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := adapter.EnsureIfObserved(context.Background(), testAddress, fingerprint); err == nil {
			t.Fatalf("EnsureIfObserved(%q) error = nil", fingerprint)
		} else {
			assertErrorKind(t, err, ErrorKindInvalidFacts)
		}
		if _, err := adapter.ReleaseIfObserved(context.Background(), testAddress, fingerprint); err == nil {
			t.Fatalf("ReleaseIfObserved(%q) error = nil", fingerprint)
		} else {
			assertErrorKind(t, err, ErrorKindInvalidFacts)
		}
	}
	if platform.assignmentCalls != 0 || platform.ensureCalls != 0 {
		t.Fatalf("invalid fingerprint touched host: observations %d, mutations %d", platform.assignmentCalls, platform.ensureCalls)
	}

	absent, err := adapter.Observe(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("Observe(absent) error = %v", err)
	}
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(absent) error = %v", err)
	}
	ensured, err := adapter.EnsureIfObserved(nil, testAddress, absentFingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if !ensured.Attempted || ensured.After.State != StateExact || platform.ensureCalls != 1 {
		t.Fatalf("EnsureIfObserved() change = %+v, calls = %d", ensured, platform.ensureCalls)
	}

	exact, err := adapter.Observe(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("Observe(exact) error = %v", err)
	}
	exactFingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(exact) error = %v", err)
	}
	released, err := adapter.ReleaseIfObserved(nil, testAddress, exactFingerprint)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if !released.Attempted || released.After.State != StateAbsent || platform.releaseCalls != 1 {
		t.Fatalf("ReleaseIfObserved() change = %+v, calls = %d", released, platform.releaseCalls)
	}
}

// TestEnsureRejectsConflictingAssignments proves ensure never repairs foreign or malformed state destructively.
func TestEnsureRejectsConflictingAssignments(t *testing.T) {
	assignments := [][]AssignmentFact{
		{{Address: testAddress, PrefixLength: 32, InterfaceIndex: testForeign.Index}},
		{{Address: testAddress, PrefixLength: 8, InterfaceIndex: testLoopback.Index}},
		{testExactFact, {Address: testAddress, PrefixLength: 32, InterfaceIndex: testForeign.Index}},
	}
	for _, facts := range assignments {
		platform := newFakeBackend(facts...)
		_, err := newAdapter(platform).Ensure(context.Background(), testAddress)
		assertErrorKind(t, err, ErrorKindConflict)
		if platform.ensureCalls != 0 {
			t.Fatalf("Ensure() mutated conflicting facts %+v", facts)
		}
	}
}

// TestEnsureReportsMutationAndVerificationFailures distinguishes OS rejection from an ineffective success.
func TestEnsureReportsMutationAndVerificationFailures(t *testing.T) {
	platform := newFakeBackend()
	platform.ensureErr = errors.New("denied")
	_, err := newAdapter(platform).Ensure(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindMutationFailed)

	platform = newFakeBackend()
	platform.skipEnsureEffect = true
	change, err := newAdapter(platform).Ensure(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindVerificationFailed)
	if !change.Attempted || change.Changed || change.Indeterminate || change.After.State != StateAbsent {
		t.Fatalf("Ensure() failed verification change = %+v", change)
	}

	platform = newFakeBackend()
	platform.ensureErr = context.Canceled
	platform.ensureBeforeError = true
	change, err = newAdapter(platform).Ensure(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindMutationFailed)
	if !change.Attempted || !change.Changed || change.Indeterminate || change.After.State != StateExact {
		t.Fatalf("Ensure() reconciled mutation error change = %+v", change)
	}

	platform = newFakeBackend()
	platform.assignmentErr = errors.New("reconciliation unavailable")
	platform.assignmentErrCall = 2
	change, err = newAdapter(platform).Ensure(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindVerificationFailed)
	if !change.Attempted || !change.Indeterminate || change.After.Address.IsValid() {
		t.Fatalf("Ensure() indeterminate reconciliation change = %+v", change)
	}
}

// TestReleaseRemovesOnlyObservedExactAssignments proves release's exact and idempotent boundaries.
func TestReleaseRemovesOnlyObservedExactAssignments(t *testing.T) {
	platform := newFakeBackend(testExactFact)
	change, err := newAdapter(platform).Release(nil, testAddress)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if !change.Attempted || !change.Changed || change.Indeterminate || change.Before.State != StateExact || change.After.State != StateAbsent {
		t.Fatalf("Release() change = %+v", change)
	}
	assertExactMutation(t, platform, 0, 1)

	change, err = newAdapter(platform).Release(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("idempotent Release() error = %v", err)
	}
	if change.Attempted || change.Changed || change.Indeterminate || platform.releaseCalls != 1 {
		t.Fatalf("idempotent Release() change = %+v, calls = %d", change, platform.releaseCalls)
	}
}

// TestReleaseRejectsConflictsAndFailures proves no unsafe delete is attempted or hidden.
func TestReleaseRejectsConflictsAndFailures(t *testing.T) {
	platform := newFakeBackend(AssignmentFact{Address: testAddress, PrefixLength: 8, InterfaceIndex: testLoopback.Index})
	_, err := newAdapter(platform).Release(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindConflict)
	if platform.releaseCalls != 0 {
		t.Fatal("Release() mutated a non-/32 assignment")
	}

	platform = newFakeBackend(testExactFact)
	platform.releaseErr = errors.New("denied")
	_, err = newAdapter(platform).Release(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindMutationFailed)

	platform = newFakeBackend(testExactFact)
	platform.skipReleaseEffect = true
	change, err := newAdapter(platform).Release(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindVerificationFailed)
	if !change.Attempted || change.Changed || change.Indeterminate || change.After.State != StateExact {
		t.Fatalf("Release() failed verification change = %+v", change)
	}

	platform = newFakeBackend(testExactFact)
	platform.releaseErr = context.Canceled
	platform.releaseBeforeError = true
	change, err = newAdapter(platform).Release(context.Background(), testAddress)
	assertErrorKind(t, err, ErrorKindMutationFailed)
	if !change.Attempted || !change.Changed || change.Indeterminate || change.After.State != StateAbsent {
		t.Fatalf("Release() reconciled mutation error change = %+v", change)
	}
}

// TestErrorDisplayDoesNotLeakPlatformOutput keeps public diagnostics bounded even when causes are verbose.
func TestErrorDisplayDoesNotLeakPlatformOutput(t *testing.T) {
	platform := newFakeBackend()
	platform.interfaceErr = errors.New("sensitive and arbitrarily long platform output")
	_, err := newAdapter(platform).Observe(context.Background(), testAddress)
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Error() leaked platform output: %v", err)
	}
	conflict := operationError(ErrorKindConflict, "ensure", testAddress, StateForeign, Observation{}, nil)
	if got := conflict.Error(); !strings.Contains(got, string(StateForeign)) {
		t.Fatalf("conflict Error() = %q", got)
	}
	invalid := operationError(ErrorKindInvalidAddress, "observe", netip.Addr{}, "", Observation{}, nil)
	if got := invalid.Error(); strings.Contains(got, "invalid IP") {
		t.Fatalf("invalid-address Error() exposed an unstable address rendering: %q", got)
	}
}

// assertExactMutation verifies a backend received only the selected interface and requested /32.
func assertExactMutation(t *testing.T, platform *fakeBackend, ensureCalls int, releaseCalls int) {
	t.Helper()
	if platform.ensureCalls != ensureCalls || platform.releaseCalls != releaseCalls {
		t.Fatalf("mutation calls = ensure %d, release %d", platform.ensureCalls, platform.releaseCalls)
	}
	if platform.lastInterface != testLoopback || platform.lastPrefix != netip.PrefixFrom(testAddress, 32) {
		t.Fatalf("mutation target = %+v %s", platform.lastInterface, platform.lastPrefix)
	}
}

// assertErrorKind verifies the public error remains machine-classifiable.
func assertErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if typed.Kind != want {
		t.Fatalf("error kind = %q, want %q", typed.Kind, want)
	}
}

// exactWindowsFact returns the active, nonpersistent address shape created by the Windows backend.
func exactWindowsFact() *WindowsAssignmentFact {
	return &WindowsAssignmentFact{
		SkipAsSource:             true,
		PrefixOrigin:             AddressOriginManual,
		SuffixOrigin:             AddressOriginManual,
		ValidLifetimeSeconds:     ^uint32(0),
		PreferredLifetimeSeconds: ^uint32(0),
		DADState:                 AddressStatePreferred,
	}
}
