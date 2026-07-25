package trust

import (
	"context"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const windowsTrustTestRequester = "S-1-5-21-100-200-300-1001"

// windowsTrustFakeStore exposes one bounded in-memory CurrentUser Root boundary.
type windowsTrustFakeStore struct {
	entries      []windowsTrustEntry
	ensureCalls  int
	releaseCalls int
}

// snapshot returns an independent copy of the injected CurrentUser Root entries.
func (store *windowsTrustFakeStore) snapshot(_ context.Context, _ Request) ([]windowsTrustEntry, error) {
	entries := make([]windowsTrustEntry, len(store.entries))
	for index, entry := range store.entries {
		entries[index] = windowsTrustEntry{
			CertificateDER: append([]byte(nil), entry.CertificateDER...),
			FriendlyName:   entry.FriendlyName,
		}
	}
	return entries, nil
}

// ensure simulates adding one marked root after the portable backend admits absence.
func (store *windowsTrustFakeStore) ensure(_ context.Context, request Request) error {
	store.ensureCalls++
	der, err := windowsRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	store.entries = append(store.entries, windowsTrustEntry{
		CertificateDER: der,
		FriendlyName:   windowsTrustOwnerName(request),
	})
	return nil
}

// release simulates deleting only the uniquely marked root admitted by the portable backend.
func (store *windowsTrustFakeStore) release(_ context.Context, request Request) error {
	store.releaseCalls++
	remaining := make([]windowsTrustEntry, 0, len(store.entries))
	for _, entry := range store.entries {
		if entry.FriendlyName != windowsTrustOwnerName(request) {
			remaining = append(remaining, entry)
		}
	}
	store.entries = remaining
	return nil
}

// TestWindowsTrustBackendOwnsOnlyItsExactFriendlyName proves the full portable ensure and release lifecycle.
func TestWindowsTrustBackendOwnsOnlyItsExactFriendlyName(t *testing.T) {
	request := windowsTrustTestRequest(t)
	store := &windowsTrustFakeStore{}
	adapter := newAdapter(newWindowsTrustBackend(store))

	absent, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(absent) error = %v", err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(absent))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	assessment, err := change.After.Classify()
	if err != nil || assessment.State != StateExact || assessment.Owned != OwnedStateExact {
		t.Fatalf("ensure assessment = %#v, error = %v", assessment, err)
	}
	if store.ensureCalls != 1 || len(change.After.Entries) != 1 || change.After.Entries[0].Owner == nil {
		t.Fatalf("ensure calls = %d, after = %#v", store.ensureCalls, change.After)
	}

	released, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(change.After))
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

// TestWindowsTrustBackendPreservesPreexistingIdenticalRoot proves an unmarked authority is reused without being claimed or removed.
func TestWindowsTrustBackendPreservesPreexistingIdenticalRoot(t *testing.T) {
	request := windowsTrustTestRequest(t)
	der, err := windowsRootDER(request.Root().CertificatePEM)
	if err != nil {
		t.Fatalf("windowsRootDER() error = %v", err)
	}
	store := &windowsTrustFakeStore{entries: []windowsTrustEntry{{
		CertificateDER: der,
		FriendlyName:   "installed by the user",
	}}}
	adapter := newAdapter(newWindowsTrustBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	assessment, err := before.Classify()
	if err != nil || assessment.State != StateForeign || assessment.Owned != OwnedStateAbsent {
		t.Fatalf("assessment = %#v, error = %v", assessment, err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(before))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if change.Attempted || store.ensureCalls != 0 {
		t.Fatalf("ensure change = %#v, calls = %d", change, store.ensureCalls)
	}
	change, err = adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(before))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if change.Attempted || store.releaseCalls != 0 || len(store.entries) != 1 {
		t.Fatalf("release change = %#v, calls = %d, entries = %d", change, store.releaseCalls, len(store.entries))
	}
}

// TestWindowsTrustBackendRejectsInvalidScopeAndChangedMutationFacts covers every Windows-specific admission branch.
func TestWindowsTrustBackendRejectsInvalidScopeAndChangedMutationFacts(t *testing.T) {
	valid := windowsTrustTestRequest(t)
	for _, requester := range []string{
		"S-1-5-21-100-200-300",
		"S-1-5-21-0100-200-300-1001",
		"S-1-5-21-4294967296-200-300-1001",
		"S-1-5-21-100-200-300-name",
	} {
		request, err := NewRequestForRequester("installation-test", requester, networkpolicy.WindowsCurrentUserTrust, valid.Root())
		if err != nil {
			t.Fatalf("NewRequestForRequester(%q) fixture error = %v", requester, err)
		}
		if _, err := newAdapter(newWindowsTrustBackend(&windowsTrustFakeStore{})).Observe(t.Context(), request); err == nil {
			t.Fatalf("Observe() accepted requester %q", requester)
		}
	}
	unbound, err := NewRequest("installation-test", networkpolicy.WindowsCurrentUserTrust, valid.Root())
	if err != nil {
		t.Fatalf("NewRequest() fixture error = %v", err)
	}
	if _, err := newAdapter(newWindowsTrustBackend(&windowsTrustFakeStore{})).Observe(t.Context(), unbound); err == nil {
		t.Fatal("Observe() accepted an unbound Windows requester")
	}

	darwin, err := NewRequestForRequester(
		"installation-test",
		windowsTrustTestRequester,
		networkpolicy.DarwinCurrentUserTrust,
		valid.Root(),
	)
	if err != nil {
		t.Fatalf("NewRequestForRequester(Darwin) fixture error = %v", err)
	}
	if _, err := newAdapter(newWindowsTrustBackend(&windowsTrustFakeStore{})).Observe(t.Context(), darwin); err == nil {
		t.Fatal("Observe() accepted a Darwin trust mechanism")
	}

	backend := newWindowsTrustBackend(&windowsTrustFakeStore{})
	ownedStore := &windowsTrustFakeStore{}
	ownedBackend := newWindowsTrustBackend(ownedStore)
	absent, err := backend.observe(t.Context(), valid)
	if err != nil {
		t.Fatalf("observe(absent) error = %v", err)
	}
	owner := valid.OwnerMarker()
	exact := Observation{Request: valid, Complete: true, Entries: []Entry{{
		Mechanism:              valid.Mechanism(),
		NativeID:               "windows-owned",
		CertificateFingerprint: valid.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: windowsTrustAttributesFingerprint(windowsTrustOwnerName(valid)),
		Owner:                  &owner,
	}}}
	if err := backend.ensure(t.Context(), valid, exact); err == nil {
		t.Fatal("ensure() accepted non-absent facts")
	}
	if err := ownedBackend.release(t.Context(), valid, absent); err == nil {
		t.Fatal("release() accepted absent facts")
	}
	if ownedStore.ensureCalls != 0 || ownedStore.releaseCalls != 0 {
		t.Fatalf("native calls = ensure %d, release %d", ownedStore.ensureCalls, ownedStore.releaseCalls)
	}
}

// TestWindowsTrustOwnershipEncodingRejectsNearMarkers proves foreign friendly names cannot acquire Harbor ownership.
func TestWindowsTrustOwnershipEncodingRejectsNearMarkers(t *testing.T) {
	request := windowsTrustTestRequest(t)
	name := windowsTrustOwnerName(request)
	marker, ok := parseWindowsTrustOwner(name)
	if !ok || marker != request.OwnerMarker() {
		t.Fatalf("parseWindowsTrustOwner() = %#v, %t", marker, ok)
	}
	for _, candidate := range []string{
		"",
		"foreign|" + name,
		name + "|extra",
		strings.Replace(name, windowsTrustTestRequester, "S-1-5-21-01-2-3-4", 1),
		strings.Replace(name, request.AuthorityFingerprint(), strings.Repeat("z", 64), 1),
		strings.Repeat("x", maximumNativeIDLength+1),
	} {
		if marker, ok := parseWindowsTrustOwner(candidate); ok {
			t.Fatalf("parseWindowsTrustOwner(%q) = %#v, true", candidate, marker)
		}
	}
	if windowsTrustAttributesFingerprint(name) == windowsTrustAttributesFingerprint(name+"x") {
		t.Fatal("friendly-name drift did not change native attributes")
	}
}

// TestWindowsRootDERRequiresCanonicalPublicPEM proves the native boundary rejects concatenated or decorated roots.
func TestWindowsRootDERRequiresCanonicalPublicPEM(t *testing.T) {
	request := windowsTrustTestRequest(t)
	certificate := request.Root().CertificatePEM
	if _, err := windowsRootDER(certificate); err != nil {
		t.Fatalf("windowsRootDER(valid) error = %v", err)
	}
	block, _ := pem.Decode(certificate)
	for _, candidate := range [][]byte{
		nil,
		append([]byte("prefix"), certificate...),
		append(append([]byte(nil), certificate...), certificate...),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block.Bytes}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Owner": "foreign"}, Bytes: block.Bytes}),
	} {
		if _, err := windowsRootDER(candidate); err == nil {
			t.Fatalf("windowsRootDER(%q) accepted invalid PEM", candidate)
		}
	}
}

// windowsTrustTestRequest creates one Windows request explicitly bound to the interactive account SID.
func windowsTrustTestRequest(t *testing.T) Request {
	t.Helper()
	request, err := NewRequestForRequester(
		"installation-test",
		windowsTrustTestRequester,
		networkpolicy.WindowsCurrentUserTrust,
		trustTestRoot(t),
	)
	if err != nil {
		t.Fatalf("NewRequestForRequester() fixture error = %v", err)
	}
	return request
}
