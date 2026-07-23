package ownershiphandler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
)

// TestReleaseNetworkOwnership proves the handler removes only the exact admitted record.
func TestReleaseNetworkOwnership(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &fakeStore{
		observations: []ownership.Observation{
			{},
		},
	}
	proof := &proofCompleter{complete: func(ctx context.Context, request ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, verifiedAt time.Time) (ownershipreleaseproof.Proof, error) {
		if request.TicketReferenceHash != testHash("reference") || request.NonceHash != testHash("nonce") ||
			request.ReleaseOperationID != "operation-release" || request.OperationRevision != 1 || request.CheckpointRevision != 2 ||
			request.RequesterIdentity != "1000" || request.TargetOwnershipFingerprint != fingerprint {
			t.Fatalf("proof request = %#v", request)
		}
		if !verifiedAt.Equal(testTime()) {
			t.Fatalf("verified at = %s, want %s", verifiedAt, testTime())
		}
		if err := transaction.CompareAndSwap(ctx); err != nil {
			return ownershipreleaseproof.Proof{}, err
		}
		present, err := transaction.ObserveOwnership(ctx)
		if err != nil {
			return ownershipreleaseproof.Proof{}, err
		}
		if present {
			t.Fatal("post-release ownership remains present")
		}
		return ownershipreleaseproof.Proof{}, nil
	}}
	handler := New(store, proof, fixedClock{now: testTime()})
	evidence, err := handler.ReleaseNetworkOwnership(
		t.Context(),
		ownershipTicket(fingerprint),
		ownershipAdmission(fingerprint, helper.OwnershipAdmissionAlreadyCurrent),
	)
	if err != nil {
		t.Fatalf("ReleaseNetworkOwnership() error = %v", err)
	}
	if store.releaseFingerprint != fingerprint || evidence.ReleasedOwnershipFingerprint != fingerprint || evidence.Postcondition != helper.OwnershipPostconditionOwnedAbsent {
		t.Fatalf("release evidence = %#v, fingerprint = %q", evidence, store.releaseFingerprint)
	}
}

// TestReleaseNetworkOwnershipAlreadyReleasedDoesNotMutate proves an admitted replay only re-observes absence.
func TestReleaseNetworkOwnershipAlreadyReleasedDoesNotMutate(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &fakeStore{
		observations: []ownership.Observation{{}},
	}
	proof := &proofCompleter{complete: func(ctx context.Context, _ ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, _ time.Time) (ownershipreleaseproof.Proof, error) {
		present, err := transaction.ObserveOwnership(ctx)
		if err != nil {
			return ownershipreleaseproof.Proof{}, err
		}
		if present {
			return ownershipreleaseproof.Proof{}, errors.New("ownership reappeared")
		}
		return ownershipreleaseproof.Proof{}, nil
	}}
	_, err := New(store, proof, fixedClock{now: testTime()}).ReleaseNetworkOwnership(
		t.Context(),
		ownershipTicket(fingerprint),
		ownershipAdmission(fingerprint, helper.OwnershipAdmissionAlreadyReleased),
	)
	if err != nil {
		t.Fatalf("ReleaseNetworkOwnership() error = %v", err)
	}
	if store.releaseFingerprint != "" {
		t.Fatalf("Release() called with %q", store.releaseFingerprint)
	}
}

// TestReleaseNetworkOwnershipAlreadyReleasedRejectsReappearedOwnership proves a replay cannot use a stale proof to hide new ownership.
func TestReleaseNetworkOwnershipAlreadyReleasedRejectsReappearedOwnership(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &fakeStore{observations: []ownership.Observation{{Exists: true, Fingerprint: fingerprint}}}
	proof := &proofCompleter{complete: func(ctx context.Context, _ ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, _ time.Time) (ownershipreleaseproof.Proof, error) {
		present, err := transaction.ObserveOwnership(ctx)
		if err != nil {
			return ownershipreleaseproof.Proof{}, err
		}
		if present {
			return ownershipreleaseproof.Proof{}, errors.New("released ownership proof found ownership present")
		}
		return ownershipreleaseproof.Proof{}, nil
	}}
	if _, err := New(store, proof, fixedClock{now: testTime()}).ReleaseNetworkOwnership(
		t.Context(),
		ownershipTicket(fingerprint),
		ownershipAdmission(fingerprint, helper.OwnershipAdmissionAlreadyReleased),
	); err == nil {
		t.Fatal("ReleaseNetworkOwnership() error = nil")
	}
	if store.releaseFingerprint != "" {
		t.Fatalf("Release() called with %q", store.releaseFingerprint)
	}
}

