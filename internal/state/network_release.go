package state

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"sort"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// ProjectNetworkRelease returns the durable recovery boundary owned by one unregister operation.
func (store *Store) ProjectNetworkRelease(
	ctx context.Context,
	operationID domain.OperationID,
) (ProjectNetworkReleaseRecord, bool, error) {
	if err := operationID.Validate(); err != nil {
		return ProjectNetworkReleaseRecord{}, false, err
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ProjectNetworkReleaseRecord{}, false, err
	}
	builder, err := store.networkState.WithContext(ctx).Builder()
	if err != nil {
		return ProjectNetworkReleaseRecord{}, false, fmt.Errorf("open network release state: %w", err)
	}

	var record ProjectNetworkReleaseRecord
	var found bool
	err = builder.Transaction(func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil || !present {
			return err
		}
		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		network, initialized, err := networkRecordFromModels(rows)
		if err != nil || !initialized {
			return err
		}
		highWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		if _, err := validateProjectNetworkReleaseOwners(tx, highWater, rows.Releases); err != nil {
			return err
		}
		row, exists := networkProjectReleaseRowByOperation(rows.Releases, operationID)
		if !exists {
			return nil
		}
		read, err := projectNetworkReleaseRecordFromRows(rows, network, row)
		if err != nil {
			return err
		}
		record = read
		found = true
		return nil
	})
	if err != nil {
		return ProjectNetworkReleaseRecord{}, false, fmt.Errorf("read project network release %q: %w", operationID, err)
	}
	return record, found, nil
}

// BeginProjectNetworkRelease atomically suppresses one project's routes while retaining exact recovery facts.
func (store *Store) BeginProjectNetworkRelease(
	ctx context.Context,
	request BeginProjectNetworkReleaseRequest,
) (ProjectNetworkReleaseMutationResult, error) {
	if err := request.Validate(); err != nil {
		return ProjectNetworkReleaseMutationResult{}, err
	}
	request = cloneBeginProjectNetworkReleaseRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ProjectNetworkReleaseMutationResult{}, err
	}

	var result ProjectNetworkReleaseMutationResult
	err := store.mutations.mutate(ctx, "project network release", func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("network persistence schema is not installed")
		}
		before, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		current, initialized, err := networkRecordFromModels(before)
		if err != nil {
			return err
		}
		if !initialized {
			return &NetworkNotInitializedError{}
		}
		highWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		existing, exists := networkProjectReleaseRowForRequest(
			before.Releases,
			request.ProjectID,
			request.OperationID,
		)
		existingOwners, err := validateProjectNetworkReleaseOwners(tx, highWater, before.Releases)
		if err != nil {
			return err
		}
		var owner projectNetworkReleaseOwner
		if exists {
			if difference := beginProjectNetworkReleaseDifference(existing, request); difference != "" {
				return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, difference)
			}
			owner = existingOwners[request.OperationID]
			if !owner.projectExists {
				return &ProjectNotFoundError{ProjectID: request.ProjectID}
			}
			operationIDs, err := activeProjectOperationIDsExcluding(tx, request.ProjectID, request.OperationID)
			if err != nil {
				return err
			}
			if len(operationIDs) != 0 {
				return &ProjectBusyError{ProjectID: request.ProjectID, OperationIDs: operationIDs}
			}
			if current.Revision < request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: current.Revision}
			}
			if owner.project.Revision < request.ExpectedProjectRevision {
				return &ProjectRevisionConflictError{
					ProjectID: request.ProjectID,
					Expected:  request.ExpectedProjectRevision,
					Actual:    owner.project.Revision,
				}
			}
			if owner.operation.Revision < request.ExpectedOperationRevision {
				return &StaleRevisionError{
					OperationID: request.OperationID,
					Expected:    request.ExpectedOperationRevision,
					Actual:      owner.operation.Revision,
				}
			}
			release, err := projectNetworkReleaseRecordFromRows(before, current, existing)
			if err != nil {
				return err
			}
			result = ProjectNetworkReleaseMutationResult{Record: current, Release: release, Replayed: true}
			return result.Validate()
		}
		owner, err = readProjectNetworkReleaseOwner(
			tx,
			highWater,
			request.ProjectID,
			request.OperationID,
		)
		if err != nil {
			return err
		}

		if !owner.projectExists {
			return &ProjectNotFoundError{ProjectID: request.ProjectID}
		}
		operationIDs, err := activeProjectOperationIDsExcluding(tx, request.ProjectID, request.OperationID)
		if err != nil {
			return err
		}
		if len(operationIDs) != 0 {
			return &ProjectBusyError{ProjectID: request.ProjectID, OperationIDs: operationIDs}
		}
		if owner.operation.Operation.State != domain.OperationRunning {
			return projectNetworkReleaseConflict(
				request.ProjectID,
				request.OperationID,
				"operation state",
			)
		}
		if current.Revision != request.ExpectedNetworkRevision {
			return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: current.Revision}
		}
		if owner.project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    owner.project.Revision,
			}
		}
		if owner.operation.Revision != request.ExpectedOperationRevision {
			return &StaleRevisionError{
				OperationID: request.OperationID,
				Expected:    request.ExpectedOperationRevision,
				Actual:      owner.operation.Revision,
			}
		}
		if request.At.Before(current.UpdatedAt) {
			return projectNetworkReleaseConflict(
				request.ProjectID,
				request.OperationID,
				"begin time",
			)
		}
		if owner.operation.Operation.StartedAt == nil || request.At.Before(*owner.operation.Operation.StartedAt) {
			return projectNetworkReleaseConflict(
				request.ProjectID,
				request.OperationID,
				"begin time",
			)
		}

		row := models.NetworkProjectRelease{
			NetworkStateId:  networkStateSingletonID,
			ProjectId:       null.StringFrom(string(request.ProjectID)),
			SourceProjectId: string(request.ProjectID),
			OperationId:     string(request.OperationID),
			State:           string(ProjectNetworkReleaseReleasing),
			BeginGeneration: int(request.BeginGeneration),
			BeganAt:         request.At,
		}
		if err := requireOneCreate(
			tx.Create(&row),
			"create project network release",
			string(request.OperationID),
		); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.NetworkState{}).
			Where("id = ? AND revision = ?", networkStateSingletonID, int(request.ExpectedNetworkRevision)).
			Updates(map[string]any{"updated_at": request.At, "revision": int(sequence)})
		if err := requireOneMutation(updated, "stage project network release", string(request.ProjectID)); err != nil {
			return err
		}

		after, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read staged project network release: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(after)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after release staging"))
		}
		persistedRow, exists := networkProjectReleaseRowByOperation(after.Releases, request.OperationID)
		if !exists {
			return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("row is missing after staging"))
		}
		release, err := projectNetworkReleaseRecordFromRows(after, persisted, persistedRow)
		if err != nil {
			return err
		}
		expected := beginProjectNetworkReleaseProjection(current, request, sequence)
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("network state", "1", fmt.Errorf("staged release projection differs from its preflighted suppression"))
		}
		if err := validateBeginProjectNetworkReleaseRows(before, after, request, row.Id); err != nil {
			return err
		}
		persistedOwner, err := readProjectNetworkReleaseOwner(
			tx,
			sequence,
			request.ProjectID,
			request.OperationID,
		)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(persistedOwner, owner) {
			return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("project or operation owner changed during staging"))
		}
		if err := validateProjectNetworkReleaseOwnerLifecycle(ProjectNetworkReleaseReleasing, persistedOwner); err != nil {
			return err
		}
		finalHighWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		if finalHighWater != sequence {
			return corruptStateError(
				"Harbor sequence",
				fmt.Sprint(finalHighWater),
				fmt.Errorf("release staging allocated revision %d", sequence),
			)
		}
		result = ProjectNetworkReleaseMutationResult{Record: persisted, Release: release, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return ProjectNetworkReleaseMutationResult{}, fmt.Errorf(
			"begin project network release %q: %w",
			request.ProjectID,
			err,
		)
	}
	return result, nil
}

