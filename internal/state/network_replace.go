package state

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// NetworkNotInitializedError reports that a project mutation reached a complete but empty network schema.
type NetworkNotInitializedError struct{}

// Error describes the missing durable network root.
func (*NetworkNotInitializedError) Error() string {
	return "network state is not initialized"
}

// NetworkProjectReplacementConflictError reports a valid request that cannot be reconciled with current durable network ownership.
type NetworkProjectReplacementConflictError struct {
	ProjectID  domain.ProjectID
	Difference string
}

// Error describes the non-secret project network fact that prevents replacement.
func (err *NetworkProjectReplacementConflictError) Error() string {
	return fmt.Sprintf("replace network state for project %q: %s", err.ProjectID, err.Difference)
}

// ReplaceProjectNetwork commits completed lease effects and one project's complete public reservation set.
func (store *Store) ReplaceProjectNetwork(
	ctx context.Context,
	request ReplaceProjectNetworkRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	request = cloneReplaceProjectNetworkRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "project network replacement", func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("network persistence schema is not installed")
		}

		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		current, initialized, err := networkRecordFromModels(rows)
		if err != nil {
			return err
		}
		if !initialized {
			return &NetworkNotInitializedError{}
		}
		if err := requireNoActiveNetworkResolverPolicyMigrationMutation(tx, "replace project network"); err != nil {
			return err
		}
		highWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		project, err := readProjectRecord(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if err := validateVisibleSequence(
			highWater,
			project.Revision,
			fmt.Sprintf("project %q", request.ProjectID),
			nil,
		); err != nil {
			return err
		}
		if err := validateProjectSequenceOwner(tx, project); err != nil {
			return err
		}
		if err := rejectNetworkProjectReplacementRelease(rows.Releases, request.ProjectID); err != nil {
			return err
		}

		satisfied := networkProjectReplacementSatisfied(rows, request)
		if satisfied {
			if current.Revision < request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{
					Expected: request.ExpectedNetworkRevision,
					Actual:   current.Revision,
				}
			}
			if project.Revision < request.ExpectedProjectRevision {
				return &ProjectRevisionConflictError{
					ProjectID: request.ProjectID,
					Expected:  request.ExpectedProjectRevision,
					Actual:    project.Revision,
				}
			}
			result = NetworkMutationResult{Record: current, Replayed: true}
			return result.Validate()
		}
		if current.Revision != request.ExpectedNetworkRevision {
			return &NetworkRevisionConflictError{
				Expected: request.ExpectedNetworkRevision,
				Actual:   current.Revision,
			}
		}
		if project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    project.Revision,
			}
		}
		if request.At.Before(current.UpdatedAt) {
			return networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("mutation time %s precedes network update time %s", request.At.Format(time.RFC3339Nano), current.UpdatedAt.Format(time.RFC3339Nano)),
			)
		}

		plan, err := planNetworkProjectReplacement(rows, current, request)
		if err != nil {
			return err
		}
		if err := applyNetworkProjectReplacement(tx, plan, request); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.NetworkState{}).
			Where("id = ? AND revision = ?", networkStateSingletonID, int(request.ExpectedNetworkRevision)).
			Updates(map[string]any{
				"updated_at": request.At,
				"revision":   int(sequence),
			})
		if err := requireOneMutation(updated, "update network state", "1"); err != nil {
			return err
		}

		persistedRows, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read replaced network state: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(persistedRows)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after project replacement"))
		}
		if persisted.Revision != sequence || !persisted.UpdatedAt.Equal(request.At) {
			return corruptStateError(
				"network state",
				"1",
				fmt.Errorf("readback revision/time is %d/%s, expected %d/%s", persisted.Revision, persisted.UpdatedAt.Format(time.RFC3339Nano), sequence, request.At.Format(time.RFC3339Nano)),
			)
		}
		if !networkProjectReplacementSatisfied(persistedRows, request) {
			return corruptStateError("network state", "1", fmt.Errorf("readback does not satisfy the requested project replacement"))
		}
		expectedProjection := plan.projection
		expectedProjection.Revision = sequence
		if !reflect.DeepEqual(persisted, expectedProjection) {
			return corruptStateError("network state", "1", fmt.Errorf("readback aggregate differs from the preflighted projection"))
		}
		if err := validateNetworkProjectReplacementUntouched(rows, persistedRows, plan); err != nil {
			return err
		}
		persistedProject, err := readProjectRecord(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(persistedProject, project) {
			return corruptStateError("project", string(request.ProjectID), fmt.Errorf("aggregate changed during network replacement"))
		}
		finalHighWater, err := validateRetainedSequenceBounds(tx)
		if err != nil {
			return err
		}
		if finalHighWater != sequence {
			return corruptStateError("Harbor sequence", fmt.Sprint(finalHighWater), fmt.Errorf("replacement allocated revision %d", sequence))
		}
		if err := validateProjectSequenceOwner(tx, persistedProject); err != nil {
			return err
		}
		result = NetworkMutationResult{Record: persisted, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("replace project network %q: %w", request.ProjectID, err)
	}
	return result, nil
}

