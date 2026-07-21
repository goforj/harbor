package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
)

var errTrustFake = errors.New("trust fake failure")

// trustFakeBackend records logical effects while exposing controlled observation boundaries.
type trustFakeBackend struct {
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

// observe returns the current injected snapshot without consulting a host trust store.
func (backend *trustFakeBackend) observe(_ context.Context, _ Request) (Observation, error) {
	backend.observeCalls++
	if backend.afterObserve != nil {
		backend.afterObserve(backend.observeCalls)
	}
	if backend.observeErr != nil && (backend.observeErrCall == 0 || backend.observeCalls == backend.observeErrCall) {
		return Observation{}, backend.observeErr
	}
	return cloneObservation(backend.observation), nil
}

// ensure records admitted facts and publishes one exact owned entry when configured to apply its effect.
func (backend *trustFakeBackend) ensure(_ context.Context, request Request, before Observation) error {
	backend.ensureCalls++
	backend.lastEnsureBefore = cloneObservation(before)
	if !backend.skipEnsureEffect {
		remaining := make([]Entry, 0, len(backend.observation.Entries)+1)
		for _, entry := range backend.observation.Entries {
			if !markerMatchesRequest(entry.Owner, request) {
				remaining = append(remaining, cloneEntry(entry))
			}
		}
		remaining = append(remaining, trustExactEntry(request, "owned-native"))
		backend.observation = Observation{Request: request, Complete: true, Entries: remaining}
	}
	return backend.ensureErr
}

// release records admitted facts and removes only entries with this request's exact owner marker.
func (backend *trustFakeBackend) release(_ context.Context, request Request, before Observation) error {
	backend.releaseCalls++
	backend.lastReleaseBefore = cloneObservation(before)
	if !backend.skipReleaseEffect {
		remaining := make([]Entry, 0, len(backend.observation.Entries))
		for _, entry := range backend.observation.Entries {
			if !markerMatchesRequest(entry.Owner, request) {
				remaining = append(remaining, cloneEntry(entry))
			}
		}
		backend.observation = Observation{Request: request, Complete: true, Entries: remaining}
	}
	return backend.releaseErr
}

// newTrustFakeBackend returns one complete observation containing independent entry storage.
func newTrustFakeBackend(request Request, entries ...Entry) *trustFakeBackend {
	return &trustFakeBackend{
		observation: Observation{
			Request:  request,
			Complete: true,
			Entries:  cloneObservation(Observation{Entries: entries}).Entries,
		},
	}
}

// TestNewRequestValidatesRootAndTrustScope covers immutable request and public-root trust boundaries.
func TestNewRequestValidatesRootAndTrustScope(t *testing.T) {
	root := trustTestRoot(t)
	for _, mechanism := range []networkpolicy.TrustMechanism{
		networkpolicy.DarwinCurrentUserTrust,
		networkpolicy.UbuntuSystemTrust,
		networkpolicy.WindowsCurrentUserTrust,
	} {
		t.Run(string(mechanism), func(t *testing.T) {
			request, err := NewRequest("installation-test", mechanism, root)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			if request.InstallationID() != "installation-test" || request.Mechanism() != mechanism ||
				request.AuthorityFingerprint() != root.Fingerprint {
				t.Fatalf("request = %#v, want exact authority", request)
			}
			gotRoot := request.Root()
			gotRoot.CertificatePEM[0] ^= 0xff
			if request.Root().CertificatePEM[0] == gotRoot.CertificatePEM[0] {
				t.Fatal("Root() exposed request-owned certificate bytes")
			}
			if err := request.OwnerMarker().Validate(); err != nil {
				t.Fatalf("OwnerMarker().Validate() error = %v", err)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*certificates.Root)
		id     string
		mode   networkpolicy.TrustMechanism
	}{
		{name: "invalid installation", id: " bad ", mode: networkpolicy.DarwinCurrentUserTrust},
		{name: "unsupported mechanism", id: "installation-test", mode: "unsupported"},
		{name: "missing certificate", id: "installation-test", mode: networkpolicy.DarwinCurrentUserTrust, mutate: func(value *certificates.Root) { value.CertificatePEM = nil }},
		{name: "private key material", id: "installation-test", mode: networkpolicy.DarwinCurrentUserTrust, mutate: func(value *certificates.Root) {
			value.CertificatePEM = append(value.CertificatePEM, []byte("PRIVATE KEY")...)
		}},
		{name: "fingerprint drift", id: "installation-test", mode: networkpolicy.DarwinCurrentUserTrust, mutate: func(value *certificates.Root) { value.Fingerprint = strings.Repeat("b", canonicalFingerprintLength) }},
		{name: "validity drift", id: "installation-test", mode: networkpolicy.DarwinCurrentUserTrust, mutate: func(value *certificates.Root) { value.NotAfter = value.NotAfter.Add(time.Minute) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := root
			candidate.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			if _, err := NewRequest(test.id, test.mode, candidate); err == nil {
				t.Fatal("NewRequest() accepted invalid authority")
			}
		})
	}
}

// TestObservationClassificationKeepsForeignAndPreexistingRootsSafe covers the complete trust state matrix.
func TestObservationClassificationKeepsForeignAndPreexistingRootsSafe(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	exact := trustExactEntry(request, "owned")
	drifted := cloneEntry(exact)
	drifted.NativeID = "owned-drifted"
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	foreign := cloneEntry(exact)
	foreign.NativeID = "preexisting"
	foreign.Owner = nil
	competing := cloneEntry(exact)
	competing.NativeID = "competing"
	otherOwner := request.OwnerMarker()
	otherOwner.InstallationID = "installation-other"
	competing.Owner = &otherOwner
	unrelated := cloneEntry(exact)
	unrelated.NativeID = "unrelated"
	unrelated.CertificateFingerprint = strings.Repeat("d", canonicalFingerprintLength)
	unrelated.Owner = nil

	tests := []struct {
		name         string
		complete     bool
		truncated    bool
		entries      []Entry
		wantState    State
		wantOwned    OwnedState
		wantForeigns int
	}{
		{name: "absent", complete: true, wantState: StateAbsent, wantOwned: OwnedStateAbsent},
		{name: "exact", complete: true, entries: []Entry{exact}, wantState: StateExact, wantOwned: OwnedStateExact},
		{name: "owned drifted", complete: true, entries: []Entry{drifted}, wantState: StateOwnedDrifted, wantOwned: OwnedStateDrifted},
		{name: "preexisting identical", complete: true, entries: []Entry{foreign}, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantForeigns: 1},
		{name: "competing owner", complete: true, entries: []Entry{competing}, wantState: StateForeign, wantOwned: OwnedStateAbsent, wantForeigns: 1},
		{name: "unrelated root", complete: true, entries: []Entry{unrelated}, wantState: StateAbsent, wantOwned: OwnedStateAbsent},
		{name: "ambiguous owned", complete: true, entries: []Entry{exact, func() Entry { value := cloneEntry(exact); value.NativeID = "owned-second"; return value }()}, wantState: StateAmbiguous, wantOwned: OwnedStateAmbiguous},
		{name: "incomplete", entries: []Entry{exact}, wantState: StateIndeterminate, wantOwned: OwnedStateExact},
		{name: "truncated", truncated: true, entries: []Entry{foreign}, wantState: StateIndeterminate, wantOwned: OwnedStateAbsent, wantForeigns: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := Observation{Request: request, Complete: test.complete, Truncated: test.truncated, Entries: test.entries}
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.State != test.wantState || assessment.Owned != test.wantOwned || assessment.ForeignCount != test.wantForeigns {
				t.Fatalf("Classify() = %#v, want %q/%q/%d", assessment, test.wantState, test.wantOwned, test.wantForeigns)
			}
		})
	}
}

// TestAdapterObserveCopiesFactsAndHonorsCancellation proves backend facts cannot alias and canceled calls do not observe.
func TestAdapterObserveCopiesFactsAndHonorsCancellation(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	backend := newTrustFakeBackend(request, trustExactEntry(request, "owned"))
	observation, err := newAdapter(backend).Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	backend.observation.Entries[0].NativeID = "changed"
	backend.observation.Entries[0].Owner.InstallationID = "installation-other"
	if observation.Entries[0].NativeID != "owned" || observation.Entries[0].Owner.InstallationID != request.InstallationID() {
		t.Fatalf("Observe() returned backend-owned facts: %#v", observation.Entries[0])
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := newAdapter(backend).Observe(ctx, request); !hasTrustErrorKind(err, ErrorKindObserveFailed) {
		t.Fatalf("Observe(canceled) error = %v, want observe failure", err)
	}
	if backend.observeCalls != 1 {
		t.Fatalf("canceled Observe() reached backend; calls = %d", backend.observeCalls)
	}
}

// TestAdapterRejectsInvalidBackendFacts keeps malformed or cross-request native observations outside mutation policy.
func TestAdapterRejectsInvalidBackendFacts(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	otherRequest, err := NewRequest("installation-other", request.Mechanism(), request.Root())
	if err != nil {
		t.Fatalf("NewRequest(other) error = %v", err)
	}
	exact := trustExactEntry(request, "owned")
	for _, test := range []struct {
		name        string
		observation Observation
		observeErr  error
		want        ErrorKind
	}{
		{name: "backend failure", observation: Observation{}, observeErr: errTrustFake, want: ErrorKindObserveFailed},
		{name: "wrong request", observation: Observation{Request: otherRequest, Complete: true, Entries: []Entry{exact}}, want: ErrorKindInvalidFacts},
		{name: "duplicate native ID", observation: Observation{Request: request, Complete: true, Entries: []Entry{exact, cloneEntry(exact)}}, want: ErrorKindInvalidFacts},
		{name: "invalid native attributes", observation: Observation{Request: request, Complete: true, Entries: []Entry{func() Entry {
			value := cloneEntry(exact)
			value.NativeAttributesSHA256 = "invalid"
			return value
		}()}}, want: ErrorKindInvalidFacts},
		{name: "complete and truncated", observation: Observation{Request: request, Complete: true, Truncated: true}, want: ErrorKindInvalidFacts},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newTrustFakeBackend(request)
			backend.observation = test.observation
			backend.observeErr = test.observeErr
			_, gotErr := newAdapter(backend).Observe(t.Context(), request)
			if !hasTrustErrorKind(gotErr, test.want) {
				t.Fatalf("Observe() error = %v, want %q", gotErr, test.want)
			}
		})
	}

	backend := newTrustFakeBackend(request)
	_, err = newAdapter(backend).Observe(t.Context(), Request{})
	if !hasTrustErrorKind(err, ErrorKindInvalidRequest) || backend.observeCalls != 0 {
		t.Fatalf("Observe(zero request) error = %v, calls = %d", err, backend.observeCalls)
	}
}

// TestAdapterEnsureHandlesExactDriftAndPreexistingRoots proves mutation admission and safe idempotent reuse.
func TestAdapterEnsureHandlesExactDriftAndPreexistingRoots(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	drifted := trustExactEntry(request, "owned-drifted")
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	for _, test := range []struct {
		name          string
		entries       []Entry
		wantCalls     int
		wantChanged   bool
		wantState     State
		wantErrorKind ErrorKind
	}{
		{name: "absent", wantCalls: 1, wantChanged: true, wantState: StateExact},
		{name: "owned drifted", entries: []Entry{drifted}, wantCalls: 1, wantChanged: true, wantState: StateExact},
		{name: "preexisting identical", entries: []Entry{func() Entry { value := trustExactEntry(request, "preexisting"); value.Owner = nil; return value }()}, wantState: StateForeign},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newTrustFakeBackend(request, test.entries...)
			before := backend.observation
			change, err := newAdapter(backend).EnsureIfObserved(t.Context(), request, trustFingerprint(t, before))
			if test.wantErrorKind != "" {
				if !hasTrustErrorKind(err, test.wantErrorKind) {
					t.Fatalf("EnsureIfObserved() error = %v, want %q", err, test.wantErrorKind)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureIfObserved() error = %v", err)
			}
			if backend.ensureCalls != test.wantCalls || change.Changed != test.wantChanged {
				t.Fatalf("EnsureIfObserved() change = %#v, calls = %d", change, backend.ensureCalls)
			}
			assessment, classifyErr := change.After.Classify()
			if classifyErr != nil || assessment.State != test.wantState {
				t.Fatalf("EnsureIfObserved() after = %#v, %v; want %q", assessment, classifyErr, test.wantState)
			}
		})
	}
}