// CompleteProjectNetworkRelease atomically commits exact host-release facts and the durable release-set digest.
func (store *Store) CompleteProjectNetworkRelease(
	ctx context.Context,
	request CompleteProjectNetworkReleaseRequest,
) (ProjectNetworkReleaseMutationResult, error) {
	if err := request.Validate(); err != nil {
		return ProjectNetworkReleaseMutationResult{}, err
	}
	request = cloneCompleteProjectNetworkReleaseRequest(request)
	releaseSetDigest := projectNetworkReleaseSetDigest(request.Releases)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ProjectNetworkReleaseMutationResult{}, err
	}

	var result ProjectNetworkReleaseMutationResult
	err := store.mutations.mutate(ctx, "project network release completion", func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("network persistence schema is not installed")
		}
		before, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		current, initialized, err := networkRecordFromModels(before)
		if err != nil {
			return err
		}
		if !initialized {
			return &NetworkNotInitializedError{}
		}
		highWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		existing, exists := networkProjectReleaseRowForRequest(
			before.Releases,
			request.ProjectID,
			request.OperationID,
		)
		owners, err := validateProjectNetworkReleaseOwners(tx, highWater, before.Releases)
		if err != nil {
			return err
		}
		if !exists {
			return &ProjectNetworkReleaseNotFoundError{
				ProjectID:   request.ProjectID,
				OperationID: request.OperationID,
			}
		}
		if difference := completeProjectNetworkReleaseIdentityDifference(existing, request); difference != "" {
			return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, difference)
		}
		owner := owners[request.OperationID]
		if existing.State == string(ProjectNetworkReleaseCompleted) {
			if difference := completedProjectNetworkReleaseDifference(existing, request, releaseSetDigest); difference != "" {
				return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, difference)
			}
			if owner.projectExists {
				operationIDs, err := activeProjectOperationIDsExcluding(tx, request.ProjectID, request.OperationID)
				if err != nil {
					return err
				}
				if len(operationIDs) != 0 {
					return &ProjectBusyError{ProjectID: request.ProjectID, OperationIDs: operationIDs}
				}
			}
			if current.Revision < request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: current.Revision}
			}
			if owner.projectExists && owner.project.Revision < request.ExpectedProjectRevision {
				return &ProjectRevisionConflictError{
					ProjectID: request.ProjectID,
					Expected:  request.ExpectedProjectRevision,
					Actual:    owner.project.Revision,
				}
			}
			if owner.operation.Revision < request.ExpectedOperationRevision {
				return &StaleRevisionError{
					OperationID: request.OperationID,
					Expected:    request.ExpectedOperationRevision,
					Actual:      owner.operation.Revision,
				}
			}
			release, err := projectNetworkReleaseRecordFromRows(before, current, existing)
			if err != nil {
				return err
			}
			result = ProjectNetworkReleaseMutationResult{Record: current, Release: release, Replayed: true}
			return result.Validate()
		}
		if existing.State != string(ProjectNetworkReleaseReleasing) {
			return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, "release state")
		}
		if !owner.projectExists {
			return corruptStateError(
				"network project release",
				string(request.OperationID),
				fmt.Errorf("releasing marker has no project"),
			)
		}
		operationIDs, err := activeProjectOperationIDsExcluding(tx, request.ProjectID, request.OperationID)
		if err != nil {
			return err
		}
		if len(operationIDs) != 0 {
			return &ProjectBusyError{ProjectID: request.ProjectID, OperationIDs: operationIDs}
		}
		if owner.operation.Operation.State != domain.OperationRunning {
			return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, "operation state")
		}
		if current.Revision != request.ExpectedNetworkRevision {
			return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: current.Revision}
		}
		if owner.project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    owner.project.Revision,
			}
		}
		if owner.operation.Revision != request.ExpectedOperationRevision {
			return &StaleRevisionError{
				OperationID: request.OperationID,
				Expected:    request.ExpectedOperationRevision,
				Actual:      owner.operation.Revision,
			}
		}
		if request.At.Before(current.UpdatedAt) || request.At.Before(existing.BeganAt) {
			return projectNetworkReleaseConflict(request.ProjectID, request.OperationID, "completion time")
		}

		plan, err := planCompleteProjectNetworkRelease(before, current, existing, request, releaseSetDigest)
		if err != nil {
			return err
		}
		if err := applyCompleteProjectNetworkRelease(tx, plan, request, releaseSetDigest); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.NetworkState{}).
			Where("id = ? AND revision = ?", networkStateSingletonID, int(request.ExpectedNetworkRevision)).
			Updates(map[string]any{"updated_at": request.At, "revision": int(sequence)})
		if err := requireOneMutation(updated, "complete project network release", string(request.ProjectID)); err != nil {
			return err
		}

		after, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read completed project network release: %w", err)
		}
		persisted, initialized, err := networkRecordFromModels(after)
		if err != nil {
			return err
		}
		if !initialized {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after release completion"))
		}
		persistedRow, exists := networkProjectReleaseRowByOperation(after.Releases, request.OperationID)
		if !exists {
			return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("row is missing after completion"))
		}
		release, err := projectNetworkReleaseRecordFromRows(after, persisted, persistedRow)
		if err != nil {
			return err
		}
		expected := plan.projection
		expected.Revision = sequence
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("network state", "1", fmt.Errorf("completed release projection differs from its preflighted teardown"))
		}
		if err := validateCompleteProjectNetworkReleaseRows(before, after, plan, request, releaseSetDigest); err != nil {
			return err
		}
		persistedOwners, err := validateProjectNetworkReleaseOwners(tx, sequence, after.Releases)
		if err != nil {
			return err
		}
		persistedOwner := persistedOwners[request.OperationID]
		if !reflect.DeepEqual(persistedOwner, owner) {
			return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("project or operation owner changed during completion"))
		}
		finalHighWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		if finalHighWater != sequence {
			return corruptStateError(
				"Harbor sequence",
				fmt.Sprint(finalHighWater),
				fmt.Errorf("release completion allocated revision %d", sequence),
			)
		}
		result = ProjectNetworkReleaseMutationResult{Record: persisted, Release: release, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return ProjectNetworkReleaseMutationResult{}, fmt.Errorf(
			"complete project network release %q: %w",
			request.ProjectID,
			err,
		)
	}
	return result, nil
}

