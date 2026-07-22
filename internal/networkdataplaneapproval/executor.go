// Package networkdataplaneapproval coordinates interactive trusted-ingress setup approvals.
package networkdataplaneapproval

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
)

var (
	// ErrInconsistentResponse indicates daemon or launcher progress crossed the selected operation boundary.
	ErrInconsistentResponse = errors.New("network data-plane approval response is inconsistent")
)

// State identifies the client-safe conclusion of one interactive approval attempt.
type State string

const (
	// Succeeded means the daemon independently confirmed the exact helper postcondition.
	Succeeded State = "succeeded"
	// Declined means native consent was dismissed before a helper process started.
	Declined State = "declined"
	// Unavailable means native consent could not be opened before a helper process started.
	Unavailable State = "unavailable"
	// HelperFailed means the helper returned one trusted bounded failure response.
	HelperFailed State = "helper_failed"
	// Indeterminate means a helper effect or durable confirmation could not be proved complete.
	Indeterminate State = "indeterminate"
)

// Request selects one exact data-plane setup operation revision for an approval phase.
type Request struct {
	// OperationID is the daemon-owned data-plane setup operation awaiting approval.
	OperationID domain.OperationID
	// ExpectedOperationRevision prevents approval from crossing a concurrent operation transition.
	ExpectedOperationRevision domain.Sequence
}

// Validate reports whether the request selects one exact approval revision.
func (request Request) Validate() error {
	return control.PrepareNetworkDataPlaneTrustApprovalRequest{
		OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision,
	}.Validate()
}

// HelperFailure is the bounded non-secret problem returned by an authenticated helper response.
type HelperFailure struct {
	// Code is the stable helper protocol failure classification.
	Code helper.ErrorCode
	// Message is the bounded client-safe helper failure message.
	Message string
}

