//go:build darwin

package trust

import (
	"context"
	"encoding/pem"
	"errors"
	"slices"
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
	releaseMutate func()
	artifacts     *darwinTrustCoreFakeAdministratorArtifacts
}

// darwinTrustCoreFakeAdministratorArtifacts models the states hidden from the trust-settings snapshot by System.keychain cleanup.
type darwinTrustCoreFakeAdministratorArtifacts struct {
	root               darwinAdministratorArtifactState
	trust              darwinAdministratorArtifactState
	rootRemoval        darwinAdministratorArtifactRemovalResult
	trustBeforeRemoval darwinAdministratorArtifactState
	trustRemoval       darwinAdministratorArtifactRemovalResult
	finalRoot          darwinAdministratorArtifactState
	finalTrust         darwinAdministratorArtifactState
}

// darwinAdministratorArtifactState models the bounded cleanup states hidden from a System.keychain marker-only observation.
type darwinAdministratorArtifactState uint8

const (
	darwinAdministratorArtifactAbsent darwinAdministratorArtifactState = iota
	darwinAdministratorArtifactExact
	darwinAdministratorArtifactDrifted
)

// darwinAdministratorArtifactRemovalResult models the two guarded deletion outcomes relevant to marker retention.
type darwinAdministratorArtifactRemovalResult uint8

const (
	darwinAdministratorArtifactRemoved darwinAdministratorArtifactRemovalResult = iota
	darwinAdministratorArtifactNotFound
)

// darwinAdministratorCleanupPlan records the test seam's bounded native cleanup decision.
type darwinAdministratorCleanupPlan struct {
	removeRoot   bool
	removeTrust  bool
	removeMarker bool
	stale        bool
}

// darwinAdministratorCleanupPlanFor mirrors the native helper's exact-or-stale admission decision for fake-native tests.
func darwinAdministratorCleanupPlanFor(root darwinAdministratorArtifactState, trust darwinAdministratorArtifactState) darwinAdministratorCleanupPlan {
	if root == darwinAdministratorArtifactDrifted || trust == darwinAdministratorArtifactDrifted {
		return darwinAdministratorCleanupPlan{stale: true}
	}
	return darwinAdministratorCleanupPlan{
		removeRoot:   root == darwinAdministratorArtifactExact,
		removeTrust:  trust == darwinAdministratorArtifactExact,
		removeMarker: true,
	}
}

// canDeleteMarker reports whether the fake native helper may delete the exact marker after its guarded artifact mutations.
func (plan darwinAdministratorCleanupPlan) canDeleteMarker(
	rootResult darwinAdministratorArtifactRemovalResult,
	trustBeforeRemoval darwinAdministratorArtifactState,
	trustResult darwinAdministratorArtifactRemovalResult,
) bool {
	if plan.stale || (plan.removeRoot && rootResult != darwinAdministratorArtifactRemoved) {
		return false
	}
	if !plan.removeTrust {
		return plan.removeMarker
	}
	return trustBeforeRemoval == darwinAdministratorArtifactExact && trustResult == darwinAdministratorArtifactRemoved
}

