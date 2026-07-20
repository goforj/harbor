//go:build darwin || linux

package ticketredeemer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// TestRedeemerConsumesAndAuthenticatesOneReference proves the complete filesystem and ownership admission path.
func TestRedeemerConsumesAndAuthenticatesOneReference(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'a', fixture.privateKey)
	redeemer := fixture.open(t)

	redemption, err := redeemer.Redeem(nil, reference)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if redemption.Ticket.Nonce != strings.Repeat("n", 32) {
		t.Fatalf("Redeem() ticket = %#v", redemption.Ticket)
	}
	ownedRecord := fixture.record()
	ownedFingerprint, err := ownedRecord.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint ownership fixture: %v", err)
	}
	wantAdmission := helper.TicketAdmission{
		TicketReference:            reference,
		RequesterIdentity:          fixture.owner,
		InstallationID:             "harbor-redeemer-test",
		OwnershipGeneration:        7,
		OwnershipSchemaVersion:     ownership.IdentitySchemaVersion,
		NetworkPolicyFingerprint:   "",
		ApprovedPool:               "127.77.0.0/24",
		OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
		OwnershipFingerprint:       ownedFingerprint,
		TargetOwnershipFingerprint: ownedFingerprint,
		TicketVerifierKey:          ownedRecord.TicketVerifierKey,
	}
	if redemption.Admission != wantAdmission {
		t.Fatalf("Redeem() admission = %#v, want %#v", redemption.Admission, wantAdmission)
	}
	if _, err := os.Lstat(filepath.Join(fixture.paths.PendingDirectory, string(reference))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("pending reference remains: %v", err)
	}
	claimedPath := filepath.Join(fixture.paths.ClaimsDirectory, string(reference))
	claimed, err := os.Open(claimedPath)
	if err != nil {
		t.Fatalf("open claimed reference: %v", err)
	}
	if err := validatePlatformMachineFile(claimed); err != nil {
		t.Errorf("claimed reference policy: %v", err)
	}
	if err := claimed.Close(); err != nil {
		t.Errorf("close claimed reference: %v", err)
	}
	if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketReferenceRedeemed) {
		t.Fatalf("Redeem() replay error = %v, want redeemed", err)
	}
}

// TestRedeemerConsumesNetworkPolicyOwnership proves schema-two tickets remain bound to the protected policy fingerprint.
func TestRedeemerConsumesNetworkPolicyOwnership(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, false)
	record := fixture.record()
	record.SchemaVersion = ownership.NetworkPolicySchemaVersion
	record.NetworkPolicyFingerprint = strings.Repeat("c", 64)
	fixture.claimOwnership(t, record)
	ticket := testRedeemerTicket(fixture.now, fixture.owner)
	ticket.OwnershipSchemaVersion = record.SchemaVersion
	ticket.NetworkPolicyFingerprint = record.NetworkPolicyFingerprint
	reference := fixture.writeTicket(t, ticket, '9', fixture.privateKey)

	redemption, err := fixture.open(t).Redeem(context.Background(), reference)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if redemption.Admission.OwnershipSchemaVersion != record.SchemaVersion || redemption.Admission.NetworkPolicyFingerprint != record.NetworkPolicyFingerprint {
		t.Fatalf("Redeem() admission = %#v, want schema-two policy binding", redemption.Admission)
	}
	if redemption.Admission.OwnershipState != helper.OwnershipAdmissionAlreadyCurrent {
		t.Fatalf("Redeem() ownership state = %q, want already current", redemption.Admission.OwnershipState)
	}
}

// TestRedeemerAdmitsResolverOwnershipTransitionWithoutMutation proves redemption only describes the exact schema upgrade.
func TestRedeemerAdmitsResolverOwnershipTransitionWithoutMutation(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	ticket := testRedeemerResolverTicket(fixture.now, fixture.owner, helper.OperationEnsureResolver)
	reference := fixture.writeTicket(t, ticket, '6', fixture.privateKey)
	redeemer := fixture.open(t)

	redemption, err := redeemer.Redeem(t.Context(), reference)
	if err != nil {
		t.Fatalf("Redeem(transition) error = %v", err)
	}
	source := fixture.record()
	sourceFingerprint, err := source.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if redemption.Admission.OwnershipState != helper.OwnershipAdmissionSchema1To2 ||
		redemption.Admission.OwnershipFingerprint != sourceFingerprint ||
		redemption.Admission.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion ||
		redemption.Admission.NetworkPolicyFingerprint != ticket.NetworkPolicyFingerprint {
		t.Fatalf("Redeem(transition) admission = %#v", redemption.Admission)
	}
	observed, err := redeemer.ownership.Observe(t.Context())
	if err != nil {
		t.Fatalf("Observe() after redemption error = %v", err)
	}
	if observed.Record != source || observed.Fingerprint != sourceFingerprint {
		t.Fatalf("redemption mutated ownership = %#v, want schema-1 %#v", observed, source)
	}

	target := source
	target.SchemaVersion = ownership.NetworkPolicySchemaVersion
	target.NetworkPolicyFingerprint = ticket.NetworkPolicyFingerprint
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if redemption.Admission.TargetOwnershipFingerprint != targetFingerprint {
		t.Fatalf("Redeem(transition) target fingerprint = %q, want %q", redemption.Admission.TargetOwnershipFingerprint, targetFingerprint)
	}
	store, err := ownership.NewStore(fixture.paths.OwnershipPath)
	if err != nil {
		t.Fatalf("open transition ownership store: %v", err)
	}
	upgraded, upgradeErr := store.Upgrade(t.Context(), sourceFingerprint, target)
	closeErr := store.Close()
	if upgradeErr != nil || closeErr != nil {
		t.Fatalf("Upgrade()/Close() = %#v, %v / %v", upgraded, upgradeErr, closeErr)
	}

	fresh := ticket
	fresh.Nonce = strings.Repeat("s", 32)
	freshReference := fixture.writeTicket(t, fresh, '7', fixture.privateKey)
	freshRedemption, err := redeemer.Redeem(t.Context(), freshReference)
	if err != nil {
		t.Fatalf("Redeem(current retry) error = %v", err)
	}
	if freshRedemption.Admission.OwnershipState != helper.OwnershipAdmissionAlreadyCurrent ||
		freshRedemption.Admission.OwnershipFingerprint != upgraded.Fingerprint {
		t.Fatalf("Redeem(current retry) admission = %#v, want target %#v", freshRedemption.Admission, upgraded)
	}
}

