package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// PutProject replaces one complete normalized project aggregate at a newly allocated global revision.
func (store *Store) PutProject(ctx context.Context, project domain.ProjectSnapshot) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := project.Validate(); err != nil {
		return ProjectRecord{}, err
	}
	project = canonicalProjectForMutation(project)
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	var result ProjectRecord
	err := store.mutations.mutate(ctx, "project projection", func(tx *gorm.DB) error {
		boundary, err := readProjectNetworkReleaseBoundary(tx, project.ID)
		if err != nil {
			return err
		}
		if err := rejectProjectNetworkReleaseMutation(boundary, project.ID, "put project"); err != nil {
			return err
		}
		if err := validateExistingProjectMutationSequenceOwner(tx, project.ID); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		projectRow, appRows, serviceRows, resourceRows, err := projectModelsFromDomain(project, sequence)
		if err != nil {
			return err
		}
		if err := putProjectAggregate(tx, projectRow, appRows, serviceRows, resourceRows); err != nil {
			return err
		}

		result, err = readProjectRecord(tx, project.ID)
		if err != nil {
			return fmt.Errorf("read project after put: %w", err)
		}
		if result.Revision != sequence {
			return corruptStateError("project", string(project.ID), fmt.Errorf("readback revision is %d, expected %d", result.Revision, sequence))
		}
		if !reflect.DeepEqual(result.Project, project) {
			return corruptStateError("project", string(project.ID), fmt.Errorf("readback aggregate differs from the requested project"))
		}
		return validateProjectSequenceOwner(tx, result)
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("put project %q: %w", project.ID, err)
	}
	return result, nil
}

