package state

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// Store owns the complete durable Harbor projection and its shared global ordering.
type Store struct {
	harborState *models.HarborStateRepo
	projects    *models.ProjectRepo
	mutations   *MutationCoordinator
	now         func() time.Time
}

// NewStore creates the aggregate store from generated named-database repositories and the shared writer coordinator.
func NewStore(
	harborState *models.HarborStateRepo,
	projects *models.ProjectRepo,
	mutations *MutationCoordinator,
) *Store {
	return newStore(harborState, projects, mutations, time.Now)
}

// newStore keeps snapshot capture time deterministic in persistence tests.
func newStore(
	harborState *models.HarborStateRepo,
	projects *models.ProjectRepo,
	mutations *MutationCoordinator,
	now func() time.Time,
) *Store {
	return &Store{
		harborState: harborState,
		projects:    projects,
		mutations:   mutations,
		now:         now,
	}
}

// CurrentSequence returns the singleton sequence after proving no competing durable authority row exists.
func (store *Store) CurrentSequence(ctx context.Context) (domain.Sequence, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	builder, err := store.harborState.WithContext(ctx).Builder()
	if err != nil {
		return 0, fmt.Errorf("open Harbor state: %w", err)
	}

	var sequence domain.Sequence
	if err := builder.Transaction(func(tx *gorm.DB) error {
		var readErr error
		sequence, readErr = readSnapshotSequence(tx)
		return readErr
	}); err != nil {
		return 0, fmt.Errorf("read Harbor sequence: %w", err)
	}
	return sequence, nil
}

// Project returns one complete project aggregate and the global revision that produced it.
func (store *Store) Project(ctx context.Context, projectID domain.ProjectID) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("open project state: %w", err)
	}

	var record ProjectRecord
	err = builder.Transaction(func(tx *gorm.DB) error {
		sequence, err := readSnapshotSequence(tx)
		if err != nil {
			return err
		}
		if err := validateOptionalNetworkSequenceOwner(tx, sequence); err != nil {
			return err
		}
		read, err := readProjectRecord(tx, projectID)
		if err != nil {
			return err
		}
		if err := validateVisibleSequence(sequence, read.Revision, "project "+strconv.Quote(string(projectID)), nil); err != nil {
			return err
		}
		if err := validateProjectSequenceOwner(tx, read); err != nil {
			return err
		}
		record = read
		return nil
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("read project %q: %w", projectID, err)
	}
	return record, nil
}

// Snapshot returns every client-visible project, active operation, and recent resource from one database instant.
func (store *Store) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("open Harbor snapshot: %w", err)
	}

	var snapshot domain.Snapshot
	err = builder.Transaction(func(tx *gorm.DB) error {
		sequence, err := readSnapshotSequence(tx)
		if err != nil {
			return err
		}
		if err := validateOptionalNetworkSequenceOwner(tx, sequence); err != nil {
			return err
		}
		projectRecords, err := readProjectRecords(tx)
		if err != nil {
			return err
		}
		operationRecords, err := readSnapshotOperations(tx, sequence)
		if err != nil {
			return err
		}
		recentRecords, err := readRecentResourceRecords(tx)
		if err != nil {
			return err
		}
		if err := validateVisibleSequences(sequence, projectRecords, operationRecords, recentRecords); err != nil {
			return err
		}
		if err := validateOperationSequenceHistory(tx, sequence, projectRecords, operationRecords, recentRecords); err != nil {
			return err
		}

		capturedAt := store.now().UTC().Round(0)
		if err := validateStoredTime("snapshot capture time", capturedAt); err != nil {
			return err
		}
		snapshot = domain.Snapshot{
			SchemaVersion:     domain.SnapshotSchemaVersion,
			Sequence:          sequence,
			CapturedAt:        capturedAt,
			Projects:          make([]domain.ProjectSnapshot, 0, len(projectRecords)),
			Operations:        make([]domain.Operation, 0, len(operationRecords)),
			RecentResourceIDs: make([]domain.ResourceRef, 0, len(recentRecords)),
		}
		for _, record := range projectRecords {
			snapshot.Projects = append(snapshot.Projects, record.Project)
		}
		for _, record := range operationRecords {
			snapshot.Operations = append(snapshot.Operations, record.Operation)
		}
		for _, record := range recentRecords {
			snapshot.RecentResourceIDs = append(snapshot.RecentResourceIDs, record.Reference)
		}
		if err := snapshot.Validate(); err != nil {
			return corruptStateError("snapshot", strconv.FormatUint(uint64(sequence), 10), err)
		}
		return nil
	})
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read Harbor snapshot: %w", err)
	}
	return snapshot, nil
}

