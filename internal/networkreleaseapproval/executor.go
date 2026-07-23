// Package networkreleaseapproval coordinates one interactive network release checkpoint.
package networkreleaseapproval

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

// ErrInconsistentResponse indicates that a daemon or helper result crossed the selected release checkpoint.
var ErrInconsistentResponse = errors.New("network release approval response is inconsistent")

// State is the client-safe conclusion of one native approval attempt.
type State string

const (
	// Succeeded means the daemon independently accepted the exact helper evidence.
	Succeeded State = "succeeded"
	// Declined means native consent was dismissed before a helper process started.
	Declined State = "declined"
	// Unavailable means native consent could not be opened before a helper process started.
	Unavailable State = "unavailable"
	// HelperFailed means the helper returned one bounded failure.
	HelperFailed State = "helper_failed"
	// Indeterminate means the helper effect or confirmation could not be proved.
	Indeterminate State = "indeterminate"
)

// Request selects one exact durable release checkpoint.
type Request struct {
	// OperationID is the daemon-owned network release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences the approval to the retained checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// Phase identifies the helper-backed checkpoint to execute.
	Phase control.NetworkReleasePhase
}

// Validate reports whether request can select exactly one helper-backed checkpoint.
func (request Request) Validate() error {
	if err := (control.PrepareNetworkReleaseApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	}).Validate(); err != nil {
		return err
	}
	switch request.Phase {
	case control.NetworkReleasePhaseLowPorts, control.NetworkReleasePhaseResolver, control.NetworkReleasePhaseTrust, control.NetworkReleasePhaseLoopbacks:
		return nil
	default:
		return fmt.Errorf("network release phase %q does not require helper approval", request.Phase)
	}
}

// HelperFailure is the bounded non-secret problem from an authenticated helper response.
type HelperFailure struct {
	// Code is the stable helper protocol failure classification.
	Code helper.ErrorCode
	// Message is the bounded helper failure message.
	Message string
}

