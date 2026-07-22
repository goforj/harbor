package main

import (
	"errors"
	"testing"
)

// TestTransitionTrustIdentityBindsCanonicalRequesterAndDropsOnce verifies the portable transition contract without changing test identities.
func TestTransitionTrustIdentityBindsCanonicalRequesterAndDropsOnce(t *testing.T) {
	transitions := 0
	err := transitionTrustIdentity("501", func(uid uint32) (trustIdentityState, trustIdentityState, error) {
		transitions++
		if uid != 501 {
			t.Fatalf("transition UID = %d, want 501", uid)
		}
		return trustIdentityState{
				realUID:      uid,
				effectiveUID: 0,
			}, trustIdentityState{
				realUID:      uid,
				effectiveUID: uid,
			}, nil
	})
	if err != nil {
		t.Fatalf("transitionTrustIdentity() error = %v", err)
	}
	if transitions != 1 {
		t.Fatalf("transitions = %d, want 1", transitions)
	}
}

// TestTransitionTrustIdentityRejectsUnsafeIdentityStates proves invalid targets and unsafe platform proofs fail closed.
func TestTransitionTrustIdentityRejectsUnsafeIdentityStates(t *testing.T) {
	for _, test := range []struct {
		name            string
		requester       string
		before          trustIdentityState
		wantTransitions int
	}{
		{
			name:      "missing requester",
			requester: "",
		},
		{
			name:      "nonnumeric requester",
			requester: "user-501",
		},
		{
			name:      "noncanonical requester",
			requester: "0501",
			before: trustIdentityState{
				realUID:      501,
				effectiveUID: 0,
			},
		},
		{
			name:      "requester exceeds Unix UID range",
			requester: "4294967296",
		},
		{
			name:      "root requester",
			requester: "0",
			before: trustIdentityState{
				realUID:      0,
				effectiveUID: 0,
			},
		},
		{
			name:            "nonroot effective identity",
			requester:       "501",
			wantTransitions: 1,
			before: trustIdentityState{
				realUID:      501,
				effectiveUID: 501,
			},
		},
		{
			name:            "different invoking user",
			requester:       "501",
			wantTransitions: 1,
			before: trustIdentityState{
				realUID:      502,
				effectiveUID: 0,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transitions := 0
			err := transitionTrustIdentity(test.requester, func(uid uint32) (trustIdentityState, trustIdentityState, error) {
				transitions++
				return test.before, trustIdentityState{
					realUID:      uid,
					effectiveUID: uid,
				}, nil
			})
			if err == nil || transitions != test.wantTransitions {
				t.Fatalf("transitionTrustIdentity() error=%v transitions=%d, want %d", err, transitions, test.wantTransitions)
			}
		})
	}
}

// TestTransitionTrustIdentityRequiresAtomicTransition keeps the native one-way boundary mandatory.
func TestTransitionTrustIdentityRequiresAtomicTransition(t *testing.T) {
	if err := transitionTrustIdentity("501", nil); err == nil {
		t.Fatal("transitionTrustIdentity() error = nil")
	}
}

// TestTransitionTrustIdentityRejectsDropAndPostconditionFailures proves a failed or incomplete transition remains fail-closed.
func TestTransitionTrustIdentityRejectsDropAndPostconditionFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		transition trustIdentityTransition
	}{
		{
			name: "transition failure",
			transition: func(uint32) (trustIdentityState, trustIdentityState, error) {
				return trustIdentityState{}, trustIdentityState{}, errors.New("drop failed")
			},
		},
		{
			name: "does not drop",
			transition: func(uid uint32) (trustIdentityState, trustIdentityState, error) {
				identity := trustIdentityState{
					realUID:      uid,
					effectiveUID: 0,
				}
				return identity, identity, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := transitionTrustIdentity("501", test.transition)
			if err == nil {
				t.Fatal("transitionTrustIdentity() error = nil")
			}
		})
	}
}
