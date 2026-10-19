package ticketredeemer

import (
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
)

// TestAdmitPoolExpansionRequiresExactSourceOrCompletedTarget proves signed growth cannot move or replace an installation claim.
func TestAdmitPoolExpansionRequiresExactSourceOrCompletedTarget(t *testing.T) {
	publicKey, _ := testRedeemerKey('g')
	ticket := testRedeemerPoolTicket(testRedeemerTime(), "501")
	source := ownershipRecordFromTicket(ticket, encodeVerifierKey(publicKey))
	sourceObservation := testOwnershipObservation(t, source)
	ticket.PreviousPool = ticket.ApprovedPool
	ticket.ApprovedPool = "127.77.0.0/24"
	ticket.ExpectedOwnershipFingerprint = sourceObservation.Fingerprint
	target := source
	target.LoopbackPoolPrefix = ticket.ApprovedPool
	for _, record := range []ownership.Record{source, target} {
		admission, err := admitTicket(helper.TicketReference(strings.Repeat("a", 64)), ticket, testOwnershipObservation(t, record), "501")
		if err != nil {
			t.Fatal(err)
		}
		want := helper.OwnershipAdmissionPoolExpansion
		if record == target {
			want = helper.OwnershipAdmissionAlreadyCurrent
		}
		if admission.OwnershipState != want {
			t.Fatalf("admission = %s, want %s", admission.OwnershipState, want)
		}
	}
	for _, change := range []func(*helper.Ticket){func(t *helper.Ticket) { t.PreviousPool = "127.77.0.16/29" }, func(t *helper.Ticket) { t.ExpectedOwnershipFingerprint = strings.Repeat("f", 64) }, func(t *helper.Ticket) { t.ApprovedPool = "127.77.1.0/24" }, func(t *helper.Ticket) { t.OwnershipGeneration++ }, func(t *helper.Ticket) { t.RequesterIdentity = "502" }, func(t *helper.Ticket) { t.NetworkPolicyFingerprint = strings.Repeat("a", 64) }} {
		changed := ticket
		change(&changed)
		if _, err := admitTicket(helper.TicketReference(strings.Repeat("a", 64)), changed, sourceObservation, "501"); err == nil {
			t.Fatal("ownership substitution admitted")
		}
	}
	if err := validateBootstrapTicket(ticket, "501"); err == nil {
		t.Fatal("expansion bootstrapped without protected source")
	}
}
