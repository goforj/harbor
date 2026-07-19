package projectapproval

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/network/identity"
)

var (
	// ErrInconsistentResponse indicates that daemon or launcher progress crossed the selected operation boundary.
	ErrInconsistentResponse = errors.New("project approval response is inconsistent")
	// ErrNoProgress indicates that a reported helper success was not confirmed by daemon observation.
	ErrNoProgress = errors.New("project approval made no authoritative progress")
	// ErrLaunchLimit indicates that the daemon requested more helper effects than the stable lease total permits.
	ErrLaunchLimit = errors.New("project approval exceeded its helper launch limit")
)

// State identifies the client-safe conclusion of one complete interactive approval attempt.
type State string

const (
	// Succeeded means the daemon confirmed the durable project unregister after observing every lease absent.
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

// Request selects one exact project-unregister operation revision for the complete interactive workflow.
type Request struct {
	// OperationID is the daemon-owned unregister operation awaiting approval.
	OperationID domain.OperationID
	// ExpectedOperationRevision prevents approval from crossing a concurrent operation transition.
	ExpectedOperationRevision domain.Sequence
}

// Validate reports whether the request selects one exact approval revision.
func (request Request) Validate() error {
	return control.PrepareProjectUnregisterApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	}.Validate()
}

// Progress is the non-secret daemon-observed release summary returned to interactive clients.
type Progress struct {
	// ProjectID identifies the unregistering project without exposing a helper capability.
	ProjectID domain.ProjectID
	// TotalLeases is the stable bound on helper effects for this workflow.
	TotalLeases int
	// ReleasedLeases is the number of assignments the daemon independently observed absent.
	ReleasedLeases int
	// PendingLeases is the number of assignments still requiring authoritative observation.
	PendingLeases int
}

// HelperFailure is the bounded non-secret problem returned by an authenticated helper response.
type HelperFailure struct {
	// Code is the stable helper protocol failure classification.
	Code helper.ErrorCode
	// Message is the bounded client-safe helper failure message.
	Message string
}

// Outcome contains no ticket reference, lease identity, address, or signed capability contents.
type Outcome struct {
	// State is the safest conclusion supported by daemon observation and native process evidence.
	State State
	// Progress is the latest valid daemon-observed release summary.
	Progress Progress
	// Confirmation is populated only after the daemon completes the durable unregister operation.
	Confirmation *control.ProjectUnregisterApprovalConfirmation
	// HelperFailure is populated only for HelperFailed.
	HelperFailure *HelperFailure
}

// Client exposes only the daemon calls required by interactive project-unregister approval.
type Client interface {
	// PrepareProjectUnregisterApproval returns current progress and at most one caller-bound helper capability.
	PrepareProjectUnregisterApproval(context.Context, control.PrepareProjectUnregisterApprovalRequest) (control.ProjectUnregisterApprovalPreparation, error)
	// ConfirmProjectUnregisterApproval independently verifies every effect before completing the unregister operation.
	ConfirmProjectUnregisterApproval(context.Context, control.ConfirmProjectUnregisterApprovalRequest) (control.ProjectUnregisterApprovalConfirmation, error)
}

// HelperLauncher performs one synchronous native-consent and one-shot-helper exchange.
type HelperLauncher interface {
	// Invoke launches only the immutable opaque capability represented by ticket.
	Invoke(context.Context, launcher.LaunchTicket) (launcher.Outcome, error)
}

// Executor owns the bounded client-side approval state machine.
type Executor struct {
	client   Client
	launcher HelperLauncher
}

// New constructs an executor from one authenticated daemon client and one interactive helper launcher.
func New(client Client, helperLauncher HelperLauncher) *Executor {
	if requiredInterfaceIsNil(client) {
		panic("projectapproval.New requires a non-nil client")
	}
	if requiredInterfaceIsNil(helperLauncher) {
		panic("projectapproval.New requires a non-nil helper launcher")
	}
	return &Executor{client: client, launcher: helperLauncher}
}

