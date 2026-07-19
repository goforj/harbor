// Package networksetupapproval coordinates one interactive network setup approval attempt.
package networksetupapproval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
)

var (
	// ErrInconsistentResponse indicates that daemon or launcher progress crossed the selected operation boundary.
	ErrInconsistentResponse = errors.New("network setup approval response is inconsistent")
)

// State identifies the client-safe conclusion of one interactive network setup approval attempt.
type State string

const (
	// Succeeded means the daemon confirmed the durable network setup after independently validating pool evidence.
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

// Request selects one exact network setup operation revision for the complete interactive workflow.
type Request struct {
	// OperationID is the daemon-owned network setup operation awaiting approval.
	OperationID domain.OperationID
	// ExpectedOperationRevision prevents approval from crossing a concurrent operation transition.
	ExpectedOperationRevision domain.Sequence
}

// Validate reports whether the request selects one exact approval revision.
func (request Request) Validate() error {
	return control.PrepareNetworkSetupApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	}.Validate()
}

// HelperFailure is the bounded non-secret problem returned by an authenticated helper response.
type HelperFailure struct {
	// Code is the stable helper protocol failure classification.
	Code helper.ErrorCode
	// Message is the bounded client-safe helper failure message.
	Message string
}

// Outcome contains no ticket reference or signed capability contents.
type Outcome struct {
	// State is the safest conclusion supported by daemon and native process evidence.
	State State
	// Confirmation is populated only after the daemon completes the durable network setup operation.
	Confirmation *control.NetworkSetupApprovalConfirmation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// Client exposes only the daemon calls required by interactive network setup approval.
type Client interface {
	// PrepareNetworkSetupApproval returns one caller-bound helper capability for the selected revision.
	PrepareNetworkSetupApproval(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	// ConfirmNetworkSetupApproval independently verifies the helper evidence before completing the operation.
	ConfirmNetworkSetupApproval(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
}

// HelperLauncher performs one synchronous native-consent and one-shot aggregate helper exchange.
type HelperLauncher interface {
	// InvokePool launches only the immutable opaque aggregate capability represented by ticket.
	InvokePool(context.Context, launcher.PoolLaunchTicket) (launcher.Outcome, error)
}

// Executor owns the bounded client-side network setup approval state machine.
type Executor struct {
	client   Client
	launcher HelperLauncher
}

// New constructs an executor from one authenticated daemon client and one interactive helper launcher.
func New(client Client, helperLauncher HelperLauncher) *Executor {
	if requiredInterfaceIsNil(client) {
		panic("networksetupapproval.New requires a non-nil client")
	}
	if requiredInterfaceIsNil(helperLauncher) {
		panic("networksetupapproval.New requires a non-nil helper launcher")
	}
	return &Executor{client: client, launcher: helperLauncher}
}

// Execute performs at most one aggregate helper launch and confirms only its exact validated pool evidence.
func (executor *Executor) Execute(ctx context.Context, request Request) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := request.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("validate network setup approval request: %w", err)
	}

	preparation, err := executor.prepare(ctx, request)
	if err != nil {
		return Outcome{}, err
	}
	launchTicket, err := launcher.NewPoolLaunchTicket(
		preparation.Ticket.OperationID,
		preparation.Ticket.Reference,
		preparation.Ticket.Operation,
		preparation.Ticket.Pool,
		preparation.Ticket.ExpiresAt,
	)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: convert helper pool launch ticket: %w", ErrInconsistentResponse, err)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}

	launched, launchErr := executor.launcher.InvokePool(ctx, launchTicket)
	if launchErr != nil {
		return indeterminateOutcome(), fmt.Errorf("launch network setup approval helper: %w", launchErr)
	}
	if err := validateLaunchOutcome(launched, preparation.Ticket); err != nil {
		return indeterminateOutcome(), fmt.Errorf("%w: %w", ErrInconsistentResponse, err)
	}

	switch launched.State {
	case launcher.Declined:
		return Outcome{State: Declined}, nil
	case launcher.Unavailable:
		return Outcome{State: Unavailable}, nil
	case launcher.HelperFailed:
		return Outcome{
			State: HelperFailed,
			HelperFailure: &HelperFailure{
				Code:    launched.Response.Error.Code,
				Message: launched.Response.Error.Message,
			},
		}, nil
	case launcher.Indeterminate:
		return indeterminateOutcome(), nil
	case launcher.Succeeded:
		if err := ctx.Err(); err != nil {
			return indeterminateOutcome(), err
		}
		evidence := clonePoolEvidence(*launched.Response.Result.PoolEvidence)
		confirmationRequest := control.ConfirmNetworkSetupApprovalRequest{
			OperationID:               request.OperationID,
			ExpectedOperationRevision: request.ExpectedOperationRevision,
			PoolEvidence:              evidence,
		}
		if err := confirmationRequest.Validate(); err != nil {
			return indeterminateOutcome(), fmt.Errorf("%w: validate helper pool evidence: %w", ErrInconsistentResponse, err)
		}

		confirmation, err := executor.confirm(ctx, request, preparation.Ticket.Pool, confirmationRequest)
		if err != nil {
			return indeterminateOutcome(), err
		}
		return Outcome{State: Succeeded, Confirmation: &confirmation}, nil
	default:
		return indeterminateOutcome(), fmt.Errorf("%w: launcher state %q is unsupported", ErrInconsistentResponse, launched.State)
	}
}

// prepare obtains and independently validates one exact daemon approval capability.
func (executor *Executor) prepare(ctx context.Context, request Request) (control.NetworkSetupApprovalPreparation, error) {
	preparation, err := executor.client.PrepareNetworkSetupApproval(ctx, control.PrepareNetworkSetupApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	})
	if err != nil {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("prepare network setup approval: %w", err)
	}
	if err := preparation.Validate(); err != nil {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("%w: validate preparation: %w", ErrInconsistentResponse, err)
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("%w: preparation crossed the requested operation revision", ErrInconsistentResponse)
	}
	return preparation, nil
}

