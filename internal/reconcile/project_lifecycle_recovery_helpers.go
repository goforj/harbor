package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

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