// CompleteProjectUnregister atomically removes one project and succeeds its owning unregister operation.
func (store *Store) CompleteProjectUnregister(
	ctx context.Context,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
	phase string,
	at time.Time,
) (OperationRecord, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return OperationRecord{}, err
	}
	if err := operationID.Validate(); err != nil {
		return OperationRecord{}, err
	}
	expectedModelRevision, err := sequenceToModelInt("expected operation revision", expectedRevision, false)
	if err != nil {
		return OperationRecord{}, err
	}
	if strings.TrimSpace(phase) == "" {
		return OperationRecord{}, fmt.Errorf("operation phase must not be empty")
	}
	if err := validateStoredTime("operation transition time", at); err != nil {
		return OperationRecord{}, err
	}
	at = at.UTC().Round(0)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	err = store.mutations.mutate(ctx, "project unregister", func(tx *gorm.DB) error {
		network, err := readProjectUnregisterNetworkState(tx, projectID)
		if err != nil {
			return err
		}
		if err := requireCompletedProjectNetworkRelease(network, projectID, operationID); err != nil {
			return err
		}

		current, history, err := readProjectUnregisterOperationState(tx, operationID)
		if err != nil {
			return err
		}
		if current.Operation.Kind != domain.OperationKindProjectUnregister {
			return fmt.Errorf("operation %q kind is %q, not %q", operationID, current.Operation.Kind, domain.OperationKindProjectUnregister)
		}
		if current.Operation.ProjectID != projectID {
			return fmt.Errorf("operation %q belongs to project %q, not %q", operationID, current.Operation.ProjectID, projectID)
		}
		if network.initialized && network.boundary.found && !reflect.DeepEqual(network.boundary.owner.operation, current) {
			return corruptStateError(
				"network project release",
				string(operationID),
				fmt.Errorf("release owner differs from unregister operation"),
			)
		}

		project, found, err := findProjectForMutation(tx, projectID)
		if err != nil {
			return err
		}
		if !found {
			if _, err := validateRetainedSequenceBounds(tx); err != nil {
				return err
			}
			replayed, err := replayCompletedProjectUnregister(current, history, expectedRevision, phase)
			if err != nil {
				return err
			}
			operationIDs, err := activeProjectOperationIDsExcluding(tx, projectID, operationID)
			if err != nil {
				return err
			}
			if len(operationIDs) != 0 {
				return corruptStateError(
					"project unregister",
					string(projectID),
					fmt.Errorf("project is absent while active operations remain: %v", operationIDs),
				)
			}
			result = replayed
			return nil
		}
		if current.Operation.State == domain.OperationSucceeded {
			return corruptStateError(
				"project unregister",
				string(projectID),
				fmt.Errorf("project remains present after succeeded operation %q", operationID),
			)
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project unregister operation %q must be running, got %q", operationID, current.Operation.State)
		}
		if current.Revision != expectedRevision {
			return &StaleRevisionError{OperationID: operationID, Expected: expectedRevision, Actual: current.Revision}
		}
		projectRecord, err := readProjectRecord(tx, projectID)
		if err != nil {
			return err
		}
		if err := validateProjectSequenceOwner(tx, projectRecord); err != nil {
			return err
		}
		if err := validateProjectRecentSequenceOwners(tx, projectRecord); err != nil {
			return err
		}
		operationIDs, err := activeProjectOperationIDsExcluding(tx, projectID, operationID)
		if err != nil {
			return err
		}
		if len(operationIDs) != 0 {
			return &ProjectBusyError{ProjectID: projectID, OperationIDs: operationIDs}
		}
		nextOperation, err := current.Operation.Transition(domain.OperationSucceeded, phase, at, nil)
		if err != nil {
			return err
		}
		lastTransition := history[len(history)-1]
		previousState := current.Operation.State
		expectedTransition := OperationTransition{
			OperationID:   operationID,
			Ordinal:       lastTransition.Ordinal + 1,
			PreviousState: &previousState,
			State:         domain.OperationSucceeded,
			Phase:         phase,
			OccurredAt:    at,
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		expectedTransition.Sequence = sequence
		deleted := tx.Where("id = ? AND project_id = ?", project.Id, project.ProjectId).Delete(&models.Project{})
		if deleted.Error != nil {
			return fmt.Errorf("delete project: %w", deleted.Error)
		}
		if deleted.RowsAffected != 1 {
			return corruptStateError("project", string(projectID), fmt.Errorf("delete affected %d rows, expected 1", deleted.RowsAffected))
		}

		nextRow, err := operationModelFromDomain(nextOperation, sequence)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.Operation{}).
			Where("id = ? AND revision = ?", string(operationID), expectedModelRevision).
			Updates(operationUpdateColumns(nextRow))
		if updated.Error != nil {
			return fmt.Errorf("complete project unregister operation: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return &StaleRevisionError{OperationID: operationID, Expected: expectedRevision, Actual: current.Revision}
		}
		transitionRow, err := operationTransitionModelFromDomain(expectedTransition)
		if err != nil {
			return err
		}
		if err := requireOneCreate(tx.Create(&transitionRow), "append project unregister transition", string(operationID)); err != nil {
			return err
		}
		result = OperationRecord{Operation: nextOperation, Revision: sequence}
		persistedOperation, persistedHistory, err := readProjectUnregisterOperationState(tx, operationID)
		if err != nil {
			return err
		}
		if err := validateProjectUnregisterOperationReadback(
			history,
			operationID,
			result,
			expectedTransition,
			persistedOperation,
			persistedHistory,
		); err != nil {
			return err
		}
		if network.initialized {
			persisted, err := readProjectUnregisterNetworkState(tx, projectID)
			if err != nil {
				return err
			}
			if err := validateProjectUnregisterNetworkReadback(
				network,
				persisted,
				projectID,
				operationID,
				result,
				persistedHistory,
			); err != nil {
				return err
			}
			if persisted.boundary.found {
				result = persisted.boundary.owner.operation
			}
		}
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("complete project unregister %q: %w", projectID, err)
	}
	return result, nil
}

// validateProjectUnregisterOperationReadback proves finalization retained the exact materialized record and appended only its expected edge.
func validateProjectUnregisterOperationReadback(
	beforeHistory []OperationTransition,
	operationID domain.OperationID,
	operation OperationRecord,
	transition OperationTransition,
	persistedOperation OperationRecord,
	persistedHistory []OperationTransition,
) error {
	if !reflect.DeepEqual(persistedOperation, operation) {
		return corruptStateError(
			"operation",
			string(operationID),
			fmt.Errorf("unregister readback differs from the committed transition"),
		)
	}
	if len(persistedHistory) != len(beforeHistory)+1 ||
		!reflect.DeepEqual(persistedHistory[:len(beforeHistory)], beforeHistory) ||
		!reflect.DeepEqual(persistedHistory[len(persistedHistory)-1], transition) {
		return corruptStateError(
			"operation",
			string(operationID),
			fmt.Errorf("unregister history readback differs from the committed transition"),
		)
	}
	return nil
}

// readProjectUnregisterOperationState proves one materialized unregister record and its complete retained history share every sequence owner.
func readProjectUnregisterOperationState(
	tx *gorm.DB,
	operationID domain.OperationID,
) (OperationRecord, []OperationTransition, error) {
	row, found, err := findOperationForMutation(tx, operationID)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if !found {
		return OperationRecord{}, nil, &OperationNotFoundError{OperationID: operationID}
	}
	record, err := operationRecordFromModel(row)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if err := validateOperationHistorySequenceOwners(tx, record, history); err != nil {
		return OperationRecord{}, nil, err
	}
	return record, history, nil
}

// projectUnregisterNetworkState retains the complete optional network proof needed around final deletion.
type projectUnregisterNetworkState struct {
	initialized bool
	highWater   domain.Sequence
	record      NetworkRecord
	rows        networkModelRows
	boundary    projectNetworkReleaseBoundary
}

// readProjectUnregisterNetworkState distinguishes legacy and uninitialized databases from an authoritative network root.
func readProjectUnregisterNetworkState(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (projectUnregisterNetworkState, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil || !present {
		return projectUnregisterNetworkState{}, err
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return projectUnregisterNetworkState{}, err
	}
	record, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return projectUnregisterNetworkState{}, err
	}
	if !initialized {
		return projectUnregisterNetworkState{rows: rows}, nil
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return projectUnregisterNetworkState{}, err
	}
	owners, err := validateProjectNetworkReleaseOwners(tx, highWater, rows.Releases)
	if err != nil {
		return projectUnregisterNetworkState{}, err
	}
	state := projectUnregisterNetworkState{
		initialized: true,
		highWater:   highWater,
		record:      record,
		rows:        rows,
	}
	for _, row := range rows.Releases {
		if row.SourceProjectId != string(projectID) {
			continue
		}
		state.boundary = projectNetworkReleaseBoundary{
			found: true,
			row:   row,
			owner: owners[domain.OperationID(row.OperationId)],
		}
		break
	}
	return state, nil
}

// requireCompletedProjectNetworkRelease makes durable host teardown a precondition only after network initialization.
func requireCompletedProjectNetworkRelease(
	state projectUnregisterNetworkState,
	projectID domain.ProjectID,
	operationID domain.OperationID,
) error {
	if !state.initialized {
		return nil
	}
	if !state.boundary.found {
		if !projectHasActiveNetworkClaims(state.record, projectID) {
			return nil
		}
		return &ProjectNetworkReleaseNotFoundError{ProjectID: projectID, OperationID: operationID}
	}
	durableOperationID := domain.OperationID(state.boundary.row.OperationId)
	if durableOperationID != operationID {
		return projectNetworkReleaseConflict(projectID, operationID, "operation owner")
	}
	releaseState := ProjectNetworkReleaseState(state.boundary.row.State)
	if releaseState != ProjectNetworkReleaseCompleted {
		return &ProjectNetworkReleaseIncompleteError{
			ProjectID:   projectID,
			OperationID: operationID,
			State:       releaseState,
		}
	}
	return nil
}

// projectHasActiveNetworkClaims reports whether deletion still requires route suppression or host identity release.
func projectHasActiveNetworkClaims(record NetworkRecord, projectID domain.ProjectID) bool {
	for _, lease := range record.Leases {
		if lease.Key.ProjectID == projectID {
			return true
		}
	}
	for _, endpoint := range record.Reservations.Endpoints {
		if endpoint.Key.ProjectID == projectID {
			return true
		}
	}
	for _, suppressedProjectID := range record.Reservations.SuppressedProjectIDs {
		if suppressedProjectID == projectID {
			return true
		}
	}
	return false
}

// validateProjectUnregisterNetworkReadback proves final deletion retained every network fact and only removed its project owner.
func validateProjectUnregisterNetworkReadback(
	before projectUnregisterNetworkState,
	after projectUnregisterNetworkState,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	operation OperationRecord,
	persistedHistory []OperationTransition,
) error {
	if !after.initialized {
		return corruptStateError(
			"network state",
			"1",
			fmt.Errorf("initialized network disappeared during project deletion"),
		)
	}
	if after.highWater != operation.Revision {
		return corruptStateError(
			"Harbor sequence",
			fmt.Sprint(after.highWater),
			fmt.Errorf("project unregister allocated revision %d", operation.Revision),
		)
	}
	if before.boundary.found {
		if !after.boundary.found {
			return corruptStateError(
				"network project release",
				string(operationID),
				fmt.Errorf("completed boundary disappeared during project deletion"),
			)
		}
		if after.boundary.owner.projectExists {
			return corruptStateError(
				"project",
				string(projectID),
				fmt.Errorf("project remains visible after deletion"),
			)
		}
		if !reflect.DeepEqual(after.boundary.owner.operation, operation) {
			return corruptStateError(
				"operation",
				string(operationID),
				fmt.Errorf("unregister readback differs from the committed transition"),
			)
		}
		if !reflect.DeepEqual(after.boundary.owner.history, persistedHistory) {
			return corruptStateError(
				"operation",
				string(operationID),
				fmt.Errorf("unregister history readback differs from the committed transition"),
			)
		}
	} else if after.boundary.found {
		return corruptStateError(
			"network project release",
			string(operationID),
			fmt.Errorf("claimless project deletion created a release boundary"),
		)
	}

	expectedRows := before.rows
	expectedRows.Projects = make([]models.Project, 0, len(before.rows.Projects))
	for _, row := range before.rows.Projects {
		if row.ProjectId != string(projectID) {
			expectedRows.Projects = append(expectedRows.Projects, row)
		}
	}
	if !reflect.DeepEqual(after.rows, expectedRows) {
		return corruptStateError(
			"network state",
			"1",
			fmt.Errorf("project unregister changed durable network facts"),
		)
	}
	return nil
}

// findOperationForMutation distinguishes absence from duplicated operation ownership inside a write transaction.
func findOperationForMutation(tx *gorm.DB, operationID domain.OperationID) (models.Operation, bool, error) {
	var rows []models.Operation
	if err := tx.Where("id = ?", string(operationID)).Order("revision ASC").Find(&rows).Error; err != nil {
		return models.Operation{}, false, fmt.Errorf("find operation: %w", err)
	}
	if len(rows) == 0 {
		return models.Operation{}, false, nil
	}
	if len(rows) != 1 {
		return models.Operation{}, false, corruptStateError("operation", string(operationID), fmt.Errorf("operation ID is duplicated"))
	}
	return rows[0], true, nil
}

// replayCompletedProjectUnregister accepts only the exact retry of the transition that atomically removed the project.
func replayCompletedProjectUnregister(
	current OperationRecord,
	history []OperationTransition,
	expectedRevision domain.Sequence,
	phase string,
) (OperationRecord, error) {
	if current.Operation.State != domain.OperationSucceeded {
		return OperationRecord{}, corruptStateError(
			"project unregister",
			string(current.Operation.ProjectID),
			fmt.Errorf("project is absent while operation %q is %q", current.Operation.ID, current.Operation.State),
		)
	}
	if len(history) < 2 {
		return OperationRecord{}, corruptStateError("operation", string(current.Operation.ID), fmt.Errorf("completed unregister history has no preceding transition"))
	}
	preceding := history[len(history)-2]
	if preceding.Sequence != expectedRevision {
		return OperationRecord{}, &StaleRevisionError{
			OperationID: current.Operation.ID,
			Expected:    expectedRevision,
			Actual:      preceding.Sequence,
		}
	}
	if current.Operation.Phase != phase {
		return OperationRecord{}, fmt.Errorf(
			"completed project unregister phase is %q, not requested phase %q",
			current.Operation.Phase,
			phase,
		)
	}
	return current, nil
}

// validateOperationHistorySequenceOwners proves the unregister journal exclusively owns every retained edge.
func validateOperationHistorySequenceOwners(
	tx *gorm.DB,
	current OperationRecord,
	history []OperationTransition,
) error {
	expected := make(map[int]OperationTransition, len(history))
	for _, transition := range history {
		sequence := int(transition.Sequence)
		expected[sequence] = transition
	}

	var projects []models.Project
	if err := tx.
		Select("id", "project_id", "revision").
		Where("revision IN (?)", unregisterHistorySequences(tx, current.Operation.ID)).
		Order("revision ASC").
		Order("id ASC").
		Find(&projects).Error; err != nil {
		return fmt.Errorf("verify unregister history project owners: %w", err)
	}
	if len(projects) != 0 {
		project := projects[0]
		return sequenceOwnerCollision(
			project.Revision,
			operationHistorySequenceOwner(expected[project.Revision]),
			"project "+strconv.Quote(project.ProjectId),
		)
	}

	var recents []models.RecentResource
	if err := tx.
		Select("id", "project_id", "resource_id", "sequence").
		Where("sequence IN (?)", unregisterHistorySequences(tx, current.Operation.ID)).
		Order("sequence ASC").
		Order("id ASC").
		Find(&recents).Error; err != nil {
		return fmt.Errorf("verify unregister history recent owners: %w", err)
	}
	if len(recents) != 0 {
		recent := recents[0]
		return sequenceOwnerCollision(
			recent.Sequence,
			operationHistorySequenceOwner(expected[recent.Sequence]),
			fmt.Sprintf("recent resource %q/%q", recent.ProjectId, recent.ResourceId),
		)
	}

	var operations []models.Operation
	if err := tx.
		Select("id", "revision").
		Where("revision IN (?)", unregisterHistorySequences(tx, current.Operation.ID)).
		Order("revision ASC").
		Order("id ASC").
		Find(&operations).Error; err != nil {
		return fmt.Errorf("verify unregister history operation owners: %w", err)
	}
	matchingCurrent := 0
	for _, operation := range operations {
		if operation.Id == string(current.Operation.ID) && operation.Revision == int(current.Revision) {
			matchingCurrent++
			continue
		}
		return sequenceOwnerCollision(
			operation.Revision,
			operationHistorySequenceOwner(expected[operation.Revision]),
			"operation "+strconv.Quote(operation.Id),
		)
	}
	if matchingCurrent != 1 {
		return corruptStateError(
			"Harbor sequence",
			strconv.FormatUint(uint64(current.Revision), 10),
			fmt.Errorf("operation %q does not exclusively mirror its latest transition", current.Operation.ID),
		)
	}

	var transitions []models.OperationTransition
	if err := tx.
		Select("id", "operation_id", "ordinal", "sequence").
		Where("sequence IN (?)", unregisterHistorySequences(tx, current.Operation.ID)).
		Order("sequence ASC").
		Order("id ASC").
		Find(&transitions).Error; err != nil {
		return fmt.Errorf("verify unregister history transition owners: %w", err)
	}
	if len(transitions) != len(history) {
		return corruptStateError(
			"operation",
			string(current.Operation.ID),
			fmt.Errorf("history owns %d global sequences but %d transition rows reuse them", len(history), len(transitions)),
		)
	}
	for _, row := range transitions {
		transition, exists := expected[row.Sequence]
		if !exists || row.OperationId != string(transition.OperationID) || row.Ordinal != int(transition.Ordinal) {
			return sequenceOwnerCollision(
				row.Sequence,
				operationHistorySequenceOwner(transition),
				fmt.Sprintf("operation transition %q ordinal %d", row.OperationId, row.Ordinal),
			)
		}
	}
	networkRevision, networkExists, err := readOptionalNetworkSequenceOwner(tx)
	if err != nil {
		return err
	}
	if networkExists {
		if transition, exists := expected[int(networkRevision)]; exists {
			return sequenceOwnerCollision(
				int(networkRevision),
				operationHistorySequenceOwner(transition),
				"network state",
			)
		}
	}
	return nil
}

// unregisterHistorySequences keeps long approval histories out of SQLite's bound-variable limit.
func unregisterHistorySequences(tx *gorm.DB, operationID domain.OperationID) *gorm.DB {
	return tx.Model(&models.OperationTransition{}).
		Select("sequence").
		Where("operation_id = ?", string(operationID))
}

// operationHistorySequenceOwner names the exact transition expected to own one global sequence.
func operationHistorySequenceOwner(transition OperationTransition) string {
	return fmt.Sprintf("operation transition %q ordinal %d", transition.OperationID, transition.Ordinal)
}

// validateProjectRecentSequenceOwners validates every recency row the project cascade will remove.
func validateProjectRecentSequenceOwners(tx *gorm.DB, project ProjectRecord) error {
	var rows []models.RecentResource
	if err := tx.
		Where("project_id = ?", string(project.Project.ID)).
		Order("resource_id ASC").
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read project recent resources before unregister: %w", err)
	}
	resources := make(map[domain.ResourceID]struct{}, len(project.Project.Resources))
	for _, resource := range project.Project.Resources {
		resources[resource.ID] = struct{}{}
	}
	seen := make(map[domain.ResourceID]struct{}, len(rows))
	for _, row := range rows {
		record, err := recentResourceRecordFromModel(row)
		if err != nil {
			return err
		}
		if _, exists := resources[record.Reference.ResourceID]; !exists {
			return corruptStateError(
				"recent resource",
				scopedMutationKey(record.Reference),
				fmt.Errorf("project resource is missing"),
			)
		}
		if _, exists := seen[record.Reference.ResourceID]; exists {
			return corruptStateError(
				"recent resource",
				scopedMutationKey(record.Reference),
				fmt.Errorf("resource reference is duplicated"),
			)
		}
		seen[record.Reference.ResourceID] = struct{}{}
		if err := validateRecentSequenceOwner(tx, record); err != nil {
			return err
		}
	}
	return nil
}

