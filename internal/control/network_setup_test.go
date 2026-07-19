package control

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestNetworkSetupContractsJSONRoundTrip verifies each valid contract survives JSON encoding and remains valid.
func TestNetworkSetupContractsJSONRoundTrip(t *testing.T) {
	ticket := validNetworkSetupApprovalTicket()
	tests := []struct {
		name  string
		value any
	}{
		{name: "start", value: StartNetworkSetupRequest{IntentID: "intent-network-setup"}},
		{name: "approval operation", value: NetworkSetupOperation{Operation: validNetworkSetupOperation(domain.OperationRequiresApproval), Revision: 7}},
		{name: "succeeded operation", value: NetworkSetupOperation{Operation: validNetworkSetupOperation(domain.OperationSucceeded), Revision: 8}},
		{name: "prepare", value: PrepareNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7}},
		{name: "ticket", value: ticket},
		{name: "preparation", value: NetworkSetupApprovalPreparation{OperationID: "operation-network-setup", OperationRevision: 7, Ticket: ticket}},
		{name: "confirm", value: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: validNetworkSetupPoolEvidence()}},
		{name: "confirmation", value: validNetworkSetupApprovalConfirmation()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			decoded := reflect.New(reflect.TypeOf(test.value))
			if err := json.Unmarshal(encoded, decoded.Interface()); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			decodedValue := decoded.Elem().Interface()
			if !reflect.DeepEqual(decodedValue, test.value) {
				t.Fatalf("JSON round trip = %#v, want %#v", decodedValue, test.value)
			}
			if err := decodedValue.(interface{ Validate() error }).Validate(); err != nil {
				t.Fatalf("decoded Validate() error = %v", err)
			}
		})
	}
}