// cloneCompleteProjectNetworkReleaseRequest isolates queued completion from caller-owned release facts.
func cloneCompleteProjectNetworkReleaseRequest(
	request CompleteProjectNetworkReleaseRequest,
) CompleteProjectNetworkReleaseRequest {
	request.Releases = slices.Clone(request.Releases)
	request.At = canonicalNetworkMutationTime(request.At)
	for index := range request.Releases {
		request.Releases[index].ReleasedAt = canonicalNetworkMutationTime(request.Releases[index].ReleasedAt)
		request.Releases[index].QuarantinedAt = canonicalNetworkMutationTime(request.Releases[index].QuarantinedAt)
		request.Releases[index].ReuseAfter = canonicalNetworkMutationTime(request.Releases[index].ReuseAfter)
	}
	return request
}

// projectNetworkReleaseCompletionAction binds one request fact to its exact active durable lease.
type projectNetworkReleaseCompletionAction struct {
	request NetworkLeaseRelease
	row     models.LoopbackAddressLease
}

// projectNetworkReleaseCompletionPlan is the preflighted complete write set and resulting public projection.
type projectNetworkReleaseCompletionPlan struct {
	marker         models.NetworkProjectRelease
	releases       []projectNetworkReleaseCompletionAction
	targetEndpoint int
	projection     NetworkRecord
}

// planCompleteProjectNetworkRelease requires the release bundle to cover every active target identity exactly once.
func planCompleteProjectNetworkRelease(
	rows networkModelRows,
	current NetworkRecord,
	marker models.NetworkProjectRelease,
	request CompleteProjectNetworkReleaseRequest,
	releaseSetDigest string,
) (projectNetworkReleaseCompletionPlan, error) {
	plan := projectNetworkReleaseCompletionPlan{
		marker:   marker,
		releases: make([]projectNetworkReleaseCompletionAction, 0, len(request.Releases)),
	}
	activeByKey := make(map[identity.LeaseKey]models.LoopbackAddressLease)
	for _, row := range rows.Leases {
		if row.State != "leased" || row.SourceProjectId != string(request.ProjectID) {
			continue
		}
		key, err := networkLeaseKeyFromModel(request.ProjectID, row.Kind, row.SecondaryId)
		if err != nil {
			return projectNetworkReleaseCompletionPlan{}, err
		}
		activeByKey[key] = row
	}
	if len(activeByKey) != len(request.Releases) {
		return projectNetworkReleaseCompletionPlan{}, projectNetworkReleaseConflict(
			request.ProjectID,
			request.OperationID,
			"release set",
		)
	}
	for _, release := range request.Releases {
		row, exists := activeByKey[release.Lease.Key]
		if !exists || row.Address != release.Lease.Address.String() ||
			row.OwnershipInstallationId != string(release.Lease.Ownership.InstallationID) ||
			row.OwnershipGeneration != int(release.Lease.Ownership.Generation) ||
			int(release.ReleaseGeneration) <= row.LeaseGeneration ||
			release.ReleasedAt.Before(row.LeasedAt) || release.QuarantinedAt.Before(row.LeasedAt) ||
			release.ReleasedAt.Before(marker.BeganAt) || release.QuarantinedAt.Before(marker.BeganAt) {
			return projectNetworkReleaseCompletionPlan{}, projectNetworkReleaseConflict(
				request.ProjectID,
				request.OperationID,
				"release set",
			)
		}
		plan.releases = append(plan.releases, projectNetworkReleaseCompletionAction{request: release, row: row})
		delete(activeByKey, release.Lease.Key)
	}
	if len(activeByKey) != 0 || projectNetworkReleaseSetDigest(request.Releases) != releaseSetDigest {
		return projectNetworkReleaseCompletionPlan{}, projectNetworkReleaseConflict(
			request.ProjectID,
			request.OperationID,
			"release set",
		)
	}
	for _, row := range rows.Endpoints {
		if row.ProjectId == string(request.ProjectID) {
			plan.targetEndpoint++
		}
	}

	leasing := make([]identity.Lease, 0, len(current.Leases)-len(request.Releases))
	for _, lease := range current.Leases {
		if lease.Key.ProjectID != request.ProjectID {
			leasing = append(leasing, lease)
		}
	}
	quarantines := make(map[netip.Addr]identity.Quarantine, len(current.Quarantines)+len(request.Releases))
	for _, quarantine := range current.Quarantines {
		quarantines[quarantine.Address] = quarantine
	}
	for _, release := range request.Releases {
		quarantines[release.Lease.Address] = identity.Quarantine{
			Address: release.Lease.Address,
			Reason:  release.QuarantineReason,
		}
	}
	quarantineProjection := make([]identity.Quarantine, 0, len(quarantines))
	for _, quarantine := range quarantines {
		quarantineProjection = append(quarantineProjection, quarantine)
	}
	endpoints := make([]EndpointReservation, 0, len(current.Reservations.Endpoints))
	for _, endpoint := range current.Reservations.Endpoints {
		if endpoint.Key.ProjectID != request.ProjectID {
			endpoints = append(endpoints, endpoint)
		}
	}
	projection := current
	projection.UpdatedAt = request.At
	projection.Leases = canonicalNetworkLeases(leasing)
	projection.Quarantines = canonicalNetworkQuarantines(quarantineProjection)
	projection.Reservations.Endpoints = canonicalEndpointReservations(endpoints)
	projection.Reservations.SuppressedProjectIDs = slices.Clone(current.Reservations.SuppressedProjectIDs)
	if err := projection.Validate(); err != nil {
		return projectNetworkReleaseCompletionPlan{}, projectNetworkReleaseConflict(
			request.ProjectID,
			request.OperationID,
			"release set",
		)
	}
	plan.projection = projection
	return plan, nil
}

