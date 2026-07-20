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

// TestNetworkResolverSetupContractsJSONRoundTrip verifies each valid contract survives JSON encoding and remains valid.
func TestNetworkResolverSetupContractsJSONRoundTrip(t *testing.T) {
	ticket := validNetworkResolverSetupApprovalTicket()
	tests := []struct {
		name  string
		value any
	}{
		{name: "start", value: StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}},
		{
			name: "approval operation",
			value: NetworkResolverSetupOperation{
				Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval),
				Revision:  7,
			},
		},
		{
			name: "succeeded operation",
			value: NetworkResolverSetupOperation{
				Operation: validNetworkResolverSetupOperation(domain.OperationSucceeded),
				Revision:  8,
			},
		},
		{
			name: "prepare",
			value: PrepareNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
			},
		},
		{name: "ticket", value: ticket},
		{
			name: "preparation",
			value: NetworkResolverSetupApprovalPreparation{
				OperationID:       "operation-network-resolver-setup",
				OperationRevision: 7,
				Ticket:            ticket,
			},
		},
		{
			name: "confirm",
			value: ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
				ResolverEvidence:          validNetworkResolverSetupEvidence(),
			},
		},
		{name: "confirmation", value: validNetworkResolverSetupApprovalConfirmation()},
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

// TestNetworkResolverSetupContractsRejectInvalidValues covers each contract-owned invalid field class.
func TestNetworkResolverSetupContractsRejectInvalidValues(t *testing.T) {
	invalidOperation := validNetworkResolverSetupOperation(domain.OperationRequiresApproval)
	invalidOperation.StartedAt = nil
	wrongKind := validNetworkResolverSetupOperation(domain.OperationRequiresApproval)
	wrongKind.Kind = domain.OperationKindProjectStart
	wrongKind.ProjectID = "project-one"
	projectScoped := validNetworkResolverSetupOperation(domain.OperationRequiresApproval)
	projectScoped.ProjectID = "project-one"

	wrongReferenceTicket := validNetworkResolverSetupApprovalTicket()
	wrongReferenceTicket.Reference = helper.TicketReference(strings.Repeat("A", 64))
	wrongOperationTicket := validNetworkResolverSetupApprovalTicket()
	wrongOperationTicket.Operation = helper.OperationReleaseResolver
	wrongPolicyTicket := validNetworkResolverSetupApprovalTicket()
	wrongPolicyTicket.PolicyFingerprint = "invalid"
	uppercasePolicyTicket := validNetworkResolverSetupApprovalTicket()
	uppercasePolicyTicket.PolicyFingerprint = strings.Repeat("A", 64)
	wrongOwnershipTicket := validNetworkResolverSetupApprovalTicket()
	wrongOwnershipTicket.TargetOwnershipFingerprint = "invalid"
	uppercaseOwnershipTicket := validNetworkResolverSetupApprovalTicket()
	uppercaseOwnershipTicket.TargetOwnershipFingerprint = strings.Repeat("B", 64)
	zeroExpiryTicket := validNetworkResolverSetupApprovalTicket()
	zeroExpiryTicket.ExpiresAt = time.Time{}
	localExpiryTicket := validNetworkResolverSetupApprovalTicket()
	localExpiryTicket.ExpiresAt = time.Date(2026, time.July, 20, 12, 5, 0, 0, time.FixedZone("zero-offset", 0))

	mismatchedPreparation := NetworkResolverSetupApprovalPreparation{
		OperationID:       "operation-network-resolver-setup",
		OperationRevision: 7,
		Ticket:            validNetworkResolverSetupApprovalTicket(),
	}
	mismatchedPreparation.Ticket.OperationID = "operation-other"
	invalidSelectionPreparation := NetworkResolverSetupApprovalPreparation{
		Ticket: validNetworkResolverSetupApprovalTicket(),
	}
	invalidTicketPreparation := NetworkResolverSetupApprovalPreparation{
		OperationID:       "operation-network-resolver-setup",
		OperationRevision: 7,
		Ticket:            validNetworkResolverSetupApprovalTicket(),
	}
	invalidTicketPreparation.Ticket.PolicyFingerprint = "invalid"

	wrongPolicyEvidence := validNetworkResolverSetupEvidence()
	wrongPolicyEvidence.PolicyFingerprint = "invalid"
	wrongOwnershipEvidence := validNetworkResolverSetupEvidence()
	wrongOwnershipEvidence.OwnershipFingerprint = "invalid"
	wrongObservationEvidence := validNetworkResolverSetupEvidence()
	wrongObservationEvidence.ObservationFingerprint = "invalid"
	wrongPostconditionEvidence := validNetworkResolverSetupEvidence()
	wrongPostconditionEvidence.Postcondition = helper.ResolverPostconditionOwnedAbsent

	invalidConfirmationOperation := validNetworkResolverSetupApprovalConfirmation()
	invalidConfirmationOperation.Operation.FinishedAt = nil
	wrongKindConfirmation := validNetworkResolverSetupApprovalConfirmation()
	wrongKindConfirmation.Operation = wrongKind
	nonSucceededConfirmation := validNetworkResolverSetupApprovalConfirmation()
	nonSucceededConfirmation.Operation = validNetworkResolverSetupOperation(domain.OperationRequiresApproval)
	noncontiguousConfirmation := validNetworkResolverSetupApprovalConfirmation()
	noncontiguousConfirmation.Revision++

	tests := []struct {
		name     string
		contract interface{ Validate() error }
	}{
		{name: "start intent", contract: StartNetworkResolverSetupRequest{}},
		{name: "operation shape", contract: NetworkResolverSetupOperation{Operation: invalidOperation, Revision: 7}},
		{name: "operation kind", contract: NetworkResolverSetupOperation{Operation: wrongKind, Revision: 7}},
		{name: "operation scope", contract: NetworkResolverSetupOperation{Operation: projectScoped, Revision: 7}},
		{name: "operation revision zero", contract: NetworkResolverSetupOperation{Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval)}},
		{
			name: "operation revision too large",
			contract: NetworkResolverSetupOperation{
				Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval),
				Revision:  domain.MaximumSequence + 1,
			},
		},
		{name: "prepare operation ID", contract: PrepareNetworkResolverSetupApprovalRequest{ExpectedOperationRevision: 7}},
		{name: "prepare revision", contract: PrepareNetworkResolverSetupApprovalRequest{OperationID: "operation-network-resolver-setup"}},
		{name: "ticket operation ID", contract: NetworkResolverSetupApprovalTicket{}},
		{name: "ticket reference", contract: wrongReferenceTicket},
		{name: "ticket operation", contract: wrongOperationTicket},
		{name: "ticket policy fingerprint", contract: wrongPolicyTicket},
		{name: "ticket uppercase policy fingerprint", contract: uppercasePolicyTicket},
		{name: "ticket ownership fingerprint", contract: wrongOwnershipTicket},
		{name: "ticket uppercase ownership fingerprint", contract: uppercaseOwnershipTicket},
		{name: "ticket zero expiry", contract: zeroExpiryTicket},
		{name: "ticket non-UTC expiry", contract: localExpiryTicket},
		{name: "preparation selection", contract: invalidSelectionPreparation},
		{name: "preparation ticket", contract: invalidTicketPreparation},
		{name: "preparation correlation", contract: mismatchedPreparation},
		{
			name: "confirm selection",
			contract: ConfirmNetworkResolverSetupApprovalRequest{
				ResolverEvidence: validNetworkResolverSetupEvidence(),
			},
		},
		{
			name: "confirm policy fingerprint",
			contract: ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
				ResolverEvidence:          wrongPolicyEvidence,
			},
		},
		{
			name: "confirm ownership fingerprint",
			contract: ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
				ResolverEvidence:          wrongOwnershipEvidence,
			},
		},
		{
			name: "confirm observation fingerprint",
			contract: ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
				ResolverEvidence:          wrongObservationEvidence,
			},
		},
		{
			name: "confirm postcondition",
			contract: ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               "operation-network-resolver-setup",
				ExpectedOperationRevision: 7,
				ResolverEvidence:          wrongPostconditionEvidence,
			},
		},
		{name: "confirmation operation shape", contract: invalidConfirmationOperation},
		{name: "confirmation operation kind", contract: wrongKindConfirmation},
		{name: "confirmation operation state", contract: nonSucceededConfirmation},
		{
			name: "confirmation revision",
			contract: NetworkResolverSetupApprovalConfirmation{
				Operation:       validNetworkResolverSetupOperation(domain.OperationSucceeded),
				NetworkRevision: 7,
			},
		},
		{
			name: "confirmation network revision",
			contract: NetworkResolverSetupApprovalConfirmation{
				Operation: validNetworkResolverSetupOperation(domain.OperationSucceeded),
				Revision:  8,
			},
		},
		{name: "confirmation revision relation", contract: noncontiguousConfirmation},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.contract.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupEvidenceAllowsChangedAndUnchanged proves idempotent helper success remains confirmable.