// Outcome contains no ticket reference or capability contents.
type Outcome struct {
	// State is the safest conclusion supported by daemon and native process evidence.
	State State
	// Operation is populated only after confirmation advances the selected operation.
	Operation *control.NetworkReleaseOperation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// Client exposes the release approval calls required by Executor.
type Client interface {
	PrepareNetworkReleaseApproval(context.Context, control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error)
	ConfirmNetworkReleaseApproval(context.Context, control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseResolverApproval(context.Context, control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error)
	ConfirmNetworkReleaseResolverApproval(context.Context, control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseTrustApproval(context.Context, control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error)
	ConfirmNetworkReleaseTrustApproval(context.Context, control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseLoopbackApproval(context.Context, control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error)
	ConfirmNetworkReleaseLoopbackApproval(context.Context, control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error)
}

// HelperLauncher launches the immutable helper capabilities used by release checkpoints.
type HelperLauncher interface {
	InvokeLowPorts(context.Context, launcher.LowPortLaunchTicket) (launcher.Outcome, error)
	InvokeResolver(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error)
	InvokeTrust(context.Context, launcher.TrustLaunchTicket) (launcher.Outcome, error)
	InvokePool(context.Context, launcher.PoolLaunchTicket) (launcher.Outcome, error)
}

// Executor owns the bounded client-side release approval state machine.
type Executor struct {
	client   Client
	launcher HelperLauncher
}

// New constructs an executor from an authenticated daemon client and interactive helper launcher.
func New(client Client, helperLauncher HelperLauncher) *Executor {
	if client == nil || helperLauncher == nil {
		panic("networkreleaseapproval.New requires non-nil dependencies")
	}
	return &Executor{
		client:   client,
		launcher: helperLauncher,
	}
}

// Execute performs one helper launch and confirms only the selected release checkpoint.
func (executor *Executor) Execute(ctx context.Context, request Request) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := request.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("validate network release approval request: %w", err)
	}
	switch request.Phase {
	case control.NetworkReleasePhaseLowPorts:
		return executor.executeLowPorts(ctx, request)
	case control.NetworkReleasePhaseResolver:
		return executor.executeResolver(ctx, request)
	case control.NetworkReleasePhaseTrust:
		return executor.executeTrust(ctx, request)
	case control.NetworkReleasePhaseLoopbacks:
		return executor.executeLoopbacks(ctx, request)
	default:
		return Outcome{}, fmt.Errorf("%w: unsupported phase %q", ErrInconsistentResponse, request.Phase)
	}
}

// executeLowPorts redeems one low-port release ticket.
func (executor *Executor) executeLowPorts(ctx context.Context, request Request) (Outcome, error) {
	p, err := executor.client.PrepareNetworkReleaseApproval(ctx, control.PrepareNetworkReleaseApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("prepare network release low-port approval: %w", err)
	}
	if err := p.Validate(); err != nil || p.OperationID != request.OperationID || p.CheckpointRevision != request.ExpectedCheckpointRevision {
		return Outcome{}, inconsistent("low-port preparation", err)
	}
	t, err := launcher.NewLowPortLaunchTicket(p.Ticket.OperationID, p.Ticket.Reference, p.Ticket.Operation, p.Ticket.PolicyFingerprint, p.Ticket.TargetOwnershipFingerprint, p.Ticket.ObservationFingerprint, p.Ticket.ExpiresAt)
	if err != nil {
		return Outcome{}, inconsistent("convert low-port ticket", err)
	}
	launched, err := executor.launcher.InvokeLowPorts(ctx, t)
	if err != nil {
		return indeterminate(), fmt.Errorf("launch network release low-port helper: %w", err)
	}
	if err := validateLaunch(launched, func(r *helper.OperationResult) bool {
		return r != nil && r.Operation == helper.OperationReleaseLowPorts && r.LowPortEvidence != nil && r.LowPortEvidence.PolicyFingerprint == p.Ticket.PolicyFingerprint && r.LowPortEvidence.OwnershipFingerprint == p.Ticket.TargetOwnershipFingerprint && r.LowPortEvidence.Postcondition == helper.LowPortPostconditionOwnedAbsent
	}); err != nil {
		return indeterminate(), inconsistent("low-port helper result", err)
	}
	if outcome, done := terminal(launched); done {
		return outcome, nil
	}
	e := *launched.Response.Result.LowPortEvidence
	confirmed, err := executor.client.ConfirmNetworkReleaseApproval(ctx, control.ConfirmNetworkReleaseApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		LowPortEvidence:            e,
	})
	return confirmedOutcome(request, control.NetworkReleasePhaseResolver, confirmed, err)
}

// executeResolver redeems one resolver release ticket.
func (executor *Executor) executeResolver(ctx context.Context, request Request) (Outcome, error) {
	p, err := executor.client.PrepareNetworkReleaseResolverApproval(ctx, control.PrepareNetworkReleaseResolverApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("prepare network release resolver approval: %w", err)
	}
	if err := p.Validate(); err != nil || p.OperationID != request.OperationID || p.CheckpointRevision != request.ExpectedCheckpointRevision {
		return Outcome{}, inconsistent("resolver preparation", err)
	}
	if p.PublicationDisposition == control.NetworkReleaseResolverPublicationIndeterminate {
		return indeterminate(), nil
	}
	t, err := launcher.NewResolverLaunchTicket(p.Ticket.OperationID, p.Ticket.Reference, p.Ticket.Operation, p.Ticket.PolicyFingerprint, p.Ticket.TargetOwnershipFingerprint, p.Ticket.ExpiresAt)
	if err != nil {
		return Outcome{}, inconsistent("convert resolver ticket", err)
	}
	launched, err := executor.launcher.InvokeResolver(ctx, t)
	if err != nil {
		return indeterminate(), fmt.Errorf("launch network release resolver helper: %w", err)
	}
	if err := validateLaunch(launched, func(r *helper.OperationResult) bool {
		return r != nil && r.Operation == helper.OperationReleaseResolver && r.ResolverEvidence != nil && r.ResolverEvidence.PolicyFingerprint == p.Ticket.PolicyFingerprint && r.ResolverEvidence.OwnershipFingerprint == p.Ticket.TargetOwnershipFingerprint
	}); err != nil {
		return indeterminate(), inconsistent("resolver helper result", err)
	}
	if outcome, done := terminal(launched); done {
		return outcome, nil
	}
	e := *launched.Response.Result.ResolverEvidence
	confirmed, err := executor.client.ConfirmNetworkReleaseResolverApproval(ctx, control.ConfirmNetworkReleaseResolverApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		ResolverEvidence:           e,
	})
	return confirmedOutcome(request, control.NetworkReleasePhaseTrust, confirmed, err)
}