// RecordRecentResource moves one existing resource to the head of the durable global recency order.
func (store *Store) RecordRecentResource(ctx context.Context, reference domain.ResourceRef) (RecentResourceRecord, error) {
	ctx = normalizeContext(ctx)
	if err := reference.Validate(); err != nil {
		return RecentResourceRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return RecentResourceRecord{}, err
	}

	var result RecentResourceRecord
	err := store.mutations.mutate(ctx, "recent resource", func(tx *gorm.DB) error {
		boundary, err := readProjectNetworkReleaseBoundary(tx, reference.ProjectID)
		if err != nil {
			return err
		}
		if err := rejectProjectNetworkReleaseMutation(boundary, reference.ProjectID, "record recent resource"); err != nil {
			return err
		}
		accessedAt := store.now().UTC().Round(0)
		if err := validateStoredTime("recent resource access time", accessedAt); err != nil {
			return err
		}
		found, err := resourceExistsForMutation(tx, reference)
		if err != nil {
			return err
		}
		if !found {
			return &ResourceNotFoundError{Reference: reference}
		}
		if err := validateExistingRecentMutationSequenceOwner(tx, reference); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		row, err := recentResourceModelFromDomain(reference, accessedAt, sequence)
		if err != nil {
			return err
		}
		if err := upsertRecentResource(tx, row); err != nil {
			return err
		}
		result, err = readRecentResourceForMutation(tx, reference)
		if err != nil {
			return err
		}
		if result.Sequence != sequence {
			return corruptStateError("recent resource", scopedMutationKey(reference), fmt.Errorf("readback sequence is %d, expected %d", result.Sequence, sequence))
		}
		expected := RecentResourceRecord{Reference: reference, AccessedAt: accessedAt, Sequence: sequence}
		if result != expected {
			return corruptStateError("recent resource", scopedMutationKey(reference), fmt.Errorf("readback record differs from the requested recency update"))
		}
		return validateRecentSequenceOwner(tx, result)
	})
	if err != nil {
		return RecentResourceRecord{}, fmt.Errorf("record recent resource %q/%q: %w", reference.ProjectID, reference.ResourceID, err)
	}
	return result, nil
}

