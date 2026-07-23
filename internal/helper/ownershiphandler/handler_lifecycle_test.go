package ownershiphandler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
)

// TestReleaseNetworkOwnershipFailsClosedAcrossReleaseAndPostconditionFailures proves every uncertain mutation outcome is rejected.
func TestReleaseNetworkOwnershipFailsClosedAcrossReleaseAndPostconditionFailures(t *testing.T) {
	fingerprint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name  string
		store *lifecycleStore
	}{
		{
			name: "release failure",
			store: &lifecycleStore{
				observations: []ownership.Observation{{
					Exists:      true,
					Fingerprint: fingerprint,
				}},
				releaseErr: errors.New("release failed"),
			},
		},
		{
			name: "postcondition observe failure",
			store: &lifecycleStore{
				observeErrAt: 1,
			},
		},
		{
			name: "postcondition remains present",
			store: &lifecycleStore{
				observations: []ownership.Observation{
					{
						Exists:      true,
						Fingerprint: fingerprint,
					},
					{
						Exists:      true,
						Fingerprint: fingerprint,
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := &proofCompleter{complete: func(ctx context.Context, _ ownershipreleaseproof.Request, transaction ownershipreleaseproof.Transaction, _ time.Time) (ownershipreleaseproof.Proof, error) {
				if err := transaction.CompareAndSwap(ctx); err != nil {
					return ownershipreleaseproof.Proof{}, err
				}
				present, err := transaction.ObserveOwnership(ctx)
				if err != nil {
					return ownershipreleaseproof.Proof{}, err
				}
				if present {
					return ownershipreleaseproof.Proof{}, errors.New("ownership remains present")
				}
				return ownershipreleaseproof.Proof{}, nil
			}}
			if _, err := New(test.store, proof, fixedClock{now: testTime()}).ReleaseNetworkOwnership(
				t.Context(),
				ownershipTicket(fingerprint),
				ownershipAdmission(fingerprint, helper.OwnershipAdmissionAlreadyCurrent),
			); err == nil {
				t.Fatal("ReleaseNetworkOwnership() error = nil")
			}
			if test.store.releaseFingerprint != fingerprint {
				t.Fatalf("Release() fingerprint = %q, want %q", test.store.releaseFingerprint, fingerprint)
			}
		})
	}
}

// TestHandlerCloseDelegatesOncePerCall proves closing a handler preserves store lifecycle errors for composition.
func TestHandlerCloseDelegatesOncePerCall(t *testing.T) {
	closeErr := errors.New("close failed")
	store := &lifecycleStore{closeErr: closeErr}
	handler := New(store, &proofCompleter{complete: func(context.Context, ownershipreleaseproof.Request, ownershipreleaseproof.Transaction, time.Time) (ownershipreleaseproof.Proof, error) {
		return ownershipreleaseproof.Proof{}, nil
	}}, fixedClock{now: testTime()})
	if err := handler.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want %v", err, closeErr)
	}
	if store.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", store.closeCalls)
	}
}

// lifecycleStore supplies controlled release and retained-store lifecycle outcomes.
type lifecycleStore struct {
	observations       []ownership.Observation
	observeCalls       int
	observeErrAt       int
	releaseErr         error
	releaseFingerprint string
	closeErr           error
	closeCalls         int
}

// Observe returns configured observations while allowing one chosen observation failure.
func (store *lifecycleStore) Observe(context.Context) (ownership.Observation, error) {
	store.observeCalls++
	if store.observeCalls == store.observeErrAt {
		return ownership.Observation{}, errors.New("observe failed")
	}
	if len(store.observations) == 0 {
		return ownership.Observation{}, nil
	}
	observation := store.observations[0]
	store.observations = store.observations[1:]
	return observation, nil
}

// Release records the target and returns the configured mutation outcome.
func (store *lifecycleStore) Release(_ context.Context, fingerprint string) error {
	store.releaseFingerprint = fingerprint
	return store.releaseErr
}

// Close records retained-store teardown and preserves its configured error.
func (store *lifecycleStore) Close() error {
	store.closeCalls++
	return store.closeErr
}