// cloneReplaceProjectNetworkRequest isolates queued persistence from caller-owned slices and endpoint identity pointers.
func cloneReplaceProjectNetworkRequest(request ReplaceProjectNetworkRequest) ReplaceProjectNetworkRequest {
	request.Ensures = slices.Clone(request.Ensures)
	request.Releases = slices.Clone(request.Releases)
	request.Endpoints = canonicalEndpointReservations(request.Endpoints)
	request.At = canonicalNetworkMutationTime(request.At)
	for index := range request.Ensures {
		request.Ensures[index].LeasedAt = canonicalNetworkMutationTime(request.Ensures[index].LeasedAt)
	}
	for index := range request.Releases {
		request.Releases[index].ReleasedAt = canonicalNetworkMutationTime(request.Releases[index].ReleasedAt)
		request.Releases[index].QuarantinedAt = canonicalNetworkMutationTime(request.Releases[index].QuarantinedAt)
		request.Releases[index].ReuseAfter = canonicalNetworkMutationTime(request.Releases[index].ReuseAfter)
	}
	return request
}

// networkReplacementEnsureMode identifies whether an ensure is already satisfied, creates a row, or consumes quarantine.
type networkReplacementEnsureMode uint8

const (
	networkReplacementEnsureRetained networkReplacementEnsureMode = iota
	networkReplacementEnsureCreate
	networkReplacementEnsureConsume
)

// networkReplacementReleaseAction pairs one requested release with the exact active row it owns.
type networkReplacementReleaseAction struct {
	request NetworkLeaseRelease
	row     models.LoopbackAddressLease
}

// networkReplacementEnsureAction captures one preflighted ensure persistence mechanism.
type networkReplacementEnsureAction struct {
	request NetworkLeaseEnsure
	mode    networkReplacementEnsureMode
	row     models.LoopbackAddressLease
}

// networkReplacementEndpointAction carries preserved endpoint lifecycle timestamps across delete and reinsert.
type networkReplacementEndpointAction struct {
	request   EndpointReservation
	id        int
	createdAt time.Time
	updatedAt time.Time
}

// networkProjectReplacementPlan is the complete preflighted write set for one project.
type networkProjectReplacementPlan struct {
	projectID      domain.ProjectID
	releases       []networkReplacementReleaseAction
	ensures        []networkReplacementEnsureAction
	endpoints      []networkReplacementEndpointAction
	targetEndpoint int
	projection     NetworkRecord
}