// TrustOutcome contains no ticket reference or signed capability contents.
type TrustOutcome struct {
	// State is the safest conclusion supported by daemon and native process evidence.
	State State
	// Setup is populated only after the same operation requires low-port approval.
	Setup *control.NetworkDataPlaneSetupOperation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// LowPortOutcome contains no ticket reference or signed capability contents.
type LowPortOutcome struct {
	// State is the safest conclusion supported by daemon and native process evidence.
	State State
	// Confirmation is populated only after the daemon completes the full setup operation.
	Confirmation *control.NetworkDataPlaneSetupConfirmation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// Client exposes only the daemon calls required by the two interactive approval phases.
type Client interface {
	// PrepareNetworkDataPlaneTrustApproval returns one caller-bound trust helper capability.
	PrepareNetworkDataPlaneTrustApproval(context.Context, control.PrepareNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneTrustApprovalPreparation, error)
	// ConfirmNetworkDataPlaneTrustApproval independently verifies trust evidence and advances to low-port approval.
	ConfirmNetworkDataPlaneTrustApproval(context.Context, control.ConfirmNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneSetupOperation, error)
	// PrepareNetworkDataPlaneLowPortApproval returns one caller-bound low-port helper capability.
	PrepareNetworkDataPlaneLowPortApproval(context.Context, control.PrepareNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneLowPortApprovalPreparation, error)
	// ConfirmNetworkDataPlaneLowPortApproval independently verifies low-port evidence and completes setup.
	ConfirmNetworkDataPlaneLowPortApproval(context.Context, control.ConfirmNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneSetupConfirmation, error)
}

// HelperLauncher performs one synchronous native-consent and one-shot helper exchange for each phase.
type HelperLauncher interface {
	// InvokeTrust launches only the immutable opaque trust capability represented by ticket.
	InvokeTrust(context.Context, launcher.TrustLaunchTicket) (launcher.Outcome, error)
	// InvokeLowPorts launches only the immutable opaque low-port capability represented by ticket.
	InvokeLowPorts(context.Context, launcher.LowPortLaunchTicket) (launcher.Outcome, error)
}

// Executor owns the bounded client-side data-plane approval state machines.
type Executor struct {
	client   Client
	launcher HelperLauncher
}

// New constructs an executor from one authenticated daemon client and one interactive helper launcher.
func New(client Client, helperLauncher HelperLauncher) *Executor {
	return &Executor{client: client, launcher: helperLauncher}
}

// ExecuteTrust performs at most one trust helper launch and advances only the exact operation to low-port approval.
func (executor *Executor) ExecuteTrust(ctx context.Context, request Request) (TrustOutcome, error) {
	ctx, err := validateRequest(ctx, request)
	if err != nil {
		return TrustOutcome{}, err
	}
	preparation, err := executor.client.PrepareNetworkDataPlaneTrustApproval(ctx, control.PrepareNetworkDataPlaneTrustApprovalRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision})
	if err != nil {
		return TrustOutcome{}, fmt.Errorf("prepare network data-plane trust approval: %w", err)
	}
	if err := validateTrustPreparation(request, preparation); err != nil {
		return TrustOutcome{}, err
	}
	ticket, err := launcher.NewTrustLaunchTicket(preparation.Ticket.OperationID, preparation.Ticket.Reference, preparation.Ticket.Operation, preparation.Ticket.PolicyFingerprint, preparation.Ticket.TargetOwnershipFingerprint, preparation.Ticket.AuthorityFingerprint, string(preparation.Ticket.Mechanism), preparation.Ticket.ExpiresAt)
	if err != nil {
		return TrustOutcome{}, fmt.Errorf("%w: convert helper trust launch ticket: %w", ErrInconsistentResponse, err)
	}
	if err := ctx.Err(); err != nil {
		return TrustOutcome{}, err
	}
	launched, err := executor.launcher.InvokeTrust(ctx, ticket)
	if err != nil {
		return indeterminateTrust(), fmt.Errorf("launch network data-plane trust helper: %w", err)
	}
	if err := validateTrustLaunchOutcome(launched, preparation.Ticket); err != nil {
		return indeterminateTrust(), fmt.Errorf("%w: %w", ErrInconsistentResponse, err)
	}
	if outcome, done := terminalTrustOutcome(launched); done {
		return outcome, nil
	}
	if err := ctx.Err(); err != nil {
		return indeterminateTrust(), err
	}
	evidence := *launched.Response.Result.TrustEvidence
	confirmationRequest := control.ConfirmNetworkDataPlaneTrustApprovalRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, TrustEvidence: evidence}
	if err := confirmationRequest.Validate(); err != nil {
		return indeterminateTrust(), fmt.Errorf("%w: validate helper trust evidence: %w", ErrInconsistentResponse, err)
	}
	setup, err := executor.client.ConfirmNetworkDataPlaneTrustApproval(ctx, confirmationRequest)
	if err != nil {
		return indeterminateTrust(), fmt.Errorf("confirm network data-plane trust approval: %w", err)
	}
	if err := validateTrustConfirmation(request, setup); err != nil {
		return indeterminateTrust(), err
	}
	return TrustOutcome{State: Succeeded, Setup: &setup}, nil
}

// ExecuteLowPorts performs at most one low-port helper launch and completes only the exact setup operation.
func (executor *Executor) ExecuteLowPorts(ctx context.Context, request Request) (LowPortOutcome, error) {
	ctx, err := validateRequest(ctx, request)
	if err != nil {
		return LowPortOutcome{}, err
	}
	preparation, err := executor.client.PrepareNetworkDataPlaneLowPortApproval(ctx, control.PrepareNetworkDataPlaneLowPortApprovalRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision})
	if err != nil {
		return LowPortOutcome{}, fmt.Errorf("prepare network data-plane low-port approval: %w", err)
	}
	if err := validateLowPortPreparation(request, preparation); err != nil {
		return LowPortOutcome{}, err
	}
	ticket, err := launcher.NewLowPortLaunchTicket(preparation.Ticket.OperationID, preparation.Ticket.Reference, preparation.Ticket.Operation, preparation.Ticket.PolicyFingerprint, preparation.Ticket.TargetOwnershipFingerprint, preparation.Ticket.ObservationFingerprint, preparation.Ticket.ExpiresAt)
	if err != nil {
		return LowPortOutcome{}, fmt.Errorf("%w: convert helper low-port launch ticket: %w", ErrInconsistentResponse, err)
	}
	if err := ctx.Err(); err != nil {
		return LowPortOutcome{}, err
	}
	launched, err := executor.launcher.InvokeLowPorts(ctx, ticket)
	if err != nil {
		return indeterminateLowPort(), fmt.Errorf("launch network data-plane low-port helper: %w", err)
	}
	if err := validateLowPortLaunchOutcome(launched, preparation.Ticket); err != nil {
		return indeterminateLowPort(), fmt.Errorf("%w: %w", ErrInconsistentResponse, err)
	}
	if outcome, done := terminalLowPortOutcome(launched); done {
		return outcome, nil
	}
	if err := ctx.Err(); err != nil {
		return indeterminateLowPort(), err
	}
	evidence := *launched.Response.Result.LowPortEvidence
	confirmationRequest := control.ConfirmNetworkDataPlaneLowPortApprovalRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, LowPortEvidence: evidence}
	if err := confirmationRequest.Validate(); err != nil {
		return indeterminateLowPort(), fmt.Errorf("%w: validate helper low-port evidence: %w", ErrInconsistentResponse, err)
	}
	confirmation, err := executor.client.ConfirmNetworkDataPlaneLowPortApproval(ctx, confirmationRequest)
	if err != nil {
		return indeterminateLowPort(), fmt.Errorf("confirm network data-plane low-port approval: %w", err)
	}
	if err := validateLowPortConfirmation(request, confirmation); err != nil {
		return indeterminateLowPort(), err
	}
	return LowPortOutcome{State: Succeeded, Confirmation: &confirmation}, nil
}

