package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestNetworkResolverPolicyMigrationCallReportsTypedUnsupportedCapability lets desktop recovery distinguish a stale negotiated session from a mutation failure.
func TestNetworkResolverPolicyMigrationCallReportsTypedUnsupportedCapability(t *testing.T) {
	t.Parallel()

	client := &Client{peer: DaemonPeer{}}
	_, err := client.networkResolverPolicyMigrationCall(context.Background(), methodNetworkResolverPolicyMigrationStart, struct{}{})
	if !errors.Is(err, ErrNetworkResolverPolicyMigrationUnsupported) {
		t.Fatalf("networkResolverPolicyMigrationCall() error = %v, want typed unsupported capability", err)
	}
}

// TestNetworkResolverPolicyMigrationContractsKeepRetirementAuthorityNarrow validates the fixed retirement-only contract.
func TestNetworkResolverPolicyMigrationContractsKeepRetirementAuthorityNarrow(t *testing.T) {
	operation, err := domain.NewOperation(
		"operation-resolver-policy-migration",
		"intent-resolver-policy-migration",
		domain.OperationKindNetworkResolverPolicyMigration,
		"",
		time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	startedAt := operation.RequestedAt
	operation.State = domain.OperationRequiresApproval
	operation.Phase = networkResolverPolicyMigrationApprovalPhase
	operation.StartedAt = &startedAt
	migration := NetworkResolverPolicyMigrationOperation{
		Operation: operation,
		Revision:  7,
	}
	if err := migration.Validate(); err != nil {
		t.Fatalf("NetworkResolverPolicyMigrationOperation.Validate() error = %v", err)
	}

	completedAt := operation.RequestedAt.Add(time.Minute)
	succeededOperation := operation
	succeededOperation.State = domain.OperationSucceeded
	succeededOperation.Phase = "completed"
	succeededOperation.FinishedAt = &completedAt
	succeeded := NetworkResolverPolicyMigrationOperation{
		Operation: succeededOperation,
		Revision:  9,
	}
	if err := succeeded.Validate(); err != nil {
		t.Fatalf("succeeded NetworkResolverPolicyMigrationOperation.Validate() error = %v", err)
	}
	encodedSucceeded, err := json.Marshal(networkResolverPolicyMigrationResponse{Migration: succeeded})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decodedSucceeded networkResolverPolicyMigrationResponse
	if err := decodeNetworkResolverPolicyMigrationResponse(encodedSucceeded, &decodedSucceeded); err != nil {
		t.Fatalf("decodeNetworkResolverPolicyMigrationResponse() error = %v", err)
	}
	if !reflect.DeepEqual(decodedSucceeded.Migration, succeeded) {
		t.Fatalf("decodeNetworkResolverPolicyMigrationResponse() = %#v, want %#v", decodedSucceeded.Migration, succeeded)
	}

	for _, mutate := range []func(*NetworkResolverPolicyMigrationOperation){
		func(candidate *NetworkResolverPolicyMigrationOperation) {
			candidate.Operation.State = domain.OperationRunning
		},
		func(candidate *NetworkResolverPolicyMigrationOperation) { candidate.Operation.Phase = "wrong" },
	} {
		candidate := migration
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("NetworkResolverPolicyMigrationOperation.Validate() accepted an invalid state or phase")
		}
	}

	ticket := NetworkResolverPolicyMigrationApprovalTicket{
		OperationID:              operation.ID,
		Reference:                helper.TicketReference(strings.Repeat("c", 64)),
		Operation:                helper.OperationRetireResolver,
		PolicyFingerprint:        strings.Repeat("a", 64),
		PostOwnershipFingerprint: strings.Repeat("b", 64),
		ExpiresAt:                time.Date(2026, time.July, 23, 1, 0, 0, 0, time.UTC),
	}
	preparation := NetworkResolverPolicyMigrationApprovalPreparation{
		OperationID:            operation.ID,
		OperationRevision:      7,
		PublicationDisposition: NetworkResolverPolicyMigrationPublicationIndeterminate,
		Ticket:                 ticket,
	}
	if err := preparation.Validate(); err != nil {
		t.Fatalf("NetworkResolverPolicyMigrationApprovalPreparation.Validate() error = %v", err)
	}

	ticket.Operation = helper.OperationReleaseResolver
	invalidPreparation := NetworkResolverPolicyMigrationApprovalPreparation{
		OperationID:            operation.ID,
		OperationRevision:      7,
		PublicationDisposition: NetworkResolverPolicyMigrationPublicationDurable,
		Ticket:                 ticket,
	}
	if err := invalidPreparation.Validate(); err == nil {
		t.Fatal("NetworkResolverPolicyMigrationApprovalPreparation.Validate() accepted a non-retirement ticket")
	}
}