// TestAdapterRejectsStaleForeignAmbiguousAndIndeterminateMutations proves every unsafe classification reaches zero native effects.
func TestAdapterRejectsStaleForeignAmbiguousAndIndeterminateMutations(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	exact := trustExactEntry(request, "owned")
	foreign := cloneEntry(exact)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	foreign.NativeExact = false
	foreign.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	second := cloneEntry(exact)
	second.NativeID = "owned-second"
	for _, test := range []struct {
		name      string
		entries   []Entry
		complete  bool
		truncated bool
		want      ErrorKind
		stale     bool
	}{
		{name: "stale", stale: true, want: ErrorKindObservationChanged},
		{name: "foreign", entries: []Entry{foreign}, complete: true, want: ErrorKindConflict},
		{name: "ambiguous", entries: []Entry{exact, second}, complete: true, want: ErrorKindConflict},
		{name: "incomplete", entries: []Entry{exact}, want: ErrorKindIndeterminate},
		{name: "truncated", entries: []Entry{foreign}, truncated: true, want: ErrorKindIndeterminate},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newTrustFakeBackend(request, test.entries...)
			backend.observation.Complete = test.complete
			backend.observation.Truncated = test.truncated
			expected := trustFingerprint(t, backend.observation)
			if test.stale {
				expected = strings.Repeat("0", canonicalFingerprintLength)
			}
			change, err := newAdapter(backend).EnsureIfObserved(t.Context(), request, expected)
			if !hasTrustErrorKind(err, test.want) {
				t.Fatalf("EnsureIfObserved() error = %v, want %q", err, test.want)
			}
			if backend.ensureCalls != 0 || change.Attempted {
				t.Fatalf("unsafe ensure mutated backend: change = %#v, calls = %d", change, backend.ensureCalls)
			}
		})
	}
}

