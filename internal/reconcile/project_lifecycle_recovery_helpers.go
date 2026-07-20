package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const projectRecoveryQuarantineRunningPhase = "isolating unresolved process authority"

// quarantinePlannedProjectStart isolates one unresolved launch without claiming that no child process exists.
func (coordinator *ProjectLifecycleCoordinator) quarantinePlannedProjectStart(
	ctx context.Context,
	record state.OperationRecord,
	session domain.ProjectSession,
) error {
	if session.State != domain.SessionPlanned || session.Process != nil {
		return priorProcessOwnershipError(record, session)
	}
	project, err := coordinator.state.Project(ctx, record.Operation.ProjectID)
	if err != nil {
		return err
	}
	at := recoveredProjectLifecycleTime(coordinator.now(), record, project.Project, session)
	problem := domain.Problem{
		Code: projectRecoveryAmbiguousLaunchCode,
		Message: "Harbor restarted before it could record the managed process identity. " +
			"This project was isolated so Harbor cannot accidentally start a second process.",
		Retryable: false,
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.QuarantinePlannedProjectStart(ctx, state.QuarantinePlannedProjectStartRequest{
			ProjectID:                 record.Operation.ProjectID,
			OperationID:               record.Operation.ID,
			ExpectedOperationRevision: record.Revision,
			ExpectedProjectRevision:   project.Revision,
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Phase:                     projectRecoveryQuarantinePhase,
			Problem:                   problem,
			At:                        at,
		})
	}); err != nil {
		return fmt.Errorf("quarantine ambiguous project start operation %q: %w", record.Operation.ID, err)
	}
	return nil
}

// isPlannedProjectStartQuarantined recognizes only the exact terminal marker written by recovery.
func (coordinator *ProjectLifecycleCoordinator) isPlannedProjectStartQuarantined(
	ctx context.Context,
	project domain.ProjectSnapshot,
	session domain.ProjectSession,
) (bool, error) {
	if project.State != domain.ProjectUnavailable || session.State != domain.SessionPlanned || session.Process != nil {
		return false, nil
	}
	record, err := coordinator.operations.LatestProjectLifecycleOperation(ctx, project.ID)
	if err != nil {
		var missing *state.ProjectLifecycleOperationNotFoundError
		if errors.As(err, &missing) {
			return false, nil
		}
		return false, fmt.Errorf("read project %q recovery quarantine marker: %w", project.ID, err)
	}
	operation := record.Operation
	return operation.Kind == domain.OperationKindProjectStart &&
		operation.State == domain.OperationFailed &&
		operation.Problem != nil &&
		operation.Problem.Code == projectRecoveryAmbiguousLaunchCode, nil
}

// quarantineTerminalProjectSession publishes a route-free failure without observing or acting on an unidentified prior process.
func (coordinator *ProjectLifecycleCoordinator) quarantineTerminalProjectSession(
	ctx context.Context,
	project domain.ProjectSnapshot,
	missing state.ProjectSessionProcessEvidenceMissingError,
) error {
	if (project.State != domain.ProjectReady && project.State != domain.ProjectDegraded) ||
		missing.ProjectID != project.ID ||
		missing.Owner != domain.SessionOwnerHarbor ||
		missing.State != domain.SessionAwaitingAttach {
		return fmt.Errorf(
			"recover project %q in state %q with session %q state %q: prior process ownership requires exact host reconciliation",
			project.ID,
			project.State,
			missing.SessionID,
			missing.State,
		)
	}
	projectRecord, err := coordinator.state.Project(ctx, project.ID)
	if err != nil {
		return err
	}
	at := lifecycleTime(coordinator.now())
	if at.Before(project.UpdatedAt) {
		at = project.UpdatedAt
	}
	if at.Before(missing.UpdatedAt) {
		at = missing.UpdatedAt
	}
	operationID, err := coordinator.newOperationID()
	if err != nil {
		return fmt.Errorf("create terminal session recovery operation ID: %w", err)
	}
	intentID, err := coordinator.newIntentID()
	if err != nil {
		return fmt.Errorf("create terminal session recovery intent ID: %w", err)
	}
	operation, err := domain.NewOperation(operationID, intentID, domain.OperationKindProjectStart, project.ID, at)
	if err != nil {
		return fmt.Errorf("create terminal session recovery operation: %w", err)
	}
	problem := terminalProjectRecoveryProblem()
	request := state.QuarantineTerminalProjectSessionRequest{
		ProjectID:                 project.ID,
		ExpectedProjectRevision:   projectRecord.Revision,
		SessionID:                 missing.SessionID,
		ExpectedSessionGeneration: missing.Generation,
		Operation:                 operation,
		RunningPhase:              projectRecoveryQuarantineRunningPhase,
		FailurePhase:              projectRecoveryQuarantinePhase,
		Problem:                   problem,
		At:                        at,
	}
	if _, err := retryLifecycleResult(func() (state.ProjectRecoveryQuarantine, error) {
		return coordinator.state.QuarantineTerminalProjectSession(ctx, request)
	}); err != nil {
		return fmt.Errorf("quarantine project %q unresolved terminal session: %w", project.ID, err)
	}
	return nil
}