// planNetworkProjectReplacement validates every current-state dependency before the transaction changes a row.
func planNetworkProjectReplacement(
	rows networkModelRows,
	current NetworkRecord,
	request ReplaceProjectNetworkRequest,
) (networkProjectReplacementPlan, error) {
	activeByKey := make(map[identity.LeaseKey]models.LoopbackAddressLease)
	rowByAddress := make(map[string]models.LoopbackAddressLease, len(rows.Leases))
	finalLeases := make(map[identity.LeaseKey]identity.Lease, len(current.Leases)+len(request.Ensures))
	for _, lease := range current.Leases {
		finalLeases[lease.Key] = lease
	}
	for _, row := range rows.Leases {
		rowByAddress[row.Address] = row
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return networkProjectReplacementPlan{}, err
		}
		activeByKey[key] = row
	}

	plan := networkProjectReplacementPlan{
		projectID: request.ProjectID,
		releases:  make([]networkReplacementReleaseAction, 0, len(request.Releases)),
		ensures:   make([]networkReplacementEnsureAction, 0, len(request.Ensures)),
	}
	releasedRows := make(map[int]struct{}, len(request.Releases))
	for _, release := range request.Releases {
		row, exists := activeByKey[release.Lease.Key]
		if !exists {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("release key %s has no active lease", networkInitializationLeaseKey(release.Lease.Key)),
			)
		}
		if row.Address != release.Lease.Address.String() ||
			row.OwnershipInstallationId != string(release.Lease.Ownership.InstallationID) ||
			row.OwnershipGeneration != int(release.Lease.Ownership.Generation) {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("release key %s does not match its active address ownership", networkInitializationLeaseKey(release.Lease.Key)),
			)
		}
		if int(release.ReleaseGeneration) <= row.LeaseGeneration {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("release generation for %s must exceed active lease generation %d", networkInitializationLeaseKey(release.Lease.Key), row.LeaseGeneration),
			)
		}
		if release.ReleasedAt.Before(row.LeasedAt) || release.QuarantinedAt.Before(row.LeasedAt) {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("release facts for %s precede its active lease", networkInitializationLeaseKey(release.Lease.Key)),
			)
		}
		plan.releases = append(plan.releases, networkReplacementReleaseAction{request: release, row: row})
		releasedRows[row.Id] = struct{}{}
		delete(activeByKey, release.Lease.Key)
		delete(finalLeases, release.Lease.Key)
	}

	protectedPrimaryQuarantines := networkReplacementProtectedPrimaryQuarantines(rows)
	for _, ensure := range request.Ensures {
		if ensure.Lease.Ownership.InstallationID != current.Ownership.InstallationID {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure key %s belongs to installation %q, not %q", networkInitializationLeaseKey(ensure.Lease.Key), ensure.Lease.Ownership.InstallationID, current.Ownership.InstallationID),
			)
		}
		if !current.Pool.Contains(ensure.Lease.Address) {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure address %s is not a network pool candidate", ensure.Lease.Address),
			)
		}
		if row, exists := activeByKey[ensure.Lease.Key]; exists {
			if !networkReplacementEnsureMatches(row, ensure) {
				return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
					request.ProjectID,
					fmt.Sprintf("ensure key %s is already active with different durable facts", networkInitializationLeaseKey(ensure.Lease.Key)),
				)
			}
			plan.ensures = append(plan.ensures, networkReplacementEnsureAction{
				request: ensure,
				mode:    networkReplacementEnsureRetained,
				row:     row,
			})
			finalLeases[ensure.Lease.Key] = ensure.Lease
			continue
		}

		occupied, exists := rowByAddress[ensure.Lease.Address.String()]
		if !exists {
			plan.ensures = append(plan.ensures, networkReplacementEnsureAction{
				request: ensure,
				mode:    networkReplacementEnsureCreate,
			})
			finalLeases[ensure.Lease.Key] = ensure.Lease
			continue
		}
		if _, releasedNow := releasedRows[occupied.Id]; releasedNow {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure address %s was released by the same transition", ensure.Lease.Address),
			)
		}
		if occupied.State != "quarantined" {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure address %s is retained by an active lease", ensure.Lease.Address),
			)
		}
		if occupied.ReuseAfter == nil || occupied.ReuseAfter.After(request.At) || occupied.ReuseAfter.After(ensure.LeasedAt) {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure address %s is still quarantined", ensure.Lease.Address),
			)
		}
		if !occupied.ReleaseGeneration.Valid || uint64(occupied.ReleaseGeneration.Int64) >= ensure.Generation {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure generation for address %s must exceed quarantine release generation", ensure.Lease.Address),
			)
		}
		if occupied.Kind == string(identity.LeaseKindPrimary) && protectedPrimaryQuarantines[domain.ProjectID(occupied.SourceProjectId)] == 1 {
			return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
				request.ProjectID,
				fmt.Sprintf("ensure address %s preserves completed release ownership for project %q", ensure.Lease.Address, occupied.SourceProjectId),
			)
		}
		if occupied.Kind == string(identity.LeaseKindPrimary) && protectedPrimaryQuarantines[domain.ProjectID(occupied.SourceProjectId)] > 0 {
			protectedPrimaryQuarantines[domain.ProjectID(occupied.SourceProjectId)]--
		}
		plan.ensures = append(plan.ensures, networkReplacementEnsureAction{
			request: ensure,
			mode:    networkReplacementEnsureConsume,
			row:     occupied,
		})
		finalLeases[ensure.Lease.Key] = ensure.Lease
	}

	finalQuarantines := make(map[netip.Addr]identity.Quarantine, len(current.Quarantines)+len(request.Releases))
	for _, quarantine := range current.Quarantines {
		finalQuarantines[quarantine.Address] = quarantine
	}
	for _, action := range plan.ensures {
		if action.mode == networkReplacementEnsureConsume {
			delete(finalQuarantines, action.request.Lease.Address)
		}
	}
	for _, release := range request.Releases {
		finalQuarantines[release.Lease.Address] = identity.Quarantine{
			Address: release.Lease.Address,
			Reason:  release.QuarantineReason,
		}
	}

	endpoints, err := planNetworkReplacementEndpoints(rows, current, request, finalLeases)
	if err != nil {
		return networkProjectReplacementPlan{}, err
	}
	plan.endpoints = endpoints
	for _, row := range rows.Endpoints {
		if row.ProjectId == string(request.ProjectID) {
			plan.targetEndpoint++
		}
	}

	leases := make([]identity.Lease, 0, len(finalLeases))
	targetPrimary := false
	for _, lease := range finalLeases {
		leases = append(leases, lease)
		if lease.Key.ProjectID == request.ProjectID && lease.Key.Kind() == identity.LeaseKindPrimary {
			targetPrimary = true
		}
	}
	if !targetPrimary {
		return networkProjectReplacementPlan{}, networkProjectReplacementConflict(
			request.ProjectID,
			"registered project requires a primary network lease",
		)
	}
	quarantines := make([]identity.Quarantine, 0, len(finalQuarantines))
	for _, quarantine := range finalQuarantines {
		quarantines = append(quarantines, quarantine)
	}
	publishable := make([]EndpointReservation, 0, len(current.Reservations.Endpoints)+len(request.Endpoints))
	for _, endpoint := range current.Reservations.Endpoints {
		if endpoint.Key.ProjectID != request.ProjectID {
			publishable = append(publishable, endpoint)
		}
	}
	publishable = append(publishable, request.Endpoints...)
	candidate := NetworkRecord{
		Stage:       current.Stage,
		Revision:    current.Revision,
		CreatedAt:   current.CreatedAt,
		UpdatedAt:   request.At,
		Ownership:   current.Ownership,
		Pool:        current.Pool,
		Leases:      canonicalNetworkLeases(leases),
		Quarantines: canonicalNetworkQuarantines(quarantines),
		Reservations: DataPlaneReservations{
			Listeners:            current.Reservations.Listeners,
			Endpoints:            canonicalEndpointReservations(publishable),
			SuppressedProjectIDs: slices.Clone(current.Reservations.SuppressedProjectIDs),
		},
	}
	if err := candidate.Validate(); err != nil {
		return networkProjectReplacementPlan{}, networkProjectReplacementConflict(request.ProjectID, err.Error())
	}
	plan.projection = candidate
	return plan, nil
}

