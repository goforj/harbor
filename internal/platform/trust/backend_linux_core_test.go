package trust

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const ubuntuTrustTestRequester = "1000"

// ubuntuTrustFakeStore exposes one bounded in-memory system-store boundary.
type ubuntuTrustFakeStore struct {
	snapshotState ubuntuTrustSnapshot
	ensureCalls   int
	releaseCalls  int
}

// snapshot returns an independent copy of the injected Ubuntu trust facts.
func (store *ubuntuTrustFakeStore) snapshot(context.Context, Request) (ubuntuTrustSnapshot, error) {
	snapshot := store.snapshotState
	if snapshot.Marker != nil {
		marker := *snapshot.Marker
		snapshot.Marker = &marker
	}
	return snapshot, nil
}

// ensure simulates publishing the exact fixed source and active bundle entry.
func (store *ubuntuTrustFakeStore) ensure(_ context.Context, request Request) error {
	store.ensureCalls++
	marker := request.OwnerMarker()
	store.snapshotState = ubuntuTrustSnapshot{
		MarkerPresent:     true,
		MarkerExact:       true,
		Marker:            &marker,
		SourcePresent:     true,
		SourceExact:       true,
		SourceFingerprint: request.AuthorityFingerprint(),
		ActiveMatches:     1,
	}
	return nil
}

// release simulates retiring the fixed source, marker, and active bundle entry.
func (store *ubuntuTrustFakeStore) release(context.Context, Request) error {
	store.releaseCalls++
	store.snapshotState = ubuntuTrustSnapshot{}
	return nil
}

// TestUbuntuTrustBackendOwnsOnlyItsFixedMarkedRoot proves the complete portable ensure and release lifecycle.
func TestUbuntuTrustBackendOwnsOnlyItsFixedMarkedRoot(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	store := &ubuntuTrustFakeStore{}
	adapter := newAdapter(newUbuntuTrustBackend(store))

	absent, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(absent) error = %v", err)
	}
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(absent))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	assessment, err := ensured.After.Classify()
	if err != nil || assessment.State != StateExact || assessment.Owned != OwnedStateExact {
		t.Fatalf("ensure assessment = %#v, error = %v", assessment, err)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1", store.ensureCalls)
	}

	released, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(ensured.After))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	assessment, err = released.After.Classify()
	if err != nil || assessment.State != StateAbsent || assessment.Owned != OwnedStateAbsent {
		t.Fatalf("release assessment = %#v, error = %v", assessment, err)
	}
	if store.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", store.releaseCalls)
	}
}

// TestUbuntuTrustBackendRepairsInterruptedEnsure proves a marked inactive source remains recoverable.
func TestUbuntuTrustBackendRepairsInterruptedEnsure(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	marker := request.OwnerMarker()
	store := &ubuntuTrustFakeStore{snapshotState: ubuntuTrustSnapshot{
		MarkerPresent:     true,
		MarkerExact:       true,
		Marker:            &marker,
		SourcePresent:     true,
		SourceExact:       true,
		SourceFingerprint: request.AuthorityFingerprint(),
	}}
	adapter := newAdapter(newUbuntuTrustBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := before.Classify()
	if err != nil || assessment.State != StateOwnedDrifted {
		t.Fatalf("interrupted ensure assessment = %#v, error = %v", assessment, err)
	}
	if _, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(before)); err != nil {
		t.Fatalf("EnsureIfObserved(recover) error = %v", err)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1", store.ensureCalls)
	}
}

// TestUbuntuTrustBackendCompletesInterruptedRelease proves the pending source remains uniquely owned.
func TestUbuntuTrustBackendCompletesInterruptedRelease(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	marker := request.OwnerMarker()
	store := &ubuntuTrustFakeStore{snapshotState: ubuntuTrustSnapshot{
		MarkerPresent:      true,
		MarkerExact:        true,
		Marker:             &marker,
		PendingPresent:     true,
		PendingExact:       true,
		PendingFingerprint: request.AuthorityFingerprint(),
	}}
	adapter := newAdapter(newUbuntuTrustBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := before.Classify()
	if err != nil || assessment.State != StateOwnedDrifted {
		t.Fatalf("interrupted release assessment = %#v, error = %v", assessment, err)
	}
	if _, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(before)); err != nil {
		t.Fatalf("ReleaseIfObserved(recover) error = %v", err)
	}
	if store.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", store.releaseCalls)
	}
}