// putProjectAggregate applies the replacement in foreign-key-safe order so surviving resource identities retain recency.
func putProjectAggregate(
	tx *gorm.DB,
	project models.Project,
	apps []models.ProjectApp,
	services []models.ProjectService,
	resources []models.ProjectResource,
) error {
	if err := upsertProject(tx, project); err != nil {
		return err
	}
	existingApps, err := existingProjectApps(tx, project.ProjectId)
	if err != nil {
		return err
	}
	existingServices, err := existingProjectServices(tx, project.ProjectId)
	if err != nil {
		return err
	}
	existingResources, err := existingProjectResources(tx, project.ProjectId)
	if err != nil {
		return err
	}

	for _, app := range apps {
		if err := upsertProjectApp(tx, app, existingApps[app.AppId]); err != nil {
			return err
		}
		delete(existingApps, app.AppId)
	}
	for _, service := range services {
		if err := upsertProjectService(tx, service, existingServices[service.ServiceId]); err != nil {
			return err
		}
		delete(existingServices, service.ServiceId)
	}
	for _, resource := range resources {
		if err := upsertProjectResource(tx, resource, existingResources[resource.ResourceId]); err != nil {
			return err
		}
		delete(existingResources, resource.ResourceId)
	}
	if err := deleteStaleProjectResources(tx, existingResources); err != nil {
		return err
	}
	if err := deleteStaleProjectApps(tx, existingApps); err != nil {
		return err
	}
	return deleteStaleProjectServices(tx, existingServices)
}