// applyCompleteProjectNetworkRelease deletes routes before clearing their referenced address ownership.
func applyCompleteProjectNetworkRelease(
	tx *gorm.DB,
	plan projectNetworkReleaseCompletionPlan,
	request CompleteProjectNetworkReleaseRequest,
	releaseSetDigest string,
) error {
	deleted := tx.Where("project_id = ?", string(request.ProjectID)).Delete(&models.PublicEndpointLease{})
	if deleted.Error != nil {
		return fmt.Errorf("delete completed project network endpoints: %w", deleted.Error)
	}
	if deleted.RowsAffected != int64(plan.targetEndpoint) {
		return corruptStateError(
			"public endpoint lease",
			string(request.ProjectID),
			fmt.Errorf("delete affected %d rows, expected %d", deleted.RowsAffected, plan.targetEndpoint),
		)
	}
	for _, action := range plan.releases {
		release := action.request
		updated := tx.Model(&models.LoopbackAddressLease{}).
			Where(
				"id = ? AND state = ? AND project_id = ? AND address = ? AND lease_generation = ?",
				action.row.Id,
				"leased",
				string(request.ProjectID),
				action.row.Address,
				action.row.LeaseGeneration,
			).
			Updates(map[string]any{
				"project_id":         nil,
				"state":              "quarantined",
				"release_generation": int(release.ReleaseGeneration),
				"release_evidence":   release.ReleaseEvidence,
				"released_at":        release.ReleasedAt,
				"quarantined_at":     release.QuarantinedAt,
				"reuse_after":        release.ReuseAfter,
				"quarantine_reason":  release.QuarantineReason,
			})
		if err := requireOneMutation(updated, "quarantine completed project lease", networkInitializationLeaseKey(release.Lease.Key)); err != nil {
			return err
		}
	}
	updated := tx.Model(&models.NetworkProjectRelease{}).
		Where(
			"id = ? AND state = ? AND project_id = ? AND source_project_id = ? AND operation_id = ? AND begin_generation = ?",
			plan.marker.Id,
			string(ProjectNetworkReleaseReleasing),
			string(request.ProjectID),
			string(request.ProjectID),
			string(request.OperationID),
			int(request.ExpectedBeginGeneration),
		).
		Updates(map[string]any{
			"project_id":            nil,
			"state":                 string(ProjectNetworkReleaseCompleted),
			"completion_generation": int(request.CompletionGeneration),
			"completed_at":          request.At,
			"release_evidence":      request.ReleaseEvidence,
			"release_set_digest":    releaseSetDigest,
		})
	return requireOneMutation(updated, "complete network project release marker", string(request.OperationID))
}

// completeProjectNetworkReleaseIdentityDifference compares the immutable release owner and staged generation.
func completeProjectNetworkReleaseIdentityDifference(
	row models.NetworkProjectRelease,
	request CompleteProjectNetworkReleaseRequest,
) string {
	if row.SourceProjectId != string(request.ProjectID) {
		return "source project"
	}
	if row.OperationId != string(request.OperationID) {
		return "operation owner"
	}
	if row.BeginGeneration != int(request.ExpectedBeginGeneration) {
		return "begin generation"
	}
	return ""
}

// completedProjectNetworkReleaseDifference compares semantic completion before optimistic stale checks.
func completedProjectNetworkReleaseDifference(
	row models.NetworkProjectRelease,
	request CompleteProjectNetworkReleaseRequest,
	releaseSetDigest string,
) string {
	if row.State != string(ProjectNetworkReleaseCompleted) {
		return "release state"
	}
	if !row.CompletionGeneration.Valid || row.CompletionGeneration.Int64 != int64(request.CompletionGeneration) {
		return "completion generation"
	}
	if row.CompletedAt == nil || !row.CompletedAt.Equal(request.At) {
		return "completion time"
	}
	if !row.ReleaseEvidence.Valid || row.ReleaseEvidence.String != request.ReleaseEvidence {
		return "completion evidence"
	}
	if !row.ReleaseSetDigest.Valid || row.ReleaseSetDigest.String != releaseSetDigest {
		return "release set"
	}
	return ""
}

