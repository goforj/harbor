package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestNetworkReleaseLoopbackApprovalProtocolConstantsRemainStable protects the additive wire names from accidental renaming.
func TestNetworkReleaseLoopbackApprovalProtocolConstantsRemainStable(t *testing.T) {
	if CapabilityNetworkReleaseLoopbackApprovalV1 != "control.network-release-loopback-approval.v1" {
		t.Fatalf("CapabilityNetworkReleaseLoopbackApprovalV1 = %q", CapabilityNetworkReleaseLoopbackApprovalV1)
	}
	if methodNetworkReleaseLoopbackPrepare != "control.v1.network.release.loopback.prepare" {
		t.Fatalf("methodNetworkReleaseLoopbackPrepare = %q", methodNetworkReleaseLoopbackPrepare)
	}
	if methodNetworkReleaseLoopbackConfirm != "control.v1.network.release.loopback.confirm" {
		t.Fatalf("methodNetworkReleaseLoopbackConfirm = %q", methodNetworkReleaseLoopbackConfirm)
	}
}

// TestNetworkReleaseLoopbackApprovalRequestValidate covers the bounded checkpoint selector.
func TestNetworkReleaseLoopbackApprovalRequestValidate(t *testing.T) {
	valid := PrepareNetworkReleaseLoopbackApprovalRequest{
		OperationID:                "operation-network-release",
		ExpectedCheckpointRevision: 7,
	}
	for _, test := range []struct {
		name    string
		request PrepareNetworkReleaseLoopbackApprovalRequest
		want    string
	}{
		{
			name:    "valid",
			request: valid,
		},
		{
			name: "operation ID",
			request: PrepareNetworkReleaseLoopbackApprovalRequest{
				ExpectedCheckpointRevision: 7,
			},
			want: "operation",
		},
		{
			name: "revision",
			request: PrepareNetworkReleaseLoopbackApprovalRequest{
				OperationID: valid.OperationID,
			},
			want: "checkpoint",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkReleaseLoopbackApprovalTicketValidate covers every ticket boundary.
func TestNetworkReleaseLoopbackApprovalTicketValidate(t *testing.T) {
	valid := validNetworkReleaseLoopbackApprovalTicket()
	for _, test := range []struct {
		name   string
		mutate func(*NetworkReleaseLoopbackApprovalTicket)
		want   string
	}{
		{
			name: "valid",
		},
		{
			name: "operation ID",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.OperationID = ""
			},
			want: "operation",
		},
		{
			name: "reference",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.Reference = "bad"
			},
			want: "ticket reference",
		},
		{
			name: "operation",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.Operation = helper.OperationEnsureLoopbackPool
			},
			want: "expected",
		},
		{
			name: "pool",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.Pool = "127.42.0.1/29"
			},
			want: "canonical IPv4 loopback /29",
		},
		{
			name: "zero expiry",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.ExpiresAt = time.Time{}
			},
			want: "expiry",
		},
		{
			name: "non UTC expiry",
			mutate: func(ticket *NetworkReleaseLoopbackApprovalTicket) {
				ticket.ExpiresAt = time.Date(2026, time.July, 23, 12, 0, 0, 0, time.FixedZone("offset", 3600))
			},
			want: "expiry",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ticket := valid
			if test.mutate != nil {
				test.mutate(&ticket)
			}
			err := ticket.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkReleaseLoopbackPublicationDispositionValidate accepts only explicit publication outcomes.
func TestNetworkReleaseLoopbackPublicationDispositionValidate(t *testing.T) {
	for _, disposition := range []NetworkReleaseLoopbackPublicationDisposition{
		NetworkReleaseLoopbackPublicationDurable,
		NetworkReleaseLoopbackPublicationIndeterminate,
	} {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", disposition, err)
		}
	}
	if err := NetworkReleaseLoopbackPublicationDisposition("unknown").Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsupported disposition error")
	}
}