// upsertProject preserves the root surrogate ID while replacing every public aggregate field.
func upsertProject(tx *gorm.DB, desired models.Project) error {
	existing, found, err := findProjectForMutation(tx, domain.ProjectID(desired.ProjectId))
	if err != nil {
		return err
	}
	if !found {
		return requireOneCreate(tx.Create(&desired), "create project", desired.ProjectId)
	}
	updated := tx.Model(&models.Project{}).
		Where("id = ? AND project_id = ?", existing.Id, existing.ProjectId).
		Updates(map[string]any{
			"name":       desired.Name,
			"path":       desired.Path,
			"slug":       desired.Slug,
			"state":      desired.State,
			"favorite":   desired.Favorite,
			"updated_at": desired.UpdatedAt,
			"revision":   desired.Revision,
		})
	return requireOneMutation(updated, "update project", durableKey(desired.ProjectId, existing.Id))
}

// findProjectForMutation distinguishes absence from duplicated roots in schemas whose uniqueness was weakened.
func findProjectForMutation(tx *gorm.DB, projectID domain.ProjectID) (models.Project, bool, error) {
	var rows []models.Project
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.Project{}, false, fmt.Errorf("find project: %w", err)
	}
	if len(rows) == 0 {
		return models.Project{}, false, nil
	}
	if len(rows) != 1 {
		return models.Project{}, false, corruptStateError("project", string(projectID), fmt.Errorf("project ID is duplicated"))
	}
	if rows[0].Id <= 0 {
		return models.Project{}, false, corruptStateError("project", durableKey(rows[0].ProjectId, rows[0].Id), fmt.Errorf("database ID must be positive"))
	}
	return rows[0], true, nil
}

// existingProjectApps indexes every current App while refusing ambiguous duplicate identities.
func existingProjectApps(tx *gorm.DB, projectID string) (map[string]models.ProjectApp, error) {
	var rows []models.ProjectApp
	if err := tx.Where("project_id = ?", projectID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read existing project Apps: %w", err)
	}
	result := make(map[string]models.ProjectApp, len(rows))
	for _, row := range rows {
		if row.Id <= 0 {
			return nil, corruptStateError("project App", scopedKey(row.ProjectId, row.AppId, row.Id), fmt.Errorf("database ID must be positive"))
		}
		if _, exists := result[row.AppId]; exists {
			return nil, corruptStateError("project App", scopedKey(row.ProjectId, row.AppId, row.Id), fmt.Errorf("App ID is duplicated"))
		}
		result[row.AppId] = row
	}
	return result, nil
}

// existingProjectServices indexes every current service while refusing ambiguous duplicate identities.
func existingProjectServices(tx *gorm.DB, projectID string) (map[string]models.ProjectService, error) {
	var rows []models.ProjectService
	if err := tx.Where("project_id = ?", projectID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read existing project services: %w", err)
	}
	result := make(map[string]models.ProjectService, len(rows))
	for _, row := range rows {
		if row.Id <= 0 {
			return nil, corruptStateError("project service", scopedKey(row.ProjectId, row.ServiceId, row.Id), fmt.Errorf("database ID must be positive"))
		}
		if _, exists := result[row.ServiceId]; exists {
			return nil, corruptStateError("project service", scopedKey(row.ProjectId, row.ServiceId, row.Id), fmt.Errorf("service ID is duplicated"))
		}
		result[row.ServiceId] = row
	}
	return result, nil
}

// existingProjectResources indexes every current resource while refusing ambiguous duplicate identities.
func existingProjectResources(tx *gorm.DB, projectID string) (map[string]models.ProjectResource, error) {
	var rows []models.ProjectResource
	if err := tx.Where("project_id = ?", projectID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read existing project resources: %w", err)
	}
	result := make(map[string]models.ProjectResource, len(rows))
	for _, row := range rows {
		if row.Id <= 0 {
			return nil, corruptStateError("project resource", scopedKey(row.ProjectId, row.ResourceId, row.Id), fmt.Errorf("database ID must be positive"))
		}
		if _, exists := result[row.ResourceId]; exists {
			return nil, corruptStateError("project resource", scopedKey(row.ProjectId, row.ResourceId, row.Id), fmt.Errorf("resource ID is duplicated"))
		}
		result[row.ResourceId] = row
	}
	return result, nil
}

// upsertProjectApp preserves a surviving App's surrogate identity.
func upsertProjectApp(tx *gorm.DB, desired models.ProjectApp, existing models.ProjectApp) error {
	if existing.Id == 0 {
		return requireOneCreate(tx.Create(&desired), "create project App", scopedKey(desired.ProjectId, desired.AppId, desired.Id))
	}
	updated := tx.Model(&models.ProjectApp{}).
		Where("id = ? AND project_id = ? AND app_id = ?", existing.Id, desired.ProjectId, desired.AppId).
		Updates(map[string]any{
			"name":     desired.Name,
			"state":    desired.State,
			"active":   desired.Active,
			"required": desired.Required,
		})
	return requireOneMutation(updated, "update project App", scopedKey(desired.ProjectId, desired.AppId, existing.Id))
}