// TestRedeemerRejectsResolverTransitionWithoutExactSchema1Source covers forged and mismatched target derivations.
func TestRedeemerRejectsResolverTransitionWithoutExactSchema1Source(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*helper.Ticket, unixRedeemerFixture)
		signingKey func(unixRedeemerFixture) ed25519.PrivateKey
		want       error
	}{
		{name: "installation", mutate: func(ticket *helper.Ticket, _ unixRedeemerFixture) {
			ticket.InstallationID = "harbor-other-installation"
		}, want: helper.ErrTicketRedemptionFailed},
		{name: "requester", mutate: func(ticket *helper.Ticket, fixture unixRedeemerFixture) {
			ticket.RequesterIdentity = fixture.owner + "9"
		}, want: helper.ErrTicketRedemptionFailed},
		{name: "generation", mutate: func(ticket *helper.Ticket, _ unixRedeemerFixture) { ticket.OwnershipGeneration++ }, want: helper.ErrTicketReferenceStale},
		{name: "pool", mutate: func(ticket *helper.Ticket, _ unixRedeemerFixture) { ticket.ApprovedPool = "127.78.0.0/24" }, want: helper.ErrTicketRedemptionFailed},
		{name: "verifier", signingKey: func(unixRedeemerFixture) ed25519.PrivateKey {
			_, privateKey := testRedeemerKey('x')
			return privateKey
		}, want: helper.ErrTicketRedemptionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			ticket := testRedeemerResolverTicket(fixture.now, fixture.owner, helper.OperationEnsureResolver)
			if test.mutate != nil {
				test.mutate(&ticket, fixture)
			}
			privateKey := fixture.privateKey
			if test.signingKey != nil {
				privateKey = test.signingKey(fixture)
			}
			reference := fixture.writeTicket(t, ticket, '8', privateKey)
			if _, err := fixture.open(t).Redeem(t.Context(), reference); !errors.Is(err, test.want) || !errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("Redeem() error = %v, want consumed rejection", err)
			}
		})
	}
}

// TestRedeemerRejectsResolverReleaseFromSchema1Ownership prevents release from implicitly changing ownership schema.
func TestRedeemerRejectsResolverReleaseFromSchema1Ownership(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	ticket := testRedeemerResolverTicket(fixture.now, fixture.owner, helper.OperationReleaseResolver)
	reference := fixture.writeTicket(t, ticket, '9', fixture.privateKey)
	if _, err := fixture.open(t).Redeem(t.Context(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
		t.Fatalf("Redeem(release) error = %v, want consumed schema-1 rejection", err)
	}
}

// TestRedeemerBootstrapsExactPoolOwnership proves the first authenticated pool ticket pins authority before dispatch.
func TestRedeemerBootstrapsExactPoolOwnership(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, false)
	ticket := testRedeemerPoolTicket(fixture.now, fixture.owner)
	reference := fixture.writeTicket(t, ticket, '2', fixture.privateKey)
	redeemer := fixture.open(t)

	redemption, err := redeemer.Redeem(t.Context(), reference)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if redemption.Ticket.Operation != helper.OperationEnsureLoopbackPool || redemption.Ticket.ApprovedPool != "127.77.0.8/29" {
		t.Fatalf("Redeem() ticket = %#v", redemption.Ticket)
	}
	observation, err := redeemer.ownership.Observe(t.Context())
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	wantRecord := ownership.Record{
		SchemaVersion:      ownership.IdentitySchemaVersion,
		InstallationID:     ticket.InstallationID,
		OwnerIdentity:      fixture.owner,
		Generation:         1,
		LoopbackPoolPrefix: ticket.ApprovedPool,
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(fixture.publicKey),
	}
	if !observation.Exists || observation.Record != wantRecord {
		t.Fatalf("bootstrapped ownership = %#v, want %#v", observation, wantRecord)
	}
	if err := validateOwnershipObservation(observation); err != nil {
		t.Fatalf("validateOwnershipObservation() error = %v", err)
	}
	wantAdmission := helper.TicketAdmission{
		TicketReference:            reference,
		RequesterIdentity:          fixture.owner,
		InstallationID:             ticket.InstallationID,
		OwnershipGeneration:        1,
		OwnershipSchemaVersion:     ownership.IdentitySchemaVersion,
		NetworkPolicyFingerprint:   "",
		ApprovedPool:               ticket.ApprovedPool,
		OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
		OwnershipFingerprint:       observation.Fingerprint,
		TargetOwnershipFingerprint: observation.Fingerprint,
		TicketVerifierKey:          wantRecord.TicketVerifierKey,
	}
	if redemption.Admission != wantAdmission {
		t.Fatalf("Redeem() admission = %#v, want %#v", redemption.Admission, wantAdmission)
	}
}

// TestRedeemerRejectsUnsafeOwnershipBootstrapShapes proves first claim cannot adopt existing identities or another generation.
func TestRedeemerRejectsUnsafeOwnershipBootstrapShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*helper.Ticket, unixRedeemerFixture)
	}{
		{name: "existing identity", mutate: func(ticket *helper.Ticket, _ unixRedeemerFixture) {
			ticket.ExpectedLoopbackPool.Identities[3].ExpectedObservation.State = helper.ObservationOwned
			ticket.ExpectedLoopbackPool.Identities[3].ExpectedPreAssignment = nil
		}},
		{name: "later generation", mutate: func(ticket *helper.Ticket, _ unixRedeemerFixture) {
			ticket.OwnershipGeneration = 2
		}},
		{name: "different requester", mutate: func(ticket *helper.Ticket, fixture unixRedeemerFixture) {
			ticket.RequesterIdentity = fixture.owner + "9"
		}},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, false)
			ticket := testRedeemerPoolTicket(fixture.now, fixture.owner)
			test.mutate(&ticket, fixture)
			reference := fixture.writeTicket(t, ticket, byte("345"[index]), fixture.privateKey)
			redeemer := fixture.open(t)
			if _, err := redeemer.Redeem(t.Context(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("Redeem() error = %v, want consumed bootstrap rejection", err)
			}
			observation, err := redeemer.ownership.Observe(t.Context())
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observation.Exists {
				t.Fatalf("rejected bootstrap published ownership %#v", observation)
			}
		})
	}
}

// TestRedeemerClassifiesInvalidUnknownExpiredAndMalformedReferences verifies stable outcomes without reusing claims.
func TestRedeemerClassifiesInvalidUnknownExpiredAndMalformedReferences(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), "../ticket"); !errors.Is(err, helper.ErrTicketRedemptionFailed) {
			t.Fatalf("Redeem(invalid) error = %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), testReference('b')); !errors.Is(err, helper.ErrTicketReferenceUnknown) {
			t.Fatalf("Redeem(unknown) error = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		ticket := testRedeemerTicket(fixture.now, fixture.owner)
		ticket.ExpiresAt = fixture.now.Add(-time.Second)
		reference := fixture.writeTicketAt(t, ticket, 'c', fixture.privateKey, ticket.ExpiresAt.Add(-time.Minute))
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketReferenceStale) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(expired) error = %v, want stale consumed", err)
		}
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketReferenceRedeemed) {
			t.Fatalf("Redeem(expired replay) error = %v, want redeemed", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := testReference('d')
		fixture.writeRaw(t, reference, []byte("{}\n"))
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(malformed) error = %v, want failed consumed", err)
		}
	})
	for _, test := range []struct {
		name    string
		marker  byte
		content []byte
	}{
		{name: "empty", marker: 'e', content: nil},
		{name: "oversized", marker: 'f', content: make([]byte, ticketauth.MaxEnvelopeBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			reference := testReference(test.marker)
			fixture.writeRaw(t, reference, test.content)
			redeemer := fixture.open(t)
			if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("Redeem(%s) error = %v, want preclaim failed", test.name, err)
			}
			if _, err := os.Stat(filepath.Join(fixture.paths.PendingDirectory, string(reference))); err != nil {
				t.Fatalf("preclaim size rejection removed reference: %v", err)
			}
		})
	}
}

