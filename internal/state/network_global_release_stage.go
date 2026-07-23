package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// StageGlobalNetworkRelease atomically binds a fully quiescent network to its durable release authority.
func (journal *OperationJournal) StageGlobalNetworkRelease(
	ctx context.Context,
	request StageGlobalNetworkReleaseRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("stage global network release: %w", err)
	}
	request = cloneStageGlobalNetworkReleaseRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	err := journal.mutations.mutateGlobalNetworkReleaseStage(ctx, "global network release staging", func(tx *gorm.DB) error {
		staged, err := stageGlobalNetworkReleaseInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = staged
		return nil
	}, func(tx *gorm.DB) error {
		return validateGlobalNetworkReleaseMutationOwner(
			tx,
			request.Operation.ID,
			GlobalNetworkReleasePlanPhaseRuntimeRelease,
		)
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("stage global network release: %w", err)
	}
	return result, nil
}

// cloneStageGlobalNetworkReleaseRequest prevents retained authority slices from being changed while writer authority waits.
func cloneStageGlobalNetworkReleaseRequest(request StageGlobalNetworkReleaseRequest) StageGlobalNetworkReleaseRequest {
	request.Authority = request.Authority.Clone()
	return request
}

// stageGlobalNetworkReleaseInTransaction performs all release admission checks at one writer instant.
func stageGlobalNetworkReleaseInTransaction(tx *gorm.DB, request StageGlobalNetworkReleaseRequest) (OperationRecord, error) {
	if err := requireCurrentGlobalNetworkReleaseAuthority(tx, request.Authority); err != nil {
		return OperationRecord{}, err
	}
	if err := requireGlobalNetworkReleaseQuiescence(tx, request.Authority.ProjectRevisions); err != nil {
		return OperationRecord{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return OperationRecord{}, err
	}

	planRow, planFound, err := readOptionalGlobalNetworkReleasePlanForStaging(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	existing, found, err := findOperationByIntent(tx, request.Operation.IntentID)
	if err != nil {
		return OperationRecord{}, err
	}
	if found {
		record, err := operationRecordFromModel(existing)
		if err != nil {
			return OperationRecord{}, err
		}
		if record.Operation.Kind != request.Operation.Kind || record.Operation.ProjectID != request.Operation.ProjectID {
			return OperationRecord{}, globalNetworkReleaseIntentConflict(request.Operation, record.Operation)
		}
		if !planFound {
			return OperationRecord{}, corruptGlobalNetworkReleasePlan(request.Operation.ID, fmt.Errorf("singleton plan is missing for staged operation"))
		}
		return replayGlobalNetworkReleaseInTransaction(tx, record, request, planRow)
	}
	existing, found, err = findOperationByID(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	if found {
		record, err := operationRecordFromModel(existing)
		if err != nil {
			return OperationRecord{}, err
		}
		return OperationRecord{}, &OperationIDConflictError{
			OperationID:       request.Operation.ID,
			ExistingIntentID:  record.Operation.IntentID,
			RequestedIntentID: request.Operation.IntentID,
		}
	}
	if planFound {
		return OperationRecord{}, corruptGlobalNetworkReleasePlan(request.Operation.ID, fmt.Errorf("singleton plan belongs to operation %q", planRow.OperationID))
	}
	if active, found, err := findActiveGlobalNetworkReleaseOperation(tx); err != nil {
		return OperationRecord{}, err
	} else if found {
		return OperationRecord{}, &GlobalNetworkReleaseActiveError{
			OperationID: active.Operation.ID,
			State:       active.Operation.State,
			Action:      "stage global network release",
		}
	}

	queued, err := insertQueuedGlobalNetworkReleaseOperation(tx, request.Operation)
	if err != nil {
		return OperationRecord{}, err
	}
	running, err := transitionOperationInTransaction(
		tx,
		request.Operation.ID,
		queued.Revision,
		domain.OperationRunning,
		globalNetworkReleaseRuntimeOperationPhase,
		request.Operation.RequestedAt,
		nil,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := insertGlobalNetworkReleasePlan(tx, running, request.Authority); err != nil {
		return OperationRecord{}, err
	}
	planRow, planFound, err = readOptionalGlobalNetworkReleasePlanForStaging(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	if !planFound {
		return OperationRecord{}, corruptGlobalNetworkReleasePlan(request.Operation.ID, fmt.Errorf("singleton plan is missing after insert"))
	}
	return replayGlobalNetworkReleaseInTransaction(tx, running, request, planRow)
}

// requireCurrentGlobalNetworkReleaseAuthority rereads every network-owned row and compares the exact full authority.
func requireCurrentGlobalNetworkReleaseAuthority(tx *gorm.DB, authority GlobalNetworkReleaseAuthority) error {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return err
	}
	if !initialized {
		return &NetworkNotInitializedError{}
	}
	if network.Stage != NetworkStageFull {
		return fmt.Errorf("global network release requires %q network stage, found %q", NetworkStageFull, network.Stage)
	}
	if len(rows.Endpoints) != 0 || len(rows.Releases) != 0 {
		return fmt.Errorf("global network release requires no project endpoint or suppression rows")
	}
	fingerprint, err := authority.Policy.Fingerprint()
	if err != nil {
		return err
	}
	projection, err := resolveNetworkDataPlaneSetupProjection(tx, authority.Policy, fingerprint)
	if err != nil {
		return err
	}
	if !sameNetworkDataPlaneSetupProjection(projection, authority.Projection) {
		return fmt.Errorf("global network release authority projection differs from current network projection")
	}
	if network.Revision != authority.Projection.NetworkRevision || !network.UpdatedAt.Equal(authority.Projection.NetworkUpdatedAt) {
		return fmt.Errorf("global network release authority network boundary differs from current network")
	}
	return nil
}

// requireGlobalNetworkReleaseQuiescence verifies every project and runtime owner is fully stopped before global teardown.
func requireGlobalNetworkReleaseQuiescence(tx *gorm.DB, expected []NetworkProjectRevision) error {
	projects, err := readProjectRecords(tx)
	if err != nil {
		return err
	}
	if len(projects) != len(expected) {
		return fmt.Errorf("global network release project set differs from authority")
	}
	for index, project := range projects {
		want := expected[index]
		if project.Project.ID != want.ProjectID || project.Revision != want.Revision {
			return fmt.Errorf("global network release project revisions differ from authority")
		}
		if project.Project.State != domain.ProjectStopped {
			return fmt.Errorf("global network release project %q is not stopped", project.Project.ID)
		}
		if err := validateStoppedRuntimeProject(project.Project); err != nil {
			return fmt.Errorf("global network release project %q is not inactive: %w", project.Project.ID, err)
		}
	}
	var sessions []models.ProjectSession
	if err := tx.Order("id ASC").Limit(1).Find(&sessions).Error; err != nil {
		return fmt.Errorf("read global network release sessions: %w", err)
	}
	if len(sessions) != 0 {
		return fmt.Errorf("global network release requires no project sessions")
	}
	var projectOperations []models.Operation
	if err := tx.Where("project_id IS NOT NULL AND (state NOT IN ? OR state IS NULL)", []string{
		string(domain.OperationSucceeded),
		string(domain.OperationFailed),
		string(domain.OperationCancelled),
	}).Order("id ASC").Limit(1).Find(&projectOperations).Error; err != nil {
		return fmt.Errorf("read global network release project operations: %w", err)
	}
	if len(projectOperations) != 0 {
		record, err := operationRecordFromModel(projectOperations[0])
		if err != nil {
			return err
		}
		return fmt.Errorf(
			"global network release requires no nonterminal project operations; operation %q is %q",
			record.Operation.ID,
			record.Operation.State,
		)
	}
	var setupOperations []models.Operation
	if err := tx.Where("project_id IS NULL AND kind IN ? AND (state NOT IN ? OR state IS NULL)", []domain.OperationKind{
		domain.OperationKindNetworkSetup,
		domain.OperationKindNetworkResolverSetup,
		domain.OperationKindNetworkResolverPolicyMigration,
		domain.OperationKindNetworkDataPlaneSetup,
	}, []string{
		string(domain.OperationSucceeded),
		string(domain.OperationFailed),
		string(domain.OperationCancelled),
	}).Order("id ASC").Limit(1).Find(&setupOperations).Error; err != nil {
		return fmt.Errorf("read global network release setup operations: %w", err)
	}
	if len(setupOperations) != 0 {
		record, err := operationRecordFromModel(setupOperations[0])
		if err != nil {
			return err
		}
		return fmt.Errorf(
			"global network release requires no active setup operations; operation %q is %q",
			record.Operation.ID,
			record.Operation.State,
		)
	}
	return nil
}

// readOptionalGlobalNetworkReleasePlanForStaging reads the singleton without permitting a malformed plan to look absent.
func readOptionalGlobalNetworkReleasePlanForStaging(tx *gorm.DB, operationID domain.OperationID) (globalNetworkReleasePlanRow, bool, error) {
	var rows []globalNetworkReleasePlanRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleasePlanRow{}, false, fmt.Errorf("read global network release plan for staging: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleasePlanRow{}, false, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton contains %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return globalNetworkReleasePlanRow{}, false, nil
	}
	if rows[0].ID != 1 {
		return globalNetworkReleasePlanRow{}, false, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton ID is %d, expected 1", rows[0].ID))
	}
	return rows[0], true, nil
}

// insertGlobalNetworkReleasePlan persists canonical authority at the running operation revision.
func insertGlobalNetworkReleasePlan(tx *gorm.DB, operation OperationRecord, authority GlobalNetworkReleaseAuthority) error {
	payload, digest, err := encodeGlobalNetworkReleaseAuthority(authority)
	if err != nil {
		return err
	}
	row := globalNetworkReleasePlanRow{
		ID:                 1,
		OperationID:        string(operation.Operation.ID),
		OperationRevision:  int(operation.Revision),
		CheckpointRevision: int(operation.Revision),
		Phase:              string(GlobalNetworkReleasePlanPhaseRuntimeRelease),
		NetworkStateID:     networkStateSingletonID,
		NetworkRevision:    int(authority.Projection.NetworkRevision),
		NetworkUpdatedAt:   authority.Projection.NetworkUpdatedAt,
		AuthorityPayload:   payload,
		AuthorityDigest:    digest,
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create global network release plan: %w", err)
	}
	return nil
}

// replayGlobalNetworkReleaseInTransaction accepts only the two fixed staging edges and exact persisted authority.
func replayGlobalNetworkReleaseInTransaction(tx *gorm.DB, record OperationRecord, request StageGlobalNetworkReleaseRequest, row globalNetworkReleasePlanRow) (OperationRecord, error) {
	if record.Operation.ID != request.Operation.ID ||
		record.Operation.IntentID != request.Operation.IntentID ||
		record.Operation.Kind != request.Operation.Kind ||
		record.Operation.ProjectID != request.Operation.ProjectID ||
		!record.Operation.RequestedAt.Equal(request.Operation.RequestedAt) {
		return OperationRecord{}, fmt.Errorf("global network release intent %q differs from its exact staged operation", request.Operation.IntentID)
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if len(history) != 2 ||
		record.Operation.State != domain.OperationRunning ||
		record.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase ||
		record.Revision != history[1].Sequence {
		return OperationRecord{}, corruptGlobalNetworkReleasePlan(record.Operation.ID, fmt.Errorf("operation does not match the fixed global release lifecycle"))
	}
	states := [...]domain.OperationState{domain.OperationQueued, domain.OperationRunning}
	phases := [...]string{string(domain.OperationQueued), globalNetworkReleaseRuntimeOperationPhase}
	for index := range history {
		if history[index].State != states[index] ||
			history[index].Phase != phases[index] ||
			!history[index].OccurredAt.Equal(record.Operation.RequestedAt) ||
			(index > 0 && history[index].Sequence != history[index-1].Sequence+1) {
			return OperationRecord{}, corruptGlobalNetworkReleasePlan(record.Operation.ID, fmt.Errorf("operation history differs from the fixed global release lifecycle"))
		}
	}
	plan, err := globalNetworkReleasePlanFromRow(row, record)
	if err != nil {
		return OperationRecord{}, err
	}
	expectedPayload, expectedDigest, err := encodeGlobalNetworkReleaseAuthority(request.Authority)
	if err != nil {
		return OperationRecord{}, err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseRuntimeRelease ||
		row.AuthorityPayload != expectedPayload ||
		row.AuthorityDigest != expectedDigest {
		return OperationRecord{}, corruptGlobalNetworkReleasePlan(record.Operation.ID, fmt.Errorf("persisted authority differs from exact staging request"))
	}
	if plan.CheckpointRevision != record.Revision {
		return OperationRecord{}, corruptGlobalNetworkReleasePlan(record.Operation.ID, fmt.Errorf("initial checkpoint revision %d differs from running operation revision %d", plan.CheckpointRevision, record.Revision))
	}
	return record, nil
}

// globalNetworkReleaseIntentConflict preserves typed idempotency when an intent crosses global operation boundaries.
func globalNetworkReleaseIntentConflict(requested, existing domain.Operation) error {
	return &IntentConflictError{
		IntentID:            requested.IntentID,
		ExistingOperationID: existing.ID,
		ExistingKind:        existing.Kind,
		ExistingProjectID:   existing.ProjectID,
		RequestedKind:       requested.Kind,
		RequestedProjectID:  requested.ProjectID,
	}
}

// insertQueuedGlobalNetworkReleaseOperation appends the first fixed release lifecycle edge.
func insertQueuedGlobalNetworkReleaseOperation(tx *gorm.DB, operation domain.Operation) (OperationRecord, error) {
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return OperationRecord{}, err
	}
	row, err := operationModelFromDomain(operation, sequence)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("create global network release operation: %w", err)
	}
	transition, err := operationTransitionModelFromDomain(OperationTransition{
		OperationID: operation.ID,
		Ordinal:     1,
		State:       domain.OperationQueued,
		Phase:       operation.Phase,
		OccurredAt:  operation.RequestedAt,
		Sequence:    sequence,
	})
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&transition).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("append queued global network release transition: %w", err)
	}
	return OperationRecord{
		Operation: operation,
		Revision:  sequence,
	}, nil
}
