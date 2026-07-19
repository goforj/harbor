package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// removeIntentFactory creates one client-owned idempotency identity before a removal reaches the daemon.
type removeIntentFactory func() (domain.IntentID, error)

// RemoveCmd unregisters one project without deleting its checkout or persistent data.
type RemoveCmd struct {
	ProjectID domain.ProjectID `arg:"" name:"project" help:"Registered Harbor project ID"`
	JSON      bool             `help:"Print the typed machine-readable removal operation"`
	Intent    domain.IntentID  `help:"Reuse this intent when retrying an indeterminate removal"`

	client      *DaemonClient
	output      io.Writer
	newIntentID removeIntentFactory
	intents     projectRemovalIntentJournal
}

// NewRemoveCmd creates the project removal command without contacting the daemon.
func NewRemoveCmd(client *DaemonClient) *RemoveCmd {
	return &RemoveCmd{
		client:      client,
		output:      os.Stdout,
		newIntentID: newRemoveIntentID,
		intents:     newFilesystemProjectRemovalIntentJournal(),
	}
}

// Signature defines CLI metadata for project removal.
func (*RemoveCmd) Signature() string {
	return `name:"remove" help:"Unregister a Harbor project"`
}

// Run starts or resumes the removal using one intent for the complete command workflow.
func (command *RemoveCmd) Run(ctx context.Context) error {
	if err := command.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project removal: %w", err)
	}
	intentID, err := command.stableIntentID(ctx)
	if err != nil {
		return fmt.Errorf("create project removal intent: %w", err)
	}
	unregistration, err := command.client.UnregisterProject(ctx, control.UnregisterProjectRequest{
		ProjectID: command.ProjectID,
		IntentID:  intentID,
	})
	if err != nil {
		return projectRemovalCallError(intentID, err)
	}

	var outputErr error
	if command.JSON {
		outputErr = writeDaemonJSON(command.output, unregistration)
	} else {
		outputErr = writeProjectUnregistration(command.output, unregistration)
	}
	if outputErr != nil {
		return outputErr
	}
	if !unregistration.Operation.State.IsTerminal() {
		return nil
	}

	clearErr := command.intents.Clear(ctx, command.ProjectID, intentID)
	terminalErr := projectRemovalTerminalStateError(unregistration)
	if clearErr != nil {
		return errors.Join(
			terminalErr,
			fmt.Errorf("clear completed project removal intent %s: %w", intentID, clearErr),
		)
	}
	return terminalErr
}

// stableIntentID persists idempotency before the daemon can commit an operation that the caller never observes.
func (command *RemoveCmd) stableIntentID(ctx context.Context) (domain.IntentID, error) {
	if command.Intent != "" {
		if err := command.Intent.Validate(); err != nil {
			return "", fmt.Errorf("validate intent override: %w", err)
		}
	}
	return command.intents.LoadOrCreate(ctx, command.ProjectID, command.Intent, command.newIntentID)
}

// projectRemovalCallError preserves a recoverable intent when no authoritative operation reached the caller.
func projectRemovalCallError(intentID domain.IntentID, err error) error {
	return fmt.Errorf(
		"project removal did not return authoritative state; retry with --intent %s: %w",
		intentID,
		err,
	)
}

// projectRemovalTerminalError makes an authoritatively failed or cancelled removal observable to scripts.
type projectRemovalTerminalError struct {
	projectID   domain.ProjectID
	operationID domain.OperationID
	state       domain.OperationState
}

// Error reports the terminal outcome after its structured or human-readable result has been rendered.
func (failure projectRemovalTerminalError) Error() string {
	return fmt.Sprintf(
		"project removal %s for %s ended %s",
		failure.operationID,
		failure.projectID,
		failure.state,
	)
}

// projectRemovalTerminalStateError returns nonzero only when authoritative output describes failure or cancellation.
func projectRemovalTerminalStateError(unregistration control.ProjectUnregistration) error {
	operation := unregistration.Operation
	switch operation.State {
	case domain.OperationFailed, domain.OperationCancelled:
		return projectRemovalTerminalError{
			projectID:   operation.ProjectID,
			operationID: operation.ID,
			state:       operation.State,
		}
	default:
		return nil
	}
}

// newRemoveIntentID uses enough operating-system entropy to make independently launched CLI workflows distinct.
func newRemoveIntentID() (domain.IntentID, error) {
	return newRemoveIntentIDFrom(rand.Reader)
}

// newRemoveIntentIDFrom keeps entropy failure observable without weakening the production random source.
func newRemoveIntentIDFrom(reader io.Reader) (domain.IntentID, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", err
	}
	return domain.IntentID("intent-remove-" + hex.EncodeToString(random)), nil
}

// writeProjectUnregistration reports the durable operation state without claiming unfinished approval work succeeded.
func writeProjectUnregistration(output io.Writer, unregistration control.ProjectUnregistration) error {
	operation := unregistration.Operation
	label := "Removal started"
	switch operation.State {
	case domain.OperationSucceeded:
		label = "Removed"
	case domain.OperationRequiresApproval:
		label = "Removal requires approval"
	case domain.OperationFailed:
		label = "Removal failed"
	case domain.OperationCancelled:
		label = "Removal cancelled"
	}
	if _, err := fmt.Fprintf(
		output,
		"%s: %s\nState: %s\nPhase: %s\nOperation: %s\nIntent: %s\nRevision: %d\n",
		label,
		operation.ProjectID,
		operation.State,
		operation.Phase,
		operation.ID,
		operation.IntentID,
		unregistration.Revision,
	); err != nil {
		return err
	}
	if operation.State == domain.OperationRequiresApproval {
		_, err := fmt.Fprintln(output, "Approval: interactive approval is not available in this CLI yet")
		return err
	}
	if operation.Problem != nil {
		_, err := fmt.Fprintf(output, "Problem: %s\nRetryable: %t\n", operation.Problem.Message, operation.Problem.Retryable)
		return err
	}
	return nil
}