// TestRedeemerBindsEveryOwnershipDimension prevents a valid signature from authorizing different protected state.
func TestRedeemerBindsEveryOwnershipDimension(t *testing.T) {
	cases := []struct {
		name         string
		mutateRecord func(*ownership.Record)
		mutateTicket func(*helper.Ticket)
		stale        bool
	}{
		{name: "requester", mutateTicket: func(ticket *helper.Ticket) { ticket.RequesterIdentity = "999" }},
		{name: "installation", mutateTicket: func(ticket *helper.Ticket) { ticket.InstallationID = "different-installation" }},
		{name: "pool", mutateTicket: func(ticket *helper.Ticket) { ticket.ApprovedPool = "127.77.0.0/25" }},
		{name: "generation", mutateTicket: func(ticket *helper.Ticket) { ticket.OwnershipGeneration++ }, stale: true},
		{name: "ownership schema", mutateTicket: func(ticket *helper.Ticket) {
			ticket.OwnershipSchemaVersion = ownership.NetworkPolicySchemaVersion
			ticket.NetworkPolicyFingerprint = strings.Repeat("a", 64)
		}},
		{
			name: "network policy fingerprint",
			mutateRecord: func(record *ownership.Record) {
				record.SchemaVersion = ownership.NetworkPolicySchemaVersion
				record.NetworkPolicyFingerprint = strings.Repeat("a", 64)
			},
			mutateTicket: func(ticket *helper.Ticket) {
				ticket.OwnershipSchemaVersion = ownership.NetworkPolicySchemaVersion
				ticket.NetworkPolicyFingerprint = strings.Repeat("b", 64)
			},
		},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, false)
			record := fixture.record()
			if test.mutateRecord != nil {
				test.mutateRecord(&record)
			}
			fixture.claimOwnership(t, record)
			ticket := testRedeemerTicket(fixture.now, fixture.owner)
			test.mutateTicket(&ticket)
			reference := fixture.writeTicket(t, ticket, byte('0'+index), fixture.privateKey)
			redeemer := fixture.open(t)
			_, err := redeemer.Redeem(context.Background(), reference)
			if test.stale {
				if !errors.Is(err, helper.ErrTicketReferenceStale) {
					t.Fatalf("Redeem() error = %v, want stale", err)
				}
			} else if !errors.Is(err, helper.ErrTicketRedemptionFailed) || errors.Is(err, helper.ErrTicketReferenceStale) {
				t.Fatalf("Redeem() error = %v, want failed", err)
			}
			if !errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("Redeem() error = %v, want consumed", err)
			}
		})
	}
}

// TestRedeemerRejectsWrongSignaturesAndMissingOwnership consumes unauthenticated capabilities before returning.
func TestRedeemerRejectsWrongSignaturesAndMissingOwnership(t *testing.T) {
	t.Run("wrong signer", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		_, wrongKey := testRedeemerKey('x')
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'e', wrongKey)
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(wrong signer) error = %v", err)
		}
	})
	t.Run("missing ownership", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, false)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'f', fixture.privateKey)
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(missing ownership) error = %v", err)
		}
	})
}

// TestRedeemerConcurrentCallsHaveOneWinner proves no-replace claims are the cross-goroutine serialization point.
func TestRedeemerConcurrentCallsHaveOneWinner(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), '1', fixture.privateKey)
	redeemer := fixture.open(t)

	const callers = 24
	results := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	for range callers {
		go func() {
			start.Wait()
			_, err := redeemer.Redeem(context.Background(), reference)
			results <- err
		}()
	}
	start.Done()
	successes := 0
	redeemed := 0
	for range callers {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, helper.ErrTicketReferenceRedeemed):
			redeemed++
		default:
			t.Fatalf("concurrent Redeem() error = %v", err)
		}
	}
	if successes != 1 || redeemed != callers-1 {
		t.Fatalf("concurrent outcomes = %d successes, %d redeemed", successes, redeemed)
	}
}

// TestRedeemerRejectsTopologyAndPendingObjectDrift catches path swaps, widened modes, and hard links before claim.
func TestRedeemerRejectsTopologyAndPendingObjectDrift(t *testing.T) {
	t.Run("pending swap", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		redeemer := fixture.open(t)
		moved := fixture.paths.PendingDirectory + "-moved"
		if err := os.Rename(fixture.paths.PendingDirectory, moved); err != nil {
			t.Fatalf("move pending directory: %v", err)
		}
		if err := os.Mkdir(fixture.paths.PendingDirectory, unixPrivateDir); err != nil {
			t.Fatalf("replace pending directory: %v", err)
		}
		if _, err := redeemer.Redeem(context.Background(), testReference('2')); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Redeem(swapped pending) error = %v, want unsafe", err)
		}
	})

	for _, target := range []string{"root", "tickets", "pending", "claims", "state"} {
		t.Run(target+" mode", func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			redeemer := fixture.open(t)
			paths := map[string]string{
				"root": fixture.paths.Root, "tickets": fixture.paths.TicketsDirectory,
				"pending": fixture.paths.PendingDirectory, "claims": fixture.paths.ClaimsDirectory,
				"state": fixture.paths.StateDirectory,
			}
			path := paths[target]
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", target, err)
			}
			if err := os.Chmod(path, info.Mode().Perm()|0o020); err != nil {
				t.Fatalf("broaden %s: %v", target, err)
			}
			defer os.Chmod(path, info.Mode().Perm())
			if _, err := redeemer.Redeem(context.Background(), testReference('3')); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Redeem(%s drift) error = %v, want unsafe", target, err)
			}
		})
	}

	t.Run("hard linked pending file", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), '4', fixture.privateKey)
		path := filepath.Join(fixture.paths.PendingDirectory, string(reference))
		if err := os.Link(path, filepath.Join(fixture.paths.PendingDirectory, "retained-link")); err != nil {
			t.Fatalf("hard link pending ticket: %v", err)
		}
		redeemer := fixture.open(t)
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(hard link) error = %v, want preclaim unsafe", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preclaim rejection removed pending file: %v", err)
		}
	})
}