// networkReplacementProtectedPrimaryQuarantines counts source-primary proof rows required by retained completed releases.
func networkReplacementProtectedPrimaryQuarantines(rows networkModelRows) map[domain.ProjectID]int {
	knownProjects := make(map[domain.ProjectID]struct{}, len(rows.Projects))
	for _, row := range rows.Projects {
		knownProjects[domain.ProjectID(row.ProjectId)] = struct{}{}
	}
	completed := make(map[domain.ProjectID]struct{}, len(rows.Releases))
	for _, row := range rows.Releases {
		projectID := domain.ProjectID(row.SourceProjectId)
		if row.State == "completed" {
			if _, retained := knownProjects[projectID]; retained {
				completed[projectID] = struct{}{}
			}
		}
	}
	result := make(map[domain.ProjectID]int, len(completed))
	for _, row := range rows.Leases {
		projectID := domain.ProjectID(row.SourceProjectId)
		if _, required := completed[projectID]; required && row.State == "quarantined" && row.Kind == string(identity.LeaseKindPrimary) {
			result[projectID]++
		}
	}
	return result
}

// planNetworkReplacementEndpoints checks global collisions and preserves natural-key lifecycle timestamps.
func planNetworkReplacementEndpoints(
	rows networkModelRows,
	current NetworkRecord,
	request ReplaceProjectNetworkRequest,
	finalLeases map[identity.LeaseKey]identity.Lease,
) ([]networkReplacementEndpointAction, error) {
	activeKeysByID := make(map[int]identity.LeaseKey, len(rows.Leases))
	for _, row := range rows.Leases {
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return nil, err
		}
		activeKeysByID[row.Id] = key
	}
	existing := make(map[EndpointReservationKey]models.PublicEndpointLease)
	for _, row := range rows.Endpoints {
		if row.ProjectId == string(request.ProjectID) {
			existing[EndpointReservationKey{ProjectID: request.ProjectID, EndpointID: row.EndpointId}] = row
			continue
		}
		for _, endpoint := range request.Endpoints {
			if row.Hostname == endpoint.Host {
				return nil, networkProjectReplacementConflict(request.ProjectID, fmt.Sprintf("endpoint host %q is already reserved", endpoint.Host))
			}
			if endpoint.Protocol == EndpointProtocolTCP && row.Protocol == string(EndpointProtocolTCP) &&
				row.Address == endpoint.Public.Addr().String() && row.Port == int(endpoint.Public.Port()) {
				return nil, networkProjectReplacementConflict(request.ProjectID, fmt.Sprintf("native endpoint socket %s is already reserved", endpoint.Public))
			}
		}
	}

	actions := make([]networkReplacementEndpointAction, 0, len(request.Endpoints))
	for _, endpoint := range request.Endpoints {
		switch endpoint.Protocol {
		case EndpointProtocolHTTP:
			if endpoint.Public != current.Reservations.Listeners.HTTPS.Advertised {
				return nil, networkProjectReplacementConflict(request.ProjectID, fmt.Sprintf("HTTP endpoint %q does not use the advertised HTTPS socket", endpoint.Host))
			}
		case EndpointProtocolTCP:
			lease, exists := finalLeases[*endpoint.Identity]
			if !exists || lease.Address != endpoint.Public.Addr() {
				return nil, networkProjectReplacementConflict(request.ProjectID, fmt.Sprintf("TCP endpoint %q does not resolve to its final active lease", endpoint.Host))
			}
			if owner, collision := sharedSocketOwner(current.Reservations.Listeners, endpoint.Public); collision {
				return nil, networkProjectReplacementConflict(request.ProjectID, fmt.Sprintf("native endpoint %q collides with %s", endpoint.Host, owner))
			}
		}

		action := networkReplacementEndpointAction{
			request:   endpoint,
			createdAt: request.At,
			updatedAt: request.At,
		}
		if row, exists := existing[endpoint.Key]; exists {
			action.id = row.Id
			action.createdAt = row.CreatedAt
			if networkReplacementEndpointMatches(row, endpoint, activeKeysByID) {
				action.updatedAt = row.UpdatedAt
			}
		}
		actions = append(actions, action)
	}
	return actions, nil
}