// isTerminalProjectSessionQuarantined recognizes only the exact route-free marker written for retained missing process evidence.
func (coordinator *ProjectLifecycleCoordinator) isTerminalProjectSessionQuarantined(
	ctx context.Context,
	project domain.ProjectSnapshot,
	missing state.ProjectSessionProcessEvidenceMissingError,
) (bool, error) {
	if project.State != domain.ProjectUnavailable ||
		missing.ProjectID != project.ID ||
		missing.Owner != domain.SessionOwnerHarbor ||
		missing.State != domain.SessionAwaitingAttach {
		return false, nil
	}
	record, err := coordinator.operations.LatestProjectLifecycleOperation(ctx, project.ID)
	if err != nil {
		var absent *state.ProjectLifecycleOperationNotFoundError
		if errors.As(err, &absent) {
			return false, nil
		}
		return false, fmt.Errorf("read project %q terminal recovery quarantine marker: %w", project.ID, err)
	}
	operation := record.Operation
	problem := terminalProjectRecoveryProblem()
	return operation.Kind == domain.OperationKindProjectStart &&
		operation.State == domain.OperationFailed &&
		operation.Phase == projectRecoveryQuarantinePhase &&
		operation.Problem != nil &&
		*operation.Problem == problem, nil
}

// terminalProjectRecoveryProblem tells the user why automatic process reconciliation is intentionally unavailable.
func terminalProjectRecoveryProblem() domain.Problem {
	return domain.Problem{
		Code: projectRecoveryAmbiguousLaunchCode,
		Message: "Harbor restarted without enough evidence to identify the previous project process. " +
			"Stop that process outside Harbor before resetting this project.",
		Retryable: false,
	}
}

// settleRecoveredProjectProcess proves one persisted process birth is terminal before durable authority is retired.
func (coordinator *ProjectLifecycleCoordinator) settleRecoveredProjectProcess(
	ctx context.Context,
	operation string,
	evidence domain.ProcessEvidence,
) (projectprocess.PriorProcessSettlement, error) {
	settlement, err := coordinator.supervisor.SettlePriorProcess(ctx, evidence)
	if err != nil {
		return projectprocess.PriorProcessSettlement{}, fmt.Errorf("settle prior process for %s: %w", operation, err)
	}
	switch settlement.Outcome {
	case projectprocess.PriorProcessSettlementAbsent,
		projectprocess.PriorProcessSettlementReplaced,
		projectprocess.PriorProcessSettlementTerminated:
		return settlement, nil
	default:
		return projectprocess.PriorProcessSettlement{}, fmt.Errorf(
			"settle prior process for %s: unsupported outcome %q",
			operation,
			settlement.Outcome,
		)
	}
}

