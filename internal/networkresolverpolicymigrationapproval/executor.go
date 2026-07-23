// Package networkresolverpolicymigrationapproval coordinates interactive legacy resolver retirement approval.
package networkresolverpolicymigrationapproval

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

// ErrInconsistentResponse indicates that daemon or launcher progress crossed the selected migration boundary.
var ErrInconsistentResponse = errors.New("network resolver policy migration approval response is inconsistent")

// State identifies the client-safe conclusion of one interactive legacy resolver retirement attempt.
type State string

const (
	// Succeeded means the daemon durably confirmed legacy resolver retirement after independently validating helper evidence.
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

// Request selects one exact resolver policy migration approval revision.
type Request struct {
	// OperationID is the daemon-owned migration operation awaiting approval.
	OperationID domain.OperationID
	// ExpectedOperationRevision prevents approval from crossing a concurrent operation transition.
	ExpectedOperationRevision domain.Sequence
}

// Validate reports whether the request selects one exact migration approval revision.
func (request Request) Validate() error {
	return control.PrepareNetworkResolverPolicyMigrationApprovalRequest{
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
	// Confirmation is populated only after the daemon durably completes the migration operation.
	Confirmation *control.NetworkResolverPolicyMigrationApprovalConfirmation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// Client exposes only the daemon calls required by interactive resolver policy migration approval.
type Client interface {
	// PrepareNetworkResolverPolicyMigrationApproval returns one caller-bound retirement capability for the selected revision.
	PrepareNetworkResolverPolicyMigrationApproval(
		context.Context,
		control.PrepareNetworkResolverPolicyMigrationApprovalRequest,
	) (control.NetworkResolverPolicyMigrationApprovalPreparation, error)
	// ConfirmNetworkResolverPolicyMigrationApproval independently verifies retirement evidence before durably completing the operation.
	ConfirmNetworkResolverPolicyMigrationApproval(
		context.Context,
		control.ConfirmNetworkResolverPolicyMigrationApprovalRequest,
	) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error)
}

// HelperLauncher performs one synchronous native-consent and one-shot resolver helper exchange.
type HelperLauncher interface {
	// InvokeResolver launches only the immutable opaque resolver capability represented by ticket.
	InvokeResolver(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error)
}

// Executor owns the bounded client-side resolver policy migration approval state machine.
type Executor struct {
	client   Client
	launcher HelperLauncher
}

// New constructs an executor from one authenticated daemon client and one interactive helper launcher.
func New(client Client, helperLauncher HelperLauncher) *Executor {
	if client == nil || helperLauncher == nil {
		panic("networkresolverpolicymigrationapproval.New requires non-nil dependencies")
	}
	return &Executor{
		client:   client,
		launcher: helperLauncher,
	}
}

// Execute performs at most one retirement helper launch and confirms only its exact validated evidence.
func (executor *Executor) Execute(ctx context.Context, request Request) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := request.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("validate network resolver policy migration approval request: %w", err)
	}

	preparation, err := executor.prepare(ctx, request)
	if err != nil {
		return Outcome{}, err
	}
	if preparation.PublicationDisposition == control.NetworkResolverPolicyMigrationPublicationIndeterminate {
		return indeterminateOutcome(), nil
	}
	launchTicket, err := launcher.NewResolverLaunchTicket(
		preparation.Ticket.OperationID,
		preparation.Ticket.Reference,
		preparation.Ticket.Operation,
		preparation.Ticket.PolicyFingerprint,
		preparation.Ticket.PostOwnershipFingerprint,
		preparation.Ticket.ExpiresAt,
	)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: convert helper resolver launch ticket: %w", ErrInconsistentResponse, err)
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}

	launched, launchErr := executor.launcher.InvokeResolver(ctx, launchTicket)
	if launchErr != nil {
		return indeterminateOutcome(), fmt.Errorf("launch network resolver policy migration approval helper: %w", launchErr)
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
		evidence := *launched.Response.Result.ResolverEvidence
		confirmationRequest := control.ConfirmNetworkResolverPolicyMigrationApprovalRequest{
			OperationID:               request.OperationID,
			ExpectedOperationRevision: request.ExpectedOperationRevision,
			ResolverEvidence:          evidence,
		}
		if err := confirmationRequest.Validate(); err != nil {
			return indeterminateOutcome(), fmt.Errorf("%w: validate helper resolver evidence: %w", ErrInconsistentResponse, err)
		}
		confirmation, err := executor.confirm(ctx, request, confirmationRequest)
		if err != nil {
			return indeterminateOutcome(), err
		}
		return Outcome{
			State:        Succeeded,
			Confirmation: &confirmation,
		}, nil
	default:
		return indeterminateOutcome(), fmt.Errorf("%w: launcher state %q is unsupported", ErrInconsistentResponse, launched.State)
	}
}

// prepare obtains and independently validates one exact daemon retirement capability.
func (executor *Executor) prepare(ctx context.Context, request Request) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
	preparation, err := executor.client.PrepareNetworkResolverPolicyMigrationApproval(ctx, control.PrepareNetworkResolverPolicyMigrationApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	})
	if err != nil {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, fmt.Errorf("prepare network resolver policy migration approval: %w", err)
	}
	if err := preparation.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, fmt.Errorf("%w: validate preparation: %w", ErrInconsistentResponse, err)
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, fmt.Errorf("%w: preparation crossed the requested operation revision", ErrInconsistentResponse)
	}
	return preparation, nil
}

// confirm completes only the exact migration operation selected before native consent.
func (executor *Executor) confirm(ctx context.Context, request Request, confirmationRequest control.ConfirmNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	confirmation, err := executor.client.ConfirmNetworkResolverPolicyMigrationApproval(ctx, confirmationRequest)
	if err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, fmt.Errorf("confirm network resolver policy migration approval: %w", err)
	}
	if err := confirmation.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, fmt.Errorf("%w: validate confirmation: %w", ErrInconsistentResponse, err)
	}
	if confirmation.Operation.ID != request.OperationID ||
		confirmation.NetworkRevision != request.ExpectedOperationRevision+2 ||
		confirmation.Revision != confirmation.NetworkRevision+1 {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, fmt.Errorf("%w: confirmation crossed the requested operation or revision", ErrInconsistentResponse)
	}
	return confirmation, nil
}

// validateLaunchOutcome rejects alternate launcher implementations that weaken retirement correlation or lifecycle classification.
func validateLaunchOutcome(outcome launcher.Outcome, ticket control.NetworkResolverPolicyMigrationApprovalTicket) error {
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
		if result.Operation != helper.OperationRetireResolver || result.Operation != ticket.Operation ||
			result.ResolverEvidence == nil ||
			result.ResolverEvidence.PolicyFingerprint != ticket.PolicyFingerprint ||
			result.ResolverEvidence.OwnershipFingerprint != ticket.PostOwnershipFingerprint ||
			result.ResolverEvidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
			return errors.New("launcher success differs from the prepared resolver retirement")
		}
		if result.Evidence != (helper.MutationEvidence{}) || result.PoolEvidence != nil {
			return errors.New("launcher resolver success contains unrelated helper evidence")
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

// indeterminateOutcome records that a helper effect or durable confirmation may have started without retaining capability data.
func indeterminateOutcome() Outcome {
	return Outcome{State: Indeterminate}
}
