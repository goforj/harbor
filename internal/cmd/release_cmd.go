package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/networkreleaseapproval"
)

// networkReleaseIntentID keeps every CLI replay correlated to one global release operation.
const networkReleaseIntentID domain.IntentID = "intent-network-release"

// NetworkReleaseApprovalRunner executes one exact helper-backed release checkpoint.
type NetworkReleaseApprovalRunner interface {
	// Execute prepares, launches, and confirms only the selected durable checkpoint.
	Execute(context.Context, networkreleaseapproval.Request) (networkreleaseapproval.Outcome, error)
}

// ReleaseCmd drives client-side approval checkpoints of a durable machine-global network release.
type ReleaseCmd struct {
	client   *DaemonClient
	approval NetworkReleaseApprovalRunner
	output   io.Writer
}

// NewReleaseCmd creates a release command without connecting to the daemon or opening native consent.
func NewReleaseCmd(client *DaemonClient, approval NetworkReleaseApprovalRunner) *ReleaseCmd {
	if client == nil || approval == nil {
		panic("cmd.NewReleaseCmd requires non-nil dependencies")
	}
	return &ReleaseCmd{
		client:   client,
		approval: approval,
		output:   os.Stdout,
	}
}

// Signature defines CLI metadata for machine-global network release.
func (*ReleaseCmd) Signature() string {
	return `name:"release" help:"Release Harbor's machine-global network resources"`
}

// Run starts or replays the stable release intent and completes only helper-backed durable checkpoints.
func (command *ReleaseCmd) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operation, err := command.client.StartNetworkRelease(ctx, control.StartNetworkReleaseRequest{IntentID: networkReleaseIntentID})
	if err != nil {
		return fmt.Errorf("start network release: %w", err)
	}
	if err := validateNetworkReleaseOperation(operation, networkReleaseIntentID); err != nil {
		return err
	}
	for attempt := 0; attempt < 10; attempt++ {
		if operation.Operation.State == domain.OperationSucceeded {
			if operation.Phase != control.NetworkReleasePhaseProjection {
				return fmt.Errorf("network release succeeded in unsupported phase %q", operation.Phase)
			}
			_, err := fmt.Fprintln(command.output, "Network release complete.")
			return err
		}
		switch operation.Phase {
		case control.NetworkReleasePhaseLowPorts, control.NetworkReleasePhaseResolver, control.NetworkReleasePhaseTrust, control.NetworkReleasePhaseLoopbacks:
			outcome, executeErr := command.approval.Execute(ctx, networkreleaseapproval.Request{
				OperationID:                operation.Operation.ID,
				ExpectedCheckpointRevision: operation.CheckpointRevision,
				Phase:                      operation.Phase,
			})
			if executeErr != nil {
				return fmt.Errorf("approve network release %s: %w", operation.Phase, executeErr)
			}
			if outcome.State != networkreleaseapproval.Succeeded || outcome.Operation == nil || outcome.HelperFailure != nil {
				return networkReleaseApprovalOutcomeError{outcome: outcome}
			}
			operation = *outcome.Operation
			if err := validateNetworkReleaseOperation(operation, networkReleaseIntentID); err != nil {
				return err
			}
		case control.NetworkReleasePhaseOwnership:
			next, confirmErr := command.client.ConfirmNetworkReleaseOwnership(ctx, control.ConfirmNetworkReleaseOwnershipRequest{
				OperationID:                operation.Operation.ID,
				ExpectedCheckpointRevision: operation.CheckpointRevision,
			})
			if confirmErr != nil {
				return fmt.Errorf("confirm network release ownership: %w", confirmErr)
			}
			if err := validateNetworkReleaseOperation(next, networkReleaseIntentID); err != nil {
				return err
			}
			if next.Operation.State != domain.OperationSucceeded ||
				next.CheckpointRevision != operation.CheckpointRevision ||
				next.Phase != control.NetworkReleasePhaseProjection {
				return errors.New("network release ownership confirmation did not complete the terminal release")
			}
			operation = next
		case control.NetworkReleasePhaseRuntimeRelease, control.NetworkReleasePhaseVerifyEffects, control.NetworkReleasePhaseProjection:
			next, startErr := command.client.StartNetworkRelease(ctx, control.StartNetworkReleaseRequest{IntentID: networkReleaseIntentID})
			if startErr != nil {
				return fmt.Errorf("resume network release: %w", startErr)
			}
			if err := validateNetworkReleaseOperation(next, networkReleaseIntentID); err != nil {
				return err
			}
			if next.Operation.ID != operation.Operation.ID {
				return fmt.Errorf("network release resume returned operation %q, expected %q", next.Operation.ID, operation.Operation.ID)
			}
			if next.Revision <= operation.Revision {
				return fmt.Errorf("network release remains at %q; daemon coordinator has not advanced the durable operation", operation.Phase)
			}
			operation = next
		default:
			return fmt.Errorf("network release has unsupported phase %q", operation.Phase)
		}
	}
	return errors.New("network release exceeded the bounded client approval state machine")
}

// validateNetworkReleaseOperation ensures every replay response remains on the stable user intent.
func validateNetworkReleaseOperation(operation control.NetworkReleaseOperation, intentID domain.IntentID) error {
	if err := operation.Validate(); err != nil {
		return fmt.Errorf("validate network release response: %w", err)
	}
	if operation.Operation.IntentID != intentID {
		return fmt.Errorf("network release response belongs to intent %q, expected %q", operation.Operation.IntentID, intentID)
	}
	return nil
}

// networkReleaseApprovalOutcomeError explains one bounded native approval conclusion.
type networkReleaseApprovalOutcomeError struct {
	outcome networkreleaseapproval.Outcome
}

// Error retains retry guidance without exposing helper capabilities.
func (failure networkReleaseApprovalOutcomeError) Error() string {
	switch failure.outcome.State {
	case networkreleaseapproval.Declined:
		return "network release approval was declined; rerun harbor release to replay the same durable operation"
	case networkreleaseapproval.Unavailable:
		return "network release approval is unavailable; rerun harbor release to replay the same durable operation"
	case networkreleaseapproval.HelperFailed:
		if failure.outcome.HelperFailure == nil {
			return "network release helper failed without a bounded failure description"
		}
		return fmt.Sprintf("network release helper failed (%s): %s", failure.outcome.HelperFailure.Code, failure.outcome.HelperFailure.Message)
	case networkreleaseapproval.Indeterminate:
		return "network release approval is indeterminate; rerun harbor release to read the durable operation"
	default:
		return fmt.Sprintf("network release approval returned unsupported state %q", failure.outcome.State)
	}
}