// validateCompleteProjectNetworkReleaseRows proves completion changed only target routes, leases, marker, and root.
func validateCompleteProjectNetworkReleaseRows(
	before networkModelRows,
	after networkModelRows,
	plan projectNetworkReleaseCompletionPlan,
	request CompleteProjectNetworkReleaseRequest,
	releaseSetDigest string,
) error {
	if len(before.States) != 1 || len(after.States) != 1 {
		return corruptStateError("network state", "1", fmt.Errorf("release completion readback has an invalid singleton count"))
	}
	beforeRoot := before.States[0]
	afterRoot := after.States[0]
	afterRoot.Revision = beforeRoot.Revision
	afterRoot.UpdatedAt = beforeRoot.UpdatedAt
	if !reflect.DeepEqual(afterRoot, beforeRoot) {
		return corruptStateError("network state", "1", fmt.Errorf("release completion changed immutable root fields"))
	}
	for _, comparison := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "network pool candidates", before: before.Candidates, after: after.Candidates},
		{name: "network setup evidence", before: before.SetupEvidence, after: after.SetupEvidence},
		{name: "network shared listeners", before: before.Listeners, after: after.Listeners},
		{name: "network projects", before: before.Projects, after: after.Projects},
		{name: "network release owners", before: before.ReleaseOwners, after: after.ReleaseOwners},
	} {
		if !reflect.DeepEqual(comparison.before, comparison.after) {
			return corruptStateError("network state", "1", fmt.Errorf("release completion changed %s", comparison.name))
		}
	}
	if err := validateCompleteProjectNetworkReleaseLeases(before.Leases, after.Leases, plan); err != nil {
		return err
	}
	if err := validateCompleteProjectNetworkReleaseEndpoints(before.Endpoints, after.Endpoints, request, plan.targetEndpoint); err != nil {
		return err
	}
	if len(before.Releases) != len(after.Releases) {
		return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("release row count changed"))
	}
	beforeByID := make(map[int]models.NetworkProjectRelease, len(before.Releases))
	for _, row := range before.Releases {
		beforeByID[row.Id] = row
	}
	for _, row := range after.Releases {
		prior, exists := beforeByID[row.Id]
		if !exists {
			return corruptStateError("network project release", durableKey(row.SourceProjectId, row.Id), fmt.Errorf("unplanned row appeared"))
		}
		if row.Id == plan.marker.Id {
			if !projectNetworkReleaseCompletedRowMatches(row, plan.marker, request, releaseSetDigest) {
				return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("completed row differs from the exact plan"))
			}
		} else if !reflect.DeepEqual(row, prior) {
			return corruptStateError("network project release", durableKey(row.SourceProjectId, row.Id), fmt.Errorf("unrelated row changed"))
		}
		delete(beforeByID, row.Id)
	}
	if len(beforeByID) != 0 {
		return corruptStateError("network project release", string(request.OperationID), fmt.Errorf("release row disappeared"))
	}
	return nil
}

// validateCompleteProjectNetworkReleaseLeases accounts for every exact quarantine and untouched lease row.
func validateCompleteProjectNetworkReleaseLeases(
	before []models.LoopbackAddressLease,
	after []models.LoopbackAddressLease,
	plan projectNetworkReleaseCompletionPlan,
) error {
	if len(before) != len(after) {
		return corruptStateError("loopback address lease", "network release", fmt.Errorf("lease row count changed"))
	}
	afterByID := make(map[int]models.LoopbackAddressLease, len(after))
	for _, row := range after {
		afterByID[row.Id] = row
	}
	actions := make(map[int]projectNetworkReleaseCompletionAction, len(plan.releases))
	for _, action := range plan.releases {
		actions[action.row.Id] = action
	}
	for _, row := range before {
		persisted, exists := afterByID[row.Id]
		if !exists {
			return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("row disappeared"))
		}
		if action, changed := actions[row.Id]; changed {
			if !networkReplacementReleasedRowMatches(persisted, row, action.request) {
				return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("completed quarantine differs from the exact release"))
			}
		} else if !reflect.DeepEqual(persisted, row) {
			return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("untouched row changed"))
		}
		delete(afterByID, row.Id)
	}
	if len(afterByID) != 0 {
		return corruptStateError("loopback address lease", "network release", fmt.Errorf("unplanned row appeared"))
	}
	return nil
}

// validateCompleteProjectNetworkReleaseEndpoints requires exact target deletion and byte-for-byte foreign preservation.
func validateCompleteProjectNetworkReleaseEndpoints(
	before []models.PublicEndpointLease,
	after []models.PublicEndpointLease,
	request CompleteProjectNetworkReleaseRequest,
	targetCount int,
) error {
	if len(after) != len(before)-targetCount {
		return corruptStateError("public endpoint lease", string(request.ProjectID), fmt.Errorf("endpoint row count differs from exact deletion"))
	}
	beforeForeign := make(map[int]models.PublicEndpointLease, len(after))
	seenTarget := 0
	for _, row := range before {
		if row.ProjectId == string(request.ProjectID) {
			seenTarget++
			continue
		}
		beforeForeign[row.Id] = row
	}
	if seenTarget != targetCount {
		return corruptStateError("public endpoint lease", string(request.ProjectID), fmt.Errorf("target endpoint accounting changed"))
	}
	for _, row := range after {
		prior, exists := beforeForeign[row.Id]
		if !exists || !reflect.DeepEqual(row, prior) {
			return corruptStateError("public endpoint lease", durableKey(row.Hostname, row.Id), fmt.Errorf("foreign endpoint appeared or changed"))
		}
		delete(beforeForeign, row.Id)
	}
	if len(beforeForeign) != 0 {
		return corruptStateError("public endpoint lease", string(request.ProjectID), fmt.Errorf("foreign endpoint disappeared"))
	}
	return nil
}

// projectNetworkReleaseCompletedRowMatches compares every tombstone field while retaining immutable Begin identity.
func projectNetworkReleaseCompletedRowMatches(
	row models.NetworkProjectRelease,
	before models.NetworkProjectRelease,
	request CompleteProjectNetworkReleaseRequest,
	releaseSetDigest string,
) bool {
	return row.Id == before.Id &&
		row.NetworkStateId == before.NetworkStateId &&
		!row.ProjectId.Valid &&
		row.SourceProjectId == before.SourceProjectId &&
		row.OperationId == before.OperationId &&
		row.State == string(ProjectNetworkReleaseCompleted) &&
		row.BeginGeneration == before.BeginGeneration &&
		row.BeganAt.Equal(before.BeganAt) &&
		row.CompletionGeneration.Valid && row.CompletionGeneration.Int64 == int64(request.CompletionGeneration) &&
		row.CompletedAt != nil && row.CompletedAt.Equal(request.At) &&
		row.ReleaseEvidence.Valid && row.ReleaseEvidence.String == request.ReleaseEvidence &&
		row.ReleaseSetDigest.Valid && row.ReleaseSetDigest.String == releaseSetDigest
}

// cloneBeginProjectNetworkReleaseRequest removes process-local time metadata before queueing the mutation.
func cloneBeginProjectNetworkReleaseRequest(request BeginProjectNetworkReleaseRequest) BeginProjectNetworkReleaseRequest {
	request.At = canonicalNetworkMutationTime(request.At)
	return request
}

// projectNetworkReleaseOwner retains the exact project and operation authorities proved before mutation.
type projectNetworkReleaseOwner struct {
	operation     OperationRecord
	history       []OperationTransition
	project       ProjectRecord
	projectExists bool
}