// applyNetworkProjectReplacement performs the preflighted endpoint, release, ensure, and endpoint order.
func applyNetworkProjectReplacement(
	tx *gorm.DB,
	plan networkProjectReplacementPlan,
	request ReplaceProjectNetworkRequest,
) error {
	deleted := tx.Where("project_id = ?", string(request.ProjectID)).Delete(&models.PublicEndpointLease{})
	if deleted.Error != nil {
		return fmt.Errorf("delete project network endpoints: %w", deleted.Error)
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
		if err := requireOneMutation(updated, "quarantine loopback address lease", networkInitializationLeaseKey(release.Lease.Key)); err != nil {
			return err
		}
	}

	for _, action := range plan.ensures {
		ensure := action.request
		switch action.mode {
		case networkReplacementEnsureRetained:
			continue
		case networkReplacementEnsureCreate:
			row := networkReplacementLeaseModel(ensure)
			if err := requireOneCreate(tx.Create(&row), "create loopback address lease", networkInitializationLeaseKey(ensure.Lease.Key)); err != nil {
				return err
			}
		case networkReplacementEnsureConsume:
			updated := tx.Model(&models.LoopbackAddressLease{}).
				Where(
					"id = ? AND state = ? AND address = ? AND reuse_after <= ?",
					action.row.Id,
					"quarantined",
					action.row.Address,
					ensure.LeasedAt,
				).
				Updates(networkReplacementEnsureColumns(ensure))
			if err := requireOneMutation(updated, "consume quarantined loopback address lease", ensure.Lease.Address.String()); err != nil {
				return err
			}
		default:
			return corruptStateError("network replacement", string(request.ProjectID), fmt.Errorf("ensure mode %d is unsupported", action.mode))
		}
	}

	activeLeaseIDs, err := readNetworkReplacementActiveLeaseIDs(tx)
	if err != nil {
		return err
	}
	for _, action := range plan.endpoints {
		row, err := networkReplacementEndpointModel(action, request.ProjectID, activeLeaseIDs)
		if err != nil {
			return err
		}
		if err := requireOneCreate(tx.Create(&row), "create public endpoint lease", networkInitializationEndpointKey(action.request.Key)); err != nil {
			return err
		}
	}
	return nil
}

// networkReplacementEndpointModel maps one planned endpoint to its exact final lease identity and lifecycle timestamps.
func networkReplacementEndpointModel(
	action networkReplacementEndpointAction,
	projectID domain.ProjectID,
	activeLeaseIDs map[identity.LeaseKey]int,
) (models.PublicEndpointLease, error) {
	endpoint := action.request
	row := models.PublicEndpointLease{
		Id:             action.id,
		NetworkStateId: networkStateSingletonID,
		ProjectId:      string(projectID),
		EndpointId:     endpoint.Key.EndpointID,
		Protocol:       string(endpoint.Protocol),
		Hostname:       endpoint.Host,
		Address:        endpoint.Public.Addr().String(),
		Port:           int(endpoint.Public.Port()),
		Generation:     int(endpoint.Generation),
		CreatedAt:      action.createdAt,
		UpdatedAt:      action.updatedAt,
	}
	if endpoint.Identity == nil {
		return row, nil
	}
	leaseID, exists := activeLeaseIDs[*endpoint.Identity]
	if !exists {
		return models.PublicEndpointLease{}, networkProjectReplacementConflict(projectID, fmt.Sprintf("TCP endpoint %q has no active lease ID", endpoint.Host))
	}
	row.LoopbackAddressLeaseId = null.IntFrom(int64(leaseID))
	return row, nil
}

// networkReplacementLeaseModel maps one new ensure to the complete active durable row.
func networkReplacementLeaseModel(ensure NetworkLeaseEnsure) models.LoopbackAddressLease {
	return models.LoopbackAddressLease{
		NetworkStateId:          networkStateSingletonID,
		ProjectId:               null.StringFrom(string(ensure.Lease.Key.ProjectID)),
		SourceProjectId:         string(ensure.Lease.Key.ProjectID),
		Kind:                    string(ensure.Lease.Key.Kind()),
		SecondaryId:             ensure.Lease.Key.SecondaryID,
		Address:                 ensure.Lease.Address.String(),
		State:                   "leased",
		LeaseGeneration:         int(ensure.Generation),
		OwnershipInstallationId: string(ensure.Lease.Ownership.InstallationID),
		OwnershipGeneration:     int(ensure.Lease.Ownership.Generation),
		EnsureEvidence:          ensure.EnsureEvidence,
		LeasedAt:                ensure.LeasedAt,
	}
}

// networkReplacementEnsureColumns maps one ensure while explicitly clearing every prior quarantine field.
func networkReplacementEnsureColumns(ensure NetworkLeaseEnsure) map[string]any {
	return map[string]any{
		"project_id":                string(ensure.Lease.Key.ProjectID),
		"source_project_id":         string(ensure.Lease.Key.ProjectID),
		"kind":                      string(ensure.Lease.Key.Kind()),
		"secondary_id":              ensure.Lease.Key.SecondaryID,
		"state":                     "leased",
		"lease_generation":          int(ensure.Generation),
		"ownership_installation_id": string(ensure.Lease.Ownership.InstallationID),
		"ownership_generation":      int(ensure.Lease.Ownership.Generation),
		"ensure_evidence":           ensure.EnsureEvidence,
		"leased_at":                 ensure.LeasedAt,
		"release_generation":        nil,
		"release_evidence":          nil,
		"released_at":               nil,
		"quarantined_at":            nil,
		"reuse_after":               nil,
		"quarantine_reason":         nil,
	}
}