// TestAdapterReleasePreservesUnownedEntries proves cleanup removes only explicit Harbor ownership.
func TestAdapterReleasePreservesUnownedEntries(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	owned := trustExactEntry(request, "owned")
	foreign := cloneEntry(owned)
	foreign.NativeID = "preexisting"
	foreign.Owner = nil
	backend := newTrustFakeBackend(request, owned, foreign)
	change, err := newAdapter(backend).ReleaseIfObserved(t.Context(), request, trustFingerprint(t, backend.observation))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if backend.releaseCalls != 1 || !change.Changed || len(change.After.Entries) != 1 || change.After.Entries[0].NativeID != foreign.NativeID {
		t.Fatalf("ReleaseIfObserved() change = %#v, calls = %d", change, backend.releaseCalls)
	}
	assessment, err := change.After.Classify()
	if err != nil || assessment.State != StateForeign || assessment.Owned != OwnedStateAbsent {
		t.Fatalf("ReleaseIfObserved() after = %#v, %v", assessment, err)
	}

	backend = newTrustFakeBackend(request, foreign)
	change, err = newAdapter(backend).ReleaseIfObserved(t.Context(), request, trustFingerprint(t, backend.observation))
	if err != nil || backend.releaseCalls != 0 || change.Attempted || len(change.After.Entries) != 1 {
		t.Fatalf("foreign-only release = %#v, err = %v, calls = %d", change, err, backend.releaseCalls)
	}
}

