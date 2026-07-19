package control

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestProjectUnregisterApprovalErrorConstructorsKeepWireClassificationInControl verifies authority can classify without depending on session internals.
func TestProjectUnregisterApprovalErrorConstructorsKeepWireClassificationInControl(t *testing.T) {
	cause := errors.New("durable approval state changed")
	for _, test := range []struct {
		name      string
		construct func(error) error
		want      rpc.ErrorCode
	}{
		{name: "conflict", construct: NewProjectUnregisterApprovalConflictError, want: rpc.ErrorCodeConflict},
		{name: "not found", construct: NewProjectUnregisterApprovalNotFoundError, want: rpc.ErrorCodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.construct(cause)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.want || !errors.Is(err, cause) {
				t.Fatalf("constructed error = %#v, want %q wrapping cause", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterApprovalRequestsValidateExactSelections covers both request types and shared revision bounds.
func TestProjectUnregisterApprovalRequestsValidateExactSelections(t *testing.T) {
	prepare := PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41}
	confirm := ConfirmProjectUnregisterApprovalRequest{OperationID: prepare.OperationID, ExpectedOperationRevision: prepare.ExpectedOperationRevision}
	if err := prepare.Validate(); err != nil {
		t.Fatalf("PrepareProjectUnregisterApprovalRequest.Validate() error = %v", err)
	}
	if err := confirm.Validate(); err != nil {
		t.Fatalf("ConfirmProjectUnregisterApprovalRequest.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name      string
		operation domain.OperationID
		revision  domain.Sequence
	}{
		{name: "operation", operation: " bad ", revision: 1},
		{name: "zero revision", operation: "operation-remove"},
		{name: "large revision", operation: "operation-remove", revision: domain.MaximumSequence + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepare := PrepareProjectUnregisterApprovalRequest{OperationID: test.operation, ExpectedOperationRevision: test.revision}
			confirm := ConfirmProjectUnregisterApprovalRequest{OperationID: test.operation, ExpectedOperationRevision: test.revision}
			if err := prepare.Validate(); err == nil {
				t.Fatal("PrepareProjectUnregisterApprovalRequest.Validate() error = nil")
			}
			if err := confirm.Validate(); err == nil {
				t.Fatal("ConfirmProjectUnregisterApprovalRequest.Validate() error = nil")
			}
		})
	}
}

// TestHelperApprovalTicketValidation covers every capability field exposed to an interactive client.
func TestHelperApprovalTicketValidation(t *testing.T) {
	valid := validControlApprovalTicket()
	if err := valid.Validate(); err != nil {
		t.Fatalf("HelperApprovalTicket.Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*HelperApprovalTicket)
	}{
		{name: "operation ID", mutate: func(ticket *HelperApprovalTicket) { ticket.OperationID = " bad " }},
		{name: "lease key", mutate: func(ticket *HelperApprovalTicket) { ticket.LeaseKey.SecondaryID = "bad secondary" }},
		{name: "reference", mutate: func(ticket *HelperApprovalTicket) { ticket.Reference = "short" }},
		{name: "operation", mutate: func(ticket *HelperApprovalTicket) { ticket.Operation = helper.OperationEnsureLoopbackIdentity }},
		{name: "malformed address", mutate: func(ticket *HelperApprovalTicket) { ticket.Address = "not-an-address" }},
		{name: "non-loopback address", mutate: func(ticket *HelperApprovalTicket) { ticket.Address = "192.0.2.1" }},
		{name: "IPv6 address", mutate: func(ticket *HelperApprovalTicket) { ticket.Address = "::1" }},
		{name: "zero expiry", mutate: func(ticket *HelperApprovalTicket) { ticket.ExpiresAt = time.Time{} }},
		{name: "non-UTC expiry", mutate: func(ticket *HelperApprovalTicket) {
			ticket.ExpiresAt = ticket.ExpiresAt.In(time.FixedZone("offset", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := valid
			test.mutate(&ticket)
			if err := ticket.Validate(); err == nil {
				t.Fatal("HelperApprovalTicket.Validate() error = nil")
			}
		})
	}
}

// TestProjectUnregisterApprovalPreparationValidation rejects contradictory progress and authority.
func TestProjectUnregisterApprovalPreparationValidation(t *testing.T) {
	valid := validControlApprovalPreparation()
	if err := valid.Validate(); err != nil {
		t.Fatalf("ProjectUnregisterApprovalPreparation.Validate() error = %v", err)
	}
	ready := valid
	ready.ReleasedLeases = ready.TotalLeases
	ready.PendingLeases = 0
	ready.Ticket = nil
	if err := ready.Validate(); err != nil {
		t.Fatalf("ready ProjectUnregisterApprovalPreparation.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ProjectUnregisterApprovalPreparation)
	}{
		{name: "operation", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.OperationID = " bad " }},
		{name: "revision", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.OperationRevision = 0 }},
		{name: "project", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.ProjectID = " bad " }},
		{name: "zero total", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.TotalLeases = 0; value.PendingLeases = 0 }},
		{name: "large total", mutate: func(value *ProjectUnregisterApprovalPreparation) {
			value.TotalLeases = maximumProjectUnregisterApprovalLeases + 1
			value.PendingLeases = value.TotalLeases
		}},
		{name: "negative released", mutate: func(value *ProjectUnregisterApprovalPreparation) {
			value.ReleasedLeases = -1
			value.PendingLeases = value.TotalLeases + 1
		}},
		{name: "negative pending", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.PendingLeases = -1 }},
		{name: "inconsistent counts", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.PendingLeases = 2 }},
		{name: "pending without ticket", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.Ticket = nil }},
		{name: "complete with ticket", mutate: func(value *ProjectUnregisterApprovalPreparation) {
			value.ReleasedLeases = value.TotalLeases
			value.PendingLeases = 0
		}},
		{name: "invalid ticket", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.Ticket.Reference = "short" }},
		{name: "other operation ticket", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.Ticket.OperationID = "operation-other" }},
		{name: "other project ticket", mutate: func(value *ProjectUnregisterApprovalPreparation) { value.Ticket.LeaseKey.ProjectID = "project-other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparation := validControlApprovalPreparation()
			test.mutate(&preparation)
			if err := preparation.Validate(); err == nil {
				t.Fatal("ProjectUnregisterApprovalPreparation.Validate() error = nil")
			}
		})
	}
}