// readProjectRecord loads one normalized project tree inside the caller's consistent read transaction.
func readProjectRecord(tx *gorm.DB, projectID domain.ProjectID) (ProjectRecord, error) {
	var projects []models.Project
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&projects).Error; err != nil {
		return ProjectRecord{}, fmt.Errorf("read project row: %w", err)
	}
	if len(projects) == 0 {
		return ProjectRecord{}, &ProjectNotFoundError{ProjectID: projectID}
	}
	if len(projects) != 1 {
		return ProjectRecord{}, corruptStateError("project", string(projectID), fmt.Errorf("project ID is duplicated"))
	}
	project := projects[0]

	apps, services, resources, err := readProjectChildren(tx, string(projectID))
	if err != nil {
		return ProjectRecord{}, err
	}
	return projectRecordFromModels(project, apps, services, resources)
}

// readProjectRecords loads every normalized project tree while rejecting orphaned child rows from weakened schemas.
func readProjectRecords(tx *gorm.DB) ([]ProjectRecord, error) {
	var projects []models.Project
	if err := tx.Order("project_id ASC").Find(&projects).Error; err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}
	var apps []models.ProjectApp
	if err := tx.Order("project_id ASC").Order("app_id ASC").Find(&apps).Error; err != nil {
		return nil, fmt.Errorf("read project Apps: %w", err)
	}
	var services []models.ProjectService
	if err := tx.Order("project_id ASC").Order("service_id ASC").Find(&services).Error; err != nil {
		return nil, fmt.Errorf("read project services: %w", err)
	}
	var resources []models.ProjectResource
	if err := tx.Order("project_id ASC").Order("resource_id ASC").Find(&resources).Error; err != nil {
		return nil, fmt.Errorf("read project resources: %w", err)
	}

	known := make(map[string]struct{}, len(projects))
	appsByProject := make(map[string][]models.ProjectApp, len(projects))
	servicesByProject := make(map[string][]models.ProjectService, len(projects))
	resourcesByProject := make(map[string][]models.ProjectResource, len(projects))
	for _, project := range projects {
		if _, exists := known[project.ProjectId]; exists {
			return nil, corruptStateError("project", durableKey(project.ProjectId, project.Id), fmt.Errorf("project ID is duplicated"))
		}
		known[project.ProjectId] = struct{}{}
		appsByProject[project.ProjectId] = []models.ProjectApp{}
		servicesByProject[project.ProjectId] = []models.ProjectService{}
		resourcesByProject[project.ProjectId] = []models.ProjectResource{}
	}
	for _, app := range apps {
		if _, exists := known[app.ProjectId]; !exists {
			return nil, corruptStateError("project App", scopedKey(app.ProjectId, app.AppId, app.Id), fmt.Errorf("parent project is missing"))
		}
		appsByProject[app.ProjectId] = append(appsByProject[app.ProjectId], app)
	}
	for _, service := range services {
		if _, exists := known[service.ProjectId]; !exists {
			return nil, corruptStateError("project service", scopedKey(service.ProjectId, service.ServiceId, service.Id), fmt.Errorf("parent project is missing"))
		}
		servicesByProject[service.ProjectId] = append(servicesByProject[service.ProjectId], service)
	}
	for _, resource := range resources {
		if _, exists := known[resource.ProjectId]; !exists {
			return nil, corruptStateError("project resource", scopedKey(resource.ProjectId, resource.ResourceId, resource.Id), fmt.Errorf("parent project is missing"))
		}
		resourcesByProject[resource.ProjectId] = append(resourcesByProject[resource.ProjectId], resource)
	}

	records := make([]ProjectRecord, 0, len(projects))
	for _, project := range projects {
		record, err := projectRecordFromModels(
			project,
			appsByProject[project.ProjectId],
			servicesByProject[project.ProjectId],
			resourcesByProject[project.ProjectId],
		)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sortProjectRecords(records)
	return records, nil
}

