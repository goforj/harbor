package loopbackhandler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/poolpolicy"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// expansionFixture builds complete source or retry authority from current per-address host observations.
func expansionFixture(t *testing.T, observations map[netip.Addr]loopback.Observation, current bool) (helper.Ticket, helper.TicketAdmission, ownership.Observation) {
	t.Helper()
	previous := netip.MustParsePrefix("127.77.0.8/29")
	addresses, err := poolpolicy.Addresses(netip.MustParsePrefix("127.77.0.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	base := handlerPoolTicket(t, handlerPoolObservations(func(int) loopback.State { return loopback.StateAbsent }))
	base.ApprovedPool = "127.77.0.0/24"
	base.PreviousPool = previous.String()
	base.RequesterIdentity = "501"
	base.OwnershipSchemaVersion = ownership.IdentitySchemaVersion
	base.OwnershipGeneration = 1
	base.InstallationID = "installation-expansion-test"
	base.ExpectedLoopbackPool = &helper.ExpectedLoopbackPool{}
	for _, address := range addresses {
		observation, found := observations[address]
		if !found {
			state := loopback.StateAbsent
			if previous.Contains(address) {
				state = loopback.StateExact
			}
			observation = handlerObservationAt(address, state)
			observations[address] = observation
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		item := helper.ExpectedLoopbackIdentity{Address: address.String(), ExpectedObservation: helper.ExpectedObservation{State: helper.ObservationOwned, Fingerprint: fingerprint}}
		if observation.State == loopback.StateAbsent {
			item.ExpectedObservation.State = helper.ObservationAbsent
			pre := helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{}}
			request, err := newPreAssignmentRequest(address, pre)
			if err != nil {
				t.Fatal(err)
			}
			pre.Fingerprint, err = handlerSafePreAssignmentObservation(request).Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			item.ExpectedPreAssignment = &pre
		}
		base.ExpectedLoopbackPool.Identities = append(base.ExpectedLoopbackPool.Identities, item)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	target := ownership.Record{SchemaVersion: base.OwnershipSchemaVersion, InstallationID: base.InstallationID, OwnerIdentity: base.RequesterIdentity, Generation: base.OwnershipGeneration, LoopbackPoolPrefix: base.ApprovedPool, TicketVerifierKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}
	source := target
	source.LoopbackPoolPrefix = previous.String()
	sourceFingerprint, err := source.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	base.ExpectedOwnershipFingerprint = sourceFingerprint
	admission := helper.TicketAdmission{OwnershipState: helper.OwnershipAdmissionPoolExpansion, OwnershipFingerprint: sourceFingerprint, TargetOwnershipFingerprint: targetFingerprint, PostOwnershipFingerprint: targetFingerprint, TicketVerifierKey: target.TicketVerifierKey}
	if current {
		admission.OwnershipState = helper.OwnershipAdmissionAlreadyCurrent
		admission.OwnershipFingerprint = targetFingerprint
	}
	return base, admission, ownership.Observation{Exists: true, Record: target, Fingerprint: targetFingerprint}
}

// TestExpansionRetriesPartialHostProgress preserves existing identities and recovers after protected ownership was already expanded.
func TestExpansionRetriesPartialHostProgress(t *testing.T) {
	observations := map[netip.Addr]loopback.Observation{}
	ticket, admission, target := expansionFixture(t, observations, false)
	failure := netip.MustParseAddr("127.77.0.100")
	adapter := &poolFakeAdapter{observations: observations, ensureErrors: map[netip.Addr]error{failure: errors.New("injected native mutation failure")}}
	observer := handlerPoolPreAssignmentObserver(t, ticket)
	claimed := false
	expand := func(_ context.Context, previous, fingerprint string, record ownership.Record) (ownership.Observation, error) {
		if previous != ticket.PreviousPool || fingerprint != ticket.ExpectedOwnershipFingerprint || record != target.Record || len(adapter.ensureCalls) != 0 || len(adapter.observeCalls) != 256 {
			t.Fatal("ownership changed before complete preflight")
		}
		claimed = true
		return target, nil
	}
	handler := newHandler(adapter, observer.observe)
	if _, err := handler.expandLoopbackPool(t.Context(), ticket, admission, expand); err == nil || !claimed {
		t.Fatal("partial mutation did not retain expanded ownership")
	}
	delete(adapter.ensureErrors, failure)
	ticket, admission, target = expansionFixture(t, observations, true)
	observer = handlerPoolPreAssignmentObserver(t, ticket)
	handler = newHandler(adapter, observer.observe)
	evidence, err := handler.expandLoopbackPool(t.Context(), ticket, admission, func(context.Context, string, string, ownership.Record) (ownership.Observation, error) {
		return target, nil
	})
	if err != nil || len(evidence.Identities) != 256 {
		t.Fatalf("retry = %d identities, %v", len(evidence.Identities), err)
	}
	for _, item := range evidence.Identities {
		if item.Observation.State != helper.ObservationOwned {
			t.Fatal("incomplete expansion exposed")
		}
	}
	// Global cleanup must cover the expanded range rather than only its original eight aliases.
	ticket, _, _ = expansionFixture(t, observations, true)
	ticket.Operation = helper.OperationReleaseLoopbackPool
	ticket.PreviousPool = ""
	ticket.ExpectedOwnershipFingerprint = ""
	released, err := handler.ReleaseLoopbackPool(t.Context(), ticket)
	if err != nil || len(released.Identities) != 256 || adapter.releaseCalls != 256 {
		t.Fatalf("expanded cleanup = %d identities, %d releases, %v", len(released.Identities), adapter.releaseCalls, err)
	}
	for _, observation := range observations {
		if observation.State != loopback.StateAbsent {
			t.Fatal("expanded alias leaked during cleanup")
		}
	}
}

// TestExpansionNeverAdoptsForeignOrDriftingAuthority proves failed approvals emit no alias mutations.
func TestExpansionNeverAdoptsForeignOrDriftingAuthority(t *testing.T) {
	for _, scenario := range []string{"existing new assignment", "source fingerprint", "ownership admission", "root claim failure", "root readback"} {
		t.Run(scenario, func(t *testing.T) {
			observations := map[netip.Addr]loopback.Observation{}
			if scenario == "existing new assignment" {
				address := netip.MustParseAddr("127.77.0.1")
				observations[address] = handlerObservationAt(address, loopback.StateExact)
			}
			ticket, admission, target := expansionFixture(t, observations, false)
			if scenario == "source fingerprint" {
				ticket.ExpectedOwnershipFingerprint = strings.Repeat("a", 64)
			}
			if scenario == "ownership admission" {
				admission.OwnershipState = helper.OwnershipAdmissionSchema1To2
			}
			adapter := &poolFakeAdapter{observations: observations}
			observer := handlerPoolPreAssignmentObserver(t, ticket)
			claims := 0
			_, err := newHandler(adapter, observer.observe).expandLoopbackPool(t.Context(), ticket, admission, func(context.Context, string, string, ownership.Record) (ownership.Observation, error) {
				claims++
				if scenario == "root claim failure" {
					return ownership.Observation{}, errors.New("CAS lost")
				}
				if scenario == "root readback" {
					return ownership.Observation{}, nil
				}
				return target, nil
			})
			if err == nil || len(adapter.ensureCalls) != 0 {
				t.Fatalf("unsafe expansion: %v, %d mutations", err, len(adapter.ensureCalls))
			}
			if scenario != "root claim failure" && scenario != "root readback" && claims != 0 {
				t.Fatal("invalid approval changed protected ownership")
			}
		})
	}
}