// TestNetworkSetupContractsRejectInvalidValues covers each contract-owned invalid field class.
func TestNetworkSetupContractsRejectInvalidValues(t *testing.T) {
	invalidOperation := validNetworkSetupOperation(domain.OperationRequiresApproval)
	invalidOperation.StartedAt = nil
	wrongKind := validNetworkSetupOperation(domain.OperationRequiresApproval)
	wrongKind.Kind = domain.OperationKindProjectStart
	wrongKind.ProjectID = "project-one"
	projectScoped := validNetworkSetupOperation(domain.OperationRequiresApproval)
	projectScoped.ProjectID = "project-one"

	wrongReferenceTicket := validNetworkSetupApprovalTicket()
	wrongReferenceTicket.Reference = helper.TicketReference(strings.Repeat("A", 64))
	wrongOperationTicket := validNetworkSetupApprovalTicket()
	wrongOperationTicket.Operation = helper.OperationEnsureLoopbackIdentity
	wrongPoolTicket := validNetworkSetupApprovalTicket()
	wrongPoolTicket.Pool = "127.42.0.1/29"
	zeroExpiryTicket := validNetworkSetupApprovalTicket()
	zeroExpiryTicket.ExpiresAt = time.Time{}
	localExpiryTicket := validNetworkSetupApprovalTicket()
	localExpiryTicket.ExpiresAt = time.Date(2026, time.July, 19, 12, 5, 0, 0, time.FixedZone("zero-offset", 0))

	mismatchedPreparation := NetworkSetupApprovalPreparation{
		OperationID:       "operation-network-setup",
		OperationRevision: 7,
		Ticket:            validNetworkSetupApprovalTicket(),
	}
	mismatchedPreparation.Ticket.OperationID = "operation-other"

	wrongEvidencePool := validNetworkSetupPoolEvidence()
	wrongEvidencePool.Pool = "127.42.0.0/28"
	shortEvidence := validNetworkSetupPoolEvidence()
	shortEvidence.Identities = shortEvidence.Identities[:7]
	unorderedEvidence := validNetworkSetupPoolEvidence()
	unorderedEvidence.Identities[1].Address = "127.42.0.2"
	invalidObservationEvidence := validNetworkSetupPoolEvidence()
	invalidObservationEvidence.Identities[0].Observation.Fingerprint = "invalid"
	unownedEvidence := validNetworkSetupPoolEvidence()
	unownedEvidence.Identities[0].Observation.State = helper.ObservationAbsent

	invalidConfirmationOperation := validNetworkSetupApprovalConfirmation()
	invalidConfirmationOperation.Operation.FinishedAt = nil
	wrongKindConfirmation := validNetworkSetupApprovalConfirmation()
	wrongKindConfirmation.Operation = wrongKind
	nonSucceededConfirmation := validNetworkSetupApprovalConfirmation()
	nonSucceededConfirmation.Operation = validNetworkSetupOperation(domain.OperationRequiresApproval)
	noncontiguousConfirmation := validNetworkSetupApprovalConfirmation()
	noncontiguousConfirmation.Revision = 9
	wrongPoolConfirmation := validNetworkSetupApprovalConfirmation()
	wrongPoolConfirmation.Pool = "10.0.0.0/29"

	tests := []struct {
		name     string
		contract interface{ Validate() error }
	}{
		{name: "start intent", contract: StartNetworkSetupRequest{}},
		{name: "operation shape", contract: NetworkSetupOperation{Operation: invalidOperation, Revision: 7}},
		{name: "operation kind", contract: NetworkSetupOperation{Operation: wrongKind, Revision: 7}},
		{name: "operation scope", contract: NetworkSetupOperation{Operation: projectScoped, Revision: 7}},
		{name: "operation revision zero", contract: NetworkSetupOperation{Operation: validNetworkSetupOperation(domain.OperationRequiresApproval)}},
		{name: "operation revision too large", contract: NetworkSetupOperation{Operation: validNetworkSetupOperation(domain.OperationRequiresApproval), Revision: domain.MaximumSequence + 1}},
		{name: "prepare operation ID", contract: PrepareNetworkSetupApprovalRequest{ExpectedOperationRevision: 7}},
		{name: "prepare revision", contract: PrepareNetworkSetupApprovalRequest{OperationID: "operation-network-setup"}},
		{name: "ticket operation ID", contract: NetworkSetupApprovalTicket{}},
		{name: "ticket reference", contract: wrongReferenceTicket},
		{name: "ticket operation", contract: wrongOperationTicket},
		{name: "ticket pool", contract: wrongPoolTicket},
		{name: "ticket zero expiry", contract: zeroExpiryTicket},
		{name: "ticket non-UTC expiry", contract: localExpiryTicket},
		{name: "preparation correlation", contract: mismatchedPreparation},
		{name: "confirm selection", contract: ConfirmNetworkSetupApprovalRequest{PoolEvidence: validNetworkSetupPoolEvidence()}},
		{name: "confirm pool", contract: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: wrongEvidencePool}},
		{name: "confirm identity count", contract: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: shortEvidence}},
		{name: "confirm address order", contract: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: unorderedEvidence}},
		{name: "confirm observation", contract: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: invalidObservationEvidence}},
		{name: "confirm ownership", contract: ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 7, PoolEvidence: unownedEvidence}},
		{name: "confirmation operation shape", contract: invalidConfirmationOperation},
		{name: "confirmation operation kind", contract: wrongKindConfirmation},
		{name: "confirmation operation state", contract: nonSucceededConfirmation},
		{name: "confirmation revision", contract: NetworkSetupApprovalConfirmation{Operation: validNetworkSetupOperation(domain.OperationSucceeded), NetworkRevision: 7, Pool: "127.42.0.0/29"}},
		{name: "confirmation network revision", contract: NetworkSetupApprovalConfirmation{Operation: validNetworkSetupOperation(domain.OperationSucceeded), Revision: 8, Pool: "127.42.0.0/29"}},
		{name: "confirmation revision relation", contract: noncontiguousConfirmation},
		{name: "confirmation pool", contract: wrongPoolConfirmation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.contract.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// validNetworkSetupOperation constructs a valid setup operation fixture for an approval or completion state.
func validNetworkSetupOperation(state domain.OperationState) domain.Operation {
	requestedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	operation := domain.Operation{
		ID:          "operation-network-setup",
		IntentID:    "intent-network-setup",
		Kind:        domain.OperationKindNetworkSetup,
		State:       state,
		Phase:       string(state),
		RequestedAt: requestedAt,
		StartedAt:   &startedAt,
	}
	if state == domain.OperationSucceeded {
		finishedAt := startedAt.Add(time.Minute)
		operation.FinishedAt = &finishedAt
	}
	return operation
}

// validNetworkSetupApprovalTicket constructs canonical non-secret helper launch metadata.
func validNetworkSetupApprovalTicket() NetworkSetupApprovalTicket {
	return NetworkSetupApprovalTicket{
		OperationID: "operation-network-setup",
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationEnsureLoopbackPool,
		Pool:        "127.42.0.0/29",
		ExpiresAt:   time.Date(2026, time.July, 19, 12, 5, 0, 0, time.UTC),
	}
}

// validNetworkSetupPoolEvidence constructs all eight owned postconditions with both change outcomes represented.
func validNetworkSetupPoolEvidence() helper.PoolMutationEvidence {
	identities := make([]helper.MutationEvidence, networkSetupPoolAddresses)
	for index := range identities {
		identities[index] = helper.MutationEvidence{
			Changed: index%2 == 0,
			Address: fmt.Sprintf("127.42.0.%d", index),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: strings.Repeat("b", 64),
			},
		}
	}
	return helper.PoolMutationEvidence{Pool: "127.42.0.0/29", Identities: identities}
}

// validNetworkSetupApprovalConfirmation constructs a succeeded contiguous setup result.
func validNetworkSetupApprovalConfirmation() NetworkSetupApprovalConfirmation {
	return NetworkSetupApprovalConfirmation{
		Operation:       validNetworkSetupOperation(domain.OperationSucceeded),
		Revision:        8,
		NetworkRevision: 7,
		Pool:            "127.42.0.0/29",
	}
}
