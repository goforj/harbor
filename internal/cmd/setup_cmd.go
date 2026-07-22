package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/networksetupapproval"
)

// networkSetupIntentID keeps every first-run invocation on the singleton setup operation across cancellation and retry.
const networkSetupIntentID domain.IntentID = "intent-network-setup"

// networkResolverSetupIntentID keeps resolver setup replay-safe until a terminal retry needs a fresh identity.
const networkResolverSetupIntentID domain.IntentID = "intent-network-resolver-setup"

// networkDataPlaneSetupIntentID keeps trusted-ingress setup replay-safe until a terminal retry needs a fresh identity.
const networkDataPlaneSetupIntentID domain.IntentID = "intent-network-data-plane-setup"

// networkSetupNeedsFreshIntent recognizes only validated terminal states that explicitly permit a new intent.
func networkSetupNeedsFreshIntent(operation domain.Operation) bool {
	return operation.State == domain.OperationCancelled || operation.State == domain.OperationFailed && operation.Problem != nil && operation.Problem.Retryable
}

// newNetworkSetupRetryIntent creates a bounded independent intent after a terminal safe retry.
func newNetworkSetupRetryIntent(prefix domain.IntentID) (domain.IntentID, error) {
	return newNetworkSetupRetryIntentFrom(rand.Reader, prefix)
}

// newNetworkSetupRetryIntentFrom keeps retry entropy failures observable without weakening production identities.
func newNetworkSetupRetryIntentFrom(reader io.Reader, prefix domain.IntentID) (domain.IntentID, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", err
	}
	intent := domain.IntentID(string(prefix) + "-" + hex.EncodeToString(random))
	return intent, intent.Validate()
}

// NetworkSetupApprovalRunner performs one bounded interactive approval attempt for an exact setup revision.
type NetworkSetupApprovalRunner interface {
	// Execute prepares, launches, and confirms only the selected network setup revision.
	Execute(context.Context, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// NetworkResolverSetupApprovalRunner performs one exact resolver approval attempt.
type NetworkResolverSetupApprovalRunner interface {
	// Execute prepares, launches, and confirms only the selected resolver setup revision.
	Execute(context.Context, networkresolverapproval.Request) (networkresolverapproval.Outcome, error)
}

// NetworkDataPlaneSetupApprovalRunner performs trust and low-port approvals for one exact operation.
type NetworkDataPlaneSetupApprovalRunner interface {
	// ExecuteTrust prepares, launches, and confirms only the selected trust setup revision.
	ExecuteTrust(context.Context, networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error)
	// ExecuteLowPorts prepares, launches, and confirms only the selected low-port setup revision.
	ExecuteLowPorts(context.Context, networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error)
}

// SetupCmd completes the first-run network foundation through the unprivileged Harbor daemon client.
type SetupCmd struct {
	client            *DaemonClient
	approval          NetworkSetupApprovalRunner
	resolverApproval  NetworkResolverSetupApprovalRunner
	dataPlaneApproval NetworkDataPlaneSetupApprovalRunner
	fullSetup         bool
	newRetryIntent    func(domain.IntentID) (domain.IntentID, error)
	output            io.Writer
}

// NewFullSetupCmd creates the production setup command which completes all network setup phases.
func NewFullSetupCmd(
	client *DaemonClient,
	approval NetworkSetupApprovalRunner,
	resolverApproval NetworkResolverSetupApprovalRunner,
	dataPlaneApproval NetworkDataPlaneSetupApprovalRunner,
) *SetupCmd {
	command := NewSetupCmd(client, approval)
	if requiredSetupDependencyIsNil(resolverApproval) || requiredSetupDependencyIsNil(dataPlaneApproval) {
		panic("cmd.NewFullSetupCmd requires non-nil approval runners")
	}
	command.resolverApproval = resolverApproval
	command.dataPlaneApproval = dataPlaneApproval
	command.fullSetup = true
	command.newRetryIntent = newNetworkSetupRetryIntent
	return command
}

// NewSetupCmd creates a setup command without opening a daemon connection or acquiring native process authority.
func NewSetupCmd(client *DaemonClient, approval NetworkSetupApprovalRunner) *SetupCmd {
	if client == nil {
		panic("cmd.NewSetupCmd requires a non-nil daemon client")
	}
	if requiredSetupDependencyIsNil(approval) {
		panic("cmd.NewSetupCmd requires a non-nil network setup approval runner")
	}
	return &SetupCmd{
		client:   client,
		approval: approval,
		output:   os.Stdout,
	}
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

	intentID := networkSetupIntentID
	setup, err := command.client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{
		IntentID: intentID,
	})
	if err == nil {
		err = validateNetworkSetupStart(setup, intentID)
	}
	if command.fullSetup && err == nil && networkSetupNeedsFreshIntent(setup.Operation) {
		intent, intentErr := command.newRetryIntent(networkSetupIntentID)
		if intentErr != nil {
			return fmt.Errorf("create network setup retry: %w", intentErr)
		}
		intentID = intent
		setup, err = command.client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{
			IntentID: intentID,
		})
		if err == nil {
			err = validateNetworkSetupStart(setup, intentID)
		}
	}
	if err != nil {
		return fmt.Errorf("start network setup: %w", err)
	}
	switch setup.Operation.State {
	case domain.OperationSucceeded:
		if !command.fullSetup {
			return writeCompletedNetworkSetup(command.output, setup)
		}
		if setup.Operation.Phase != "completed" {
			return fmt.Errorf("network setup succeeded in unsupported phase %q", setup.Operation.Phase)
		}
	case domain.OperationRequiresApproval:
		if err := command.approve(ctx, setup); err != nil {
			return err
		}
		if !command.fullSetup {
			return nil
		}
	default:
		return networkSetupOperationStateError{operation: setup.Operation}
	}
	if err := command.completeResolver(ctx); err != nil {
		return err
	}
	if err := command.completeDataPlane(ctx); err != nil {
		return err
	}
	_, err = fmt.Fprintln(command.output, "Network setup complete.")
	return err
}

