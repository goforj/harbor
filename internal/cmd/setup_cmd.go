package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/networksetupapproval"
)

// networkSetupIntentID keeps every first-run invocation on the singleton setup operation across cancellation and retry.
const networkSetupIntentID domain.IntentID = "intent-network-setup"

// NetworkSetupApprovalRunner performs one bounded interactive approval attempt for an exact setup revision.
type NetworkSetupApprovalRunner interface {
	// Execute prepares, launches, and confirms only the selected network setup revision.
	Execute(context.Context, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// SetupCmd completes the first-run network foundation through the unprivileged Harbor daemon client.
type SetupCmd struct {
	client   *DaemonClient
	approval NetworkSetupApprovalRunner
	output   io.Writer
}

// NewSetupCmd creates a setup command without opening a daemon connection or acquiring native process authority.
func NewSetupCmd(client *DaemonClient, approval NetworkSetupApprovalRunner) *SetupCmd {
	if client == nil {
		panic("cmd.NewSetupCmd requires a non-nil daemon client")
	}
	if requiredSetupDependencyIsNil(approval) {
		panic("cmd.NewSetupCmd requires a non-nil network setup approval runner")
	}
	return &SetupCmd{client: client, approval: approval, output: os.Stdout}
}

// Signature defines CLI metadata for first-run network setup.
func (*SetupCmd) Signature() string {
	return `name:"setup" help:"Configure Harbor's first-run network foundation"`
}

// Run starts or replays setup and opens interactive approval only at the daemon-selected revision.
func (command *SetupCmd) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	setup, err := command.client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: networkSetupIntentID})
	if err != nil {
		return fmt.Errorf("start network setup: %w", err)
	}
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("start network setup: validate response: %w", err)
	}
	if setup.Operation.IntentID != networkSetupIntentID {
		return fmt.Errorf("start network setup: response belongs to intent %q, expected %q", setup.Operation.IntentID, networkSetupIntentID)
	}
	switch setup.Operation.State {
	case domain.OperationSucceeded:
		return writeCompletedNetworkSetup(command.output, setup)
	case domain.OperationRequiresApproval:
		return command.approve(ctx, setup)
	default:
		return networkSetupOperationStateError{operation: setup.Operation}
	}
}

// approve delegates the exact optimistic revision without constructing or launching a privileged command.
func (command *SetupCmd) approve(ctx context.Context, setup control.NetworkSetupOperation) error {
	outcome, err := command.approval.Execute(ctx, networksetupapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	})
	if err != nil {
		return fmt.Errorf("approve network setup: %w", err)
	}
	if outcome.State != networksetupapproval.Succeeded {
		return networkSetupApprovalOutcomeError{outcome: outcome}
	}
	if outcome.Confirmation == nil {
		return fmt.Errorf("approve network setup: succeeded outcome is missing confirmation")
	}
	if outcome.HelperFailure != nil {
		return fmt.Errorf("approve network setup: succeeded outcome contains a helper failure")
	}
	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("approve network setup: validate confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.NetworkRevision != setup.Revision+2 ||
		confirmation.Revision != setup.Revision+3 {
		return fmt.Errorf("approve network setup: confirmation crossed the selected operation revision")
	}

	return writeConfirmedNetworkSetup(command.output, confirmation)
}

// networkSetupOperationStateError reports durable daemon progress that cannot open a new approval attempt.
type networkSetupOperationStateError struct {
	operation domain.Operation
}

// Error identifies the authoritative operation and state without claiming setup completed.
func (failure networkSetupOperationStateError) Error() string {
	if failure.operation.Problem != nil {
		return fmt.Sprintf(
			"network setup operation %s is %s: %s",
			failure.operation.ID,
			failure.operation.State,
			failure.operation.Problem.Message,
		)
	}
	return fmt.Sprintf("network setup operation %s is %s", failure.operation.ID, failure.operation.State)
}

// networkSetupApprovalOutcomeError maps one safe non-success conclusion into a nonzero CLI result.
type networkSetupApprovalOutcomeError struct {
	outcome networksetupapproval.Outcome
}

// Error explains whether another interactive retry is safe or authoritative state must be re-read.
func (failure networkSetupApprovalOutcomeError) Error() string {
	switch failure.outcome.State {
	case networksetupapproval.Declined:
		return "network setup approval was declined; run harbor setup to try again"
	case networksetupapproval.Unavailable:
		return "network setup approval is unavailable; run harbor setup to try again"
	case networksetupapproval.HelperFailed:
		if failure.outcome.HelperFailure == nil {
			return "network setup helper failed without a bounded failure description"
		}
		return fmt.Sprintf(
			"network setup helper failed (%s): %s",
			failure.outcome.HelperFailure.Code,
			failure.outcome.HelperFailure.Message,
		)
	case networksetupapproval.Indeterminate:
		return "network setup approval is indeterminate; run harbor setup to read the authoritative state"
	default:
		return fmt.Sprintf("network setup approval returned unsupported state %q", failure.outcome.State)
	}
}

// writeCompletedNetworkSetup reports an idempotent replay without inventing pool details absent from the start response.
func writeCompletedNetworkSetup(output io.Writer, setup control.NetworkSetupOperation) error {
	_, err := fmt.Fprintf(
		output,
		"Network setup is already complete.\nOperation: %s\nState: %s\nRevision: %d\n",
		setup.Operation.ID,
		setup.Operation.State,
		setup.Revision,
	)
	return err
}

// writeConfirmedNetworkSetup reports only the pool and revisions independently confirmed by the daemon.
func writeConfirmedNetworkSetup(output io.Writer, confirmation control.NetworkSetupApprovalConfirmation) error {
	_, err := fmt.Fprintf(
		output,
		"Network setup complete.\nPool: %s\nOperation: %s\nState: %s\nNetwork revision: %d\nRevision: %d\n",
		confirmation.Pool,
		confirmation.Operation.ID,
		confirmation.Operation.State,
		confirmation.NetworkRevision,
		confirmation.Revision,
	)
	return err
}

// requiredSetupDependencyIsNil rejects typed-nil runners before the command can reach interactive consent.
func requiredSetupDependencyIsNil(value any) bool {
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
