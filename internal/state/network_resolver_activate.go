package state

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkResolverActivationConflictError reports durable resolver-stage facts that differ from an activation retry.
type NetworkResolverActivationConflictError struct {
	ActualRevision domain.Sequence
	Difference     string
}

// Error describes the active revision and the non-secret fact group that differs.
func (err *NetworkResolverActivationConflictError) Error() string {
	return fmt.Sprintf(
		"network resolver is already active at revision %d with different %s",
		err.ActualRevision,
		err.Difference,
	)
}

// ActivateNetworkResolver binds exact helper-confirmed resolver authority to an identity-stage network revision.
func (store *Store) ActivateNetworkResolver(
	ctx context.Context,
	request ActivateNetworkResolverRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	request = cloneActivateNetworkResolverRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "network resolver activation", func(tx *gorm.DB) error {
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
		if _, err := validateRetainedSequenceBounds(tx); err != nil {
			return err
		}

		switch current.Stage {
		case NetworkStageResolver:
			projectedOwnership, err := readMachineOwnershipProjectionStateInTransaction(tx)
			if err != nil {
				return err
			}
			if projectedOwnership.observation.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
				return corruptStateError(
					"machine ownership projection",
					fmt.Sprint(projectedOwnership.row.Id),
					fmt.Errorf("resolver-stage network retains schema-%d ownership", projectedOwnership.observation.Record.SchemaVersion),
				)
			}
			if difference := networkResolverActivationDifference(before, request); difference != "" {
				return &NetworkResolverActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     difference,
				}
			}
			if projectedOwnership.observation != request.ConfirmedOwnership {
				return &NetworkResolverActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     "machine ownership projection",
				}
			}
			if current.Revision < request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{
					Expected: request.ExpectedNetworkRevision,
					Actual:   current.Revision,
				}
			}
			result = NetworkMutationResult{Record: current, Replayed: true}
			return result.Validate()
		case NetworkStageIdentity:
			if current.Revision != request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{
					Expected: request.ExpectedNetworkRevision,
					Actual:   current.Revision,
				}
			}
		case NetworkStageFull:
			return &NetworkResolverActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "network stage",
			}
		default:
			return corruptStateError("network state", "1", fmt.Errorf("stage %q cannot activate resolver authority", current.Stage))
		}
		if len(before.Listeners) != 0 || len(before.Endpoints) != 0 {
			return corruptStateError(
				"network state",
				"identity-stage",
				fmt.Errorf("identity-stage network must not contain listener or endpoint reservations"),
			)
		}
		if request.At.Before(current.UpdatedAt) {
			return &NetworkResolverActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "activation time",
			}
		}
		expectedOwnership, err := networkDataPlaneActivationIdentityOwnership(request.ConfirmedOwnership)
		if err != nil {
			return err
		}
		projectedOwnership, err := readMachineOwnershipProjectionStateInTransaction(tx)
		if err != nil {
			return err
		}
		if projectedOwnership.observation.Record.SchemaVersion != ownership.IdentitySchemaVersion {
			return corruptStateError(
				"machine ownership projection",
				fmt.Sprint(projectedOwnership.row.Id),
				fmt.Errorf("identity-stage network retains schema-%d ownership", projectedOwnership.observation.Record.SchemaVersion),
			)
		}
		if projectedOwnership.observation != expectedOwnership {
			return &NetworkResolverActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "machine ownership projection",
			}
		}
		if request.At.Before(projectedOwnership.confirmedAt) {
			return &NetworkResolverActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "activation time",
			}
		}

		if err := insertNetworkSetupProof(tx, request.Resolver); err != nil {
			return err
		}
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		updated := tx.Model(&models.NetworkState{}).
			Where(
				"id = ? AND stage = ? AND revision = ?",
				networkStateSingletonID,
				string(NetworkStageIdentity),
				int(request.ExpectedNetworkRevision),
			).
			Updates(map[string]any{
				"stage":      string(NetworkStageResolver),
				"updated_at": request.At,
				"revision":   int(sequence),
			})
		if err := requireOneMutation(updated, "activate network resolver", "1"); err != nil {
			return err
		}
		if err := upgradeMachineOwnershipProjectionInTransaction(
			tx,
			projectedOwnership,
			request.ConfirmedOwnership,
			request.At,
		); err != nil {
			return err
		}

		persistedRows, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read activated network resolver: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(persistedRows)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after resolver activation"))
		}
		if persisted.Stage != NetworkStageResolver ||
			persisted.Revision != sequence ||
			!persisted.UpdatedAt.Equal(request.At) {
			return corruptStateError(
				"network state",
				"1",
				fmt.Errorf(
					"readback stage/revision/time is %q/%d/%s, expected %q/%d/%s",
					persisted.Stage,
					persisted.Revision,
					persisted.UpdatedAt.Format(time.RFC3339Nano),
					NetworkStageResolver,
					sequence,
					request.At.Format(time.RFC3339Nano),
				),
			)
		}
		if difference := networkResolverActivationDifference(persistedRows, request); difference != "" {
			return corruptStateError("network state", "1", fmt.Errorf("resolver activation readback differs in %s", difference))
		}
		expected := current
		expected.Stage = NetworkStageResolver
		expected.Revision = sequence
		expected.UpdatedAt = request.At
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("network state", "1", fmt.Errorf("resolver activation readback aggregate differs from its preflighted projection"))
		}
		if err := validateNetworkResolverActivationRows(before, persistedRows, request, sequence); err != nil {
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
				fmt.Errorf("resolver activation allocated revision %d", sequence),
			)
		}
		if err := validateNetworkSequenceExclusivity(tx, sequence); err != nil {
			return err
		}
		result = NetworkMutationResult{Record: persisted, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("activate network resolver: %w", err)
	}
	return result, nil
}