// validateNetworkSetupStart rejects malformed or crossed pool setup responses before retry decisions.
func validateNetworkSetupStart(setup control.NetworkSetupOperation, intentID domain.IntentID) error {
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("validate response: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return fmt.Errorf("response belongs to intent %q, expected %q", setup.Operation.IntentID, intentID)
	}
	return nil
}

// completeResolver completes the resolver phase after loopback setup succeeds.
func (command *SetupCmd) completeResolver(ctx context.Context) error {
	intentID := networkResolverSetupIntentID
	setup, err := command.client.StartNetworkResolverSetup(ctx, control.StartNetworkResolverSetupRequest{
		IntentID: intentID,
	})
	if err == nil {
		err = validateNetworkResolverSetupStart(setup, intentID)
	}
	if err == nil && networkSetupNeedsFreshIntent(setup.Operation) {
		intent, intentErr := command.newRetryIntent(networkResolverSetupIntentID)
		if intentErr != nil {
			return fmt.Errorf("create network resolver setup retry: %w", intentErr)
		}
		intentID = intent
		setup, err = command.client.StartNetworkResolverSetup(ctx, control.StartNetworkResolverSetupRequest{
			IntentID: intentID,
		})
		if err == nil {
			err = validateNetworkResolverSetupStart(setup, intentID)
		}
	}
	if err != nil {
		return fmt.Errorf("start network resolver setup: %w", err)
	}
	if setup.Operation.State == domain.OperationSucceeded {
		if setup.Operation.Phase != "completed" {
			return fmt.Errorf("network resolver setup succeeded in unsupported phase %q", setup.Operation.Phase)
		}
		return nil
	}
	if setup.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("network resolver setup is %s", setup.Operation.State)
	}
	if setup.Operation.Phase != "awaiting resolver approval" {
		return fmt.Errorf("network resolver setup requires approval in unsupported phase %q", setup.Operation.Phase)
	}
	outcome, err := command.resolverApproval.Execute(ctx, networkresolverapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	})
	if err != nil {
		return fmt.Errorf("approve network resolver setup: %w", err)
	}
	if outcome.State != networkresolverapproval.Succeeded {
		return networkResolverSetupApprovalOutcomeError{outcome: outcome}
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return errors.New("approve network resolver setup: successful approval returned inconsistent evidence")
	}
	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("validate network resolver setup confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.Operation.Phase != "completed" ||
		confirmation.NetworkRevision <= setup.Revision ||
		confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("validate network resolver setup confirmation: result crossed the selected operation revision")
	}
	return nil
}