// readProjectChildren loads one project's children in the same canonical identity order used by complete snapshots.
func readProjectChildren(tx *gorm.DB, projectID string) ([]models.ProjectApp, []models.ProjectService, []models.ProjectResource, error) {
	apps := []models.ProjectApp{}
	if err := tx.Where("project_id = ?", projectID).Order("app_id ASC").Find(&apps).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("read project Apps: %w", err)
	}
	services := []models.ProjectService{}
	if err := tx.Where("project_id = ?", projectID).Order("service_id ASC").Find(&services).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("read project services: %w", err)
	}
	resources := []models.ProjectResource{}
	if err := tx.Where("project_id = ?", projectID).Order("resource_id ASC").Find(&resources).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("read project resources: %w", err)
	}
	return apps, services, resources, nil
}

// readRecentResourceRecords returns validated recency rows in newest-first global sequence order.
func readRecentResourceRecords(tx *gorm.DB) ([]RecentResourceRecord, error) {
	var rows []models.RecentResource
	if err := tx.Order("sequence DESC").Order("project_id ASC").Order("resource_id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read recent resources: %w", err)
	}
	records := make([]RecentResourceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := recentResourceRecordFromModel(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// validateVisibleSequences proves every materialized snapshot row owns one unique revision from the captured global history.
func validateVisibleSequences(
	maximum domain.Sequence,
	projects []ProjectRecord,
	operations []OperationRecord,
	recents []RecentResourceRecord,
) error {
	used := make(map[domain.Sequence]string, len(projects)+len(operations)+len(recents))
	for _, project := range projects {
		if err := validateVisibleSequence(maximum, project.Revision, "project "+strconv.Quote(string(project.Project.ID)), used); err != nil {
			return err
		}
	}
	for _, operation := range operations {
		if err := validateVisibleSequence(maximum, operation.Revision, "operation "+strconv.Quote(string(operation.Operation.ID)), used); err != nil {
			return err
		}
	}
	for _, recent := range recents {
		identity := fmt.Sprintf("recent resource %q/%q", recent.Reference.ProjectID, recent.Reference.ResourceID)
		if err := validateVisibleSequence(maximum, recent.Sequence, identity, used); err != nil {
			return err
		}
	}
	return nil
}

// validateVisibleSequence rejects future or reused revisions before clients treat them as a total order.
func validateVisibleSequence(maximum, sequence domain.Sequence, owner string, used map[domain.Sequence]string) error {
	if sequence == 0 {
		return corruptStateError("Harbor sequence", "0", fmt.Errorf("%s uses a zero revision", owner))
	}
	if sequence > maximum {
		return corruptStateError(
			"Harbor sequence",
			strconv.FormatUint(uint64(sequence), 10),
			fmt.Errorf("%s revision exceeds captured sequence %d", owner, maximum),
		)
	}
	if used != nil {
		if existing, exists := used[sequence]; exists {
			return corruptStateError(
				"Harbor sequence",
				strconv.FormatUint(uint64(sequence), 10),
				fmt.Errorf("%s reuses revision owned by %s", owner, existing),
			)
		}
		used[sequence] = owner
	}
	return nil
}

// validateProjectSequenceOwner rejects a singular read that cannot exclusively own its claimed global revision.
func validateProjectSequenceOwner(tx *gorm.DB, record ProjectRecord) error {
	sequence := int(record.Revision)
	owner := "project " + strconv.Quote(string(record.Project.ID))

	var projects []models.Project
	if err := tx.Select("id", "project_id").Where("revision = ?", sequence).Order("id ASC").Find(&projects).Error; err != nil {
		return fmt.Errorf("verify project sequence owner: %w", err)
	}
	if len(projects) != 1 || projects[0].ProjectId != string(record.Project.ID) {
		return corruptStateError(
			"Harbor sequence",
			strconv.Itoa(sequence),
			fmt.Errorf("%s does not exclusively own its revision", owner),
		)
	}

	var recents []models.RecentResource
	if err := tx.Select("id", "project_id", "resource_id").Where("sequence = ?", sequence).Find(&recents).Error; err != nil {
		return fmt.Errorf("verify recent resource sequence owner: %w", err)
	}
	if len(recents) != 0 {
		recent := recents[0]
		return sequenceOwnerCollision(
			sequence,
			owner,
			fmt.Sprintf("recent resource %q/%q", recent.ProjectId, recent.ResourceId),
		)
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

// sequenceOwnerCollision gives targeted and complete snapshot reads one corruption shape for cross-table reuse.
func sequenceOwnerCollision(sequence int, owner, existing string) error {
	return corruptStateError(
		"Harbor sequence",
		strconv.Itoa(sequence),
		fmt.Errorf("%s reuses revision owned by %s", owner, existing),
	)
}

// validateOperationSequenceHistory proves retained journal edges remain the sole owners of operation revisions.
func validateOperationSequenceHistory(
	tx *gorm.DB,
	maximum domain.Sequence,
	projects []ProjectRecord,
	operations []OperationRecord,
	recents []RecentResourceRecord,
) error {
	used := make(map[domain.Sequence]string, len(projects)+len(recents))
	for _, project := range projects {
		owner := "project " + strconv.Quote(string(project.Project.ID))
		if err := validateVisibleSequence(maximum, project.Revision, owner, used); err != nil {
			return err
		}
	}
	for _, recent := range recents {
		owner := fmt.Sprintf("recent resource %q/%q", recent.Reference.ProjectID, recent.Reference.ResourceID)
		if err := validateVisibleSequence(maximum, recent.Sequence, owner, used); err != nil {
			return err
		}
	}

	var rows []models.OperationTransition
	if err := tx.
		Order("operation_id ASC").
		Order("ordinal ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read operation sequence history: %w", err)
	}

	latestSequences := make(map[domain.OperationID]domain.Sequence)
	latestOrdinals := make(map[domain.OperationID]uint64)
	rowsByOperation := make(map[domain.OperationID][]models.OperationTransition)
	for _, row := range rows {
		transition, err := operationTransitionFromModel(row)
		if err != nil {
			return err
		}
		key := durableKey(row.OperationId, row.Id)
		operationID := transition.OperationID
		expectedOrdinal := latestOrdinals[operationID] + 1
		if transition.Ordinal != expectedOrdinal {
			return corruptStateError(
				"operation transition",
				key,
				fmt.Errorf("ordinal is %d, expected %d", transition.Ordinal, expectedOrdinal),
			)
		}
		sequence := transition.Sequence
		if previous := latestSequences[operationID]; previous != 0 && sequence <= previous {
			return corruptStateError("operation transition", key, fmt.Errorf("sequence must increase across operation history"))
		}
		owner := "operation transition " + strconv.Quote(string(operationID))
		if err := validateVisibleSequence(maximum, sequence, owner, used); err != nil {
			return err
		}
		latestSequences[operationID] = sequence
		latestOrdinals[operationID] = transition.Ordinal
		rowsByOperation[operationID] = append(rowsByOperation[operationID], row)
	}

	for _, operation := range operations {
		operationID := operation.Operation.ID
		history, err := operationTransitionsFromModels(rowsByOperation[operationID], operationID)
		if err != nil {
			return err
		}
		if err := validateOperationHistory(operation, history); err != nil {
			return err
		}
	}

	return nil
}

// sortProjectRecords keeps defensive callers canonical even when a test replaces the database ordering contract.
func sortProjectRecords(records []ProjectRecord) {
	sort.Slice(records, func(left, right int) bool {
		return records[left].Project.ID < records[right].Project.ID
	})
}