// TestUbuntuTrustBackendPreservesPreexistingIdenticalRoot proves unmarked system trust is never claimed.
func TestUbuntuTrustBackendPreservesPreexistingIdenticalRoot(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	store := &ubuntuTrustFakeStore{snapshotState: ubuntuTrustSnapshot{ActiveMatches: 1}}
	adapter := newAdapter(newUbuntuTrustBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(before))
	if err != nil {
		t.Fatalf("EnsureIfObserved(preexisting) error = %v", err)
	}
	if store.ensureCalls != 0 || change.Attempted {
		t.Fatalf("preexisting root was mutated: calls=%d change=%#v", store.ensureCalls, change)
	}
}

// TestUbuntuTrustBackendRejectsDuplicateActiveRoots proves one owned source cannot hide another identical trust claim.
func TestUbuntuTrustBackendRejectsDuplicateActiveRoots(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	marker := request.OwnerMarker()
	store := &ubuntuTrustFakeStore{snapshotState: ubuntuTrustSnapshot{
		MarkerPresent:     true,
		MarkerExact:       true,
		Marker:            &marker,
		SourcePresent:     true,
		SourceExact:       true,
		SourceFingerprint: request.AuthorityFingerprint(),
		ActiveMatches:     2,
	}}
	observation, err := newAdapter(newUbuntuTrustBackend(store)).Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateForeign || assessment.ForeignCount != 1 {
		t.Fatalf("duplicate assessment = %#v, error = %v", assessment, err)
	}
}

// TestUbuntuTrustRequestRequiresCanonicalNonRootUID covers the platform-specific identity boundary.
func TestUbuntuTrustRequestRequiresCanonicalNonRootUID(t *testing.T) {
	root := trustTestRoot(t)
	for _, requester := range []string{"", "0", "01000", "user", "4294967296"} {
		request, err := NewRequestForRequester("installation-test", requester, networkpolicy.UbuntuSystemTrust, root)
		if err != nil {
			if requester == "" {
				request, err = NewRequest("installation-test", networkpolicy.UbuntuSystemTrust, root)
			}
			if err != nil {
				t.Fatalf("construct request for %q: %v", requester, err)
			}
		}
		if err := validateUbuntuTrustRequest(request); err == nil {
			t.Fatalf("validateUbuntuTrustRequest() accepted %q", requester)
		}
	}
}

// TestUbuntuTrustOwnerTextRoundTripsOnlyCanonicalMarkers pins the protected sidecar grammar.
func TestUbuntuTrustOwnerTextRoundTripsOnlyCanonicalMarkers(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	marker, ok := parseUbuntuTrustOwner(ubuntuTrustOwnerText(request))
	if !ok || marker != request.OwnerMarker() {
		t.Fatalf("parseUbuntuTrustOwner() = (%#v, %t)", marker, ok)
	}
	for _, malformed := range []string{
		strings.TrimSuffix(ubuntuTrustOwnerText(request), "\n"),
		ubuntuTrustOwnerText(request) + "\n",
		strings.Replace(ubuntuTrustOwnerText(request), "|1000|", "|01000|", 1),
		strings.Replace(ubuntuTrustOwnerText(request), ubuntuTrustOwnerPrefix, "foreign|", 1),
	} {
		if _, ok := parseUbuntuTrustOwner(malformed); ok {
			t.Fatalf("parseUbuntuTrustOwner() accepted %q", malformed)
		}
	}
}

// ubuntuTrustTestRequest returns one canonical requester-bound Ubuntu trust request.
func ubuntuTrustTestRequest(t *testing.T) Request {
	t.Helper()
	request, err := NewRequestForRequester(
		"installation-test",
		ubuntuTrustTestRequester,
		networkpolicy.UbuntuSystemTrust,
		trustTestRoot(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