// TestNetworkReleaseLoopbackApprovalPreparationValidate preserves selection, publication, and ticket correlation.
func TestNetworkReleaseLoopbackApprovalPreparationValidate(t *testing.T) {
	ticket := validNetworkReleaseLoopbackApprovalTicket()
	valid := NetworkReleaseLoopbackApprovalPreparation{
		OperationID:            ticket.OperationID,
		CheckpointRevision:     7,
		PublicationDisposition: NetworkReleaseLoopbackPublicationDurable,
		Ticket:                 ticket,
	}
	for _, test := range []struct {
		name  string
		value NetworkReleaseLoopbackApprovalPreparation
		want  string
	}{
		{
			name:  "durable",
			value: valid,
		},
		{
			name: "indeterminate",
			value: func() NetworkReleaseLoopbackApprovalPreparation {
				value := valid
				value.PublicationDisposition = NetworkReleaseLoopbackPublicationIndeterminate
				return value
			}(),
		},
		{
			name: "selection",
			value: NetworkReleaseLoopbackApprovalPreparation{
				CheckpointRevision: 7,
			},
			want: "operation",
		},
		{
			name: "publication",
			value: func() NetworkReleaseLoopbackApprovalPreparation {
				value := valid
				value.PublicationDisposition = "unknown"
				return value
			}(),
			want: "publication disposition",
		},
		{
			name: "ticket",
			value: func() NetworkReleaseLoopbackApprovalPreparation {
				value := valid
				value.Ticket.Reference = "bad"
				return value
			}(),
			want: "ticket reference",
		},
		{
			name: "ticket operation ID",
			value: func() NetworkReleaseLoopbackApprovalPreparation {
				value := valid
				value.Ticket.OperationID = "operation-other"
				return value
			}(),
			want: "another operation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestConfirmNetworkReleaseLoopbackApprovalRequestValidate covers complete canonical absent-pool evidence.
func TestConfirmNetworkReleaseLoopbackApprovalRequestValidate(t *testing.T) {
	valid := ConfirmNetworkReleaseLoopbackApprovalRequest{
		OperationID:                "operation-network-release",
		ExpectedCheckpointRevision: 7,
		LoopbackEvidence:           validNetworkReleaseLoopbackEvidence(),
	}
	for _, test := range []struct {
		name    string
		request ConfirmNetworkReleaseLoopbackApprovalRequest
		want    string
	}{
		{
			name:    "valid",
			request: valid,
		},
		{
			name: "operation ID",
			request: ConfirmNetworkReleaseLoopbackApprovalRequest{
				ExpectedCheckpointRevision: 7,
				LoopbackEvidence:           valid.LoopbackEvidence,
			},
			want: "operation",
		},
		{
			name: "revision",
			request: ConfirmNetworkReleaseLoopbackApprovalRequest{
				OperationID:      valid.OperationID,
				LoopbackEvidence: valid.LoopbackEvidence,
			},
			want: "checkpoint",
		},
		{
			name: "pool",
			request: func() ConfirmNetworkReleaseLoopbackApprovalRequest {
				value := valid
				value.LoopbackEvidence = validNetworkReleaseLoopbackEvidence()
				value.LoopbackEvidence.Pool = "127.42.0.1/29"
				return value
			}(),
			want: "canonical IPv4 loopback /29",
		},
		{
			name: "identity count",
			request: func() ConfirmNetworkReleaseLoopbackApprovalRequest {
				value := valid
				value.LoopbackEvidence = validNetworkReleaseLoopbackEvidence()
				value.LoopbackEvidence.Identities = value.LoopbackEvidence.Identities[:7]
				return value
			}(),
			want: "exactly 8 identities",
		},
		{
			name: "identity address",
			request: func() ConfirmNetworkReleaseLoopbackApprovalRequest {
				value := valid
				value.LoopbackEvidence = validNetworkReleaseLoopbackEvidence()
				value.LoopbackEvidence.Identities[0].Address = "127.42.0.1"
				return value
			}(),
			want: "canonical order",
		},
		{
			name: "identity fingerprint",
			request: func() ConfirmNetworkReleaseLoopbackApprovalRequest {
				value := valid
				value.LoopbackEvidence = validNetworkReleaseLoopbackEvidence()
				value.LoopbackEvidence.Identities[0].Observation.Fingerprint = "bad"
				return value
			}(),
			want: "observation is invalid",
		},
		{
			name: "identity state",
			request: func() ConfirmNetworkReleaseLoopbackApprovalRequest {
				value := valid
				value.LoopbackEvidence = validNetworkReleaseLoopbackEvidence()
				value.LoopbackEvidence.Identities[0].Observation.State = helper.ObservationOwned
				return value
			}(),
			want: "must be absent",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkReleaseLoopbackApprovalModelsMarshalCanonicalJSON protects the reviewed field names and required ticket shape.
func TestNetworkReleaseLoopbackApprovalModelsMarshalCanonicalJSON(t *testing.T) {
	ticket := validNetworkReleaseLoopbackApprovalTicket()
	preparation := NetworkReleaseLoopbackApprovalPreparation{
		OperationID:            ticket.OperationID,
		CheckpointRevision:     7,
		PublicationDisposition: NetworkReleaseLoopbackPublicationDurable,
		Ticket:                 ticket,
	}
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{
			name: "prepare request",
			value: PrepareNetworkReleaseLoopbackApprovalRequest{
				OperationID:                "operation-network-release",
				ExpectedCheckpointRevision: 7,
			},
			want: `{"operation_id":"operation-network-release","expected_checkpoint_revision":7}`,
		},
		{
			name:  "ticket",
			value: ticket,
			want:  `{"operation_id":"operation-network-release","reference":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operation":"release_loopback_pool","pool":"127.42.0.0/29","expires_at":"2026-07-23T12:00:00Z"}`,
		},
		{
			name:  "preparation",
			value: preparation,
			want:  `{"operation_id":"operation-network-release","checkpoint_revision":7,"publication_disposition":"durable","ticket":{"operation_id":"operation-network-release","reference":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","operation":"release_loopback_pool","pool":"127.42.0.0/29","expires_at":"2026-07-23T12:00:00Z"}}`,
		},
		{
			name: "confirm request",
			value: ConfirmNetworkReleaseLoopbackApprovalRequest{
				OperationID:                "operation-network-release",
				ExpectedCheckpointRevision: 7,
				LoopbackEvidence:           validNetworkReleaseLoopbackEvidence(),
			},
			want: `{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"loopback_evidence":{"pool":"127.42.0.0/29","identities":[{"changed":false,"address":"127.42.0.0","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.1","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.2","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.3","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.4","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.5","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.6","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},{"changed":false,"address":"127.42.0.7","observation":{"state":"absent","fingerprint":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("json.Marshal() = %s, want %s", got, test.want)
			}
		})
	}
}

// validNetworkReleaseLoopbackApprovalTicket constructs canonical non-secret loopback-pool release launch metadata.
func validNetworkReleaseLoopbackApprovalTicket() NetworkReleaseLoopbackApprovalTicket {
	return NetworkReleaseLoopbackApprovalTicket{
		OperationID: domain.OperationID("operation-network-release"),
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationReleaseLoopbackPool,
		Pool:        "127.42.0.0/29",
		ExpiresAt:   time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
	}
}

// validNetworkReleaseLoopbackEvidence constructs a complete canonical absent loopback-pool release postcondition.
func validNetworkReleaseLoopbackEvidence() helper.PoolMutationEvidence {
	identities := make([]helper.MutationEvidence, networkSetupPoolAddresses)
	for index := range identities {
		identities[index] = helper.MutationEvidence{
			Address: "127.42.0." + string(rune('0'+index)),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: strings.Repeat("b", 64),
			},
		}
	}
	return helper.PoolMutationEvidence{
		Pool:       "127.42.0.0/29",
		Identities: identities,
	}
}