// confirm completes only the exact operation and pool selected before native consent.
func (executor *Executor) confirm(
	ctx context.Context,
	request Request,
	pool string,
	confirmationRequest control.ConfirmNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalConfirmation, error) {
	confirmation, err := executor.client.ConfirmNetworkSetupApproval(ctx, confirmationRequest)
	if err != nil {
		return control.NetworkSetupApprovalConfirmation{}, fmt.Errorf("confirm network setup approval: %w", err)
	}
	if err := confirmation.Validate(); err != nil {
		return control.NetworkSetupApprovalConfirmation{}, fmt.Errorf("%w: validate confirmation: %w", ErrInconsistentResponse, err)
	}
	if confirmation.Operation.ID != request.OperationID || confirmation.Pool != pool ||
		confirmation.NetworkRevision != request.ExpectedOperationRevision+2 ||
		confirmation.Revision != request.ExpectedOperationRevision+3 {
		return control.NetworkSetupApprovalConfirmation{}, fmt.Errorf("%w: confirmation crossed the requested operation, revision, or pool", ErrInconsistentResponse)
	}
	return confirmation, nil
}

// validateLaunchOutcome rejects alternate launcher implementations that weaken correlation or lifecycle classification.
func validateLaunchOutcome(outcome launcher.Outcome, ticket control.NetworkSetupApprovalTicket) error {
	switch outcome.State {
	case launcher.Succeeded:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return fmt.Errorf("launcher success response is invalid: %w", err)
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeSucceeded ||
			outcome.Response.Version != helper.ProtocolVersion || !outcome.Response.OK ||
			outcome.Response.Result == nil || outcome.Response.Error != nil {
			return errors.New("launcher success does not contain one successful helper exchange")
		}
		result := outcome.Response.Result
		if result.Operation != helper.OperationEnsureLoopbackPool || result.Operation != ticket.Operation ||
			result.PoolEvidence == nil || result.PoolEvidence.Pool != ticket.Pool {
			return errors.New("launcher success differs from the prepared helper pool effect")
		}
		if result.Evidence != (helper.MutationEvidence{}) {
			return errors.New("launcher pool success contains scalar helper evidence")
		}
		return nil
	case launcher.HelperFailed:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return fmt.Errorf("launcher failure response is invalid: %w", err)
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeHelperFailed ||
			outcome.Response.Version != helper.ProtocolVersion || outcome.Response.OK ||
			outcome.Response.Result != nil || outcome.Response.Error == nil ||
			outcome.Response.Error.Code == "" || outcome.Response.Error.Message == "" {
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

// clonePoolEvidence prevents launcher-owned response memory from crossing into the confirmation call.
func clonePoolEvidence(evidence helper.PoolMutationEvidence) helper.PoolMutationEvidence {
	cloned := evidence
	cloned.Identities = append([]helper.MutationEvidence(nil), evidence.Identities...)
	return cloned
}

// indeterminateOutcome records that a helper effect or confirmation may have started without retaining capability data.
func indeterminateOutcome() Outcome {
	return Outcome{State: Indeterminate}
}

// requiredInterfaceIsNil rejects typed-nil dependencies before an approval workflow can reach them.
func requiredInterfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
