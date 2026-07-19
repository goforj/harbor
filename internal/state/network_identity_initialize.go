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
		if _, err := validateRetainedSequenceBounds(tx); err != nil {
			return err
		}
		if initialized {
			if difference := networkIdentityInitializationDifference(rows, request); difference != "" {
				return &NetworkInitializationConflictError{
					ActualRevision: current.Revision,
					Difference:     difference,
				}
			}
			result = NetworkMutationResult{Record: current, Replayed: true}
			return result.Validate()
		}

		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
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
			return err
		}

		persistedRows, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read initialized network identity state: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(persistedRows)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("initialized identity aggregate is missing after insert"))
		}
		if persisted.Revision != sequence {
			return corruptStateError(
				"network state",
				"1",
				fmt.Errorf("readback revision is %d, expected %d", persisted.Revision, sequence),
			)
		}
		if difference := networkIdentityInitializationDifference(persistedRows, request); difference != "" {
			return corruptStateError("network state", "1", fmt.Errorf("readback differs in %s", difference))
		}
		expected := networkIdentityInitializationProjection(request, sequence)
		if err := validateNetworkInitializationProjection(persisted, expected); err != nil {
			return err
		}
		if err := validateNetworkSequenceExclusivity(tx, sequence); err != nil {
			return err
		}
		result = NetworkMutationResult{Record: persisted, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("initialize network identity state: %w", err)
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