// readNetworkReplacementActiveLeaseIDs resolves exact logical identity joins after every lease change.
func readNetworkReplacementActiveLeaseIDs(tx *gorm.DB) (map[identity.LeaseKey]int, error) {
	var rows []models.LoopbackAddressLease
	if err := tx.Where("state = ?", "leased").Order("source_project_id ASC").Order("kind ASC").Order("secondary_id ASC").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read active leases for endpoint replacement: %w", err)
	}
	result := make(map[identity.LeaseKey]int, len(rows))
	for _, row := range rows {
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return nil, corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("active lease key is duplicated"))
		}
		result[key] = row.Id
	}
	return result, nil
}

// rejectNetworkProjectReplacementRelease prevents normal reconciliation from racing staged or completed teardown.
func rejectNetworkProjectReplacementRelease(rows []models.NetworkProjectRelease, projectID domain.ProjectID) error {
	for _, row := range rows {
		if row.SourceProjectId == string(projectID) {
			return networkProjectReplacementConflict(projectID, fmt.Sprintf("project release is %q", row.State))
		}
	}
	return nil
}

// networkProjectReplacementSatisfied proves current hidden facts and the full target endpoint set already satisfy the request.
func networkProjectReplacementSatisfied(rows networkModelRows, request ReplaceProjectNetworkRequest) bool {
	activeByKey := make(map[identity.LeaseKey]models.LoopbackAddressLease)
	rowByAddress := make(map[string]models.LoopbackAddressLease, len(rows.Leases))
	activeKeysByID := make(map[int]identity.LeaseKey)
	for _, row := range rows.Leases {
		rowByAddress[row.Address] = row
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return false
		}
		activeByKey[key] = row
		activeKeysByID[row.Id] = key
	}
	for _, ensure := range request.Ensures {
		if !networkReplacementEnsureMatches(activeByKey[ensure.Lease.Key], ensure) {
			return false
		}
	}
	for _, release := range request.Releases {
		if !networkReplacementReleaseMatches(rowByAddress[release.Lease.Address.String()], release) {
			return false
		}
	}

	targetEndpoints := make(map[EndpointReservationKey]models.PublicEndpointLease)
	for _, row := range rows.Endpoints {
		if row.ProjectId == string(request.ProjectID) {
			targetEndpoints[EndpointReservationKey{ProjectID: request.ProjectID, EndpointID: row.EndpointId}] = row
		}
	}
	if len(targetEndpoints) != len(request.Endpoints) {
		return false
	}
	for _, endpoint := range request.Endpoints {
		if !networkReplacementEndpointMatches(targetEndpoints[endpoint.Key], endpoint, activeKeysByID) {
			return false
		}
	}
	return true
}

// networkReplacementEnsureMatches compares every active ensure fact without exposing evidence in diagnostics.
func networkReplacementEnsureMatches(row models.LoopbackAddressLease, ensure NetworkLeaseEnsure) bool {
	return row.Id > 0 &&
		row.NetworkStateId == networkStateSingletonID &&
		row.ProjectId.Valid && row.ProjectId.String == string(ensure.Lease.Key.ProjectID) &&
		row.SourceProjectId == string(ensure.Lease.Key.ProjectID) &&
		row.Kind == string(ensure.Lease.Key.Kind()) &&
		row.SecondaryId == ensure.Lease.Key.SecondaryID &&
		row.Address == ensure.Lease.Address.String() &&
		row.State == "leased" &&
		row.LeaseGeneration == int(ensure.Generation) &&
		row.OwnershipInstallationId == string(ensure.Lease.Ownership.InstallationID) &&
		row.OwnershipGeneration == int(ensure.Lease.Ownership.Generation) &&
		row.EnsureEvidence == ensure.EnsureEvidence &&
		row.LeasedAt.Equal(ensure.LeasedAt) &&
		!networkLeaseHasReleaseFields(row)
}

// networkReplacementReleaseMatches compares the complete requested quarantine proof while retaining prior ensure history.
func networkReplacementReleaseMatches(row models.LoopbackAddressLease, release NetworkLeaseRelease) bool {
	return row.Id > 0 &&
		row.NetworkStateId == networkStateSingletonID &&
		!row.ProjectId.Valid &&
		row.SourceProjectId == string(release.Lease.Key.ProjectID) &&
		row.Kind == string(release.Lease.Key.Kind()) &&
		row.SecondaryId == release.Lease.Key.SecondaryID &&
		row.Address == release.Lease.Address.String() &&
		row.State == "quarantined" &&
		row.OwnershipInstallationId == string(release.Lease.Ownership.InstallationID) &&
		row.OwnershipGeneration == int(release.Lease.Ownership.Generation) &&
		row.ReleaseGeneration.Valid && row.ReleaseGeneration.Int64 == int64(release.ReleaseGeneration) &&
		row.ReleaseEvidence.Valid && row.ReleaseEvidence.String == release.ReleaseEvidence &&
		row.ReleasedAt != nil && row.ReleasedAt.Equal(release.ReleasedAt) &&
		row.QuarantinedAt != nil && row.QuarantinedAt.Equal(release.QuarantinedAt) &&
		row.ReuseAfter != nil && row.ReuseAfter.Equal(release.ReuseAfter) &&
		row.QuarantineReason.Valid && row.QuarantineReason.String == release.QuarantineReason
}

