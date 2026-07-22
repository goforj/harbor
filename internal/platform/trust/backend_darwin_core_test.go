//go:build darwin

package trust

import (
	"context"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// darwinTrustCoreFakeNative keeps the Security.framework boundary replaceable for core tests.
type darwinTrustCoreFakeNative struct {
	entries       []darwinTrustEntry
	owned         bool
	ownerErr      error
	ensureCalls   int
	ensureStarted chan struct{}
	ensureBlock   chan struct{}
	releaseCalls  int
	releaseErr    error
}

// snapshot returns an independent copy of the fake current-user trust entries.
func (native *darwinTrustCoreFakeNative) snapshot(context.Context, Request) ([]darwinTrustEntry, error) {
	return append([]darwinTrustEntry(nil), native.entries...), nil
}

// ensure records native mutation entry and can hold it for deterministic serialization tests.
func (native *darwinTrustCoreFakeNative) ensure(context.Context, Request) error {
	native.ensureCalls++
	if native.ensureStarted != nil {
		native.ensureStarted <- struct{}{}
	}
	if native.ensureBlock != nil {
		<-native.ensureBlock
	}
	return nil
}

// release records whether Darwin-specific admission reached the native effect boundary.
func (native *darwinTrustCoreFakeNative) release(context.Context, Request) error {
	native.releaseCalls++
	return native.releaseErr
}

// ownerExists returns the configured ownership marker result.
func (native *darwinTrustCoreFakeNative) ownerExists(context.Context, Request) (bool, error) {
	return native.owned, native.ownerErr
}

// TestDarwinTrustBackendMapsCertificateFacts proves native DER and exactness become bounded CAS facts with ownership.
func TestDarwinTrustBackendMapsCertificateFacts(t *testing.T) {
	root := trustTestRoot(t)
	request, err := NewRequestForRequester("installation-darwin", "501", networkpolicy.DarwinCurrentUserTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	if block == nil {
		t.Fatal("test root did not contain a PEM block")
	}
	native := &darwinTrustCoreFakeNative{
		entries: []darwinTrustEntry{
			{
				CertificateDER: block.Bytes,
				NativeExact:    true,
			},
		},
		owned: true,
	}
	observation, err := newDarwinTrustBackend(native).observe(context.Background(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if len(observation.Entries) != 1 || observation.Entries[0].CertificateFingerprint != request.AuthorityFingerprint() {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.Entries[0].Owner == nil || observation.Entries[0].Owner.RequesterIdentity != "501" || !observation.Entries[0].NativeExact {
		t.Fatalf("observation entry = %#v", observation.Entries[0])
	}
}

// TestDarwinAdministratorMarkerVerificationRejectsWrongOrMalformedData ensures observation never turns marker presence into exact ownership.
func TestDarwinAdministratorMarkerVerificationRejectsWrongOrMalformedData(t *testing.T) {
	root := trustTestRoot(t)
	request, err := NewRequestForRequester("installation-darwin", "harbord", networkpolicy.DarwinAdministratorTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	for _, test := range []struct {
		name      string
		markerErr error
	}{
		{
			name:      "wrong fingerprint",
			markerErr: errors.New("marker generic attribute fingerprint differs"),
		},
		{
			name:      "malformed data",
			markerErr: errors.New("marker generic attribute is malformed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &darwinTrustCoreFakeNative{
				entries: []darwinTrustEntry{
					{
						CertificateDER: block.Bytes,
						NativeExact:    true,
					},
				},
				owned:    true,
				ownerErr: test.markerErr,
			}
			adapter := newAdapter(newDarwinTrustBackend(native))
			if _, err := adapter.Observe(t.Context(), request); err == nil {
				t.Fatal("Observe() accepted an unverifiable administrator ownership marker")
			}
			if _, err := adapter.EnsureIfObserved(t.Context(), request, strings.Repeat("a", 64)); err == nil {
				t.Fatal("EnsureIfObserved() accepted an unverifiable administrator ownership marker")
			}
			if native.ensureCalls != 0 {
				t.Fatalf("ensure calls = %d, want 0", native.ensureCalls)
			}
		})
	}
}

// TestDarwinAdministratorOwnerClaimUsesOneAccountPerAuthority prevents distinct requesters from creating parallel markers for one root.
func TestDarwinAdministratorOwnerClaimUsesOneAccountPerAuthority(t *testing.T) {
	root := trustTestRoot(t)
	first, err := NewRequestForRequester("installation-first", "harbord-first", networkpolicy.DarwinAdministratorTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() first error = %v", err)
	}
	second, err := NewRequestForRequester("installation-second", "harbord-second", networkpolicy.DarwinAdministratorTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() second error = %v", err)
	}
	if darwinTrustOwnerAccount(first) != darwinTrustOwnerAccount(second) {
		t.Fatalf("administrator owner accounts differ: %q and %q", darwinTrustOwnerAccount(first), darwinTrustOwnerAccount(second))
	}
	if darwinAdministratorTrustOwnerAttribute(first) == darwinAdministratorTrustOwnerAttribute(second) {
		t.Fatal("administrator owner attributes did not retain distinct canonical owner markers")
	}
	user, err := NewRequestForRequester("installation-first", "501", networkpolicy.DarwinCurrentUserTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() user error = %v", err)
	}
	wantUserAccount := darwinTrustOwnerPrefix + "installation-first|501|" + string(networkpolicy.DarwinCurrentUserTrust) + "|" + user.AuthorityFingerprint()
	if darwinTrustOwnerAccount(user) != wantUserAccount {
		t.Fatalf("current-user owner account = %q, want %q", darwinTrustOwnerAccount(user), wantUserAccount)
	}
}

// TestDarwinAdministratorMarkerCleanupRequiresThisInvocationClaim prevents a failed duplicate-marker install from deleting an existing owner claim.
func TestDarwinAdministratorMarkerCleanupRequiresThisInvocationClaim(t *testing.T) {
	if darwinAdministratorMarkerCleanupRequired(false) {
		t.Fatal("duplicate marker set failure selected cleanup for a pre-existing marker")
	}
	if !darwinAdministratorMarkerCleanupRequired(true) {
		t.Fatal("new marker set failure did not select cleanup")
	}
}

// TestDarwinAdministratorMutationBusyRejectsConcurrentEnsure proves a helper never waits behind a marker-first mutation.
func TestDarwinAdministratorMutationBusyRejectsConcurrentEnsure(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	secondRequest, err := NewRequestForRequester("installation-second", "harbord-second", networkpolicy.DarwinAdministratorTrust, request.Root())
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	native := &darwinTrustCoreFakeNative{
		ensureStarted: make(chan struct{}, 2),
		ensureBlock:   make(chan struct{}),
	}
	backend := newDarwinTrustBackend(native)
	before := Observation{
		Request:  request,
		Complete: true,
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- backend.ensure(t.Context(), request, before)
	}()
	<-native.ensureStarted
	go func() {
		secondDone <- backend.ensure(t.Context(), secondRequest, Observation{
			Request:  secondRequest,
			Complete: true,
		})
	}()
	if err := <-secondDone; err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second ensure() error = %v, want busy retry error", err)
	}
	select {
	case <-native.ensureStarted:
		t.Fatal("second administrator ensure entered the native mutation while the first was active")
	default:
	}
	close(native.ensureBlock)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ensure() error = %v", err)
	}
	if native.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1", native.ensureCalls)
	}
}

// TestDarwinTrustBackendScopesAdministratorFacts proves the admin domain cannot share portable CAS IDs with user trust.
func TestDarwinTrustBackendScopesAdministratorFacts(t *testing.T) {
	root := trustTestRoot(t)
	request, err := NewRequestForRequester("installation-darwin", "harbord", networkpolicy.DarwinAdministratorTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	native := &darwinTrustCoreFakeNative{
		entries: []darwinTrustEntry{
			{
				CertificateDER: block.Bytes,
				NativeExact:    true,
			},
		},
		owned: true,
	}
	observation, err := newDarwinTrustBackend(native).observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if len(observation.Entries) != 1 || observation.Entries[0].Mechanism != networkpolicy.DarwinAdministratorTrust {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.Entries[0].NativeID == darwinUserTrustNativeIDPrefix+request.AuthorityFingerprint()+"-0" {
		t.Fatalf("administrator native ID is not mechanism scoped: %q", observation.Entries[0].NativeID)
	}
	if observation.Entries[0].Owner == nil || observation.Entries[0].Owner.RequesterIdentity != "harbord" {
		t.Fatalf("administrator observation owner = %#v", observation.Entries[0].Owner)
	}
}

// TestValidateDarwinTrustRequesterPreservesUserBindingButAllowsAdministratorObserver proves the daemon can observe the system domain.
func TestValidateDarwinTrustRequesterPreservesUserBindingButAllowsAdministratorObserver(t *testing.T) {
	root := trustTestRoot(t)
	user, err := NewRequestForRequester("installation-darwin", "not-the-current-uid", networkpolicy.DarwinCurrentUserTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	if err := validateDarwinTrustRequester(user); err == nil {
		t.Fatal("validateDarwinTrustRequester() accepted a cross-user request")
	}
	administrator, err := NewRequestForRequester("installation-darwin", "harbord", networkpolicy.DarwinAdministratorTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	if err := validateDarwinTrustRequester(administrator); err != nil {
		t.Fatalf("validateDarwinTrustRequester() administrator error = %v", err)
	}
}

// TestDarwinTrustBackendReleasesOnlyExactOwnedObservation prevents drift or competing facts from reaching certificate-only native removal.
func TestDarwinTrustBackendReleasesOnlyExactOwnedObservation(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	exact := trustExactEntry(request, "owned")
	drifted := cloneEntry(exact)
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = darwinTrustAttributesFingerprint(request.Mechanism(), false)
	foreign := cloneEntry(exact)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	second := cloneEntry(exact)
	second.NativeID = "owned-second"
	for _, test := range []struct {
		name       string
		complete   bool
		entries    []Entry
		wantNative bool
	}{
		{
			name:       "exact owned",
			complete:   true,
			entries:    []Entry{exact},
			wantNative: true,
		},
		{
			name:     "owned drifted",
			complete: true,
			entries:  []Entry{drifted},
		},
		{
			name:     "competing identical entry",
			complete: true,
			entries: []Entry{
				exact,
				foreign,
			},
		},
		{
			name:     "ambiguous ownership",
			complete: true,
			entries: []Entry{
				exact,
				second,
			},
		},
		{
			name:    "incomplete observation",
			entries: []Entry{exact},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &darwinTrustCoreFakeNative{}
			before := Observation{
				Request:  request,
				Complete: test.complete,
				Entries:  test.entries,
			}
			err := newDarwinTrustBackend(native).release(t.Context(), request, before)
			if test.wantNative {
				if err != nil || native.releaseCalls != 1 {
					t.Fatalf("release() error = %v, native calls = %d", err, native.releaseCalls)
				}
				return
			}
			if !errors.Is(err, errNativeMutationConflict) || native.releaseCalls != 0 {
				t.Fatalf("release() error = %v, native calls = %d", err, native.releaseCalls)
			}
		})
	}
}

// TestDarwinRootDERConvertsCanonicalPEM keeps Security.framework from receiving PEM armor instead of certificate DER.
func TestDarwinRootDERConvertsCanonicalPEM(t *testing.T) {
	root := trustTestRoot(t)
	der, err := darwinRootDER(root.CertificatePEM)
	if err != nil {
		t.Fatalf("darwinRootDER() error = %v", err)
	}
	block, rest := pem.Decode(root.CertificatePEM)
	if block == nil || len(rest) != 0 || string(der) != string(block.Bytes) {
		t.Fatalf("darwinRootDER() = %x, want %x", der, block.Bytes)
	}
	if _, err := darwinRootDER(append(append([]byte(nil), root.CertificatePEM...), '\n')); err == nil {
		t.Fatal("darwinRootDER() accepted trailing data")
	}
}
