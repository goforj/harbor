package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestNetworkReleaseOwnershipApprovalProtocolConstantsRemainStable protects the v2 replacement wire contract.
func TestNetworkReleaseOwnershipApprovalProtocolConstantsRemainStable(t *testing.T) {
	if CapabilityNetworkReleaseOwnershipApprovalV2 != "control.network-release-ownership-approval.v2" {
		t.Fatalf("CapabilityNetworkReleaseOwnershipApprovalV2 = %q", CapabilityNetworkReleaseOwnershipApprovalV2)
	}
	for _, capability := range capabilities() {
		if capability == "control.network-release-ownership-approval.v1" {
			t.Fatal("capabilities() advertises retired evidence-free ownership approval V1")
		}
	}
	if methodNetworkReleaseOwnershipPrepare != "control.v1.network.release.ownership.prepare" {
		t.Fatalf("methodNetworkReleaseOwnershipPrepare = %q", methodNetworkReleaseOwnershipPrepare)
	}
	if methodNetworkReleaseOwnershipConfirm != "control.v1.network.release.ownership.confirm" {
		t.Fatalf("methodNetworkReleaseOwnershipConfirm = %q", methodNetworkReleaseOwnershipConfirm)
	}
}

// TestNetworkReleaseOwnershipApprovalDTOValidate covers ownership ticket and evidence boundaries.
func TestNetworkReleaseOwnershipApprovalDTOValidate(t *testing.T) {
	preparation := validNetworkReleaseOwnershipApprovalPreparation()
	if err := preparation.Validate(); err != nil {
		t.Fatalf("Preparation.Validate() error = %v", err)
	}
	confirmation := validNetworkReleaseOwnershipApprovalConfirmation()
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("Confirmation.Validate() error = %v", err)
	}

	preparation.Ticket.CheckpointRevision = 8
	if err := preparation.Validate(); err == nil || !strings.Contains(err.Error(), "another checkpoint") {
		t.Fatalf("Preparation.Validate() error = %v, want checkpoint correlation error", err)
	}
	preparation = validNetworkReleaseOwnershipApprovalPreparation()
	preparation.PublicationDisposition = "unknown"
	if err := preparation.Validate(); err == nil || !strings.Contains(err.Error(), "publication disposition") {
		t.Fatalf("Preparation.Validate() error = %v, want publication disposition error", err)
	}
	confirmation.OwnershipEvidence.ReleaseCheckpointRevision = 7
	if err := confirmation.Validate(); err == nil || !strings.Contains(err.Error(), "ownership evidence") {
		t.Fatalf("Confirmation.Validate() error = %v, want ownership evidence error", err)
	}
}

// TestNetworkReleaseOwnershipApprovalTransportRoundTrips rejects unbounded wire shapes while preserving canonical DTOs.
func TestNetworkReleaseOwnershipApprovalTransportRoundTrips(t *testing.T) {
	preparation := validNetworkReleaseOwnershipApprovalPreparation()
	payload, err := json.Marshal(networkReleaseOwnershipApprovalPreparationResponse{Preparation: preparation})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var response networkReleaseOwnershipApprovalPreparationResponse
	if err := decodeNetworkReleaseOwnershipApprovalPreparationResponse(payload, &response); err != nil {
		t.Fatalf("decodeNetworkReleaseOwnershipApprovalPreparationResponse() error = %v", err)
	}
	if !reflect.DeepEqual(response.Preparation, preparation) {
		t.Fatalf("preparation = %#v, want %#v", response.Preparation, preparation)
	}

	confirmation := validNetworkReleaseOwnershipApprovalConfirmation()
	payload, err = json.Marshal(confirmation)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := decodeConfirmNetworkReleaseOwnershipApprovalRequest(payload)
	if err != nil {
		t.Fatalf("decodeConfirmNetworkReleaseOwnershipApprovalRequest() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, confirmation) {
		t.Fatalf("confirmation = %#v, want %#v", decoded, confirmation)
	}

	if _, err := decodeConfirmNetworkReleaseOwnershipApprovalRequest([]byte(`{"operation_id":"operation-network-release","expected_checkpoint_revision":6}`)); err == nil {
		t.Fatal("decodeConfirmNetworkReleaseOwnershipApprovalRequest() error = nil, want missing evidence error")
	}
	if err := decodeNetworkReleaseOwnershipApprovalPreparationResponse([]byte(`{"preparation":{},"unexpected":true}`), &response); err == nil {
		t.Fatal("decodeNetworkReleaseOwnershipApprovalPreparationResponse() error = nil, want unknown field error")
	}
}

// validNetworkReleaseOwnershipApprovalPreparation returns one complete ownership ticket projection.
func validNetworkReleaseOwnershipApprovalPreparation() NetworkReleaseOwnershipApprovalPreparation {
	return NetworkReleaseOwnershipApprovalPreparation{
		OperationID:            "operation-network-release",
		CheckpointRevision:     6,
		PublicationDisposition: NetworkReleaseOwnershipPublicationDurable,
		Ticket: NetworkReleaseOwnershipApprovalTicket{
			OperationID:          "operation-network-release",
			OperationRevision:    5,
			CheckpointRevision:   6,
			Reference:            helper.TicketReference(strings.Repeat("a", 64)),
			Operation:            helper.OperationReleaseNetworkOwnership,
			OwnershipFingerprint: strings.Repeat("b", 64),
			ExpiresAt:            time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		},
	}
}

// validNetworkReleaseOwnershipApprovalConfirmation returns one exact ownership absence proof.
func validNetworkReleaseOwnershipApprovalConfirmation() ConfirmNetworkReleaseOwnershipApprovalRequest {
	return ConfirmNetworkReleaseOwnershipApprovalRequest{
		OperationID:                domain.OperationID("operation-network-release"),
		ExpectedCheckpointRevision: 6,
		OwnershipEvidence: helper.OwnershipMutationEvidence{
			ReleaseOperationID:           "operation-network-release",
			ReleaseOperationRevision:     5,
			ReleaseCheckpointRevision:    6,
			ReleasedOwnershipFingerprint: strings.Repeat("b", 64),
			Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
		},
	}
}