// networkReplacementEndpointMatches compares one endpoint's public shape, generation, and logical identity join.
func networkReplacementEndpointMatches(
	row models.PublicEndpointLease,
	endpoint EndpointReservation,
	activeKeysByID map[int]identity.LeaseKey,
) bool {
	if row.Id <= 0 ||
		row.NetworkStateId != networkStateSingletonID ||
		row.ProjectId != string(endpoint.Key.ProjectID) ||
		row.EndpointId != endpoint.Key.EndpointID ||
		row.Protocol != string(endpoint.Protocol) ||
		row.Hostname != endpoint.Host ||
		row.Address != endpoint.Public.Addr().String() ||
		row.Port != int(endpoint.Public.Port()) ||
		row.Generation != int(endpoint.Generation) {
		return false
	}
	if endpoint.Identity == nil {
		return !row.LoopbackAddressLeaseId.Valid
	}
	if !row.LoopbackAddressLeaseId.Valid || row.LoopbackAddressLeaseId.Int64 <= 0 {
		return false
	}
	leaseID := int(row.LoopbackAddressLeaseId.Int64)
	return int64(leaseID) == row.LoopbackAddressLeaseId.Int64 && activeKeysByID[leaseID] == *endpoint.Identity
}

// validateNetworkProjectReplacementUntouched rejects side effects outside the root and preflighted target rows.
func validateNetworkProjectReplacementUntouched(
	before networkModelRows,
	after networkModelRows,
	plan networkProjectReplacementPlan,
) error {
	if len(before.States) != 1 || len(after.States) != 1 {
		return corruptStateError("network state", "1", fmt.Errorf("replacement readback has an invalid singleton count"))
	}
	beforeRoot := before.States[0]
	afterRoot := after.States[0]
	afterRoot.Revision = beforeRoot.Revision
	afterRoot.UpdatedAt = beforeRoot.UpdatedAt
	if !reflect.DeepEqual(afterRoot, beforeRoot) {
		return corruptStateError("network state", "1", fmt.Errorf("replacement changed immutable root fields"))
	}
	for _, comparison := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "network pool candidates", before: before.Candidates, after: after.Candidates},
		{name: "network setup evidence", before: before.SetupEvidence, after: after.SetupEvidence},
		{name: "network shared listeners", before: before.Listeners, after: after.Listeners},
		{name: "network project releases", before: before.Releases, after: after.Releases},
		{name: "network projects", before: before.Projects, after: after.Projects},
		{name: "network release owners", before: before.ReleaseOwners, after: after.ReleaseOwners},
	} {
		if !reflect.DeepEqual(comparison.before, comparison.after) {
			return corruptStateError("network state", "1", fmt.Errorf("replacement changed %s", comparison.name))
		}
	}
	afterLeases := make(map[int]models.LoopbackAddressLease, len(after.Leases))
	for _, row := range after.Leases {
		afterLeases[row.Id] = row
	}
	beforeLeaseIDs := make(map[int]struct{}, len(before.Leases))
	releasesByID := make(map[int]networkReplacementReleaseAction, len(plan.releases))
	consumesByID := make(map[int]networkReplacementEnsureAction, len(plan.ensures))
	for _, action := range plan.releases {
		releasesByID[action.row.Id] = action
	}
	for _, action := range plan.ensures {
		if action.mode == networkReplacementEnsureConsume {
			consumesByID[action.row.Id] = action
		}
	}
	accountedLeaseIDs := make(map[int]struct{}, len(after.Leases))
	for _, row := range before.Leases {
		beforeLeaseIDs[row.Id] = struct{}{}
		persisted, exists := afterLeases[row.Id]
		if !exists {
			return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("row disappeared"))
		}
		if action, changed := releasesByID[row.Id]; changed {
			if !networkReplacementReleasedRowMatches(persisted, row, action.request) {
				return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("released row differs from its exact planned quarantine"))
			}
			accountedLeaseIDs[row.Id] = struct{}{}
			continue
		}
		if action, changed := consumesByID[row.Id]; changed {
			if !networkReplacementConsumedRowMatches(persisted, row.Id, action.request) {
				return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("consumed row differs from its exact planned ensure"))
			}
			accountedLeaseIDs[row.Id] = struct{}{}
			continue
		}
		if !reflect.DeepEqual(persisted, row) {
			return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("untouched row changed"))
		}
		accountedLeaseIDs[row.Id] = struct{}{}
	}
	for _, action := range plan.ensures {
		if action.mode != networkReplacementEnsureCreate {
			continue
		}
		matchedID := 0
		for _, row := range after.Leases {
			if _, existed := beforeLeaseIDs[row.Id]; existed || !networkReplacementEnsureMatches(row, action.request) {
				continue
			}
			if matchedID != 0 {
				return corruptStateError("loopback address lease", action.request.Lease.Address.String(), fmt.Errorf("created ensure is duplicated"))
			}
			matchedID = row.Id
			expected := networkReplacementLeaseModel(action.request)
			expected.Id = row.Id
			if !reflect.DeepEqual(row, expected) {
				return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("created row differs from its exact planned ensure"))
			}
		}
		if matchedID == 0 {
			return corruptStateError("loopback address lease", action.request.Lease.Address.String(), fmt.Errorf("created ensure is missing"))
		}
		accountedLeaseIDs[matchedID] = struct{}{}
	}
	if len(accountedLeaseIDs) != len(after.Leases) {
		return corruptStateError("loopback address lease", "readback", fmt.Errorf("replacement produced %d unplanned rows", len(after.Leases)-len(accountedLeaseIDs)))
	}

	afterEndpoints := make(map[int]models.PublicEndpointLease, len(after.Endpoints))
	for _, row := range after.Endpoints {
		afterEndpoints[row.Id] = row
	}
	beforeEndpointIDs := make(map[int]struct{}, len(before.Endpoints))
	accountedEndpointIDs := make(map[int]struct{}, len(after.Endpoints))
	for _, row := range before.Endpoints {
		beforeEndpointIDs[row.Id] = struct{}{}
		if row.ProjectId == string(plan.projectID) {
			continue
		}
		persisted, exists := afterEndpoints[row.Id]
		if !exists || !reflect.DeepEqual(persisted, row) {
			return corruptStateError("public endpoint lease", scopedKey(row.ProjectId, row.EndpointId, row.Id), fmt.Errorf("untouched row changed"))
		}
		accountedEndpointIDs[row.Id] = struct{}{}
	}
	activeLeaseIDs, err := networkReplacementLeaseIDsFromRows(after.Leases)
	if err != nil {
		return err
	}
	for _, action := range plan.endpoints {
		persistedID := action.id
		if persistedID == 0 {
			for _, row := range after.Endpoints {
				if row.ProjectId != string(plan.projectID) || row.EndpointId != action.request.Key.EndpointID {
					continue
				}
				if _, existed := beforeEndpointIDs[row.Id]; existed {
					return corruptStateError("public endpoint lease", networkInitializationEndpointKey(action.request.Key), fmt.Errorf("new endpoint reused an existing row ID"))
				}
				if persistedID != 0 {
					return corruptStateError("public endpoint lease", networkInitializationEndpointKey(action.request.Key), fmt.Errorf("new endpoint is duplicated"))
				}
				persistedID = row.Id
			}
		}
		persisted, exists := afterEndpoints[persistedID]
		if !exists {
			return corruptStateError("public endpoint lease", networkInitializationEndpointKey(action.request.Key), fmt.Errorf("planned endpoint is missing"))
		}
		expectedAction := action
		expectedAction.id = persistedID
		expected, err := networkReplacementEndpointModel(expectedAction, plan.projectID, activeLeaseIDs)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("public endpoint lease", scopedKey(persisted.ProjectId, persisted.EndpointId, persisted.Id), fmt.Errorf("row differs from its exact lifecycle plan"))
		}
		accountedEndpointIDs[persistedID] = struct{}{}
	}
	if len(accountedEndpointIDs) != len(after.Endpoints) {
		return corruptStateError("public endpoint lease", "readback", fmt.Errorf("replacement produced %d unplanned rows", len(after.Endpoints)-len(accountedEndpointIDs)))
	}
	return nil
}