// upsertProjectService preserves a surviving service's surrogate identity.
func upsertProjectService(tx *gorm.DB, desired models.ProjectService, existing models.ProjectService) error {
	if existing.Id == 0 {
		return requireOneCreate(tx.Create(&desired), "create project service", scopedKey(desired.ProjectId, desired.ServiceId, desired.Id))
	}
	updated := tx.Model(&models.ProjectService{}).
		Where("id = ? AND project_id = ? AND service_id = ?", existing.Id, desired.ProjectId, desired.ServiceId).
		Updates(map[string]any{
			"name":      desired.Name,
			"kind":      desired.Kind,
			"state":     desired.State,
			"owner":     desired.Owner,
			"selection": desired.Selection,
			"required":  desired.Required,
		})
	return requireOneMutation(updated, "update project service", scopedKey(desired.ProjectId, desired.ServiceId, existing.Id))
}

// upsertProjectResource updates owners before stale parents are removed so surviving endpoints keep their identity and recency.
func upsertProjectResource(tx *gorm.DB, desired models.ProjectResource, existing models.ProjectResource) error {
	if existing.Id == 0 {
		return requireOneCreate(tx.Create(&desired), "create project resource", scopedKey(desired.ProjectId, desired.ResourceId, desired.Id))
	}
	updated := tx.Model(&models.ProjectResource{}).
		Where("id = ? AND project_id = ? AND resource_id = ?", existing.Id, desired.ProjectId, desired.ResourceId).
		Updates(map[string]any{
			"name":             desired.Name,
			"kind":             desired.Kind,
			"url":              desired.Url,
			"owner_kind":       desired.OwnerKind,
			"owner_app_id":     desired.OwnerAppId,
			"owner_service_id": desired.OwnerServiceId,
		})
	return requireOneMutation(updated, "update project resource", scopedKey(desired.ProjectId, desired.ResourceId, existing.Id))
}

// deleteStaleProjectResources removes endpoints before their obsolete owners while preserving surviving recency rows.
func deleteStaleProjectResources(tx *gorm.DB, stale map[string]models.ProjectResource) error {
	ids := sortedResourceDatabaseIDs(stale)
	return deleteProjectRows(tx, &models.ProjectResource{}, ids, "delete stale project resources")
}

// deleteStaleProjectApps removes Apps only after every surviving resource has moved to its desired owner.
func deleteStaleProjectApps(tx *gorm.DB, stale map[string]models.ProjectApp) error {
	ids := make([]int, 0, len(stale))
	for _, row := range stale {
		ids = append(ids, row.Id)
	}
	sort.Ints(ids)
	return deleteProjectRows(tx, &models.ProjectApp{}, ids, "delete stale project Apps")
}

// deleteStaleProjectServices removes services only after every surviving resource has moved to its desired owner.
func deleteStaleProjectServices(tx *gorm.DB, stale map[string]models.ProjectService) error {
	ids := make([]int, 0, len(stale))
	for _, row := range stale {
		ids = append(ids, row.Id)
	}
	sort.Ints(ids)
	return deleteProjectRows(tx, &models.ProjectService{}, ids, "delete stale project services")
}

// sortedResourceDatabaseIDs gives stale endpoint deletion deterministic diagnostics and lock order.
func sortedResourceDatabaseIDs(stale map[string]models.ProjectResource) []int {
	ids := make([]int, 0, len(stale))
	for _, row := range stale {
		ids = append(ids, row.Id)
	}
	sort.Ints(ids)
	return ids
}

// deleteProjectRows enforces that a stale-set delete cannot silently affect fewer or additional rows.
func deleteProjectRows(tx *gorm.DB, model any, ids []int, scope string) error {
	if len(ids) == 0 {
		return nil
	}
	deleted := tx.Where("id IN ?", ids).Delete(model)
	if deleted.Error != nil {
		return fmt.Errorf("%s: %w", scope, deleted.Error)
	}
	if deleted.RowsAffected != int64(len(ids)) {
		return corruptStateError(scope, fmt.Sprint(ids), fmt.Errorf("delete affected %d rows, expected %d", deleted.RowsAffected, len(ids)))
	}
	return nil
}

// requireOneMutation rejects stale or ambiguous update targets even if database constraints were weakened.
func requireOneMutation(result *gorm.DB, scope, key string) error {
	if result.Error != nil {
		return fmt.Errorf("%s: %w", scope, result.Error)
	}
	if result.RowsAffected != 1 {
		return corruptStateError(scope, key, fmt.Errorf("update affected %d rows, expected 1", result.RowsAffected))
	}
	return nil
}

// requireOneCreate rejects ignored inserts before incomplete aggregates can appear successful.
func requireOneCreate(result *gorm.DB, scope, key string) error {
	if result.Error != nil {
		return fmt.Errorf("%s: %w", scope, result.Error)
	}
	if result.RowsAffected != 1 {
		return corruptStateError(scope, key, fmt.Errorf("insert affected %d rows, expected 1", result.RowsAffected))
	}
	return nil
}

// mutationSequenceColumn describes one retained ordering column checked before allocation.
type mutationSequenceColumn struct {
	model  any
	column string
	owner  string
}

// mutationSequenceBound holds one index-extreme value while distinguishing an empty table from NULL.
type mutationSequenceBound struct {
	Value sql.NullInt64 `gorm:"column:value"`
}