// Execute releases at most one prepared lease at a time and trusts only subsequent daemon observation as progress.
func (executor *Executor) Execute(ctx context.Context, request Request) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := request.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("validate project approval request: %w", err)
	}

	current, err := executor.prepare(ctx, request)
	if err != nil {
		return Outcome{}, err
	}
	projectID := current.ProjectID
	totalLeases := current.TotalLeases
	seenKeys := make(map[identity.LeaseKey]struct{}, totalLeases)
	seenAddresses := make(map[string]struct{}, totalLeases)
	launches := 0

	for current.PendingLeases > 0 {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		if launches >= totalLeases {
			return indeterminateOutcome(current), ErrLaunchLimit
		}

		launchTicket, effect, err := convertTicket(*current.Ticket)
		if err != nil {
			return Outcome{}, fmt.Errorf("%w: convert helper launch ticket: %w", ErrInconsistentResponse, err)
		}
		if _, duplicate := seenKeys[effect.key]; duplicate {
			return indeterminateOutcome(current), fmt.Errorf("%w: daemon repeated a previously launched lease", ErrInconsistentResponse)
		}
		if _, duplicate := seenAddresses[effect.address]; duplicate {
			return indeterminateOutcome(current), fmt.Errorf("%w: daemon repeated a previously launched address", ErrInconsistentResponse)
		}
		seenKeys[effect.key] = struct{}{}
		seenAddresses[effect.address] = struct{}{}
		launches++

		launched, launchErr := executor.launcher.Invoke(ctx, launchTicket)
		if launchErr != nil {
			return indeterminateOutcome(current), fmt.Errorf("launch project approval helper: %w", launchErr)
		}
		if err := validateLaunchOutcome(launched, *current.Ticket); err != nil {
			return indeterminateOutcome(current), fmt.Errorf("%w: %w", ErrInconsistentResponse, err)
		}

		switch launched.State {
		case launcher.Declined:
			return outcomeFor(Declined, current), nil
		case launcher.Unavailable:
			return outcomeFor(Unavailable, current), nil
		case launcher.HelperFailed:
			failure := HelperFailure{
				Code:    launched.Response.Error.Code,
				Message: launched.Response.Error.Message,
			}
			outcome := outcomeFor(HelperFailed, current)
			outcome.HelperFailure = &failure
			return outcome, nil
		case launcher.Succeeded, launcher.Indeterminate:
			next, prepareErr := executor.prepare(ctx, request)
			if prepareErr != nil {
				return indeterminateOutcome(current), fmt.Errorf("observe project approval after helper launch: %w", prepareErr)
			}
			progressed, effectProven, transitionErr := validateProgressTransition(current, next, effect, projectID, totalLeases)
			if transitionErr != nil {
				return indeterminateOutcome(current), transitionErr
			}
			if !progressed || !effectProven {
				if launched.State == launcher.Indeterminate {
					return indeterminateOutcome(next), nil
				}
				return indeterminateOutcome(next), ErrNoProgress
			}
			current = next
		}
	}

	confirmation, err := executor.confirm(ctx, request, projectID)
	if err != nil {
		return indeterminateOutcome(current), err
	}
	outcome := outcomeFor(Succeeded, current)
	outcome.Confirmation = &confirmation
	return outcome, nil
}

// prepare obtains and independently validates one exact daemon progress snapshot.
func (executor *Executor) prepare(ctx context.Context, request Request) (control.ProjectUnregisterApprovalPreparation, error) {
	preparation, err := executor.client.PrepareProjectUnregisterApproval(ctx, control.PrepareProjectUnregisterApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	})
	if err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, fmt.Errorf("prepare project approval: %w", err)
	}
	if err := preparation.Validate(); err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, fmt.Errorf("%w: validate preparation: %w", ErrInconsistentResponse, err)
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return control.ProjectUnregisterApprovalPreparation{}, fmt.Errorf("%w: preparation crossed the requested operation revision", ErrInconsistentResponse)
	}
	return preparation, nil
}

// confirm completes only the exact operation and project whose pending count reached zero.
func (executor *Executor) confirm(
	ctx context.Context,
	request Request,
	projectID domain.ProjectID,
) (control.ProjectUnregisterApprovalConfirmation, error) {
	confirmation, err := executor.client.ConfirmProjectUnregisterApproval(ctx, control.ConfirmProjectUnregisterApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	})
	if err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, fmt.Errorf("confirm project approval: %w", err)
	}
	if err := confirmation.Validate(); err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, fmt.Errorf("%w: validate confirmation: %w", ErrInconsistentResponse, err)
	}
	if confirmation.Operation.ID != request.OperationID || confirmation.Operation.ProjectID != projectID {
		return control.ProjectUnregisterApprovalConfirmation{}, fmt.Errorf("%w: confirmation crossed the requested operation or project", ErrInconsistentResponse)
	}
	return confirmation, nil
}