// recoverQueuedProjectStop settles the exact prior process before crossing the queued-to-running durability edge.
func (coordinator *ProjectLifecycleCoordinator) recoverQueuedProjectStop(
	ctx context.Context,
	record state.OperationRecord,
	session domain.ProjectSession,
) error {
	if session.Process == nil {
		return priorProcessOwnershipError(record, session)
	}
	if _, err := coordinator.settleRecoveredProjectProcess(
		ctx,
		fmt.Sprintf("queued project stop operation %q", record.Operation.ID),
		*session.Process,
	); err != nil {
		return err
	}

	project, err := coordinator.state.Project(ctx, record.Operation.ProjectID)
	if err != nil {
		return err
	}
	at := recoveredProjectLifecycleTime(coordinator.now(), record, project.Project, session)
	mutation, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.BeginProjectStop(ctx, state.BeginProjectStopRequest{
			ProjectID:                 record.Operation.ProjectID,
			OperationID:               record.Operation.ID,
			ExpectedOperationRevision: record.Revision,
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Phase:                     "recovering interrupted stop",
			At:                        at,
		})
	})
	if err != nil {
		return fmt.Errorf("begin recovered project stop operation %q: %w", record.Operation.ID, err)
	}
	if mutation.Session == nil {
		return fmt.Errorf("begin recovered project stop operation %q: state did not retain its exact session", record.Operation.ID)
	}
	return coordinator.completeRecoveredProjectStop(ctx, mutation.Operation, mutation.Project.Project, *mutation.Session)
}

// recoverRunningProjectStop completes a durable stopping transition after settling its exact prior process.
func (coordinator *ProjectLifecycleCoordinator) recoverRunningProjectStop(
	ctx context.Context,
	record state.OperationRecord,
	session domain.ProjectSession,
) error {
	if session.State != domain.SessionStopping || session.Process == nil {
		return priorProcessOwnershipError(record, session)
	}
	if _, err := coordinator.settleRecoveredProjectProcess(
		ctx,
		fmt.Sprintf("running project stop operation %q", record.Operation.ID),
		*session.Process,
	); err != nil {
		return err
	}
	project, err := coordinator.state.Project(ctx, record.Operation.ProjectID)
	if err != nil {
		return err
	}
	return coordinator.completeRecoveredProjectStop(ctx, record, project.Project, session)
}

// completeRecoveredProjectStop retires only the session generation whose process birth was settled by recovery.
func (coordinator *ProjectLifecycleCoordinator) completeRecoveredProjectStop(
	ctx context.Context,
	record state.OperationRecord,
	project domain.ProjectSnapshot,
	session domain.ProjectSession,
) error {
	if session.Process == nil {
		return fmt.Errorf("complete recovered project stop operation %q without process evidence", record.Operation.ID)
	}
	at := recoveredProjectLifecycleTime(coordinator.now(), record, project, session)
	evidence := *session.Process
	request := state.CompleteProjectStopRequest{
		ProjectID:                 record.Operation.ProjectID,
		OperationID:               record.Operation.ID,
		ExpectedOperationRevision: record.Revision,
		Exit: state.ConfirmedProjectProcessExit{
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Process:                   &evidence,
			ExitedAt:                  at,
		},
		Phase: "recovered interrupted stop",
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.CompleteProjectStop(ctx, request)
	}); err != nil {
		return fmt.Errorf("complete recovered project stop operation %q: %w", record.Operation.ID, err)
	}
	return nil
}

// recoveredProjectLifecycleTime preserves every persisted chronology lower bound during restart convergence.
func recoveredProjectLifecycleTime(
	now time.Time,
	record state.OperationRecord,
	project domain.ProjectSnapshot,
	session domain.ProjectSession,
) time.Time {
	at := lifecycleTime(now)
	for _, lowerBound := range []time.Time{record.Operation.RequestedAt, project.UpdatedAt, session.UpdatedAt} {
		if at.Before(lowerBound) {
			at = lowerBound
		}
	}
	if record.Operation.StartedAt != nil && at.Before(*record.Operation.StartedAt) {
		at = *record.Operation.StartedAt
	}
	return at
}