// validateProjectNetworkReleaseOwners proves every retained marker has one recoverable operation/project lifecycle.
func validateProjectNetworkReleaseOwners(
	tx *gorm.DB,
	highWater domain.Sequence,
	rows []models.NetworkProjectRelease,
) (map[domain.OperationID]projectNetworkReleaseOwner, error) {
	owners := make(map[domain.OperationID]projectNetworkReleaseOwner, len(rows))
	for _, row := range rows {
		operationID := domain.OperationID(row.OperationId)
		owner, err := readProjectNetworkReleaseOwner(
			tx,
			highWater,
			domain.ProjectID(row.SourceProjectId),
			operationID,
		)
		if err != nil {
			return nil, err
		}
		if err := validateProjectNetworkReleaseOwnerLifecycle(ProjectNetworkReleaseState(row.State), owner); err != nil {
			return nil, err
		}
		owners[operationID] = owner
	}
	return owners, nil
}

// readProjectNetworkReleaseOwner proves operation history, project revision, and cross-table sequence ownership.
func readProjectNetworkReleaseOwner(
	tx *gorm.DB,
	highWater domain.Sequence,
	projectID domain.ProjectID,
	operationID domain.OperationID,
) (projectNetworkReleaseOwner, error) {
	row, found, err := findOperationForMutation(tx, operationID)
	if err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if !found {
		return projectNetworkReleaseOwner{}, &OperationNotFoundError{OperationID: operationID}
	}
	operation, err := operationRecordFromModel(row)
	if err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	history, err := operationHistoryInTransaction(tx, operation)
	if err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if err := validateOperationHistorySequenceOwners(tx, operation, history); err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if operation.Operation.Kind != domain.OperationKindProjectUnregister {
		return projectNetworkReleaseOwner{}, projectNetworkReleaseConflict(
			projectID,
			operationID,
			"operation kind",
		)
	}
	if operation.Operation.ProjectID != projectID {
		return projectNetworkReleaseOwner{}, projectNetworkReleaseConflict(
			projectID,
			operationID,
			"operation project",
		)
	}

	owner := projectNetworkReleaseOwner{operation: operation, history: history}
	projectRow, exists, err := findProjectForMutation(tx, projectID)
	if err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if !exists {
		return owner, nil
	}
	project, err := readProjectRecord(tx, projectID)
	if err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if err := validateVisibleSequence(highWater, project.Revision, fmt.Sprintf("project %q", projectID), nil); err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if err := validateProjectSequenceOwner(tx, project); err != nil {
		return projectNetworkReleaseOwner{}, err
	}
	if projectRow.Revision != int(project.Revision) {
		return projectNetworkReleaseOwner{}, corruptStateError(
			"project",
			string(projectID),
			fmt.Errorf("mutation root revision differs from aggregate readback"),
		)
	}
	owner.project = project
	owner.projectExists = true
	return owner, nil
}

// validateProjectNetworkReleaseOwnerLifecycle binds each marker state to its recoverable operation/project boundary.
func validateProjectNetworkReleaseOwnerLifecycle(
	state ProjectNetworkReleaseState,
	owner projectNetworkReleaseOwner,
) error {
	switch state {
	case ProjectNetworkReleaseReleasing:
		if !owner.projectExists {
			return corruptStateError(
				"network project release",
				string(owner.operation.Operation.ProjectID),
				fmt.Errorf("releasing marker has no project"),
			)
		}
		if owner.operation.Operation.State != domain.OperationRunning &&
			owner.operation.Operation.State != domain.OperationRequiresApproval {
			return corruptStateError(
				"network project release",
				string(owner.operation.Operation.ProjectID),
				fmt.Errorf("releasing marker owner is %q", owner.operation.Operation.State),
			)
		}
	case ProjectNetworkReleaseCompleted:
		if owner.projectExists {
			if owner.operation.Operation.State != domain.OperationRunning &&
				owner.operation.Operation.State != domain.OperationRequiresApproval {
				return corruptStateError(
					"network project release",
					string(owner.operation.Operation.ProjectID),
					fmt.Errorf("completed marker with retained project has owner state %q", owner.operation.Operation.State),
				)
			}
		} else if owner.operation.Operation.State != domain.OperationSucceeded {
			return corruptStateError(
				"network project release",
				string(owner.operation.Operation.ProjectID),
				fmt.Errorf("completed marker without project has owner state %q", owner.operation.Operation.State),
			)
		}
	default:
		return corruptStateError(
			"network project release",
			string(owner.operation.Operation.ProjectID),
			fmt.Errorf("state %q is unsupported", state),
		)
	}
	return nil
}

// networkProjectReleaseRowByOperation finds one already-validated marker by its unique operation owner.
func networkProjectReleaseRowByOperation(
	rows []models.NetworkProjectRelease,
	operationID domain.OperationID,
) (models.NetworkProjectRelease, bool) {
	for _, row := range rows {
		if row.OperationId == string(operationID) {
			return row, true
		}
	}
	return models.NetworkProjectRelease{}, false
}

// networkProjectReleaseRowForRequest detects either natural identity so conflicts cannot hide behind another unique key.
func networkProjectReleaseRowForRequest(
	rows []models.NetworkProjectRelease,
	projectID domain.ProjectID,
	operationID domain.OperationID,
) (models.NetworkProjectRelease, bool) {
	for _, row := range rows {
		if row.SourceProjectId == string(projectID) || row.OperationId == string(operationID) {
			return row, true
		}
	}
	return models.NetworkProjectRelease{}, false
}

// beginProjectNetworkReleaseDifference compares the complete durable Begin identity without exposing evidence.
func beginProjectNetworkReleaseDifference(
	row models.NetworkProjectRelease,
	request BeginProjectNetworkReleaseRequest,
) string {
	if row.SourceProjectId != string(request.ProjectID) {
		return "source project"
	}
	if row.OperationId != string(request.OperationID) {
		return "operation owner"
	}
	if row.State != string(ProjectNetworkReleaseReleasing) && row.State != string(ProjectNetworkReleaseCompleted) {
		return "release state"
	}
	if row.BeginGeneration != int(request.BeginGeneration) {
		return "begin generation"
	}
	if !row.BeganAt.Equal(request.At) {
		return "begin time"
	}
	return ""
}

