package state

import (
	"context"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// InitializeNetworkIdentity commits the durable ownership and loopback pool foundation after their host postconditions are verified.
func (store *Store) InitializeNetworkIdentity(
	ctx context.Context,
	request InitializeNetworkIdentityRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	request = cloneInitializeNetworkIdentityRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "network identity initialization", func(tx *gorm.DB) error {
		if err := requireNoNetworkSetupPlanForDirectInitialization(tx); err != nil {
			return err
		}
		initialized, err := initializeNetworkIdentityInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = initialized
		return nil
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("initialize network identity state: %w", err)
	}
	return result, nil
}

// initializeNetworkIdentityInTransaction applies or replays the existing identity-foundation contract without opening a nested writer transaction.
func initializeNetworkIdentityInTransaction(
	tx *gorm.DB,
	request InitializeNetworkIdentityRequest,
) (NetworkMutationResult, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if !present {
		return NetworkMutationResult{}, fmt.Errorf("network persistence schema is not installed")
	}

	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	current, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return NetworkMutationResult{}, err
	}
	if initialized {
		if difference := networkIdentityInitializationDifference(rows, request); difference != "" {
			return NetworkMutationResult{}, &NetworkInitializationConflictError{
				ActualRevision: current.Revision,
				Difference:     difference,
			}
		}
		result := NetworkMutationResult{Record: current, Replayed: true}
		if err := result.Validate(); err != nil {
			return NetworkMutationResult{}, err
		}
		return result, nil
	}

	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if err := insertNetworkInitializationFoundation(
		tx,
		NetworkStageIdentity,
		request.Ownership,
		request.Pool,
		request.PoolGeneration,
		request.Setup,
		request.At,
		sequence,
	); err != nil {
		return NetworkMutationResult{}, err
	}

	persistedRows, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("read initialized network identity state: %w", err)
	}
	persisted, exists, err := networkRecordFromModels(persistedRows)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if !exists {
		return NetworkMutationResult{}, corruptStateError("network state", "1", fmt.Errorf("initialized identity aggregate is missing after insert"))
	}
	if persisted.Revision != sequence {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"1",
			fmt.Errorf("readback revision is %d, expected %d", persisted.Revision, sequence),
		)
	}
	if difference := networkIdentityInitializationDifference(persistedRows, request); difference != "" {
		return NetworkMutationResult{}, corruptStateError("network state", "1", fmt.Errorf("readback differs in %s", difference))
	}
	expected := networkIdentityInitializationProjection(request, sequence)
	if err := validateNetworkInitializationProjection(persisted, expected); err != nil {
		return NetworkMutationResult{}, err
	}
	if err := validateNetworkSequenceExclusivity(tx, sequence); err != nil {
		return NetworkMutationResult{}, err
	}
	result := NetworkMutationResult{Record: persisted, Replayed: false}
	if err := result.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	return result, nil
}

// cloneInitializeNetworkIdentityRequest isolates the queued transaction from caller-owned proof mutation.
func cloneInitializeNetworkIdentityRequest(request InitializeNetworkIdentityRequest) InitializeNetworkIdentityRequest {
	request.Setup = slices.Clone(request.Setup)
	request.At = canonicalNetworkMutationTime(request.At)
	for index := range request.Setup {
		request.Setup[index].VerifiedAt = canonicalNetworkMutationTime(request.Setup[index].VerifiedAt)
	}
	return request
}

// networkIdentityInitializationDifference compares every durable identity-stage fact while excluding surrogate IDs and revision.
func networkIdentityInitializationDifference(rows networkModelRows, request InitializeNetworkIdentityRequest) string {
	if len(rows.States) != 1 {
		return "network root"
	}
	root := rows.States[0]
	if root.Stage != string(NetworkStageIdentity) {
		return "network stage"
	}
	if root.Id != networkStateSingletonID ||
		root.InstallationId != string(request.Ownership.InstallationID) ||
		root.OwnershipGeneration != int(request.Ownership.Generation) {
		return "network ownership"
	}
	if root.PoolNetwork != request.Pool.Prefix().Addr().String() ||
		root.PoolPrefixLength != request.Pool.Prefix().Bits() ||
		root.DnsSuffix != ".test" {
		return "network pool"
	}
	if !root.CreatedAt.Equal(request.At) || !root.UpdatedAt.Equal(request.At) {
		return "network root timestamps"
	}
	if difference := networkInitializationCandidateDifference(rows.Candidates, request.Pool, request.PoolGeneration); difference != "" {
		return difference
	}
	if difference := networkInitializationSetupDifference(rows.SetupEvidence, request.Setup); difference != "" {
		return difference
	}
	if len(rows.Listeners) != 0 {
		return "network listeners"
	}
	if len(rows.Leases) != 0 {
		return "network lease state"
	}
	if len(rows.Endpoints) != 0 {
		return "network endpoints"
	}
	if len(rows.Releases) != 0 {
		return "project release state"
	}
	return ""
}

// networkIdentityInitializationProjection builds the safe identity-only record expected from a successful readback.
func networkIdentityInitializationProjection(
	request InitializeNetworkIdentityRequest,
	sequence domain.Sequence,
) NetworkRecord {
	return NetworkRecord{
		Stage:       NetworkStageIdentity,
		Revision:    sequence,
		CreatedAt:   request.At,
		UpdatedAt:   request.At,
		Ownership:   request.Ownership,
		Pool:        request.Pool,
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: DataPlaneReservations{
			Endpoints:            []EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
}