// validateRequest normalizes a caller context and validates the operation selection before any daemon call.
func validateRequest(ctx context.Context, request Request) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	if err := request.Validate(); err != nil {
		return ctx, fmt.Errorf("validate network data-plane approval request: %w", err)
	}
	return ctx, nil
}

// validateTrustPreparation ensures the daemon capability cannot cross the selected revision.
func validateTrustPreparation(request Request, preparation control.NetworkDataPlaneTrustApprovalPreparation) error {
	if err := preparation.Validate(); err != nil {
		return fmt.Errorf("%w: validate trust preparation: %w", ErrInconsistentResponse, err)
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return fmt.Errorf("%w: trust preparation crossed the requested operation revision", ErrInconsistentResponse)
	}
	return nil
}

// validateLowPortPreparation ensures the daemon capability cannot cross the selected revision.
func validateLowPortPreparation(request Request, preparation control.NetworkDataPlaneLowPortApprovalPreparation) error {
	if err := preparation.Validate(); err != nil {
		return fmt.Errorf("%w: validate low-port preparation: %w", ErrInconsistentResponse, err)
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return fmt.Errorf("%w: low-port preparation crossed the requested operation revision", ErrInconsistentResponse)
	}
	return nil
}

// validateTrustConfirmation accepts only a monotonic transition of the same operation to the next approval phase.
func validateTrustConfirmation(request Request, setup control.NetworkDataPlaneSetupOperation) error {
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("%w: validate trust confirmation: %w", ErrInconsistentResponse, err)
	}
	if setup.Operation.ID != request.OperationID || setup.Revision <= request.ExpectedOperationRevision || !control.RequiresNetworkDataPlaneLowPortApproval(setup) {
		return fmt.Errorf("%w: trust confirmation did not advance the selected operation to low-port approval", ErrInconsistentResponse)
	}
	return nil
}

// validateLowPortConfirmation accepts only a monotonic terminal transition of the same operation.
func validateLowPortConfirmation(request Request, confirmation control.NetworkDataPlaneSetupConfirmation) error {
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("%w: validate low-port confirmation: %w", ErrInconsistentResponse, err)
	}
	if confirmation.Operation.ID != request.OperationID || confirmation.Revision <= request.ExpectedOperationRevision {
		return fmt.Errorf("%w: low-port confirmation crossed the selected operation revision", ErrInconsistentResponse)
	}
	return nil
}

// validateTrustLaunchOutcome rejects launcher evidence unrelated to the prepared trust effect.
func validateTrustLaunchOutcome(outcome launcher.Outcome, ticket control.NetworkDataPlaneTrustApprovalTicket) error {
	return validateLaunchOutcome(outcome, func(result *helper.OperationResult) bool {
		return result != nil &&
			result.Operation == helper.OperationEnsureTrust &&
			result.Operation == ticket.Operation &&
			result.TrustEvidence != nil &&
			result.TrustEvidence.AuthorityFingerprint == ticket.AuthorityFingerprint &&
			result.TrustEvidence.Mechanism == ticket.Mechanism &&
			(result.TrustEvidence.Postcondition == helper.TrustPostconditionExact ||
				result.TrustEvidence.Postcondition == helper.TrustPostconditionPreexisting)
	})
}