// validateNetworkResolverSetupStart rejects malformed or crossed resolver responses before retry decisions.
func validateNetworkResolverSetupStart(setup control.NetworkResolverSetupOperation, intentID domain.IntentID) error {
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("validate response: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return errors.New("result crossed the selected intent")
	}
	return nil
}

// completeDataPlane completes trust and low-port approvals and accepts success only at completed.
func (command *SetupCmd) completeDataPlane(ctx context.Context) error {
	intentID := networkDataPlaneSetupIntentID
	setup, err := command.client.StartNetworkDataPlaneSetup(ctx, control.StartNetworkDataPlaneSetupRequest{
		IntentID: intentID,
	})
	if err == nil {
		err = validateNetworkDataPlaneSetupStart(setup, intentID)
	}
	if err == nil && networkSetupNeedsFreshIntent(setup.Operation) {
		intent, intentErr := command.newRetryIntent(networkDataPlaneSetupIntentID)
		if intentErr != nil {
			return fmt.Errorf("create network data-plane setup retry: %w", intentErr)
		}
		intentID = intent
		setup, err = command.client.StartNetworkDataPlaneSetup(ctx, control.StartNetworkDataPlaneSetupRequest{
			IntentID: intentID,
		})
		if err == nil {
			err = validateNetworkDataPlaneSetupStart(setup, intentID)
		}
	}
	if err != nil {
		return fmt.Errorf("start network data-plane setup: %w", err)
	}
	if setup.Operation.State == domain.OperationSucceeded {
		if setup.Operation.Phase != "completed" {
			return fmt.Errorf("network data-plane setup succeeded in unsupported phase %q", setup.Operation.Phase)
		}
		return nil
	}
	if setup.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("network data-plane setup is %s", setup.Operation.State)
	}
	if setup.Operation.Phase == "awaiting trust approval" {
		selected := setup
		outcome, err := command.dataPlaneApproval.ExecuteTrust(ctx, networkdataplaneapproval.Request{
			OperationID:               selected.Operation.ID,
			ExpectedOperationRevision: selected.Revision,
		})
		if err != nil {
			return fmt.Errorf("approve network data-plane trust: %w", err)
		}
		if outcome.State != networkdataplaneapproval.Succeeded {
			return networkDataPlaneTrustApprovalOutcomeError{outcome: outcome}
		}
		if outcome.Setup == nil || outcome.HelperFailure != nil {
			return errors.New("approve network data-plane trust: successful approval returned inconsistent evidence")
		}
		trusted := *outcome.Setup
		if err := trusted.Validate(); err != nil {
			return fmt.Errorf("validate network data-plane trust confirmation: %w", err)
		}
		if trusted.Operation.ID != selected.Operation.ID ||
			trusted.Operation.IntentID != selected.Operation.IntentID ||
			trusted.Revision <= selected.Revision ||
			!control.RequiresNetworkDataPlaneLowPortApproval(trusted) {
			return errors.New("validate network data-plane trust confirmation: result crossed the selected operation revision")
		}
		setup = trusted
	}
	if setup.Operation.Phase != "awaiting low-port approval" {
		return fmt.Errorf("network data-plane setup requires approval in unsupported phase %q", setup.Operation.Phase)
	}
	outcome, err := command.dataPlaneApproval.ExecuteLowPorts(ctx, networkdataplaneapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	})
	if err != nil {
		return fmt.Errorf("approve network data-plane low-port setup: %w", err)
	}
	if outcome.State != networkdataplaneapproval.Succeeded {
		return networkDataPlaneLowPortApprovalOutcomeError{outcome: outcome}
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return errors.New("approve network data-plane low-port setup: successful approval returned inconsistent evidence")
	}
	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("validate network data-plane low-port confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.NetworkRevision <= setup.Revision ||
		confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("validate network data-plane low-port confirmation: result crossed the selected operation revision")
	}
	return nil
}

