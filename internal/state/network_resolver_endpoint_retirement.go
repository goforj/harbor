package state

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// ClearResolverStageNativeTCPEndpointsRequest identifies the exact quiescent resolver-stage network projection to repair.
type ClearResolverStageNativeTCPEndpointsRequest struct {
	ExpectedNetworkRevision domain.Sequence
	At                      time.Time
}

// Validate rejects a repair request that cannot safely fence one durable network projection.
func (request ClearResolverStageNativeTCPEndpointsRequest) Validate() error {
	if _, err := sequenceToModelInt("expected resolver endpoint retirement network revision", request.ExpectedNetworkRevision, false); err != nil {
		return err
	}
	return validateStoredTime("resolver endpoint retirement time", request.At)
}

// ClearResolverStageNativeTCPEndpoints retires only stale native TCP endpoints after every registered project is fully stopped.
func (store *Store) ClearResolverStageNativeTCPEndpoints(
	ctx context.Context,
	request ClearResolverStageNativeTCPEndpointsRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "resolver-stage native endpoint retirement", func(tx *gorm.DB) error {
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
		if len(before.Listeners) != 0 {
			return fmt.Errorf("resolver endpoint retirement requires no shared listener reservations")
		}
		for _, endpoint := range before.Endpoints {
			if endpoint.Protocol != string(EndpointProtocolTCP) {
				return fmt.Errorf("resolver endpoint retirement refuses non-TCP endpoint %q/%q", endpoint.ProjectId, endpoint.EndpointId)
			}
		}
		current, initialized, err := networkRecordFromModels(before)
		if err != nil {
			return err
		}
		if !initialized {
			return &NetworkNotInitializedError{}
		}
		if current.Stage != NetworkStageResolver {
			return fmt.Errorf("resolver endpoint retirement requires %q network stage, found %q", NetworkStageResolver, current.Stage)
		}
		if current.Revision != request.ExpectedNetworkRevision {
			return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: current.Revision}
		}
		if request.At.Before(current.UpdatedAt) {
			return fmt.Errorf("resolver endpoint retirement time precedes network update time")
		}
		if err := requireStoppedResolverEndpointProjects(tx); err != nil {
			return err
		}
		if _, err := validateRetainedSequenceBounds(tx); err != nil {
			return err
		}
		if len(before.Endpoints) == 0 {
			result = NetworkMutationResult{Record: current, Replayed: true}
			return result.Validate()
		}

		deleted := tx.Where("network_state_id = ? AND protocol = ?", networkStateSingletonID, string(EndpointProtocolTCP)).Delete(&models.PublicEndpointLease{})
		if err := requireResolverEndpointRetirementCount(deleted, len(before.Endpoints)); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.NetworkState{}).
			Where("id = ? AND stage = ? AND revision = ?", networkStateSingletonID, string(NetworkStageResolver), int(request.ExpectedNetworkRevision)).
			Updates(map[string]any{
				"updated_at": request.At,
				"revision":   int(sequence),
			})
		if err := requireOneMutation(updated, "retire resolver native endpoints", "1"); err != nil {
			return err
		}
		after, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read retired resolver endpoints: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(after)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after resolver endpoint retirement"))
		}
		expected := current
		expected.Revision = sequence
		expected.UpdatedAt = request.At
		expected.Reservations.Endpoints = []EndpointReservation{}
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("network state", "1", fmt.Errorf("resolver endpoint retirement changed durable state beyond endpoint reservations"))
		}
		result = NetworkMutationResult{Record: persisted}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("clear resolver-stage native TCP endpoints: %w", err)
	}
	return result, nil
}

// requireStoppedResolverEndpointProjects prevents a legacy cleanup from withdrawing routes while any project can still use them.
func requireStoppedResolverEndpointProjects(tx *gorm.DB) error {
	projects, err := readProjectRecords(tx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project.Project.State != domain.ProjectStopped {
			return fmt.Errorf("resolver endpoint retirement requires project %q to be stopped, found %q", project.Project.ID, project.Project.State)
		}
		if err := validateStoppedRuntimeProject(project.Project); err != nil {
			return fmt.Errorf("resolver endpoint retirement requires project %q to be inactive: %w", project.Project.ID, err)
		}
	}
	return nil
}

// requireResolverEndpointRetirementCount prevents a concurrent or weakened-schema delete from being mistaken for the fenced cleanup.
func requireResolverEndpointRetirementCount(result *gorm.DB, expected int) error {
	if result.Error != nil {
		return fmt.Errorf("retire resolver native endpoints: %w", result.Error)
	}
	if result.RowsAffected != int64(expected) {
		return fmt.Errorf("retire resolver native endpoints affected %d rows, expected %d", result.RowsAffected, expected)
	}
	return nil
}