// cloneActivateNetworkResolverRequest canonicalizes caller-owned times before the request enters the writer queue.
func cloneActivateNetworkResolverRequest(request ActivateNetworkResolverRequest) ActivateNetworkResolverRequest {
	request.At = canonicalNetworkMutationTime(request.At)
	request.Resolver.VerifiedAt = canonicalNetworkMutationTime(request.Resolver.VerifiedAt)
	return request
}

// insertNetworkSetupProof persists one validated helper postcondition under the network singleton.
func insertNetworkSetupProof(tx *gorm.DB, proof NetworkSetupProof) error {
	row := models.NetworkSetupEvidence{
		NetworkStateId: networkStateSingletonID,
		Component:      string(proof.Component),
		Evidence:       proof.Evidence,
		Generation:     int(proof.Generation),
		VerifiedAt:     proof.VerifiedAt,
	}
	return requireOneCreate(tx.Create(&row), "create network setup evidence", string(proof.Component))
}

// networkResolverActivationDifference compares every durable resolver fact supplied by an activation retry.
func networkResolverActivationDifference(rows networkModelRows, request ActivateNetworkResolverRequest) string {
	if len(rows.States) != 1 || rows.States[0].Stage != string(NetworkStageResolver) {
		return "network stage"
	}
	if len(rows.SetupEvidence) != 3 {
		return "network setup proofs"
	}
	byComponent := make(map[string]models.NetworkSetupEvidence, len(rows.SetupEvidence))
	for _, row := range rows.SetupEvidence {
		byComponent[row.Component] = row
	}
	for _, component := range []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
		NetworkSetupComponentResolver,
	} {
		row, exists := byComponent[string(component)]
		if !exists || row.NetworkStateId != networkStateSingletonID {
			return "network setup proofs"
		}
	}
	resolver := byComponent[string(NetworkSetupComponentResolver)]
	if resolver.Evidence != request.Resolver.Evidence ||
		resolver.Generation != int(request.Resolver.Generation) ||
		!resolver.VerifiedAt.Equal(request.Resolver.VerifiedAt) {
		return "network setup proofs"
	}
	if len(rows.Listeners) != 0 || len(rows.Endpoints) != 0 {
		return "network reservations"
	}
	return ""
}

// validateNetworkResolverActivationRows proves activation retained every old row and appended only resolver authority.
func validateNetworkResolverActivationRows(
	before networkModelRows,
	after networkModelRows,
	request ActivateNetworkResolverRequest,
	sequence domain.Sequence,
) error {
	if len(before.States) != 1 || len(after.States) != 1 {
		return corruptStateError("network state", "1", fmt.Errorf("resolver activation changed singleton cardinality"))
	}
	expectedRoot := before.States[0]
	expectedRoot.Stage = string(NetworkStageResolver)
	expectedRoot.UpdatedAt = request.At
	expectedRoot.Revision = int(sequence)
	if !reflect.DeepEqual(after.States[0], expectedRoot) {
		return corruptStateError("network state", "1", fmt.Errorf("resolver activation changed immutable root facts"))
	}

	for _, rows := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "network pool candidates", before: before.Candidates, after: after.Candidates},
		{name: "network shared listeners", before: before.Listeners, after: after.Listeners},
		{name: "loopback address leases", before: before.Leases, after: after.Leases},
		{name: "public endpoint leases", before: before.Endpoints, after: after.Endpoints},
		{name: "network project releases", before: before.Releases, after: after.Releases},
		{name: "network projects", before: before.Projects, after: after.Projects},
		{name: "network release owners", before: before.ReleaseOwners, after: after.ReleaseOwners},
	} {
		if !reflect.DeepEqual(rows.before, rows.after) {
			return corruptStateError("network state", "1", fmt.Errorf("resolver activation changed %s", rows.name))
		}
	}
	if len(before.SetupEvidence) != 2 || len(after.SetupEvidence) != 3 {
		return corruptStateError("network setup evidence", "resolver activation", fmt.Errorf("resolver activation changed proof cardinality unexpectedly"))
	}
	afterByComponent := make(map[string]models.NetworkSetupEvidence, len(after.SetupEvidence))
	for _, row := range after.SetupEvidence {
		afterByComponent[row.Component] = row
	}
	for _, row := range before.SetupEvidence {
		if persisted, exists := afterByComponent[row.Component]; !exists || !reflect.DeepEqual(persisted, row) {
			return corruptStateError("network setup evidence", row.Component, fmt.Errorf("identity proof changed during resolver activation"))
		}
	}
	resolver, exists := afterByComponent[string(NetworkSetupComponentResolver)]
	if !exists || resolver.Id <= 0 ||
		resolver.NetworkStateId != networkStateSingletonID ||
		resolver.Evidence != request.Resolver.Evidence ||
		resolver.Generation != int(request.Resolver.Generation) ||
		!resolver.VerifiedAt.Equal(request.Resolver.VerifiedAt) {
		return corruptStateError("network setup evidence", string(NetworkSetupComponentResolver), fmt.Errorf("appended resolver proof differs from request"))
	}
	return nil
}