// TestRedeemerFaultsPreserveTheSingleUseBoundary classifies preclaim retryability separately from postclaim failures.
func TestRedeemerFaultsPreserveTheSingleUseBoundary(t *testing.T) {
	cause := errors.New("injected storage failure")
	t.Run("rename before apply is retryable", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), '5', fixture.privateKey)
		redeemer := fixture.open(t)
		original := redeemer.dependencies.files.rename
		redeemer.dependencies.files.rename = func(*os.File, *os.File, *os.File, string, string) (bool, error) {
			return false, cause
		}
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, cause) || errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(preclaim fault) error = %v", err)
		}
		redeemer.dependencies.files.rename = original
		if _, err := redeemer.Redeem(context.Background(), reference); err != nil {
			t.Fatalf("Redeem(retry) error = %v", err)
		}
	})

	tests := []struct {
		name       string
		inject     func(*Redeemer)
		durability bool
	}{
		{name: "secure", inject: func(redeemer *Redeemer) {
			redeemer.dependencies.files.secureClaim = func(*os.File) error { return cause }
		}},
		{name: "read", inject: func(redeemer *Redeemer) {
			redeemer.dependencies.files.read = func(*os.File, int64) ([]byte, error) { return nil, cause }
		}},
		{name: "directory barrier", durability: true, inject: func(redeemer *Redeemer) { redeemer.dependencies.files.syncDir = func(*os.File) error { return cause } }},
		{name: "applied rename report", durability: true, inject: func(redeemer *Redeemer) {
			original := redeemer.dependencies.files.rename
			redeemer.dependencies.files.rename = func(pending *os.File, claims *os.File, source *os.File, from string, to string) (bool, error) {
				applied, err := original(pending, claims, source, from, to)
				if applied && err == nil {
					return true, cause
				}
				return applied, err
			}
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), byte('6'+index), fixture.privateKey)
			redeemer := fixture.open(t)
			test.inject(redeemer)
			_, err := redeemer.Redeem(context.Background(), reference)
			if !errors.Is(err, cause) || !errors.Is(err, ErrReferenceConsumed) || !errors.Is(err, helper.ErrTicketRedemptionFailed) {
				t.Fatalf("Redeem(postclaim fault) error = %v", err)
			}
			if errors.Is(err, ErrClaimDurabilityUncertain) != test.durability {
				t.Fatalf("Redeem(postclaim fault) durability = %t, want %t: %v", errors.Is(err, ErrClaimDurabilityUncertain), test.durability, err)
			}
			if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, helper.ErrTicketReferenceRedeemed) {
				t.Fatalf("Redeem(postclaim retry) error = %v", err)
			}
		})
	}
}

// TestRedeemerAdditionalFaultsFailClosed covers every authenticated boundary after a permanent claim.
func TestRedeemerAdditionalFaultsFailClosed(t *testing.T) {
	cause := errors.New("additional injected failure")
	tests := []struct {
		name   string
		inject func(*testing.T, *Redeemer, unixRedeemerFixture, context.CancelFunc)
	}{
		{name: "pending open", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			redeemer.dependencies.files.openPending = func(*os.File, string, string) (*os.File, error) { return nil, cause }
		}},
		{name: "claim open", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			redeemer.dependencies.files.openClaim = func(*os.File, string, string) (*os.File, error) { return nil, cause }
		}},
		{name: "claimed identity stat", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			original := redeemer.dependencies.files.openClaim
			redeemer.dependencies.files.openClaim = func(parent *os.File, path string, name string) (*os.File, error) {
				file, err := original(parent, path, name)
				if err == nil {
					_ = file.Close()
				}
				return file, err
			}
		}},
		{name: "claimed identity mismatch", inject: func(t *testing.T, redeemer *Redeemer, fixture unixRedeemerFixture, _ context.CancelFunc) {
			otherPath := filepath.Join(fixture.paths.ClaimsDirectory, "other-file")
			if err := os.WriteFile(otherPath, []byte("other"), unixPrivateFile); err != nil {
				t.Fatalf("write other claim fixture: %v", err)
			}
			redeemer.dependencies.files.openClaim = func(*os.File, string, string) (*os.File, error) { return os.Open(otherPath) }
		}},
		{name: "claimed source policy", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			original := redeemer.dependencies.files.openClaim
			redeemer.dependencies.files.openClaim = func(parent *os.File, path string, name string) (*os.File, error) {
				file, err := original(parent, path, name)
				if err == nil {
					_ = file.Chmod(0o640)
				}
				return file, err
			}
		}},
		{name: "claimed size race", inject: func(_ *testing.T, redeemer *Redeemer, fixture unixRedeemerFixture, _ context.CancelFunc) {
			original := redeemer.dependencies.files.openClaim
			redeemer.dependencies.files.openClaim = func(parent *os.File, path string, name string) (*os.File, error) {
				file, err := original(parent, path, name)
				if err == nil {
					_ = os.Truncate(filepath.Join(fixture.paths.ClaimsDirectory, name), ticketauth.MaxEnvelopeBytes+1)
				}
				return file, err
			}
		}},
		{name: "protected claim policy", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			redeemer.dependencies.files.secureClaim = func(file *os.File) error { return file.Chmod(0o640) }
		}},
		{name: "file sync", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			redeemer.dependencies.files.syncFile = func(*os.File) error { return cause }
		}},
		{name: "pending close", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			calls := 0
			redeemer.dependencies.files.closeFile = func(file *os.File) error {
				calls++
				err := file.Close()
				if calls == 1 {
					return errors.Join(err, cause)
				}
				return err
			}
		}},
		{name: "claimed close", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			calls := 0
			redeemer.dependencies.files.closeFile = func(file *os.File) error {
				calls++
				err := file.Close()
				if calls == 2 {
					return errors.Join(err, cause)
				}
				return err
			}
		}},
		{name: "cancel after read", inject: func(_ *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, cancel context.CancelFunc) {
			original := redeemer.dependencies.files.read
			redeemer.dependencies.files.read = func(file *os.File, maximum int64) ([]byte, error) {
				content, err := original(file, maximum)
				cancel()
				return content, err
			}
		}},
		{name: "ownership observe", inject: func(t *testing.T, redeemer *Redeemer, _ unixRedeemerFixture, _ context.CancelFunc) {
			replaceOwnershipObserver(t, redeemer, &testOwnershipObserver{err: cause})
		}},
		{name: "ownership requester", inject: func(t *testing.T, redeemer *Redeemer, fixture unixRedeemerFixture, _ context.CancelFunc) {
			record := fixture.record()
			record.OwnerIdentity = "999"
			replaceOwnershipObserver(t, redeemer, &testOwnershipObserver{observation: ownership.Observation{Exists: true, Record: record}})
		}},
		{name: "ownership verifier", inject: func(t *testing.T, redeemer *Redeemer, fixture unixRedeemerFixture, _ context.CancelFunc) {
			record := fixture.record()
			record.TicketVerifierKey = "invalid"
			replaceOwnershipObserver(t, redeemer, &testOwnershipObserver{observation: ownership.Observation{Exists: true, Record: record}})
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), byte("0123456789abcd"[index]), fixture.privateKey)
			redeemer := fixture.open(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.inject(t, redeemer, fixture, cancel)
			_, err := redeemer.Redeem(ctx, reference)
			if !errors.Is(err, helper.ErrTicketRedemptionFailed) {
				t.Fatalf("Redeem() error = %v, want failed", err)
			}
			if test.name == "pending open" {
				if errors.Is(err, ErrReferenceConsumed) {
					t.Fatalf("Redeem() error = %v, preclaim failure was consumed", err)
				}
				return
			}
			if !errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("Redeem() error = %v, want consumed", err)
			}
		})
	}
}