// validateRetainedSequenceBounds checks index extrema against the singleton without scanning append-only history.
func validateRetainedSequenceBounds(tx *gorm.DB) (domain.Sequence, error) {
	highWater, err := readSnapshotSequence(tx)
	if err != nil {
		return 0, err
	}
	checks := []mutationSequenceColumn{
		{model: &models.Project{}, column: "revision", owner: "project revision"},
		{model: &models.RecentResource{}, column: "sequence", owner: "recent resource sequence"},
		{model: &models.Operation{}, column: "revision", owner: "operation revision"},
		{model: &models.OperationTransition{}, column: "sequence", owner: "operation transition sequence"},
	}
	for _, check := range checks {
		minimum, minimumExists, err := readMutationSequenceBound(tx, check, false)
		if err != nil {
			return 0, err
		}
		maximum, maximumExists, err := readMutationSequenceBound(tx, check, true)
		if err != nil {
			return 0, err
		}
		if !minimumExists && !maximumExists {
			continue
		}
		if minimumExists != maximumExists || !minimum.Valid || !maximum.Valid {
			return 0, corruptStateError("Harbor sequence", check.owner, fmt.Errorf("retained sequence must not be NULL"))
		}
		if _, err := validateMutationSequenceBound(highWater, maximum.Int64, check.owner+" maximum"); err != nil {
			return 0, err
		}
		if _, err := validateMutationSequenceBound(highWater, minimum.Int64, check.owner+" minimum"); err != nil {
			return 0, err
		}
	}
	if err := validateOptionalNetworkSequenceOwner(tx, highWater); err != nil {
		return 0, err
	}
	return highWater, nil
}

// readMutationSequenceBound uses the retained sequence index for one minimum or maximum lookup.
func readMutationSequenceBound(tx *gorm.DB, check mutationSequenceColumn, descending bool) (sql.NullInt64, bool, error) {
	direction := "ASC"
	label := "minimum"
	if descending {
		direction = "DESC"
		label = "maximum"
	}
	var rows []mutationSequenceBound
	if err := tx.
		Model(check.model).
		Select(check.column + " AS value").
		Order(check.column + " " + direction).
		Limit(1).
		Find(&rows).Error; err != nil {
		return sql.NullInt64{}, false, fmt.Errorf("read %s %s: %w", check.owner, label, err)
	}
	if len(rows) == 0 {
		return sql.NullInt64{}, false, nil
	}
	if len(rows) != 1 {
		return sql.NullInt64{}, false, corruptStateError("Harbor sequence", check.owner, fmt.Errorf("%s lookup returned %d rows", label, len(rows)))
	}
	return rows[0].Value, true, nil
}

// validateMutationSequenceBound rejects zero, negative, or future owners before the singleton can advance.
func validateMutationSequenceBound(highWater domain.Sequence, value int64, owner string) (domain.Sequence, error) {
	if value <= 0 {
		return 0, corruptStateError("Harbor sequence", strconv.FormatInt(value, 10), fmt.Errorf("%s uses a nonpositive sequence", owner))
	}
	sequence := domain.Sequence(value)
	if _, err := sequenceToModelInt("retained Harbor sequence", sequence, false); err != nil {
		return 0, corruptStateError("Harbor sequence", strconv.FormatInt(value, 10), fmt.Errorf("%s: %w", owner, err))
	}
	if sequence > highWater {
		return 0, corruptStateError(
			"Harbor sequence",
			strconv.FormatUint(uint64(sequence), 10),
			fmt.Errorf("%s sequence exceeds Harbor high-water %d", owner, highWater),
		)
	}
	return sequence, nil
}

// validateExistingProjectMutationSequenceOwner protects a target's old revision before PutProject overwrites it.
func validateExistingProjectMutationSequenceOwner(tx *gorm.DB, projectID domain.ProjectID) error {
	project, found, err := findProjectForMutation(tx, projectID)
	if err != nil || !found {
		return err
	}
	return validateProjectMutationSequenceOwner(tx, project)
}

// validateProjectMutationSequenceOwner proves one existing root exclusively owns its claimed revision.
func validateProjectMutationSequenceOwner(tx *gorm.DB, project models.Project) error {
	record := ProjectRecord{
		Project:  domain.ProjectSnapshot{ID: domain.ProjectID(project.ProjectId)},
		Revision: domain.Sequence(project.Revision),
	}
	return validateProjectSequenceOwner(tx, record)
}

// validateExistingRecentMutationSequenceOwner protects one recency row before an in-place update overwrites its sequence.
func validateExistingRecentMutationSequenceOwner(tx *gorm.DB, reference domain.ResourceRef) error {
	var rows []models.RecentResource
	if err := tx.
		Where("project_id = ? AND resource_id = ?", string(reference.ProjectID), string(reference.ResourceID)).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read existing recent resource sequence owner: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) != 1 {
		return corruptStateError("recent resource", scopedMutationKey(reference), fmt.Errorf("resource reference is duplicated"))
	}
	record, err := recentResourceRecordFromModel(rows[0])
	if err != nil {
		return err
	}
	return validateRecentSequenceOwner(tx, record)
}

// activeProjectOperationIDsExcluding returns validated nonterminal operation identities other than the named owner.
func activeProjectOperationIDsExcluding(
	tx *gorm.DB,
	projectID domain.ProjectID,
	excluded domain.OperationID,
) ([]domain.OperationID, error) {
	var rows []models.Operation
	if err := tx.
		Where("project_id = ?", string(projectID)).
		Where("state NOT IN ? OR state IS NULL", []string{
			string(domain.OperationSucceeded),
			string(domain.OperationFailed),
			string(domain.OperationCancelled),
		}).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read active project operations: %w", err)
	}
	identities := make([]domain.OperationID, 0, len(rows))
	seen := make(map[domain.OperationID]struct{}, len(rows))
	for _, row := range rows {
		record, err := operationRecordFromModel(row)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[record.Operation.ID]; exists {
			return nil, corruptStateError("operation", string(record.Operation.ID), fmt.Errorf("operation ID is duplicated"))
		}
		seen[record.Operation.ID] = struct{}{}
		if record.Operation.ID == excluded {
			continue
		}
		identities = append(identities, record.Operation.ID)
	}
	sort.Slice(identities, func(left, right int) bool { return identities[left] < identities[right] })
	return identities, nil
}