// launchEffect identifies the non-secret durable host effect without retaining its opaque capability reference.
type launchEffect struct {
	key     identity.LeaseKey
	address string
}

// convertTicket explicitly narrows the validated control DTO into immutable launcher-owned metadata.
func convertTicket(ticket control.HelperApprovalTicket) (launcher.LaunchTicket, launchEffect, error) {
	key := identity.LeaseKey{
		ProjectID:   ticket.LeaseKey.ProjectID,
		SecondaryID: ticket.LeaseKey.SecondaryID,
	}
	launchTicket, err := launcher.NewLaunchTicket(
		ticket.OperationID,
		key,
		ticket.Reference,
		ticket.Operation,
		ticket.Address,
		ticket.ExpiresAt,
	)
	if err != nil {
		return launcher.LaunchTicket{}, launchEffect{}, err
	}
	return launchTicket, launchEffect{key: key, address: ticket.Address}, nil
}

// validateProgressTransition requires stable authority, monotonic counts, and retirement of the launched lease.
func validateProgressTransition(
	current control.ProjectUnregisterApprovalPreparation,
	next control.ProjectUnregisterApprovalPreparation,
	launched launchEffect,
	projectID domain.ProjectID,
	totalLeases int,
) (bool, bool, error) {
	if next.ProjectID != projectID || next.TotalLeases != totalLeases {
		return false, false, fmt.Errorf("%w: project or total lease count changed", ErrInconsistentResponse)
	}
	if next.ReleasedLeases < current.ReleasedLeases || next.PendingLeases > current.PendingLeases {
		return false, false, fmt.Errorf("%w: daemon release progress regressed", ErrInconsistentResponse)
	}
	progressed := next.ReleasedLeases > current.ReleasedLeases
	if next.PendingLeases == 0 {
		return progressed, progressed, nil
	}

	nextEffect := launchEffect{
		key: identity.LeaseKey{
			ProjectID:   next.Ticket.LeaseKey.ProjectID,
			SecondaryID: next.Ticket.LeaseKey.SecondaryID,
		},
		address: next.Ticket.Address,
	}
	sameKey := nextEffect.key == launched.key
	sameAddress := nextEffect.address == launched.address
	if sameKey != sameAddress {
		return false, false, fmt.Errorf("%w: a pending lease changed its address binding", ErrInconsistentResponse)
	}
	if !progressed && !sameKey {
		return false, false, fmt.Errorf("%w: pending lease changed without release progress", ErrInconsistentResponse)
	}
	return progressed, !sameKey, nil
}

// validateLaunchOutcome rejects alternate launcher implementations that weaken correlation or lifecycle classification.
func validateLaunchOutcome(outcome launcher.Outcome, ticket control.HelperApprovalTicket) error {
	switch outcome.State {
	case launcher.Succeeded:
		if outcome.Exit == nil || outcome.Exit.Code != launcher.ExitCodeSucceeded ||
			outcome.Response.Version != helper.ProtocolVersion || !outcome.Response.OK ||
			outcome.Response.Result == nil || outcome.Response.Error != nil {
			return errors.New("launcher success does not contain one successful helper exchange")
		}
		if outcome.Response.Result.Operation != ticket.Operation || outcome.Response.Result.Evidence.Address != ticket.Address {
			return errors.New("launcher success differs from the prepared helper effect")
		}
		return nil
	case launcher.HelperFailed:
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
		return nil
	default:
		return fmt.Errorf("launcher state %q is unsupported", outcome.State)
	}
}

// outcomeFor strips helper capability metadata while preserving the latest daemon progress.
func outcomeFor(state State, preparation control.ProjectUnregisterApprovalPreparation) Outcome {
	return Outcome{State: state, Progress: progressFor(preparation)}
}

// indeterminateOutcome records the latest authoritative counts without implying that a host effect completed.
func indeterminateOutcome(preparation control.ProjectUnregisterApprovalPreparation) Outcome {
	return outcomeFor(Indeterminate, preparation)
}

// progressFor copies only non-secret aggregate progress from a preparation response.
func progressFor(preparation control.ProjectUnregisterApprovalPreparation) Progress {
	return Progress{
		ProjectID:      preparation.ProjectID,
		TotalLeases:    preparation.TotalLeases,
		ReleasedLeases: preparation.ReleasedLeases,
		PendingLeases:  preparation.PendingLeases,
	}
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