// TestClaimClassificationsCoverNativeRaceOutcomes keeps missing, collided, and anomalous rename reports distinct.
func TestClaimClassificationsCoverNativeRaceOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		renameErr error
		want      error
	}{
		{name: "collision", renameErr: fs.ErrExist, want: helper.ErrTicketReferenceRedeemed},
		{name: "missing", renameErr: fs.ErrNotExist, want: helper.ErrTicketReferenceUnknown},
		{name: "empty outcome", renameErr: nil, want: helper.ErrTicketRedemptionFailed},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), byte("def"[index]), fixture.privateKey)
			redeemer := fixture.open(t)
			redeemer.dependencies.files.rename = func(*os.File, *os.File, *os.File, string, string) (bool, error) {
				return false, test.renameErr
			}
			_, err := redeemer.Redeem(context.Background(), reference)
			if !errors.Is(err, test.want) {
				t.Fatalf("Redeem() error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestReferenceClassificationFaultsPreserveEvidence covers protected existence probes without filesystem races.
func TestReferenceClassificationFaultsPreserveEvidence(t *testing.T) {
	cause := errors.New("existence probe failed")
	redeemer := &Redeemer{topology: &topology{paths: testPaths(filepath.Join(string(filepath.Separator), "tmp", "classification"))}}
	redeemer.dependencies.files.entryExists = func(*os.File, string, string) (bool, error) { return false, cause }
	if err := redeemer.classifyAbsentReference(string(testReference('a'))); !errors.Is(err, cause) {
		t.Fatalf("classifyAbsentReference() error = %v", err)
	}
	if err, classified := redeemer.classifyConsumedReference(string(testReference('a'))); !classified || !errors.Is(err, cause) {
		t.Fatalf("classifyConsumedReference(claim error) = %v, %t", err, classified)
	}
	redeemer.dependencies.files.entryExists = func(*os.File, string, string) (bool, error) { return true, nil }
	if err, classified := redeemer.classifyConsumedReference(string(testReference('a'))); !classified || !errors.Is(err, helper.ErrTicketReferenceRedeemed) {
		t.Fatalf("classifyConsumedReference(claimed exists) = %v, %t", err, classified)
	}
	redeemer.dependencies.files.entryExists = func(*os.File, string, string) (bool, error) { return false, nil }
	if err, classified := redeemer.classifyConsumedReference(string(testReference('a'))); classified || err != nil {
		t.Fatalf("classifyConsumedReference(no claim) = %v, %t", err, classified)
	}
}

// TestClaimDeferredCloseFailuresPreservePreAndPostClaimState proves every opened source handle is consumed once.
func TestClaimDeferredCloseFailuresPreservePreAndPostClaimState(t *testing.T) {
	cause := errors.New("pending close failed")
	t.Run("preclaim", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := testReference('1')
		fixture.writeRaw(t, reference, nil)
		redeemer := fixture.open(t)
		redeemer.dependencies.files.closeFile = func(file *os.File) error {
			return errors.Join(file.Close(), cause)
		}
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, cause) || errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(preclaim close) error = %v", err)
		}
	})
	t.Run("postclaim", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), '2', fixture.privateKey)
		redeemer := fixture.open(t)
		redeemer.dependencies.files.secureClaim = func(*os.File) error { return errors.New("secure failed") }
		calls := 0
		redeemer.dependencies.files.closeFile = func(file *os.File) error {
			calls++
			err := file.Close()
			if calls == 2 {
				return errors.Join(err, cause)
			}
			return err
		}
		if _, err := redeemer.Redeem(context.Background(), reference); !errors.Is(err, cause) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(postclaim close) error = %v", err)
		}
		if calls != 2 {
			t.Fatalf("close calls = %d, want claimed and pending exactly once", calls)
		}
	})
}

// TestClaimCancellationAfterOpenStopsBeforeRename covers the last cancellable preclaim boundary.
func TestClaimCancellationAfterOpenStopsBeforeRename(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), '3', fixture.privateKey)
	redeemer := fixture.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	original := redeemer.dependencies.files.openPending
	redeemer.dependencies.files.openPending = func(parent *os.File, path string, name string) (*os.File, error) {
		file, err := original(parent, path, name)
		cancel()
		return file, err
	}
	if _, err := redeemer.Redeem(ctx, reference); !errors.Is(err, context.Canceled) || errors.Is(err, ErrReferenceConsumed) {
		t.Fatalf("Redeem() error = %v, want preclaim cancellation", err)
	}
}

// TestRedeemerCancellationBeforeAndAfterClaimPreservesRetrySemantics proves cancellation cannot reopen a consumed name.
func TestRedeemerCancellationBeforeAndAfterClaimPreservesRetrySemantics(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'a', fixture.privateKey)
		redeemer := fixture.open(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := redeemer.Redeem(ctx, reference); !errors.Is(err, context.Canceled) || errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(preclaim cancellation) error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.paths.PendingDirectory, string(reference))); err != nil {
			t.Fatalf("preclaim cancellation consumed reference: %v", err)
		}
	})
	t.Run("after", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, true)
		reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'b', fixture.privateKey)
		redeemer := fixture.open(t)
		ctx, cancel := context.WithCancel(context.Background())
		original := redeemer.dependencies.files.secureClaim
		redeemer.dependencies.files.secureClaim = func(file *os.File) error {
			err := original(file)
			cancel()
			return err
		}
		if _, err := redeemer.Redeem(ctx, reference); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReferenceConsumed) {
			t.Fatalf("Redeem(postclaim cancellation) error = %v", err)
		}
	})
}

// TestRedeemerRetainedWriterCanOnlySubstituteAnotherValidOwnedTicket documents the same-principal capability boundary.
func TestRedeemerRetainedWriterCanOnlySubstituteAnotherValidOwnedTicket(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	reference := fixture.writeTicket(t, testRedeemerTicket(fixture.now, fixture.owner), 'c', fixture.privateKey)
	path := filepath.Join(fixture.paths.PendingDirectory, string(reference))
	retainedWriter, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("retain same-owner writer: %v", err)
	}
	defer retainedWriter.Close()

	alternate := testRedeemerTicket(fixture.now, fixture.owner)
	alternate.Nonce = strings.Repeat("m", 32)
	envelope, err := ticketauth.Sign(alternate, fixture.privateKey, fixture.now)
	if err != nil {
		t.Fatalf("sign alternate ticket: %v", err)
	}
	encoded, err := ticketauth.Encode(envelope)
	if err != nil {
		t.Fatalf("encode alternate ticket: %v", err)
	}
	redeemer := fixture.open(t)
	originalSync := redeemer.dependencies.files.syncFile
	redeemer.dependencies.files.syncFile = func(claimed *os.File) error {
		if _, err := retainedWriter.Seek(0, 0); err != nil {
			return err
		}
		if err := retainedWriter.Truncate(0); err != nil {
			return err
		}
		if _, err := retainedWriter.Write(encoded); err != nil {
			return err
		}
		if err := retainedWriter.Sync(); err != nil {
			return err
		}
		return originalSync(claimed)
	}
	redemption, err := redeemer.Redeem(context.Background(), reference)
	if err != nil {
		t.Fatalf("Redeem() error = %v", err)
	}
	if redemption.Ticket.Nonce != alternate.Nonce {
		t.Fatalf("Redeem() nonce = %q, want alternate valid authority", redemption.Ticket.Nonce)
	}
}