// validateLowPortLaunchOutcome rejects launcher evidence unrelated to the prepared low-port effect.
func validateLowPortLaunchOutcome(outcome launcher.Outcome, ticket control.NetworkDataPlaneLowPortApprovalTicket) error {
	return validateLaunchOutcome(outcome, func(result *helper.OperationResult) bool {
		return result != nil && result.Operation == helper.OperationEnsureLowPorts && result.Operation == ticket.Operation && result.LowPortEvidence != nil && result.LowPortEvidence.PolicyFingerprint == ticket.PolicyFingerprint && result.LowPortEvidence.OwnershipFingerprint == ticket.TargetOwnershipFingerprint && result.LowPortEvidence.Postcondition == helper.LowPortPostconditionExact
	})
}

// validateLaunchOutcome ensures only trusted lifecycle states expose helper evidence.
func validateLaunchOutcome(outcome launcher.Outcome, match func(*helper.OperationResult) bool) error {
	switch outcome.State {
	case launcher.Succeeded:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return fmt.Errorf("launcher success response is invalid: %w", err)
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeSucceeded || outcome.Response.Version != helper.ProtocolVersion || !outcome.Response.OK || outcome.Response.Result == nil || outcome.Response.Error != nil || !match(outcome.Response.Result) {
			return errors.New("launcher success does not contain the prepared helper effect")
		}
		return nil
	case launcher.HelperFailed:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return fmt.Errorf("launcher failure response is invalid: %w", err)
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeHelperFailed || outcome.Response.Version != helper.ProtocolVersion || outcome.Response.OK || outcome.Response.Result != nil || outcome.Response.Error == nil || outcome.Response.Error.Code == "" || outcome.Response.Error.Message == "" {
			return errors.New("launcher failure does not contain one bounded helper rejection")
		}
		return nil
	case launcher.Declined, launcher.Unavailable:
		if outcome.Exit != nil || outcome.Response != (helper.Response{}) {
			return errors.New("launcher no-child outcome contains helper process evidence")
		}
		return nil
	case launcher.Indeterminate:
		if outcome.Response != (helper.Response{}) {
			return errors.New("launcher indeterminate outcome contains a trusted helper response")
		}
		return nil
	default:
		return fmt.Errorf("launcher state %q is unsupported", outcome.State)
	}
}

// terminalTrustOutcome maps states that cannot safely reach a trust confirmation.
func terminalTrustOutcome(launched launcher.Outcome) (TrustOutcome, bool) {
	switch launched.State {
	case launcher.Declined:
		return TrustOutcome{State: Declined}, true
	case launcher.Unavailable:
		return TrustOutcome{State: Unavailable}, true
	case launcher.HelperFailed:
		return TrustOutcome{State: HelperFailed, HelperFailure: &HelperFailure{Code: launched.Response.Error.Code, Message: launched.Response.Error.Message}}, true
	case launcher.Indeterminate:
		return indeterminateTrust(), true
	}
	return TrustOutcome{}, false
}

// terminalLowPortOutcome maps states that cannot safely reach a low-port confirmation.
func terminalLowPortOutcome(launched launcher.Outcome) (LowPortOutcome, bool) {
	switch launched.State {
	case launcher.Declined:
		return LowPortOutcome{State: Declined}, true
	case launcher.Unavailable:
		return LowPortOutcome{State: Unavailable}, true
	case launcher.HelperFailed:
		return LowPortOutcome{State: HelperFailed, HelperFailure: &HelperFailure{Code: launched.Response.Error.Code, Message: launched.Response.Error.Message}}, true
	case launcher.Indeterminate:
		return indeterminateLowPort(), true
	}
	return LowPortOutcome{}, false
}

// indeterminateTrust records uncertainty without retaining capability data.
func indeterminateTrust() TrustOutcome { return TrustOutcome{State: Indeterminate} }

// indeterminateLowPort records uncertainty without retaining capability data.
func indeterminateLowPort() LowPortOutcome { return LowPortOutcome{State: Indeterminate} }