// TestAdapterReleaseRejectsAmbiguousOwnershipAndMalformedCAS proves cleanup never guesses between duplicate owners.
func TestAdapterReleaseRejectsAmbiguousOwnershipAndMalformedCAS(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	owned := trustExactEntry(request, "owned")
	second := cloneEntry(owned)
	second.NativeID = "owned-second"
	backend := newTrustFakeBackend(request, owned, second)
	change, err := newAdapter(backend).ReleaseIfObserved(t.Context(), request, trustFingerprint(t, backend.observation))
	if !hasTrustErrorKind(err, ErrorKindConflict) || change.Attempted || backend.releaseCalls != 0 {
		t.Fatalf("ambiguous release = %#v, err = %v, calls = %d", change, err, backend.releaseCalls)
	}

	backend = newTrustFakeBackend(request)
	change, err = newAdapter(backend).ReleaseIfObserved(t.Context(), request, "invalid")
	if !hasTrustErrorKind(err, ErrorKindInvalidRequest) || change.Attempted || backend.releaseCalls != 0 {
		t.Fatalf("malformed release CAS = %#v, err = %v, calls = %d", change, err, backend.releaseCalls)
	}
}

// TestAdapterReportsMutationAndVerificationFailures preserves uncertainty around effects and postconditions.
func TestAdapterReportsMutationAndVerificationFailures(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	for _, test := range []struct {
		name        string
		configure   func(*trustFakeBackend)
		wantKind    ErrorKind
		wantChanged bool
		wantUnknown bool
	}{
		{name: "mutation returned error after effect", configure: func(backend *trustFakeBackend) { backend.ensureErr = errTrustFake }, wantKind: ErrorKindMutationFailed, wantChanged: true},
		{name: "effect missing", configure: func(backend *trustFakeBackend) { backend.skipEnsureEffect = true }, wantKind: ErrorKindVerificationFailed},
		{name: "post observation failed", configure: func(backend *trustFakeBackend) { backend.observeErr = errTrustFake; backend.observeErrCall = 2 }, wantKind: ErrorKindVerificationFailed, wantUnknown: true},
		{name: "mutation and post observation failed", configure: func(backend *trustFakeBackend) {
			backend.ensureErr = errTrustFake
			backend.observeErr = errTrustFake
			backend.observeErrCall = 2
		}, wantKind: ErrorKindMutationFailed, wantUnknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newTrustFakeBackend(request)
			test.configure(backend)
			change, err := newAdapter(backend).EnsureIfObserved(t.Context(), request, trustFingerprint(t, backend.observation))
			if !hasTrustErrorKind(err, test.wantKind) {
				t.Fatalf("EnsureIfObserved() error = %v, want %q", err, test.wantKind)
			}
			if !change.Attempted || change.Changed != test.wantChanged || change.Indeterminate != test.wantUnknown {
				t.Fatalf("EnsureIfObserved() change = %#v", change)
			}
		})
	}
}