// TestRedeemerCloseIsIdempotentAndTerminal prevents released handles from silently reopening authority.
func TestRedeemerCloseIsIdempotentAndTerminal(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, true)
	redeemer := fixture.open(t)
	if err := redeemer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := redeemer.Close(); err != nil {
		t.Fatalf("Close() replay error = %v", err)
	}
	if _, err := redeemer.Redeem(context.Background(), testReference('d')); !errors.Is(err, helper.ErrTicketRedemptionFailed) {
		t.Fatalf("Redeem(closed) error = %v", err)
	}
}

// TestUnixProductionAdmissionRequiresRootAndRejectsRootPendingOwners proves machine and requester identities stay distinct.
func TestUnixProductionAdmissionRequiresRootAndRejectsRootPendingOwners(t *testing.T) {
	err := validatePlatformProcessAdmission()
	if os.Geteuid() == 0 && err != nil {
		t.Fatalf("validatePlatformProcessAdmission() error = %v as root", err)
	}
	if os.Geteuid() != 0 && err == nil {
		t.Fatal("validatePlatformProcessAdmission() accepted an unelevated process")
	}
	if err := validateUnixPendingOwnerID(0); err == nil {
		t.Fatal("validateUnixPendingOwnerID() accepted root")
	}
	if err := validateUnixPendingOwnerID(501); err != nil {
		t.Fatalf("validateUnixPendingOwnerID() error = %v", err)
	}
}

// TestOpenFailuresReleasePartialTopology covers launch admission, layout, ownership, and every missing fixed edge.
func TestOpenFailuresReleasePartialTopology(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		redeemer, err := OpenDefault()
		if redeemer != nil {
			_ = redeemer.Close()
		}
		if os.Geteuid() != 0 && err == nil {
			t.Fatal("OpenDefault() accepted an unelevated process")
		}
	})
	t.Run("admission", func(t *testing.T) {
		dependencies := inertDependencies()
		dependencies.admitProcess = func() error { return errors.New("not elevated") }
		if _, err := open(testPaths(filepath.Join(t.TempDir(), "root")), dependencies); err == nil {
			t.Fatal("open() accepted failed process admission")
		}
	})
	t.Run("dependencies", func(t *testing.T) {
		if _, err := open(testPaths(filepath.Join(t.TempDir(), "root")), dependencies{}); err == nil {
			t.Fatal("open() accepted incomplete dependencies")
		}
	})
	t.Run("layout", func(t *testing.T) {
		paths := testPaths(filepath.Join(t.TempDir(), "root"))
		paths.ReplayDirectory += "-redirected"
		if _, err := open(paths, inertDependencies()); err == nil {
			t.Fatal("open() accepted redirected layout")
		}
	})
	t.Run("ownership", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, false)
		dependencies := defaultDependencies()
		dependencies.admitProcess = func() error { return nil }
		cause := errors.New("ownership unavailable")
		dependencies.openOwnership = func(string) (ownershipStore, error) { return nil, cause }
		if _, err := open(fixture.paths, dependencies); !errors.Is(err, cause) {
			t.Fatalf("open() error = %v", err)
		}
	})
	t.Run("topology through open", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, false)
		if err := os.Remove(fixture.paths.StateDirectory); err != nil {
			t.Fatalf("remove state fixture: %v", err)
		}
		dependencies := defaultDependencies()
		dependencies.admitProcess = func() error { return nil }
		if _, err := open(fixture.paths, dependencies); err == nil {
			t.Fatal("open() accepted an incomplete topology")
		}
	})
	t.Run("post-ownership revalidation", func(t *testing.T) {
		fixture := newUnixRedeemerFixture(t, false)
		dependencies := defaultDependencies()
		dependencies.admitProcess = func() error { return nil }
		dependencies.openOwnership = func(string) (ownershipStore, error) {
			if err := os.Rename(fixture.paths.Root, fixture.paths.Root+"-moved"); err != nil {
				return nil, err
			}
			return &testOwnershipObserver{}, nil
		}
		if _, err := open(fixture.paths, dependencies); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("open() error = %v, want post-ownership unsafe", err)
		}
	})

	for _, target := range []string{"root", "tickets", "pending", "claims", "state"} {
		t.Run("missing "+target, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, false)
			paths := map[string]string{
				"root": fixture.paths.Root, "tickets": fixture.paths.TicketsDirectory,
				"pending": fixture.paths.PendingDirectory, "claims": fixture.paths.ClaimsDirectory,
				"state": fixture.paths.StateDirectory,
			}
			path := paths[target]
			if target == "tickets" {
				if err := os.Remove(fixture.paths.PendingDirectory); err != nil {
					t.Fatalf("remove pending fixture: %v", err)
				}
				if err := os.Remove(fixture.paths.ClaimsDirectory); err != nil {
					t.Fatalf("remove claims fixture: %v", err)
				}
			}
			if target == "root" {
				if err := os.Remove(fixture.paths.PendingDirectory); err != nil {
					t.Fatalf("remove pending fixture: %v", err)
				}
				if err := os.Remove(fixture.paths.ClaimsDirectory); err != nil {
					t.Fatalf("remove claims fixture: %v", err)
				}
				if err := os.Remove(fixture.paths.TicketsDirectory); err != nil {
					t.Fatalf("remove tickets fixture: %v", err)
				}
				if err := os.Remove(fixture.paths.StateDirectory); err != nil {
					t.Fatalf("remove state fixture: %v", err)
				}
			}
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove %s fixture: %v", target, err)
			}
			if _, err := openTopology(fixture.paths); err == nil {
				t.Fatalf("openTopology() accepted missing %s", target)
			}
		})
	}
}

// TestTopologyRevalidationRejectsMissingFixedNames catches removals as well as same-policy replacements.
func TestTopologyRevalidationRejectsMissingFixedNames(t *testing.T) {
	for _, target := range []string{"root", "tickets"} {
		t.Run(target, func(t *testing.T) {
			fixture := newUnixRedeemerFixture(t, true)
			redeemer := fixture.open(t)
			path := fixture.paths.Root
			if target == "tickets" {
				path = fixture.paths.TicketsDirectory
			}
			if err := os.Rename(path, path+"-moved"); err != nil {
				t.Fatalf("move %s: %v", target, err)
			}
			if _, err := redeemer.Redeem(context.Background(), testReference('e')); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Redeem() error = %v, want unsafe", err)
			}
		})
	}
}