// projectNetworkReleaseRecordFromRows restores hidden host facts for staged teardown and tombstone facts for completion.
func projectNetworkReleaseRecordFromRows(
	rows networkModelRows,
	network NetworkRecord,
	row models.NetworkProjectRelease,
) (ProjectNetworkReleaseRecord, error) {
	record := ProjectNetworkReleaseRecord{
		ProjectID:       domain.ProjectID(row.SourceProjectId),
		OperationID:     domain.OperationID(row.OperationId),
		State:           ProjectNetworkReleaseState(row.State),
		BeginGeneration: uint64(row.BeginGeneration),
		BeganAt:         row.BeganAt,
		ActiveLeases:    []NetworkLeaseEnsure{},
		Endpoints:       []EndpointReservation{},
	}
	if row.State == string(ProjectNetworkReleaseCompleted) {
		if !row.CompletionGeneration.Valid || row.CompletedAt == nil || !row.ReleaseEvidence.Valid {
			return ProjectNetworkReleaseRecord{}, corruptStateError(
				"network project release",
				string(record.OperationID),
				fmt.Errorf("completed marker is missing completion facts"),
			)
		}
		record.Completion = &ProjectNetworkReleaseCompletion{
			Generation:       uint64(row.CompletionGeneration.Int64),
			CompletedAt:      *row.CompletedAt,
			Evidence:         row.ReleaseEvidence.String,
			ReleaseSetDigest: row.ReleaseSetDigest.String,
		}
	} else {
		ensures, err := projectNetworkReleaseEnsuresFromRows(rows.Leases, record.ProjectID)
		if err != nil {
			return ProjectNetworkReleaseRecord{}, err
		}
		endpoints, err := projectNetworkReleaseEndpointsFromRows(rows, record.ProjectID)
		if err != nil {
			return ProjectNetworkReleaseRecord{}, err
		}
		record.ActiveLeases = ensures
		record.Endpoints = endpoints
	}
	if err := record.Validate(); err != nil {
		return ProjectNetworkReleaseRecord{}, corruptStateError(
			"network project release",
			string(record.OperationID),
			err,
		)
	}
	if record.State == ProjectNetworkReleaseReleasing {
		active := make(map[identity.LeaseKey]identity.Lease, len(record.ActiveLeases))
		for _, ensure := range record.ActiveLeases {
			active[ensure.Lease.Key] = ensure.Lease
		}
		for _, lease := range network.Leases {
			if lease.Key.ProjectID == record.ProjectID && active[lease.Key] != lease {
				return ProjectNetworkReleaseRecord{}, corruptStateError(
					"network project release",
					string(record.OperationID),
					fmt.Errorf("recovery lease projection differs from the network aggregate"),
				)
			}
		}
	}
	return record, nil
}

// projectNetworkReleaseEnsuresFromRows restores exact active lease evidence in logical identity order.
func projectNetworkReleaseEnsuresFromRows(
	rows []models.LoopbackAddressLease,
	projectID domain.ProjectID,
) ([]NetworkLeaseEnsure, error) {
	ensures := make([]NetworkLeaseEnsure, 0)
	for _, row := range rows {
		if row.State != "leased" || row.SourceProjectId != string(projectID) {
			continue
		}
		key, err := networkLeaseKeyFromModel(projectID, row.Kind, row.SecondaryId)
		if err != nil {
			return nil, err
		}
		address, err := parseCanonicalNetworkAddress("project release lease address", row.Address)
		if err != nil {
			return nil, err
		}
		ensures = append(ensures, NetworkLeaseEnsure{
			Lease: identity.Lease{
				Key:     key,
				Address: address,
				Ownership: identity.Ownership{
					InstallationID: identity.InstallationID(row.OwnershipInstallationId),
					Generation:     uint64(row.OwnershipGeneration),
				},
			},
			Generation:     uint64(row.LeaseGeneration),
			EnsureEvidence: row.EnsureEvidence,
			LeasedAt:       row.LeasedAt,
		})
	}
	sort.Slice(ensures, func(left, right int) bool {
		return networkLeaseLess(ensures[left].Lease, ensures[right].Lease)
	})
	return ensures, nil
}

// projectNetworkReleaseEndpointsFromRows restores raw routes that public aggregate conversion intentionally suppresses.
func projectNetworkReleaseEndpointsFromRows(
	rows networkModelRows,
	projectID domain.ProjectID,
) ([]EndpointReservation, error) {
	activeKeys := make(map[int]identity.LeaseKey, len(rows.Leases))
	for _, row := range rows.Leases {
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return nil, err
		}
		activeKeys[row.Id] = key
	}
	endpoints := make([]EndpointReservation, 0)
	for _, row := range rows.Endpoints {
		if row.ProjectId != string(projectID) {
			continue
		}
		public, err := networkAddressPortFromModel("project release endpoint", row.Address, row.Port)
		if err != nil {
			return nil, err
		}
		endpoint := EndpointReservation{
			Key:        EndpointReservationKey{ProjectID: projectID, EndpointID: row.EndpointId},
			Protocol:   EndpointProtocol(row.Protocol),
			Host:       row.Hostname,
			Public:     public,
			Generation: uint64(row.Generation),
		}
		if row.LoopbackAddressLeaseId.Valid {
			leaseID := int(row.LoopbackAddressLeaseId.Int64)
			key, exists := activeKeys[leaseID]
			if !exists || int64(leaseID) != row.LoopbackAddressLeaseId.Int64 {
				return nil, corruptStateError(
					"public endpoint lease",
					durableKey(row.Hostname, row.Id),
					fmt.Errorf("active lease identity is missing"),
				)
			}
			keyCopy := key
			endpoint.Identity = &keyCopy
		}
		endpoints = append(endpoints, endpoint)
	}
	return canonicalEndpointReservations(endpoints), nil
}

// beginProjectNetworkReleaseProjection computes the sole public change made by staging: target route suppression.
func beginProjectNetworkReleaseProjection(
	current NetworkRecord,
	request BeginProjectNetworkReleaseRequest,
	sequence domain.Sequence,
) NetworkRecord {
	projection := current
	projection.Revision = sequence
	projection.UpdatedAt = request.At
	projection.Leases = slices.Clone(current.Leases)
	projection.Quarantines = slices.Clone(current.Quarantines)
	projection.Reservations.Endpoints = make([]EndpointReservation, 0, len(current.Reservations.Endpoints))
	for _, endpoint := range current.Reservations.Endpoints {
		if endpoint.Key.ProjectID != request.ProjectID {
			projection.Reservations.Endpoints = append(projection.Reservations.Endpoints, endpoint)
		}
	}
	projection.Reservations.Endpoints = canonicalEndpointReservations(projection.Reservations.Endpoints)
	projection.Reservations.SuppressedProjectIDs = append(
		slices.Clone(current.Reservations.SuppressedProjectIDs),
		request.ProjectID,
	)
	slices.Sort(projection.Reservations.SuppressedProjectIDs)
	return projection
}

