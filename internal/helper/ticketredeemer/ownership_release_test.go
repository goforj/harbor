package ticketredeemer

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
)

// TestAdmitTicketRequiresExactCurrentOwnershipForRelease proves present ownership admits only the signed release target.
func TestAdmitTicketRequiresExactCurrentOwnershipForRelease(t *testing.T) {
	now := testRedeemerTime()
	publicKey, _ := testRedeemerKey('r')
	ticket := testRedeemerOwnershipReleaseTicket(now, "501")
	record := ownershipRecordFromTicket(ticket, encodeVerifierKey(publicKey))
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	ticket.ExpectedOwnershipFingerprint = fingerprint
	observation := testOwnershipObservation(t, record)
	admission, err := admitTicket(helper.TicketReference(strings.Repeat("a", 64)), ticket, observation, ticket.RequesterIdentity)
	if err != nil {
		t.Fatalf("admitTicket() error = %v", err)
	}
	if admission.OwnershipState != helper.OwnershipAdmissionAlreadyCurrent || admission.TargetOwnershipFingerprint != fingerprint {
		t.Fatalf("admission = %#v", admission)
	}

	ticket.ExpectedOwnershipFingerprint = strings.Repeat("b", 64)
	if _, err := admitTicket(helper.TicketReference(strings.Repeat("a", 64)), ticket, observation, ticket.RequesterIdentity); err == nil {
		t.Fatal("admitTicket() accepted a release target different from current ownership")
	}
}

// TestAdmitAbsentOwnershipAuthenticatesOnlyTheSignedRequesterBoundRelease keeps proof observation separate from bootstrap authentication.
func TestAdmitAbsentOwnershipAuthenticatesOnlyTheSignedRequesterBoundRelease(t *testing.T) {
	now := testRedeemerTime()
	_, privateKey := testRedeemerKey('a')
	ticket := testRedeemerOwnershipReleaseTicket(now, "501")
	envelope, err := ticketauth.Sign(ticket, privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	redeemer := &Redeemer{topology: &topology{requesterIdentity: ticket.RequesterIdentity}}
	admitted, observation, err := redeemer.admitAbsentOwnership(context.Background(), envelope, now)
	if err != nil {
		t.Fatalf("admitAbsentOwnership() error = %v", err)
	}
	if observation.Exists || admitted.Operation != helper.OperationReleaseNetworkOwnership || admitted.RequesterIdentity != ticket.RequesterIdentity {
		t.Fatalf("admitted ticket = %#v, observation = %#v", admitted, observation)
	}

	redeemer.topology.requesterIdentity = "502"
	if _, _, err := redeemer.admitAbsentOwnership(context.Background(), envelope, now); err == nil {
		t.Fatal("admitAbsentOwnership() accepted a release for another pending owner")
	}
}

// TestAdmitAlreadyReleasedRequiresMatchingProof rejects absent ownership unless root-authored proof admits the exact signed authority.
func TestAdmitAlreadyReleasedRequiresMatchingProof(t *testing.T) {
	now := testRedeemerTime()
	publicKey, privateKey := testRedeemerKey('p')
	ticket := testRedeemerOwnershipReleaseTicket(now, "501")
	target := ownershipRecordFromTicket(ticket, encodeVerifierKey(publicKey))
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	ticket.ExpectedOwnershipFingerprint = targetFingerprint
	envelope, err := ticketauth.Sign(ticket, privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	wantAuthority := ownershipReleaseProofAuthority(ticket, targetFingerprint)

	cases := []struct {
		name     string
		observer testOwnershipReleaseProofObserver
		wantErr  error
	}{
		{
			name:     "missing proof",
			observer: testOwnershipReleaseProofObserver{err: ownershipreleaseproof.ErrAbsentProof},
			wantErr:  ownershipreleaseproof.ErrAbsentProof,
		},
		{
			name: "mismatched proof",
			observer: testOwnershipReleaseProofObserver{
				expected: ownershipreleaseproof.Authority{RequesterIdentity: "other"},
			},
			wantErr: ownershipreleaseproof.ErrAbsentProof,
		},
		{
			name:     "exact replay",
			observer: testOwnershipReleaseProofObserver{expected: wantAuthority},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observer := test.observer
			redeemer := &Redeemer{
				topology: &topology{requesterIdentity: ticket.RequesterIdentity},
				dependencies: dependencies{
					openReleaseProof: func() (ownershipReleaseProofObserver, error) { return &observer, nil },
				},
			}
			redemption, err := redeemer.admitAlreadyReleased(context.Background(), helper.TicketReference(strings.Repeat("a", 64)), ticket, envelope, now)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || !errors.Is(err, ErrReferenceConsumed) {
					t.Fatalf("admitAlreadyReleased() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("admitAlreadyReleased() error = %v", err)
			}
			if observer.received != wantAuthority {
				t.Fatalf("AdmitReplay() authority = %#v, want %#v", observer.received, wantAuthority)
			}
			if redemption.Admission.OwnershipState != helper.OwnershipAdmissionAlreadyReleased || redemption.Admission.TargetOwnershipFingerprint != targetFingerprint {
				t.Fatalf("admitAlreadyReleased() redemption = %#v", redemption)
			}
		})
	}
}

// testOwnershipReleaseProofObserver captures replay authority and models proof absence or mismatch without storage I/O.
type testOwnershipReleaseProofObserver struct {
	expected ownershipreleaseproof.Authority
	received ownershipreleaseproof.Authority
	err      error
}

// AdmitReplay admits only the configured exact authority, mirroring the default observer's authority boundary.
func (observer *testOwnershipReleaseProofObserver) AdmitReplay(_ context.Context, authority ownershipreleaseproof.Authority) (ownershipreleaseproof.Proof, error) {
	observer.received = authority
	if observer.err != nil {
		return ownershipreleaseproof.Proof{}, observer.err
	}
	if observer.expected != authority {
		return ownershipreleaseproof.Proof{}, ownershipreleaseproof.ErrAbsentProof
	}
	return ownershipreleaseproof.Proof{}, nil
}

// testRedeemerOwnershipReleaseTicket builds a schema-two release ticket without mutation-specific ticket authority.
func testRedeemerOwnershipReleaseTicket(nowTime time.Time, requester string) helper.Ticket {
	ticket := testRedeemerResolverTicket(nowTime, requester, helper.OperationReleaseNetworkOwnership)
	ticket.NetworkPolicy = nil
	ticket.ExpectedResolverObservation = nil
	ticket.ReleaseOperationID = "release-network-ownership"
	ticket.ReleaseOperationRevision = 1
	ticket.ReleaseCheckpointRevision = 2
	ticket.ExpectedOwnershipFingerprint = strings.Repeat("a", 64)
	return ticket
}

// encodeVerifierKey returns the canonical protected-state representation required by ownership records.
func encodeVerifierKey(key []byte) string {
	return base64.StdEncoding.EncodeToString(key)
}