// networkReplacementReleasedRowMatches proves release mutation retained hidden ensure history and surrogate identity exactly.
func networkReplacementReleasedRowMatches(
	persisted models.LoopbackAddressLease,
	previous models.LoopbackAddressLease,
	release NetworkLeaseRelease,
) bool {
	expected := previous
	expected.ProjectId = null.String{}
	expected.State = "quarantined"
	expected.ReleaseGeneration = null.IntFrom(int64(release.ReleaseGeneration))
	expected.ReleaseEvidence = null.StringFrom(release.ReleaseEvidence)
	releasedAt := release.ReleasedAt
	quarantinedAt := release.QuarantinedAt
	reuseAfter := release.ReuseAfter
	expected.ReleasedAt = &releasedAt
	expected.QuarantinedAt = &quarantinedAt
	expected.ReuseAfter = &reuseAfter
	expected.QuarantineReason = null.StringFrom(release.QuarantineReason)
	return reflect.DeepEqual(persisted, expected)
}

// networkReplacementConsumedRowMatches proves quarantine reuse retained its row identity and only the new ensure facts.
func networkReplacementConsumedRowMatches(
	persisted models.LoopbackAddressLease,
	id int,
	ensure NetworkLeaseEnsure,
) bool {
	expected := networkReplacementLeaseModel(ensure)
	expected.Id = id
	return reflect.DeepEqual(persisted, expected)
}

// networkReplacementLeaseIDsFromRows resolves exact active IDs without issuing another read during verification.
func networkReplacementLeaseIDsFromRows(rows []models.LoopbackAddressLease) (map[identity.LeaseKey]int, error) {
	result := make(map[identity.LeaseKey]int, len(rows))
	for _, row := range rows {
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return nil, corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, corruptStateError("loopback address lease", durableKey(row.Address, row.Id), fmt.Errorf("active lease key is duplicated"))
		}
		result[key] = row.Id
	}
	return result, nil
}

// networkProjectReplacementConflict constructs the stable typed preflight failure boundary.
func networkProjectReplacementConflict(projectID domain.ProjectID, difference string) error {
	return &NetworkProjectReplacementConflictError{ProjectID: projectID, Difference: difference}
}