// TestReleaseNetworkOwnershipAlreadyReleasedNeverReleases proves an incomplete proof recovery cannot call the ownership mutation.
func TestReleaseNetworkOwnershipAlreadyReleasedNeverReleases(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &fakeStore{}
	proof := &proofCompleter{complete: func(ctx context.Context, _ ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, _ time.Time) (ownershipreleaseproof.Proof, error) {
		return ownershipreleaseproof.Proof{}, transaction.CompareAndSwap(ctx)
	}}
	if _, err := New(store, proof, fixedClock{now: testTime()}).ReleaseNetworkOwnership(
		t.Context(),
		ownershipTicket(fingerprint),
		ownershipAdmission(fingerprint, helper.OwnershipAdmissionAlreadyReleased),
	); err == nil {
		t.Fatal("ReleaseNetworkOwnership() error = nil")
	}
	if store.releaseFingerprint != "" {
		t.Fatalf("Release() called with %q", store.releaseFingerprint)
	}
}

// TestReleaseNetworkOwnershipRejectsAdmissionMismatchBeforeProofCompletion proves direct callers cannot substitute a target.
func TestReleaseNetworkOwnershipRejectsAdmissionMismatchBeforeProofCompletion(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &fakeStore{}
	proof := &proofCompleter{}
	admission := ownershipAdmission("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", helper.OwnershipAdmissionAlreadyCurrent)
	if _, err := New(store, proof, fixedClock{now: testTime()}).ReleaseNetworkOwnership(t.Context(), ownershipTicket(fingerprint), admission); err == nil {
		t.Fatal("ReleaseNetworkOwnership() error = nil")
	}
	if proof.calls != 0 || store.releaseFingerprint != "" {
		t.Fatalf("proof calls = %d, released fingerprint = %q", proof.calls, store.releaseFingerprint)
	}
}

// ownershipTicket returns one exact terminal release checkpoint for handler tests.
func ownershipTicket(fingerprint string) helper.Ticket {
	return helper.Ticket{
		RequesterIdentity:            "1000",
		ReleaseOperationID:           "operation-release",
		ReleaseOperationRevision:     1,
		ReleaseCheckpointRevision:    2,
		ExpectedOwnershipFingerprint: fingerprint,
		Nonce:                        "nonce",
	}
}

// ownershipAdmission returns one ticket admission bound to the selected protected record.
func ownershipAdmission(fingerprint string, state helper.OwnershipAdmissionState) helper.TicketAdmission {
	return helper.TicketAdmission{
		TicketReference:            "reference",
		OwnershipState:             state,
		TargetOwnershipFingerprint: fingerprint,
	}
}

// proofCompleter controls the root-owned proof boundary for handler tests.
type proofCompleter struct {
	calls    int
	complete func(context.Context, ownershipreleaseproof.Request, ownershipreleaseproof.Transaction, time.Time) (ownershipreleaseproof.Proof, error)
}

// Complete records the proof attempt and executes the configured root-boundary behavior.
func (proof *proofCompleter) Complete(ctx context.Context, request ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, verifiedAt time.Time) (ownershipreleaseproof.Proof, error) {
	proof.calls++
	if proof.complete == nil {
		return ownershipreleaseproof.Proof{}, errors.New("unexpected proof completion")
	}
	return proof.complete(ctx, request, transaction, verifiedAt)
}

// fixedClock returns a deterministic proof verification time.
type fixedClock struct {
	now time.Time
}

// Now returns the deterministic proof verification time.
func (clock fixedClock) Now() time.Time {
	return clock.now
}

// testHash returns the canonical SHA-256 digest used by proof requests.
func testHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// testTime returns one deterministic UTC proof verification time.
func testTime() time.Time {
	return time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
}

// fakeStore records the exact ownership release selected by handler tests.
type fakeStore struct {
	observations       []ownership.Observation
	observeErr         error
	releaseFingerprint string
}

// Observe returns the next configured protected ownership observation.
func (store *fakeStore) Observe(context.Context) (ownership.Observation, error) {
	if store.observeErr != nil {
		return ownership.Observation{}, store.observeErr
	}
	if len(store.observations) == 0 {
		return ownership.Observation{}, nil
	}
	observation := store.observations[0]
	store.observations = store.observations[1:]
	return observation, nil
}

// Release records the exact protected ownership fingerprint selected for removal.
func (store *fakeStore) Release(_ context.Context, fingerprint string) error {
	store.releaseFingerprint = fingerprint
	return nil
}

// Close satisfies the handler's retained protected-store lifecycle.
func (store *fakeStore) Close() error {
	return nil
}