// TestAdapterChecksCancellationBeforeMutation ensures a canceled caller cannot cross the observation-to-effect boundary.
func TestAdapterChecksCancellationBeforeMutation(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	ctx, cancel := context.WithCancel(t.Context())
	backend := newTrustFakeBackend(request)
	backend.afterObserve = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	_, err := newAdapter(backend).EnsureIfObserved(ctx, request, trustFingerprint(t, backend.observation))
	if !hasTrustErrorKind(err, ErrorKindMutationFailed) || backend.ensureCalls != 0 {
		t.Fatalf("EnsureIfObserved() cancellation = %v, calls = %d", err, backend.ensureCalls)
	}
}

// TestObservationFingerprintBindsOrderInsensitiveFactsAndNativeDrift proves CAS sees hidden native changes.
func TestObservationFingerprintBindsOrderInsensitiveFactsAndNativeDrift(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	owned := trustExactEntry(request, "owned")
	foreign := cloneEntry(owned)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	left := Observation{Request: request, Complete: true, Entries: []Entry{owned, foreign}}
	right := Observation{Request: request, Complete: true, Entries: []Entry{foreign, owned}}
	if trustFingerprint(t, left) != trustFingerprint(t, right) {
		t.Fatal("entry order changed fingerprint")
	}
	drifted := cloneEntry(owned)
	drifted.NativeAttributesSHA256 = strings.Repeat("c", canonicalFingerprintLength)
	drifted.NativeExact = false
	if trustFingerprint(t, Observation{Request: request, Complete: true, Entries: []Entry{drifted}}) == trustFingerprint(t, Observation{Request: request, Complete: true, Entries: []Entry{owned}}) {
		t.Fatal("native drift did not change fingerprint")
	}
}

// trustTestRequest creates one validated request backed by a generated public-only CA.
func trustTestRequest(t *testing.T, mechanism networkpolicy.TrustMechanism) Request {
	t.Helper()
	request, err := NewRequest("installation-test", mechanism, trustTestRoot(t))
	if err != nil {
		t.Fatalf("NewRequest() fixture error = %v", err)
	}
	return request
}

// trustTestRoot creates one deterministic local CA root without retaining private material in the fixture.
func trustTestRoot(t *testing.T) certificates.Root {
	t.Helper()
	clock := time.Date(2032, time.March, 4, 12, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{
		CAValidity:   24 * time.Hour,
		LeafValidity: time.Hour,
		Backdate:     time.Minute,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	return certificates.Root{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
}

// trustExactEntry returns one complete entry carrying the request's exact owner marker.
func trustExactEntry(request Request, nativeID string) Entry {
	owner := request.OwnerMarker()
	return Entry{
		Mechanism:              request.Mechanism(),
		NativeID:               nativeID,
		CertificateFingerprint: request.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("b", canonicalFingerprintLength),
		Owner:                  &owner,
	}
}

// trustFingerprint returns a validated observation fingerprint for focused adapter fixtures.
func trustFingerprint(t *testing.T, observation Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Observation.Fingerprint() error = %v", err)
	}
	return fingerprint
}

// hasTrustErrorKind verifies one bounded adapter error without exposing native causes.
func hasTrustErrorKind(err error, want ErrorKind) bool {
	var trustError *Error
	return errors.As(err, &trustError) && trustError.Kind == want
}

// TestTrustFingerprintUsesCanonicalRootDigest keeps the fixture's public root identity bound to its DER bytes.
func TestTrustFingerprintUsesCanonicalRootDigest(t *testing.T) {
	root := trustTestRoot(t)
	block, _ := pemDecodeForTest(root.CertificatePEM)
	digest := sha256.Sum256(block)
	if got := hex.EncodeToString(digest[:]); got != root.Fingerprint {
		t.Fatalf("root fingerprint = %q, want %q", root.Fingerprint, got)
	}
}

// pemDecodeForTest decodes the generated certificate without expanding the production root parser surface.
func pemDecodeForTest(encoded []byte) ([]byte, []byte) {
	block, rest := pem.Decode(encoded)
	if block == nil {
		return nil, rest
	}
	return block.Bytes, rest
}