// resourceExistsForMutation distinguishes absence from duplicated references in weakened schemas.
func resourceExistsForMutation(tx *gorm.DB, reference domain.ResourceRef) (bool, error) {
	var rows []models.ProjectResource
	if err := tx.Where("project_id = ? AND resource_id = ?", string(reference.ProjectID), string(reference.ResourceID)).Order("id ASC").Find(&rows).Error; err != nil {
		return false, fmt.Errorf("find project resource: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) != 1 {
		return false, corruptStateError("project resource", scopedMutationKey(reference), fmt.Errorf("resource ID is duplicated"))
	}
	if rows[0].Id <= 0 {
		return false, corruptStateError("project resource", scopedKey(rows[0].ProjectId, rows[0].ResourceId, rows[0].Id), fmt.Errorf("database ID must be positive"))
	}
	if _, err := projectResourceFromModel(string(reference.ProjectID), rows[0]); err != nil {
		return false, err
	}
	_, err := readProjectRecord(tx, reference.ProjectID)
	if err != nil {
		var missing *ProjectNotFoundError
		if errors.As(err, &missing) {
			return false, corruptStateError("project resource", scopedMutationKey(reference), fmt.Errorf("parent project is missing"))
		}
		return false, err
	}
	return true, nil
}

// upsertRecentResource preserves the row surrogate while changing its position in global recency order.
func upsertRecentResource(tx *gorm.DB, desired models.RecentResource) error {
	var rows []models.RecentResource
	if err := tx.
		Where("project_id = ? AND resource_id = ?", desired.ProjectId, desired.ResourceId).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("find recent resource: %w", err)
	}
	if len(rows) > 1 {
		return corruptStateError("recent resource", scopedKey(desired.ProjectId, desired.ResourceId, rows[1].Id), fmt.Errorf("resource reference is duplicated"))
	}
	if len(rows) == 0 {
		return requireOneCreate(tx.Create(&desired), "create recent resource", scopedKey(desired.ProjectId, desired.ResourceId, desired.Id))
	}
	existing := rows[0]
	if existing.Id <= 0 {
		return corruptStateError("recent resource", scopedKey(existing.ProjectId, existing.ResourceId, existing.Id), fmt.Errorf("database ID must be positive"))
	}
	updated := tx.Model(&models.RecentResource{}).
		Where("id = ? AND project_id = ? AND resource_id = ?", existing.Id, desired.ProjectId, desired.ResourceId).
		Updates(map[string]any{"accessed_at": desired.AccessedAt, "sequence": desired.Sequence})
	return requireOneMutation(updated, "update recent resource", scopedKey(desired.ProjectId, desired.ResourceId, existing.Id))
}

// readRecentResourceForMutation enforces one canonical readback row after an in-place recency upsert.
func readRecentResourceForMutation(tx *gorm.DB, reference domain.ResourceRef) (RecentResourceRecord, error) {
	var rows []models.RecentResource
	if err := tx.Where("project_id = ? AND resource_id = ?", string(reference.ProjectID), string(reference.ResourceID)).Order("id ASC").Find(&rows).Error; err != nil {
		return RecentResourceRecord{}, fmt.Errorf("read recent resource after put: %w", err)
	}
	if len(rows) != 1 {
		return RecentResourceRecord{}, corruptStateError("recent resource", scopedMutationKey(reference), fmt.Errorf("readback contains %d rows, expected 1", len(rows)))
	}
	return recentResourceRecordFromModel(rows[0])
}

// validateRecentSequenceOwner proves one recency update did not reuse another durable owner's global position.
func validateRecentSequenceOwner(tx *gorm.DB, record RecentResourceRecord) error {
	sequence := int(record.Sequence)
	owner := fmt.Sprintf("recent resource %q/%q", record.Reference.ProjectID, record.Reference.ResourceID)

	var recents []models.RecentResource
	if err := tx.Select("id", "project_id", "resource_id").Where("sequence = ?", sequence).Order("id ASC").Find(&recents).Error; err != nil {
		return fmt.Errorf("verify recent resource sequence owner: %w", err)
	}
	if len(recents) != 1 || recents[0].ProjectId != string(record.Reference.ProjectID) || recents[0].ResourceId != string(record.Reference.ResourceID) {
		return corruptStateError("Harbor sequence", strconv.Itoa(sequence), fmt.Errorf("%s does not exclusively own its sequence", owner))
	}

	var projects []models.Project
	if err := tx.Select("id", "project_id").Where("revision = ?", sequence).Find(&projects).Error; err != nil {
		return fmt.Errorf("verify project sequence owner: %w", err)
	}
	if len(projects) != 0 {
		return sequenceOwnerCollision(sequence, owner, "project "+strconv.Quote(projects[0].ProjectId))
	}
	var operations []models.Operation
	if err := tx.Select("id").Where("revision = ?", sequence).Find(&operations).Error; err != nil {
		return fmt.Errorf("verify operation sequence owner: %w", err)
	}
	if len(operations) != 0 {
		return sequenceOwnerCollision(sequence, owner, "operation "+strconv.Quote(operations[0].Id))
	}
	var transitions []models.OperationTransition
	if err := tx.Select("id", "operation_id").Where("sequence = ?", sequence).Find(&transitions).Error; err != nil {
		return fmt.Errorf("verify operation transition sequence owner: %w", err)
	}
	if len(transitions) != 0 {
		return sequenceOwnerCollision(sequence, owner, "operation transition "+strconv.Quote(transitions[0].OperationId))
	}
	return validateNetworkSequenceCollision(tx, sequence, owner)
}

// scopedMutationKey formats the durable natural identity shared by resource and recency mutations.
func scopedMutationKey(reference domain.ResourceRef) string {
	return string(reference.ProjectID) + "/" + string(reference.ResourceID)
}

// canonicalProjectForMutation matches the deterministic identity order and UTC timestamp returned by normalized reads.
func canonicalProjectForMutation(project domain.ProjectSnapshot) domain.ProjectSnapshot {
	project.UpdatedAt = project.UpdatedAt.UTC().Round(0)
	project.Apps = append(make([]domain.AppSnapshot, 0, len(project.Apps)), project.Apps...)
	project.Services = append(make([]domain.ServiceSnapshot, 0, len(project.Services)), project.Services...)
	project.Resources = append(make([]domain.ResourceSnapshot, 0, len(project.Resources)), project.Resources...)
	sort.Slice(project.Apps, func(left, right int) bool { return project.Apps[left].ID < project.Apps[right].ID })
	sort.Slice(project.Services, func(left, right int) bool { return project.Services[left].ID < project.Services[right].ID })
	sort.Slice(project.Resources, func(left, right int) bool { return project.Resources[left].ID < project.Resources[right].ID })
	return project
}
