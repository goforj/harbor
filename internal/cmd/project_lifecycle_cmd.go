package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// projectLifecycleIntentFactory creates one caller-owned retry identity before a project action reaches the daemon.
type projectLifecycleIntentFactory func(domain.OperationKind) (domain.IntentID, error)

// StartCmd starts one registered GoForj project through Harbor's daemon.
type StartCmd struct {
	ProjectID domain.ProjectID `arg:"" name:"project" help:"Registered Harbor project ID"`
	JSON      bool             `help:"Print the typed machine-readable lifecycle operation"`
	Intent    domain.IntentID  `help:"Reuse this intent when retrying an indeterminate start"`

	client      *DaemonClient
	output      io.Writer
	newIntentID projectLifecycleIntentFactory
}

// NewStartCmd creates the project start command without contacting the daemon.
func NewStartCmd(client *DaemonClient) *StartCmd {
	return &StartCmd{client: client, output: os.Stdout, newIntentID: newProjectLifecycleIntentID}
}

// Signature defines CLI metadata for project start.
func (*StartCmd) Signature() string {
	return `name:"start" help:"Start a registered Harbor project"`
}

// Run starts or resumes one project start intent and reports only daemon-authoritative progress.
func (command *StartCmd) Run(ctx context.Context) error {
	return runProjectLifecycle(ctx, projectLifecycleCommand{
		projectID: command.ProjectID,
		intent:    command.Intent,
		json:      command.JSON,
		kind:      domain.OperationKindProjectStart,
		client:    command.client,
		output:    command.output,
		newIntent: command.newIntentID,
	})
}

// StopCmd stops one registered GoForj project through Harbor's daemon.
type StopCmd struct {
	ProjectID domain.ProjectID `arg:"" name:"project" help:"Registered Harbor project ID"`
	JSON      bool             `help:"Print the typed machine-readable lifecycle operation"`
	Intent    domain.IntentID  `help:"Reuse this intent when retrying an indeterminate stop"`

	client      *DaemonClient
	output      io.Writer
	newIntentID projectLifecycleIntentFactory
}

// NewStopCmd creates the project stop command without contacting the daemon.
func NewStopCmd(client *DaemonClient) *StopCmd {
	return &StopCmd{client: client, output: os.Stdout, newIntentID: newProjectLifecycleIntentID}
}

// Signature defines CLI metadata for project stop.
func (*StopCmd) Signature() string {
	return `name:"stop" help:"Stop a registered Harbor project"`
}

// Run starts or resumes one project stop intent and reports only daemon-authoritative progress.
func (command *StopCmd) Run(ctx context.Context) error {
	return runProjectLifecycle(ctx, projectLifecycleCommand{
		projectID: command.ProjectID,
		intent:    command.Intent,
		json:      command.JSON,
		kind:      domain.OperationKindProjectStop,
		client:    command.client,
		output:    command.output,
		newIntent: command.newIntentID,
	})
}

// projectLifecycleCommand keeps StartCmd and StopCmd on one validation, retry, and presentation contract.
type projectLifecycleCommand struct {
	projectID domain.ProjectID
	intent    domain.IntentID
	json      bool
	kind      domain.OperationKind
	client    *DaemonClient
	output    io.Writer
	newIntent projectLifecycleIntentFactory
}

// runProjectLifecycle validates caller-owned input before generating an idempotency identity or opening the daemon connection.
func runProjectLifecycle(ctx context.Context, command projectLifecycleCommand) error {
	if err := command.projectID.Validate(); err != nil {
		return fmt.Errorf("project %s: %w", projectLifecycleAction(command.kind), err)
	}
	intentID, err := projectLifecycleIntent(command.intent, command.kind, command.newIntent)
	if err != nil {
		return err
	}

	var lifecycle control.ProjectLifecycleOperation
	switch command.kind {
	case domain.OperationKindProjectStart:
		lifecycle, err = command.client.StartProject(ctx, control.StartProjectRequest{ProjectID: command.projectID, IntentID: intentID})
	case domain.OperationKindProjectStop:
		lifecycle, err = command.client.StopProject(ctx, control.StopProjectRequest{ProjectID: command.projectID, IntentID: intentID})
	default:
		return fmt.Errorf("unsupported project lifecycle kind %q", command.kind)
	}
	if err != nil {
		return fmt.Errorf(
			"project %s did not return authoritative state; retry with --intent %s: %w",
			projectLifecycleAction(command.kind), intentID, err,
		)
	}
	if command.json {
		err = writeDaemonJSON(command.output, lifecycle)
	} else {
		err = writeProjectLifecycle(command.output, lifecycle)
	}
	if err != nil {
		return err
	}
	return projectLifecycleTerminalStateError(lifecycle)
}