// executeTrust confirms preservation without a helper or redeems one trust release ticket.
func (executor *Executor) executeTrust(ctx context.Context, request Request) (Outcome, error) {
	p, err := executor.client.PrepareNetworkReleaseTrustApproval(ctx, control.PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("prepare network release trust approval: %w", err)
	}
	if err := p.Validate(); err != nil || p.OperationID != request.OperationID || p.CheckpointRevision != request.ExpectedCheckpointRevision {
		return Outcome{}, inconsistent("trust preparation", err)
	}
	if p.PublicationDisposition == control.NetworkReleaseTrustPublicationIndeterminate {
		return indeterminate(), nil
	}
	confirmation := control.ConfirmNetworkReleaseTrustApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	}
	if p.Disposition == control.NetworkReleaseTrustOwned {
		ticket := *p.Ticket
		t, err := launcher.NewTrustLaunchTicket(ticket.OperationID, ticket.Reference, ticket.Operation, ticket.PolicyFingerprint, ticket.TargetOwnershipFingerprint, ticket.AuthorityFingerprint, string(ticket.Mechanism), ticket.ExpiresAt)
		if err != nil {
			return Outcome{}, inconsistent("convert trust ticket", err)
		}
		launched, err := executor.launcher.InvokeTrust(ctx, t)
		if err != nil {
			return indeterminate(), fmt.Errorf("launch network release trust helper: %w", err)
		}
		if err := validateLaunch(launched, func(r *helper.OperationResult) bool {
			return r != nil && r.Operation == helper.OperationReleaseTrust && r.TrustEvidence != nil && r.TrustEvidence.AuthorityFingerprint == ticket.AuthorityFingerprint && r.TrustEvidence.Postcondition == helper.TrustPostconditionOwnedAbsent
		}); err != nil {
			return indeterminate(), inconsistent("trust helper result", err)
		}
		if outcome, done := terminal(launched); done {
			return outcome, nil
		}
		e := *launched.Response.Result.TrustEvidence
		confirmation.TrustEvidence = &e
	}
	confirmed, err := executor.client.ConfirmNetworkReleaseTrustApproval(ctx, confirmation)
	return confirmedOutcome(request, control.NetworkReleasePhaseLoopbacks, confirmed, err)
}

// executeLoopbacks redeems one aggregate loopback-pool release ticket.
func (executor *Executor) executeLoopbacks(ctx context.Context, request Request) (Outcome, error) {
	p, err := executor.client.PrepareNetworkReleaseLoopbackApproval(ctx, control.PrepareNetworkReleaseLoopbackApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("prepare network release loopback approval: %w", err)
	}
	if err := p.Validate(); err != nil || p.OperationID != request.OperationID || p.CheckpointRevision != request.ExpectedCheckpointRevision {
		return Outcome{}, inconsistent("loopback preparation", err)
	}
	if p.PublicationDisposition == control.NetworkReleaseLoopbackPublicationIndeterminate {
		return indeterminate(), nil
	}
	t, err := launcher.NewPoolLaunchTicket(p.Ticket.OperationID, p.Ticket.Reference, p.Ticket.Operation, p.Ticket.Pool, p.Ticket.ExpiresAt)
	if err != nil {
		return Outcome{}, inconsistent("convert loopback ticket", err)
	}
	launched, err := executor.launcher.InvokePool(ctx, t)
	if err != nil {
		return indeterminate(), fmt.Errorf("launch network release loopback helper: %w", err)
	}
	if err := validateLaunch(launched, func(r *helper.OperationResult) bool {
		return r != nil && r.Operation == helper.OperationReleaseLoopbackPool && r.PoolEvidence != nil && r.PoolEvidence.Pool == p.Ticket.Pool
	}); err != nil {
		return indeterminate(), inconsistent("loopback helper result", err)
	}
	if outcome, done := terminal(launched); done {
		return outcome, nil
	}
	e := *launched.Response.Result.PoolEvidence
	confirmed, err := executor.client.ConfirmNetworkReleaseLoopbackApproval(ctx, control.ConfirmNetworkReleaseLoopbackApprovalRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		LoopbackEvidence:           e,
	})
	return confirmedOutcome(request, control.NetworkReleasePhaseOwnership, confirmed, err)
}