// validateBeginProjectNetworkReleaseRows proves staging changed only the root revision and one exact marker row.
func validateBeginProjectNetworkReleaseRows(
	before networkModelRows,
	after networkModelRows,
	request BeginProjectNetworkReleaseRequest,
	insertedID int,
) error {
	if len(before.States) != 1 || len(after.States) != 1 {
		return corruptStateError("network state", "1", fmt.Errorf("release staging readback has an invalid singleton count"))
	}
	beforeRoot := before.States[0]
	afterRoot := after.States[0]
	afterRoot.Revision = beforeRoot.Revision
	afterRoot.UpdatedAt = beforeRoot.UpdatedAt
	if !reflect.DeepEqual(afterRoot, beforeRoot) {
		return corruptStateError("network state", "1", fmt.Errorf("release staging changed immutable root fields"))
	}
	for _, comparison := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "network pool candidates", before: before.Candidates, after: after.Candidates},
		{name: "network setup evidence", before: before.SetupEvidence, after: after.SetupEvidence},
		{name: "network shared listeners", before: before.Listeners, after: after.Listeners},
		{name: "loopback address leases", before: before.Leases, after: after.Leases},
		{name: "public endpoint leases", before: before.Endpoints, after: after.Endpoints},
		{name: "network projects", before: before.Projects, after: after.Projects},
	} {
		if !reflect.DeepEqual(comparison.before, comparison.after) {
			return corruptStateError("network state", "1", fmt.Errorf("release staging changed %s", comparison.name))
		}
	}
	if len(after.Releases) != len(before.Releases)+1 {
		return corruptStateError(
			"network project release",
			string(request.OperationID),
			fmt.Errorf("row count changed from %d to %d", len(before.Releases), len(after.Releases)),
		)
	}
	beforeByID := make(map[int]models.NetworkProjectRelease, len(before.Releases))
	for _, row := range before.Releases {
		beforeByID[row.Id] = row
	}
	foundInserted := false
	for _, row := range after.Releases {
		if row.Id == insertedID {
			foundInserted = true
			if !projectNetworkReleaseBeginRowMatches(row, request) {
				return corruptStateError(
					"network project release",
					string(request.OperationID),
					fmt.Errorf("inserted row differs from the exact begin request"),
				)
			}
			continue
		}
		prior, exists := beforeByID[row.Id]
		if !exists || !reflect.DeepEqual(row, prior) {
			return corruptStateError(
				"network project release",
				durableKey(row.SourceProjectId, row.Id),
				fmt.Errorf("unplanned release row appeared or changed"),
			)
		}
		delete(beforeByID, row.Id)
	}
	if !foundInserted || len(beforeByID) != 0 {
		return corruptStateError(
			"network project release",
			string(request.OperationID),
			fmt.Errorf("release row accounting is incomplete"),
		)
	}
	return validateBeginProjectNetworkReleaseOwners(before.ReleaseOwners, after.ReleaseOwners, request)
}

// validateBeginProjectNetworkReleaseOwners accounts for the owner row that becomes visible through the new marker join.
func validateBeginProjectNetworkReleaseOwners(
	before []models.Operation,
	after []models.Operation,
	request BeginProjectNetworkReleaseRequest,
) error {
	if len(after) != len(before)+1 {
		return corruptStateError(
			"network release owner",
			string(request.OperationID),
			fmt.Errorf("row count changed from %d to %d", len(before), len(after)),
		)
	}
	beforeByID := make(map[string]models.Operation, len(before))
	for _, row := range before {
		beforeByID[row.Id] = row
	}
	foundTarget := false
	for _, row := range after {
		if row.Id == string(request.OperationID) {
			if foundTarget || row.Kind != string(domain.OperationKindProjectUnregister) ||
				!row.ProjectId.Valid || row.ProjectId.String != string(request.ProjectID) {
				return corruptStateError(
					"network release owner",
					string(request.OperationID),
					fmt.Errorf("new owner row differs from the unregister authority"),
				)
			}
			foundTarget = true
			continue
		}
		prior, exists := beforeByID[row.Id]
		if !exists || !reflect.DeepEqual(row, prior) {
			return corruptStateError(
				"network release owner",
				row.Id,
				fmt.Errorf("preexisting owner row appeared or changed"),
			)
		}
		delete(beforeByID, row.Id)
	}
	if !foundTarget || len(beforeByID) != 0 {
		return corruptStateError(
			"network release owner",
			string(request.OperationID),
			fmt.Errorf("owner row accounting is incomplete"),
		)
	}
	return nil
}

// projectNetworkReleaseBeginRowMatches compares every staged marker field including absent completion columns.
func projectNetworkReleaseBeginRowMatches(
	row models.NetworkProjectRelease,
	request BeginProjectNetworkReleaseRequest,
) bool {
	return row.Id > 0 &&
		row.NetworkStateId == networkStateSingletonID &&
		row.ProjectId.Valid && row.ProjectId.String == string(request.ProjectID) &&
		row.SourceProjectId == string(request.ProjectID) &&
		row.OperationId == string(request.OperationID) &&
		row.State == string(ProjectNetworkReleaseReleasing) &&
		row.BeginGeneration == int(request.BeginGeneration) &&
		row.BeganAt.Equal(request.At) &&
		!row.CompletionGeneration.Valid && row.CompletedAt == nil &&
		!row.ReleaseEvidence.Valid && !row.ReleaseSetDigest.Valid
}

// projectNetworkReleaseConflict returns one typed non-secret release conflict.
func projectNetworkReleaseConflict(
	projectID domain.ProjectID,
	operationID domain.OperationID,
	difference string,
) error {
	return &ProjectNetworkReleaseConflictError{
		ProjectID:   projectID,
		OperationID: operationID,
		Difference:  difference,
	}
}