// snapshot returns an independent copy of the fake current-user trust entries.
func (native *darwinTrustCoreFakeNative) snapshot(_ context.Context, request Request) ([]darwinTrustEntry, error) {
	entries := append([]darwinTrustEntry(nil), native.entries...)
	if native.artifacts == nil || native.artifacts.trust == darwinAdministratorArtifactAbsent {
		return entries, nil
	}
	der, err := darwinRootDER(request.Root().CertificatePEM)
	if err != nil {
		return nil, err
	}
	entries = append(entries, darwinTrustEntry{
		CertificateDER: der,
		NativeExact:    native.artifacts.trust == darwinAdministratorArtifactExact,
	})
	return entries, nil
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
	if native.artifacts != nil {
		plan := darwinAdministratorCleanupPlanFor(native.artifacts.root, native.artifacts.trust)
		if !plan.canDeleteMarker(native.artifacts.rootRemoval, native.artifacts.trustBeforeRemoval, native.artifacts.trustRemoval) {
			return errNativeObservationChanged
		}
		native.artifacts.root = darwinAdministratorArtifactAbsent
		native.artifacts.trust = darwinAdministratorArtifactAbsent
		if native.artifacts.finalRoot != darwinAdministratorArtifactAbsent || native.artifacts.finalTrust != darwinAdministratorArtifactAbsent {
			native.artifacts.root = native.artifacts.finalRoot
			native.artifacts.trust = native.artifacts.finalTrust
			return errNativeObservationChanged
		}
		native.owned = false
	}
	if native.releaseMutate != nil {
		native.releaseMutate()
	}
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

// TestDarwinAdministratorMarkerOnlyObservationKeepsInterruptedCleanupRetryable proves an exact marker remains visible after its root and settings were removed.
func TestDarwinAdministratorMarkerOnlyObservationKeepsInterruptedCleanupRetryable(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	native := &darwinTrustCoreFakeNative{owned: true}
	backend := newDarwinTrustBackend(native)
	observation, err := backend.observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if !darwinAdministratorMarkerOnlyObservation(observation, request) {
		t.Fatalf("observation = %#v, want exact administrator marker-only fact", observation)
	}
	if !IsRecoverableReleaseObservation(observation) {
		t.Fatalf("IsRecoverableReleaseObservation(%#v) = false, want true", observation)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Observation)
	}{
		{
			name: "native ID",
			mutate: func(value *Observation) {
				value.Entries[0].NativeID += "-near-miss"
			},
		},
		{
			name: "extra entry",
			mutate: func(value *Observation) {
				extra := cloneEntry(value.Entries[0])
				extra.NativeID = "darwin-admin-trust-owner-marker-extra"
				value.Entries = append(value.Entries, extra)
			},
		},
		{
			name: "wrong owner",
			mutate: func(value *Observation) {
				value.Entries[0].Owner.InstallationID = "installation-other"
			},
		},
		{
			name: "authority fingerprint",
			mutate: func(value *Observation) {
				value.Entries[0].CertificateFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "exact flag",
			mutate: func(value *Observation) {
				value.Entries[0].NativeExact = true
			},
		},
		{
			name: "attributes",
			mutate: func(value *Observation) {
				value.Entries[0].NativeAttributesSHA256 = strings.Repeat("a", 64)
			},
		},
		{
			name: "incomplete",
			mutate: func(value *Observation) {
				value.Complete = false
			},
		},
		{
			name: "truncated",
			mutate: func(value *Observation) {
				value.Complete = false
				value.Truncated = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			nearMiss := cloneObservation(observation)
			test.mutate(&nearMiss)
			if IsRecoverableReleaseObservation(nearMiss) {
				t.Fatalf("IsRecoverableReleaseObservation(%#v) = true, want false", nearMiss)
			}
		})
	}
	assessment := classifyValidated(observation)
	if assessment.State != StateOwnedDrifted || assessment.Owned != OwnedStateDrifted {
		t.Fatalf("assessment = %#v, want marker-only owned drift", assessment)
	}

	native.releaseMutate = func() { native.owned = false }
	adapter := newAdapter(backend)
	change, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(observation))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if native.releaseCalls != 1 || !change.Changed || classifyValidated(change.After).Owned != OwnedStateAbsent {
		t.Fatalf("release calls = %d, change = %#v", native.releaseCalls, change)
	}
}

// TestDarwinAdministratorInterruptedPartialCleanupRetriesOnlyExactMarker proves every exact partial subset reaches the native recheck while the marker remains.
func TestDarwinAdministratorInterruptedPartialCleanupRetriesOnlyExactMarker(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	for _, test := range []struct {
		name  string
		root  darwinAdministratorArtifactState
		trust darwinAdministratorArtifactState
		state State
	}{
		{
			name:  "marker only",
			state: StateOwnedDrifted,
		},
		{
			name:  "marker and exact reserved-label root",
			root:  darwinAdministratorArtifactExact,
			state: StateOwnedDrifted,
		},
		{
			name:  "marker and exact trust settings",
			trust: darwinAdministratorArtifactExact,
			state: StateExact,
		},
		{
			name:  "marker, root, and settings",
			root:  darwinAdministratorArtifactExact,
			trust: darwinAdministratorArtifactExact,
			state: StateExact,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &darwinTrustCoreFakeNative{owned: true, artifacts: &darwinTrustCoreFakeAdministratorArtifacts{
				root:               test.root,
				trust:              test.trust,
				trustBeforeRemoval: test.trust,
			}}
			backend := newDarwinTrustBackend(native)
			before, err := backend.observe(t.Context(), request)
			if err != nil {
				t.Fatalf("observe() error = %v", err)
			}
			if assessment := classifyValidated(before); assessment.State != test.state {
				t.Fatalf("assessment = %#v, want state %q", assessment, test.state)
			}
			if err := backend.release(t.Context(), request, before); err != nil {
				t.Fatalf("release() error = %v", err)
			}
			if native.releaseCalls != 1 || native.owned || native.artifacts.root != darwinAdministratorArtifactAbsent || native.artifacts.trust != darwinAdministratorArtifactAbsent {
				t.Fatalf("release calls = %d, artifacts = %#v", native.releaseCalls, native.artifacts)
			}
		})
	}
}