// confirmedOutcome validates the durable advance after one helper-backed checkpoint.
func confirmedOutcome(request Request, phase control.NetworkReleasePhase, operation control.NetworkReleaseOperation, err error) (Outcome, error) {
	if err != nil {
		return indeterminate(), fmt.Errorf("confirm network release %s approval: %w", request.Phase, err)
	}
	if validationErr := operation.Validate(); validationErr != nil || operation.Operation.ID != request.OperationID || operation.CheckpointRevision <= request.ExpectedCheckpointRevision || operation.Phase != phase {
		return indeterminate(), inconsistent("confirmation", validationErr)
	}
	return Outcome{
		State:     Succeeded,
		Operation: &operation,
	}, nil
}

// validateLaunch ensures only one authenticated helper lifecycle conclusion reaches confirmation.
func validateLaunch(outcome launcher.Outcome, match func(*helper.OperationResult) bool) error {
	switch outcome.State {
	case launcher.Succeeded:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return err
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeSucceeded || !outcome.Response.OK || outcome.Response.Result == nil || outcome.Response.Error != nil || !match(outcome.Response.Result) {
			return errors.New("success does not contain the prepared helper effect")
		}
	case launcher.HelperFailed:
		if err := helper.WriteResponse(io.Discard, outcome.Response); err != nil {
			return err
		}
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeHelperFailed || outcome.Response.OK || outcome.Response.Result != nil || outcome.Response.Error == nil || outcome.Response.Error.Code == "" || outcome.Response.Error.Message == "" {
			return errors.New("failure does not contain one bounded helper rejection")
		}
	case launcher.Declined, launcher.Unavailable:
		if outcome.Exit != nil || outcome.Response != (helper.Response{}) {
			return errors.New("no-child result contains helper evidence")
		}
	case launcher.Indeterminate:
		if outcome.Response != (helper.Response{}) {
			return errors.New("indeterminate result contains helper evidence")
		}
	default:
		return fmt.Errorf("unsupported launcher state %q", outcome.State)
	}
	return nil
}

// terminal maps helper conclusions that cannot safely reach daemon confirmation.
func terminal(launched launcher.Outcome) (Outcome, bool) {
	switch launched.State {
	case launcher.Declined:
		return Outcome{State: Declined}, true
	case launcher.Unavailable:
		return Outcome{State: Unavailable}, true
	case launcher.HelperFailed:
		return Outcome{
			State: HelperFailed,
			HelperFailure: &HelperFailure{
				Code:    launched.Response.Error.Code,
				Message: launched.Response.Error.Message,
			},
		}, true
	case launcher.Indeterminate:
		return indeterminate(), true
	}
	return Outcome{}, false
}

// indeterminate records uncertainty without retaining helper capability data.
func indeterminate() Outcome { return Outcome{State: Indeterminate} }

// inconsistent preserves the sentinel while retaining structural validation detail.
func inconsistent(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s crossed the selected checkpoint", ErrInconsistentResponse, stage)
	}
	return fmt.Errorf("%w: %s: %w", ErrInconsistentResponse, stage, err)
}