// projectLifecycleIntent validates an override or creates one distinct intent before the daemon can commit work.
func projectLifecycleIntent(
	override domain.IntentID,
	kind domain.OperationKind,
	create projectLifecycleIntentFactory,
) (domain.IntentID, error) {
	if override != "" {
		if err := override.Validate(); err != nil {
			return "", fmt.Errorf("validate intent override: %w", err)
		}
		return override, nil
	}
	if create == nil {
		return "", fmt.Errorf("project %s intent factory is required", projectLifecycleAction(kind))
	}
	intentID, err := create(kind)
	if err != nil {
		return "", fmt.Errorf("create project %s intent: %w", projectLifecycleAction(kind), err)
	}
	if err := intentID.Validate(); err != nil {
		return "", fmt.Errorf("validate generated intent: %w", err)
	}
	return intentID, nil
}

// newProjectLifecycleIntentID uses operating-system entropy so separately launched commands cannot share an action identity.
func newProjectLifecycleIntentID(kind domain.OperationKind) (domain.IntentID, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", err
	}
	return domain.IntentID("intent-" + projectLifecycleAction(kind) + "-" + hex.EncodeToString(random)), nil
}

// projectLifecycleAction maps only daemon-supported lifecycle kinds to the operator-facing action word.
func projectLifecycleAction(kind domain.OperationKind) string {
	switch kind {
	case domain.OperationKindProjectStart:
		return "start"
	case domain.OperationKindProjectStop:
		return "stop"
	default:
		return string(kind)
	}
}

// writeProjectLifecycle reports durable state without claiming that an accepted operation has completed.
func writeProjectLifecycle(output io.Writer, lifecycle control.ProjectLifecycleOperation) error {
	operation := lifecycle.Operation
	label := "Starting"
	if operation.Kind == domain.OperationKindProjectStop {
		label = "Stopping"
	}
	if operation.State == domain.OperationSucceeded {
		label = "Started"
		if operation.Kind == domain.OperationKindProjectStop {
			label = "Stopped"
		}
	}
	if operation.State == domain.OperationFailed {
		label += " failed"
	}
	if operation.State == domain.OperationCancelled {
		label += " cancelled"
	}
	if _, err := fmt.Fprintf(output, "%s: %s\nState: %s\nPhase: %s\nOperation: %s\nIntent: %s\nRevision: %d\n", label, operation.ProjectID, operation.State, operation.Phase, operation.ID, operation.IntentID, lifecycle.Revision); err != nil {
		return err
	}
	if operation.Problem != nil {
		_, err := fmt.Fprintf(output, "Problem: %s\nRetryable: %t\n", operation.Problem.Message, operation.Problem.Retryable)
		return err
	}
	return nil
}

// projectLifecycleTerminalError makes an authoritative failed or cancelled start/stop visible to shell callers.
type projectLifecycleTerminalError struct {
	projectID   domain.ProjectID
	operationID domain.OperationID
	state       domain.OperationState
	kind        domain.OperationKind
}

// Error reports the terminal action outcome after it has been rendered.
func (failure projectLifecycleTerminalError) Error() string {
	return fmt.Sprintf("project %s %s for %s ended %s", projectLifecycleAction(failure.kind), failure.operationID, failure.projectID, failure.state)
}

// projectLifecycleTerminalStateError returns nonzero only for authoritative failure or cancellation.
func projectLifecycleTerminalStateError(lifecycle control.ProjectLifecycleOperation) error {
	operation := lifecycle.Operation
	if operation.State != domain.OperationFailed && operation.State != domain.OperationCancelled {
		return nil
	}
	return projectLifecycleTerminalError{projectID: operation.ProjectID, operationID: operation.ID, state: operation.State, kind: operation.Kind}
}