// TestDarwinAdministratorCleanupDecisionRejectsConcurrentArtifactChanges proves missing or drifted effects retain the marker for a later exact retry.
func TestDarwinAdministratorCleanupDecisionRejectsConcurrentArtifactChanges(t *testing.T) {
	for _, test := range []struct {
		name               string
		root               darwinAdministratorArtifactState
		trust              darwinAdministratorArtifactState
		rootRemoval        darwinAdministratorArtifactRemovalResult
		trustBeforeRemoval darwinAdministratorArtifactState
		trustRemoval       darwinAdministratorArtifactRemovalResult
		wantDeleteMarker   bool
	}{
		{
			name:             "both absent",
			wantDeleteMarker: true,
		},
		{
			name:             "root only",
			root:             darwinAdministratorArtifactExact,
			wantDeleteMarker: true,
		},
		{
			name:               "settings only",
			trust:              darwinAdministratorArtifactExact,
			trustBeforeRemoval: darwinAdministratorArtifactExact,
			wantDeleteMarker:   true,
		},
		{
			name:               "both exact",
			root:               darwinAdministratorArtifactExact,
			trust:              darwinAdministratorArtifactExact,
			trustBeforeRemoval: darwinAdministratorArtifactExact,
			wantDeleteMarker:   true,
		},
		{
			name: "drifted root",
			root: darwinAdministratorArtifactDrifted,
		},
		{
			name:  "drifted settings",
			trust: darwinAdministratorArtifactDrifted,
		},
		{
			name:        "root disappeared during delete",
			root:        darwinAdministratorArtifactExact,
			rootRemoval: darwinAdministratorArtifactNotFound,
		},
		{
			name:               "settings changed before delete",
			trust:              darwinAdministratorArtifactExact,
			trustBeforeRemoval: darwinAdministratorArtifactAbsent,
		},
		{
			name:               "settings disappeared during delete",
			trust:              darwinAdministratorArtifactExact,
			trustBeforeRemoval: darwinAdministratorArtifactExact,
			trustRemoval:       darwinAdministratorArtifactNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := darwinAdministratorCleanupPlanFor(test.root, test.trust)
			if got := plan.canDeleteMarker(test.rootRemoval, test.trustBeforeRemoval, test.trustRemoval); got != test.wantDeleteMarker {
				t.Fatalf("canDeleteMarker() = %t, want %t; plan = %#v", got, test.wantDeleteMarker, plan)
			}
		})
	}
}

// TestDarwinAdministratorMarkerCleanupRetainsMarkerWhenArtifactsReappear proves the final native recheck fences late root or settings creation.
func TestDarwinAdministratorMarkerCleanupRetainsMarkerWhenArtifactsReappear(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	for _, test := range []struct {
		name       string
		root       darwinAdministratorArtifactState
		trust      darwinAdministratorArtifactState
		finalRoot  darwinAdministratorArtifactState
		finalTrust darwinAdministratorArtifactState
	}{
		{
			name:      "root appears before marker deletion",
			finalRoot: darwinAdministratorArtifactExact,
		},
		{
			name:       "settings appear before marker deletion",
			finalTrust: darwinAdministratorArtifactExact,
		},
		{
			name:      "root drifts before marker deletion",
			finalRoot: darwinAdministratorArtifactDrifted,
		},
		{
			name:       "settings drift before marker deletion",
			finalTrust: darwinAdministratorArtifactDrifted,
		},
		{
			name:      "root reappears after guarded deletion",
			root:      darwinAdministratorArtifactExact,
			finalRoot: darwinAdministratorArtifactExact,
		},
		{
			name:       "settings reappear after guarded deletion",
			trust:      darwinAdministratorArtifactExact,
			finalTrust: darwinAdministratorArtifactExact,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &darwinTrustCoreFakeNative{owned: true, artifacts: &darwinTrustCoreFakeAdministratorArtifacts{
				root:               test.root,
				trust:              test.trust,
				trustBeforeRemoval: test.trust,
				finalRoot:          test.finalRoot,
				finalTrust:         test.finalTrust,
			}}
			backend := newDarwinTrustBackend(native)
			before, err := backend.observe(t.Context(), request)
			if err != nil {
				t.Fatalf("observe() error = %v", err)
			}
			if err := backend.release(t.Context(), request, before); !errors.Is(err, errNativeObservationChanged) {
				t.Fatalf("release() error = %v, want stale observation", err)
			}
			if !native.owned || native.releaseCalls != 1 {
				t.Fatalf("owned = %t, release calls = %d", native.owned, native.releaseCalls)
			}
		})
	}
}