// TestUnixNativeStorageHelpersRejectUnsafeObjects exercises native errors without relying on elevated fixtures.
func TestUnixNativeStorageHelpersRejectUnsafeObjects(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing")
	if _, err := openPlatformRootDirectory(missing); err == nil {
		t.Fatal("openPlatformRootDirectory() accepted a missing path")
	}
	regularPath := filepath.Join(directory, "regular")
	if err := os.WriteFile(regularPath, []byte("x"), unixPrivateFile); err != nil {
		t.Fatalf("write regular fixture: %v", err)
	}
	if _, err := openPlatformRootDirectory(regularPath); err == nil {
		t.Fatal("openPlatformRootDirectory() accepted a file")
	}
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open parent fixture: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close parent fixture: %v", err)
	}
	if _, err := openPlatformDirectory(parent, directory, "child"); err == nil {
		t.Fatal("openPlatformDirectory() accepted a closed parent")
	}
	if _, err := platformEntryExists(parent, directory, "child"); err == nil {
		t.Fatal("platformEntryExists() accepted a closed parent")
	}

	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatalf("open regular fixture: %v", err)
	}
	defer regular.Close()
	if _, err := platformPendingIdentity(regular); err == nil {
		t.Fatal("platformPendingIdentity() accepted a regular file")
	}
	if err := validatePlatformPendingFile(regular, "not-a-uid"); err == nil {
		t.Fatal("validatePlatformPendingFile() accepted a noncanonical UID")
	}
	if err := validateUnixObject(regular, false, unixPrivateFile, uint32(os.Geteuid()+1), true); err == nil {
		t.Fatal("validateUnixObject() accepted the wrong owner")
	}
	linkPath := filepath.Join(directory, "hard-link")
	if err := os.Link(regularPath, linkPath); err != nil {
		t.Fatalf("create hard-link fixture: %v", err)
	}
	if err := validateUnixObject(regular, false, unixPrivateFile, uint32(os.Geteuid()), true); err == nil {
		t.Fatal("validateUnixObject() accepted multiple links")
	}
	if err := regular.Close(); err != nil {
		t.Fatalf("close regular fixture: %v", err)
	}
	if err := securePlatformClaim(regular); err == nil {
		t.Fatal("securePlatformClaim() accepted a closed handle")
	}
	if _, _, err := unixObjectInfo(regular, false); err == nil {
		t.Fatal("unixObjectInfo() accepted a closed handle")
	}

	if got := unixRenameError(fs.ErrNotExist); !errors.Is(got, fs.ErrNotExist) {
		t.Fatalf("unixRenameError(not exist) = %v", got)
	}
	if got := unixRenameError(fs.ErrExist); !errors.Is(got, fs.ErrExist) {
		t.Fatalf("unixRenameError(exist) = %v", got)
	}
	cause := errors.New("rename failure")
	if got := unixRenameError(cause); !errors.Is(got, cause) {
		t.Fatalf("unixRenameError(other) = %v", got)
	}
}

// TestNativeRenameCollisionAndCrossFilesystemTopology cover no-replace and atomic-volume enforcement.
func TestNativeRenameCollisionAndCrossFilesystemTopology(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, false)
	pending, err := os.Open(fixture.paths.PendingDirectory)
	if err != nil {
		t.Fatalf("open pending fixture: %v", err)
	}
	defer pending.Close()
	claims, err := os.Open(fixture.paths.ClaimsDirectory)
	if err != nil {
		t.Fatalf("open claims fixture: %v", err)
	}
	defer claims.Close()
	name := string(testReference('f'))
	fixture.writeRaw(t, helper.TicketReference(name), []byte("pending"))
	if err := os.WriteFile(filepath.Join(fixture.paths.ClaimsDirectory, name), []byte("claimed"), unixPrivateFile); err != nil {
		t.Fatalf("write collision fixture: %v", err)
	}
	source, err := os.Open(filepath.Join(fixture.paths.PendingDirectory, name))
	if err != nil {
		t.Fatalf("open source fixture: %v", err)
	}
	defer source.Close()
	if applied, err := renamePlatformNoReplace(pending, claims, source, name, name); applied || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renamePlatformNoReplace() = %t, %v", applied, err)
	}

	other, err := os.Open("/dev/shm")
	if err == nil {
		defer other.Close()
		if err := validatePlatformTopology(pending, pending, other); err == nil {
			t.Fatal("validatePlatformTopology() accepted different filesystems")
		}
	}
}

// TestReadBoundedReportsSeekAndReadFailures exercises failures after trustworthy metadata succeeds.
func TestReadBoundedReportsSeekAndReadFailures(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open directory fixture: %v", err)
	}
	defer directory.Close()
	if _, err := readBounded(directory, 1<<20); err == nil {
		t.Fatal("readBounded() accepted an unreadable directory stream")
	}
	path := filepath.Join(t.TempDir(), "write-only")
	if err := os.WriteFile(path, []byte("ticket"), unixPrivateFile); err != nil {
		t.Fatalf("write read-failure fixture: %v", err)
	}
	writeOnly, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write-only fixture: %v", err)
	}
	defer writeOnly.Close()
	if _, err := readBounded(writeOnly, 1<<20); err == nil {
		t.Fatal("readBounded() accepted a write-only stream")
	}
}

// TestTopologyAndObjectIdentityHelpersRejectClosedHandles covers retained-handle metadata failures directly.
func TestTopologyAndObjectIdentityHelpersRejectClosedHandles(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open directory fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close directory fixture: %v", err)
	}
	if err := validatePlatformTopology(file, file, file); err == nil {
		t.Fatal("validatePlatformTopology() accepted closed handles")
	}
	opened, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open comparison fixture: %v", err)
	}
	defer opened.Close()
	if err := sameOpenedObject(file, opened, "closed"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("sameOpenedObject(closed opened) error = %v", err)
	}
	if err := sameOpenedObject(opened, file, "closed"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("sameOpenedObject(closed retained) error = %v", err)
	}
	if err := (*topology)(nil).close(); err != nil {
		t.Fatalf("nil topology close error = %v", err)
	}
}

// TestValidateRetainedCoversIdentityAndFilesystemMismatch binds wrapper classifications to native evidence.
func TestValidateRetainedCoversIdentityAndFilesystemMismatch(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, false)
	topology, err := openTopology(fixture.paths)
	if err != nil {
		t.Fatalf("openTopology() error = %v", err)
	}
	defer topology.close()
	originalIdentity := topology.requesterIdentity
	topology.requesterIdentity = "999"
	if err := topology.validateRetained(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("validateRetained(identity) error = %v", err)
	}
	topology.requesterIdentity = originalIdentity

	otherPath, err := os.MkdirTemp("/dev/shm", "harbor-ticket-claims-")
	if err != nil {
		t.Skipf("cross-filesystem fixture unavailable: %v", err)
	}
	defer os.RemoveAll(otherPath)
	if err := os.Chmod(otherPath, unixPrivateDir); err != nil {
		t.Fatalf("set cross-filesystem fixture mode: %v", err)
	}
	other, err := os.Open(otherPath)
	if err != nil {
		t.Fatalf("open cross-filesystem fixture: %v", err)
	}
	originalClaims := topology.claims
	topology.claims = other
	if err := topology.validateRetained(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("validateRetained(filesystem) error = %v", err)
	}
	topology.claims = originalClaims
	if err := other.Close(); err != nil {
		t.Fatalf("close cross-filesystem fixture: %v", err)
	}
}