func TestNetworkResolverSetupEvidenceAllowsChangedAndUnchanged(t *testing.T) {
	for _, changed := range []bool{false, true} {
		evidence := validNetworkResolverSetupEvidence()
		evidence.Changed = changed
		request := ConfirmNetworkResolverSetupApprovalRequest{
			OperationID:               "operation-network-resolver-setup",
			ExpectedOperationRevision: 7,
			ResolverEvidence:          evidence,
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("Validate(changed=%t) error = %v", changed, err)
		}
	}
}

// validNetworkResolverSetupOperation constructs a valid resolver setup operation fixture for an approval or completion state.
func validNetworkResolverSetupOperation(state domain.OperationState) domain.Operation {
	requestedAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	operation := domain.Operation{
		ID:          "operation-network-resolver-setup",
		IntentID:    "intent-network-resolver-setup",
		Kind:        domain.OperationKindNetworkResolverSetup,
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

// validNetworkResolverSetupApprovalTicket constructs canonical non-secret resolver helper launch metadata.
func validNetworkResolverSetupApprovalTicket() NetworkResolverSetupApprovalTicket {
	return NetworkResolverSetupApprovalTicket{
		OperationID:                "operation-network-resolver-setup",
		Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
		Operation:                  helper.OperationEnsureResolver,
		PolicyFingerprint:          strings.Repeat("b", 64),
		TargetOwnershipFingerprint: strings.Repeat("c", 64),
		ExpiresAt:                  time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC),
	}
}

// validNetworkResolverSetupEvidence constructs one exact policy-bound resolver postcondition.
func validNetworkResolverSetupEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      strings.Repeat("b", 64),
		OwnershipFingerprint:   strings.Repeat("c", 64),
		ObservationFingerprint: strings.Repeat("d", 64),
		Postcondition:          helper.ResolverPostconditionExact,
	}
}

// validNetworkResolverSetupApprovalConfirmation constructs a succeeded contiguous resolver setup result.
func validNetworkResolverSetupApprovalConfirmation() NetworkResolverSetupApprovalConfirmation {
	return NetworkResolverSetupApprovalConfirmation{
		Operation:       validNetworkResolverSetupOperation(domain.OperationSucceeded),
		Revision:        9,
		NetworkRevision: 8,
	}
}