// TestDarwinAdministratorMarkerCleanupRejectsDrift prevents a marker from authorizing certificate settings that no longer have Harbor's exact shape.
func TestDarwinAdministratorMarkerCleanupRejectsDrift(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	native := &darwinTrustCoreFakeNative{
		owned:     true,
		artifacts: &darwinTrustCoreFakeAdministratorArtifacts{trust: darwinAdministratorArtifactDrifted},
	}
	before, err := newDarwinTrustBackend(native).observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if err := newDarwinTrustBackend(native).release(t.Context(), request, before); !errors.Is(err, errNativeMutationConflict) {
		t.Fatalf("release() error = %v, want exact-artifact conflict", err)
	}
	if native.releaseCalls != 0 {
		t.Fatalf("release calls = %d, want 0", native.releaseCalls)
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

// TestDarwinAdministratorRootLabelBindsOneCanonicalAuthority proves certificate ownership is encoded in a fixed label.
func TestDarwinAdministratorRootLabelBindsOneCanonicalAuthority(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinAdministratorTrust)
	label := darwinAdministratorRootLabel(request)
	if !strings.HasSuffix(label, request.AuthorityFingerprint()) {
		t.Fatalf("root label = %q, want authority fingerprint suffix", label)
	}
	if err := validateDarwinAdministratorRootLabel(request); err != nil {
		t.Fatalf("validateDarwinAdministratorRootLabel() error = %v", err)
	}
}

// TestDarwinAdministratorRollbackOrderPreservesPreexistingArtifacts proves failed installs undo only their own effects.
func TestDarwinAdministratorRollbackOrderPreservesPreexistingArtifacts(t *testing.T) {
	for _, test := range []struct {
		name    string
		created darwinAdministratorRollbackArtifacts
		want    []darwinAdministratorRollbackArtifact
	}{
		{
			name: "preexisting root certificate",
		},
		{
			name:    "new trust marker",
			created: darwinAdministratorRollbackArtifacts{TrustMarker: true},
			want:    []darwinAdministratorRollbackArtifact{darwinAdministratorRollbackTrustMarker},
		},
		{
			name:    "all invocation artifacts",
			created: darwinAdministratorRollbackArtifacts{TrustMarker: true, RootCertificate: true},
			want:    []darwinAdministratorRollbackArtifact{darwinAdministratorRollbackRootCertificate, darwinAdministratorRollbackTrustMarker},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.created.rollbackOrder()
			if !slices.Equal(got, test.want) {
				t.Fatalf("rollbackOrder() = %v, want %v", got, test.want)
			}
		})
	}
}

// TestDarwinAdministratorRollbackFailurePreservesRemainingOwnership proves a failed artifact removal cannot erase the evidence needed for retry.
func TestDarwinAdministratorRollbackFailurePreservesRemainingOwnership(t *testing.T) {
	operationErr := errors.New("set administrator trust failed")
	cleanupErr := errors.New("certificate cleanup failed")
	var attempted []darwinAdministratorRollbackArtifact
	err := rollbackDarwinAdministratorArtifacts(
		darwinAdministratorRollbackArtifacts{
			TrustMarker:     true,
			RootCertificate: true,
		},
		func(artifact darwinAdministratorRollbackArtifact) error {
			attempted = append(attempted, artifact)
			return cleanupErr
		},
	)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("rollbackDarwinAdministratorArtifacts() error = %v, want %v", err, cleanupErr)
	}
	joined := joinDarwinAdministratorRollbackError(operationErr, err)
	if !errors.Is(joined, operationErr) || !errors.Is(joined, cleanupErr) {
		t.Fatalf("joinDarwinAdministratorRollbackError() = %v, want both operation and cleanup errors", joined)
	}
	want := []darwinAdministratorRollbackArtifact{darwinAdministratorRollbackRootCertificate}
	if !slices.Equal(attempted, want) {
		t.Fatalf("rollback attempts = %v, want %v", attempted, want)
	}
}

// TestDarwinAdministratorRootStoreStateClassifiesOwnership proves only an absent exact label may be claimed.
func TestDarwinAdministratorRootStoreStateClassifiesOwnership(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   darwinAdministratorRootStoreState
		wantAdd bool
	}{
		{
			name:    "absent label",
			state:   darwinAdministratorRootStoreAbsent,
			wantAdd: true,
		},
		{
			name:  "one owned exact-DER item",
			state: darwinAdministratorRootStoreOwned,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.canAddCertificate(); got != test.wantAdd {
				t.Fatalf("canAddCertificate() = %t, want %t", got, test.wantAdd)
			}
		})
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