// validateNetworkDataPlaneSetupStart rejects malformed or crossed data-plane responses before retry decisions.
func validateNetworkDataPlaneSetupStart(setup control.NetworkDataPlaneSetupOperation, intentID domain.IntentID) error {
	if err := setup.Validate(); err != nil {
		return fmt.Errorf("validate response: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return errors.New("result crossed the selected intent")
	}
	return nil
}

// networkResolverSetupApprovalOutcomeError explains one bounded resolver approval conclusion.
type networkResolverSetupApprovalOutcomeError struct {
	outcome networkresolverapproval.Outcome
}

// Error preserves retry and indeterminate guidance without exposing helper capabilities.
func (failure networkResolverSetupApprovalOutcomeError) Error() string {
	switch failure.outcome.State {
	case networkresolverapproval.Declined:
		return "network resolver setup approval was declined; run harbor setup to try again"
	case networkresolverapproval.Unavailable:
		return "network resolver setup approval is unavailable; run harbor setup to try again"
	case networkresolverapproval.HelperFailed:
		if failure.outcome.HelperFailure == nil {
			return "network resolver setup helper failed without a bounded failure description"
		}
		return fmt.Sprintf("network resolver setup helper failed (%s): %s", failure.outcome.HelperFailure.Code, failure.outcome.HelperFailure.Message)
	case networkresolverapproval.Indeterminate:
		return "network resolver setup approval is indeterminate; run harbor setup to read the authoritative state"
	default:
		return fmt.Sprintf("network resolver setup approval returned unsupported state %q", failure.outcome.State)
	}
}

// networkDataPlaneTrustApprovalOutcomeError explains one bounded trust approval conclusion.
type networkDataPlaneTrustApprovalOutcomeError struct {
	outcome networkdataplaneapproval.TrustOutcome
}

// Error preserves phase-specific retry and indeterminate guidance.
func (failure networkDataPlaneTrustApprovalOutcomeError) Error() string {
	return networkDataPlaneApprovalError("trust", failure.outcome.State, failure.outcome.HelperFailure)
}

// networkDataPlaneLowPortApprovalOutcomeError explains one bounded low-port approval conclusion.
type networkDataPlaneLowPortApprovalOutcomeError struct {
	outcome networkdataplaneapproval.LowPortOutcome
}

// Error preserves phase-specific retry and indeterminate guidance.
func (failure networkDataPlaneLowPortApprovalOutcomeError) Error() string {
	return networkDataPlaneApprovalError("low-port", failure.outcome.State, failure.outcome.HelperFailure)
}

// networkDataPlaneApprovalError formats a bounded data-plane approval conclusion.
func networkDataPlaneApprovalError(phase string, state networkdataplaneapproval.State, failure *networkdataplaneapproval.HelperFailure) string {
	switch state {
	case networkdataplaneapproval.Declined:
		return fmt.Sprintf("network data-plane %s approval was declined; run harbor setup to try again", phase)
	case networkdataplaneapproval.Unavailable:
		return fmt.Sprintf("network data-plane %s approval is unavailable; run harbor setup to try again", phase)
	case networkdataplaneapproval.HelperFailed:
		if failure == nil {
			return fmt.Sprintf("network data-plane %s helper failed without a bounded failure description", phase)
		}
		return fmt.Sprintf("network data-plane %s helper failed (%s): %s", phase, failure.Code, failure.Message)
	case networkdataplaneapproval.Indeterminate:
		return fmt.Sprintf("network data-plane %s approval is indeterminate; run harbor setup to read the authoritative state", phase)
	default:
		return fmt.Sprintf("network data-plane %s approval returned unsupported state %q", phase, state)
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
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.Operation.Phase != "completed" ||
		confirmation.NetworkRevision != setup.Revision+2 ||
		confirmation.Revision != setup.Revision+3 {
		return fmt.Errorf("approve network setup: confirmation crossed the selected operation revision")
	}

	if command.fullSetup {
		return nil
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