// TestProjectUnregisterApprovalConfirmationValidation requires a succeeded unregister result at a bounded revision.
func TestProjectUnregisterApprovalConfirmationValidation(t *testing.T) {
	confirmation := validControlApprovalConfirmation(t)
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("ProjectUnregisterApprovalConfirmation.Validate() error = %v", err)
	}

	wrongKind := confirmation
	wrongKind.Operation.Kind = "project.start"
	if err := wrongKind.Validate(); err == nil {
		t.Fatal("wrong-kind ProjectUnregisterApprovalConfirmation.Validate() error = nil")
	}
	running := confirmation
	running.Operation.State = domain.OperationRunning
	running.Operation.FinishedAt = nil
	if err := running.Validate(); err == nil {
		t.Fatal("running ProjectUnregisterApprovalConfirmation.Validate() error = nil")
	}
	zeroRevision := confirmation
	zeroRevision.Revision = 0
	if err := zeroRevision.Validate(); err == nil {
		t.Fatal("zero-revision ProjectUnregisterApprovalConfirmation.Validate() error = nil")
	}
}

// validControlApprovalTicket returns one structurally valid short-lived release capability.
func validControlApprovalTicket() HelperApprovalTicket {
	return HelperApprovalTicket{
		OperationID: "operation-remove",
		LeaseKey: HelperApprovalLeaseKey{
			ProjectID: "project-orders",
		},
		Reference: helper.TicketReference(strings.Repeat("a", 64)),
		Operation: helper.OperationReleaseLoopbackIdentity,
		Address:   "127.77.0.10",
		ExpiresAt: time.Date(2026, time.July, 19, 12, 1, 0, 0, time.UTC),
	}
}

// validControlApprovalPreparation returns one coherent partially released operation.
func validControlApprovalPreparation() ProjectUnregisterApprovalPreparation {
	ticket := validControlApprovalTicket()
	return ProjectUnregisterApprovalPreparation{
		OperationID:       ticket.OperationID,
		OperationRevision: 41,
		ProjectID:         ticket.LeaseKey.ProjectID,
		TotalLeases:       2,
		ReleasedLeases:    1,
		PendingLeases:     1,
		Ticket:            &ticket,
	}
}

// validControlApprovalConfirmation returns one durable succeeded unregister result.
func validControlApprovalConfirmation(t *testing.T) ProjectUnregisterApprovalConfirmation {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 19, 11, 55, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		"operation-remove",
		"intent-remove",
		domain.OperationKindProjectUnregister,
		"project-orders",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "releasing network", requestedAt.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	operation, err = operation.Transition(domain.OperationSucceeded, "project unregistered", requestedAt.Add(2*time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(succeeded) error = %v", err)
	}
	return ProjectUnregisterApprovalConfirmation{Operation: operation, Revision: 43}
}