// TestDecodeNetworkResolverPolicyMigrationRequestsRejectsAmbiguousAuthority validates strict bounded request decoding.
func TestDecodeNetworkResolverPolicyMigrationRequestsRejectsAmbiguousAuthority(t *testing.T) {
	for _, payload := range []string{
		`{"intent_id":"intent-resolver-policy-migration","extra":true}`,
		`{"intent_id":"intent-resolver-policy-migration","intent_id":"intent-replayed"}`,
	} {
		if _, err := decodeStartNetworkResolverPolicyMigrationRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeStartNetworkResolverPolicyMigrationRequest(%s) error = nil", payload)
		}
	}

	for _, payload := range []string{
		`{"operation_id":"operation-resolver-policy-migration","expected_operation_revision":7,"extra":true}`,
		`{"operation_id":"operation-resolver-policy-migration","expected_operation_revision":7,"expected_operation_revision":8}`,
	} {
		if _, err := decodePrepareNetworkResolverPolicyMigrationApprovalRequest([]byte(payload)); err == nil {
			t.Fatalf("decodePrepareNetworkResolverPolicyMigrationApprovalRequest(%s) error = nil", payload)
		}
	}

	for _, payload := range []string{
		`{"operation_id":"operation-resolver-policy-migration","expected_operation_revision":7,"resolver_evidence":{},"extra":true}`,
		`{"operation_id":"operation-resolver-policy-migration","expected_operation_revision":7,"resolver_evidence":{},"resolver_evidence":{}}`,
	} {
		if _, err := decodeConfirmNetworkResolverPolicyMigrationApprovalRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeConfirmNetworkResolverPolicyMigrationApprovalRequest(%s) error = nil", payload)
		}
	}
}

// TestDecodeNetworkResolverPolicyMigrationResponsesRejectsAmbiguousFields validates strict client-side response decoding.
func TestDecodeNetworkResolverPolicyMigrationResponsesRejectsAmbiguousFields(t *testing.T) {
	tests := []struct {
		name   string
		decode func([]byte) error
	}{
		{
			name: "start unknown",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationResponse(payload, &networkResolverPolicyMigrationResponse{})
			},
		},
		{
			name: "start duplicate",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationResponse(payload, &networkResolverPolicyMigrationResponse{})
			},
		},
		{
			name: "prepare unknown",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationPreparationResponse(payload, &networkResolverPolicyMigrationApprovalPreparationResponse{})
			},
		},
		{
			name: "prepare duplicate",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationPreparationResponse(payload, &networkResolverPolicyMigrationApprovalPreparationResponse{})
			},
		},
		{
			name: "confirm unknown",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationConfirmationResponse(payload, &networkResolverPolicyMigrationApprovalConfirmationResponse{})
			},
		},
		{
			name: "confirm duplicate",
			decode: func(payload []byte) error {
				return decodeNetworkResolverPolicyMigrationConfirmationResponse(payload, &networkResolverPolicyMigrationApprovalConfirmationResponse{})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"extra":true}`)
			if strings.Contains(test.name, "duplicate") {
				payload = []byte(`{"migration":{},"migration":{}}`)
				if strings.Contains(test.name, "prepare") {
					payload = []byte(`{"preparation":{},"preparation":{}}`)
				}
				if strings.Contains(test.name, "confirm") {
					payload = []byte(`{"confirmation":{},"confirmation":{}}`)
				}
			}
			if err := test.decode(payload); err == nil {
				t.Fatalf("strict response decode accepted %s", payload)
			}
		})
	}
}

// TestNetworkResolverPolicyMigrationCapabilityTracksItsDedicatedAuthority verifies feature negotiation is independent.
func TestNetworkResolverPolicyMigrationCapabilityTracksItsDedicatedAuthority(t *testing.T) {
	if containsCapability(daemonCapabilities(false, false, false, false), CapabilityNetworkResolverPolicyMigrationV1) {
		t.Fatal("daemonCapabilities() advertised resolver policy migration without its authority")
	}
	if !containsCapability(daemonCapabilities(false, false, false, true), CapabilityNetworkResolverPolicyMigrationV1) {
		t.Fatal("daemonCapabilities() omitted resolver policy migration with its authority")
	}
}

// TestNetworkResolverPolicyMigrationConfirmationSelectionRequiresContiguousWrites rejects skipped and wrapped durable revisions.
func TestNetworkResolverPolicyMigrationConfirmationSelectionRequiresContiguousWrites(t *testing.T) {
	confirmation := NetworkResolverPolicyMigrationApprovalConfirmation{
		Operation: domain.Operation{
			ID: "operation-resolver-policy-migration",
		},
		Revision:        10,
		NetworkRevision: 9,
	}
	if !networkResolverPolicyMigrationConfirmationMatchesSelection(
		confirmation,
		"operation-resolver-policy-migration",
		7,
	) {
		t.Fatal("exact migration completion did not match its selected approval revision")
	}

	tests := []struct {
		name     string
		mutate   func(*NetworkResolverPolicyMigrationApprovalConfirmation)
		revision domain.Sequence
	}{
		{
			name: "wrong operation",
			mutate: func(value *NetworkResolverPolicyMigrationApprovalConfirmation) {
				value.Operation.ID = "operation-other"
			},
			revision: 7,
		},
		{
			name: "skipped network revision",
			mutate: func(value *NetworkResolverPolicyMigrationApprovalConfirmation) {
				value.NetworkRevision++
				value.Revision++
			},
			revision: 7,
		},
		{
			name: "skipped terminal revision",
			mutate: func(value *NetworkResolverPolicyMigrationApprovalConfirmation) {
				value.Revision++
			},
			revision: 7,
		},
		{
			name:     "approval underflow",
			mutate:   func(*NetworkResolverPolicyMigrationApprovalConfirmation) {},
			revision: domain.MaximumSequence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := confirmation
			test.mutate(&candidate)
			if networkResolverPolicyMigrationConfirmationMatchesSelection(
				candidate,
				"operation-resolver-policy-migration",
				test.revision,
			) {
				t.Fatal("migration confirmation matched a noncontiguous selection")
			}
		})
	}
}