// TestOpenTopologyRejectsUnsafePendingPolicy covers admission failure during initial handle retention.
func TestOpenTopologyRejectsUnsafePendingPolicy(t *testing.T) {
	fixture := newUnixRedeemerFixture(t, false)
	if err := os.Chmod(fixture.paths.PendingDirectory, 0o755); err != nil {
		t.Fatalf("broaden pending fixture: %v", err)
	}
	if _, err := openTopology(fixture.paths); err == nil {
		t.Fatal("openTopology() accepted broadened pending policy")
	}
}

// unixRedeemerFixture owns one exact installer-style topology and signing identity.
type unixRedeemerFixture struct {
	paths      machinepaths.Paths
	now        time.Time
	owner      string
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
}

// newUnixRedeemerFixture provisions exact modes and optional protected ownership for one test.
func newUnixRedeemerFixture(t *testing.T, claimed bool) unixRedeemerFixture {
	t.Helper()
	paths := testPaths(filepath.Join(t.TempDir(), "privileged"))
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{path: paths.Root, mode: unixGatewayMode},
		{path: paths.TicketsDirectory, mode: unixGatewayMode},
		{path: paths.PendingDirectory, mode: unixPrivateDir},
		{path: paths.ClaimsDirectory, mode: unixPrivateDir},
		{path: paths.StateDirectory, mode: unixPrivateDir},
	} {
		if err := os.MkdirAll(directory.path, directory.mode); err != nil {
			t.Fatalf("create %s: %v", directory.path, err)
		}
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			t.Fatalf("set exact mode on %s: %v", directory.path, err)
		}
	}
	now := testRedeemerTime()
	ownerID := os.Geteuid()
	if ownerID == 0 {
		ownerID = 1
		if err := os.Chown(paths.PendingDirectory, ownerID, os.Getegid()); err != nil {
			t.Fatalf("assign non-root pending owner: %v", err)
		}
	}
	owner := strconv.Itoa(ownerID)
	publicKey, privateKey := testRedeemerKey('r')
	fixture := unixRedeemerFixture{
		paths: paths, now: now, owner: owner, publicKey: publicKey, privateKey: privateKey,
	}
	if claimed {
		fixture.claimOwnership(t, fixture.record())
	}
	return fixture
}

// claimOwnership stores one protected record before the redeemer retains its ownership handle.
func (fixture unixRedeemerFixture) claimOwnership(t *testing.T, record ownership.Record) {
	t.Helper()
	store, err := ownership.NewStore(fixture.paths.OwnershipPath)
	if err != nil {
		t.Fatalf("open ownership fixture: %v", err)
	}
	if _, err := store.Claim(context.Background(), record); err != nil {
		t.Fatalf("claim ownership fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close ownership fixture: %v", err)
	}
}

// open constructs a redeemer with fixed time while retaining native filesystem operations.
func (fixture unixRedeemerFixture) open(t *testing.T) *Redeemer {
	t.Helper()
	dependencies := defaultDependencies()
	dependencies.clock = testRedeemerClock{now: fixture.now}
	// Native policy helpers still validate every object; only the production euid==0 launch gate is bypassed in unprivileged tests.
	dependencies.admitProcess = func() error { return nil }
	redeemer, err := open(fixture.paths, dependencies)
	if err != nil {
		t.Fatalf("open redeemer fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := redeemer.Close(); err != nil {
			t.Errorf("close redeemer fixture: %v", err)
		}
	})
	return redeemer
}

// record returns the canonical protected ownership bound to this fixture's pending owner and verifier.
func (fixture unixRedeemerFixture) record() ownership.Record {
	return ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     "harbor-redeemer-test",
		OwnerIdentity:      fixture.owner,
		Generation:         7,
		LoopbackPoolPrefix: "127.77.0.0/24",
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(fixture.publicKey),
	}
}

// writeTicket signs and stores one canonical pending file at the fixture's trusted time.
func (fixture unixRedeemerFixture) writeTicket(t *testing.T, ticket helper.Ticket, marker byte, privateKey ed25519.PrivateKey) helper.TicketReference {
	t.Helper()
	return fixture.writeTicketAt(t, ticket, marker, privateKey, fixture.now)
}

// writeTicketAt signs and stores one canonical pending file at an explicit valid signing instant.
func (fixture unixRedeemerFixture) writeTicketAt(t *testing.T, ticket helper.Ticket, marker byte, privateKey ed25519.PrivateKey, signingTime time.Time) helper.TicketReference {
	t.Helper()
	envelope, err := ticketauth.Sign(ticket, privateKey, signingTime)
	if err != nil {
		t.Fatalf("sign pending ticket: %v", err)
	}
	encoded, err := ticketauth.Encode(envelope)
	if err != nil {
		t.Fatalf("encode pending ticket: %v", err)
	}
	reference := testReference(marker)
	fixture.writeRaw(t, reference, encoded)
	return reference
}

// writeRaw creates one exact owner-private pending object without invoking daemon-side publisher code.
func (fixture unixRedeemerFixture) writeRaw(t *testing.T, reference helper.TicketReference, content []byte) {
	t.Helper()
	path := filepath.Join(fixture.paths.PendingDirectory, string(reference))
	if err := os.WriteFile(path, content, unixPrivateFile); err != nil {
		t.Fatalf("write pending ticket: %v", err)
	}
	if err := os.Chmod(path, unixPrivateFile); err != nil {
		t.Fatalf("set exact pending ticket mode: %v", err)
	}
	owner, err := strconv.Atoi(fixture.owner)
	if err != nil {
		t.Fatalf("parse pending owner: %v", err)
	}
	if owner != os.Geteuid() {
		if err := os.Chown(path, owner, os.Getegid()); err != nil {
			t.Fatalf("assign pending ticket owner: %v", err)
		}
	}
}

// replaceOwnershipObserver swaps the retained store only after releasing its native handles.
func replaceOwnershipObserver(t *testing.T, redeemer *Redeemer, observer ownershipStore) {
	t.Helper()
	if err := redeemer.ownership.Close(); err != nil {
		t.Fatalf("close original ownership observer: %v", err)
	}
	redeemer.ownership = observer
}

// testReference maps one hexadecimal marker to the exact 32-byte opaque encoding.
func testReference(marker byte) helper.TicketReference {
	if !strings.ContainsRune("0123456789abcdef", rune(marker)) {
		panic(fmt.Sprintf("test reference marker %q is not lowercase hexadecimal", marker))
	}
	return helper.TicketReference(strings.Repeat(string(marker), 64))
}
