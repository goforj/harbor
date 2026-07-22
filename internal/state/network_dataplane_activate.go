package state

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkDataPlaneActivationConflictError reports durable full-stage facts that differ from an activation retry.
type NetworkDataPlaneActivationConflictError struct {
	ActualRevision domain.Sequence
	Difference     string
}

// Error describes the active revision and the non-secret fact group that differs.
func (err *NetworkDataPlaneActivationConflictError) Error() string {
	return fmt.Sprintf(
		"network data plane is already active at revision %d with different %s",
		err.ActualRevision,
		err.Difference,
	)
}

// ActivateNetworkDataPlane adds the remaining resolver and listener authority to an exact pre-full network revision.
func (store *Store) ActivateNetworkDataPlane(
	ctx context.Context,
	request ActivateNetworkDataPlaneRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	request = cloneActivateNetworkDataPlaneRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "network data-plane activation", func(tx *gorm.DB) error {
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

		sourceStage := current.Stage
		var projectedOwnership machineOwnershipProjectionState
		var setupToInsert []NetworkSetupProof
		upgradeOwnership := false
		switch current.Stage {
		case NetworkStageFull:
			projectedOwnership, err := readMachineOwnershipProjectionStateInTransaction(tx)
			if err != nil {
				return err
			}
			if projectedOwnership.observation.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
				return corruptStateError(
					"machine ownership projection",
					fmt.Sprint(projectedOwnership.row.Id),
					fmt.Errorf("full-stage network retains schema-%d ownership", projectedOwnership.observation.Record.SchemaVersion),
				)
			}
			if difference := networkDataPlaneActivationDifference(before, request); difference != "" {
				return &NetworkDataPlaneActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     difference,
				}
			}
			if projectedOwnership.observation != request.ConfirmedOwnership {
				return &NetworkDataPlaneActivationConflictError{
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
		case NetworkStageResolver:
			if current.Revision != request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{
					Expected: request.ExpectedNetworkRevision,
					Actual:   current.Revision,
				}
			}
			projectedOwnership, err = readMachineOwnershipProjectionStateInTransaction(tx)
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
			if projectedOwnership.observation != request.ConfirmedOwnership {
				return &NetworkDataPlaneActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     "machine ownership projection",
				}
			}
			if networkDataPlaneResolverProofDifference(before.SetupEvidence, request.Setup[0]) != "" {
				return &NetworkDataPlaneActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     "network resolver proof",
				}
			}
			setupToInsert = request.Setup[1:]
		case NetworkStageIdentity:
			if current.Revision != request.ExpectedNetworkRevision {
				return &NetworkRevisionConflictError{
					Expected: request.ExpectedNetworkRevision,
					Actual:   current.Revision,
				}
			}
			expectedOwnership, err := networkDataPlaneActivationIdentityOwnership(request.ConfirmedOwnership)
			if err != nil {
				return err
			}
			projectedOwnership, err = readMachineOwnershipProjectionStateInTransaction(tx)
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
				return &NetworkDataPlaneActivationConflictError{
					ActualRevision: current.Revision,
					Difference:     "machine ownership projection",
				}
			}
			setupToInsert = request.Setup
			upgradeOwnership = true
		default:
			return corruptStateError("network state", "1", fmt.Errorf("stage %q cannot be activated", current.Stage))
		}
		if request.At.Before(current.UpdatedAt) {
			return &NetworkDataPlaneActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "activation time",
			}
		}
		if request.At.Before(projectedOwnership.confirmedAt) {
			return &NetworkDataPlaneActivationConflictError{
				ActualRevision: current.Revision,
				Difference:     "activation time",
			}
		}

		if err := insertNetworkDataPlaneActivation(tx, setupToInsert, request.Listeners); err != nil {
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
				string(sourceStage),
				int(request.ExpectedNetworkRevision),
			).
			Updates(map[string]any{
				"stage":      string(NetworkStageFull),
				"updated_at": request.At,
				"revision":   int(sequence),
			})
		if err := requireOneMutation(updated, "activate network data plane", "1"); err != nil {
			return err
		}
		if upgradeOwnership {
			if err := upgradeMachineOwnershipProjectionInTransaction(
				tx,
				projectedOwnership,
				request.ConfirmedOwnership,
				request.At,
			); err != nil {
				return err
			}
		}

		persistedRows, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read activated network data plane: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(persistedRows)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("aggregate is missing after data-plane activation"))
		}
		if persisted.Stage != NetworkStageFull ||
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
					NetworkStageFull,
					sequence,
					request.At.Format(time.RFC3339Nano),
				),
			)
		}
		if difference := networkDataPlaneActivationDifference(persistedRows, request); difference != "" {
			return corruptStateError("network state", "1", fmt.Errorf("activation readback differs in %s", difference))
		}
		expected := current
		expected.Stage = NetworkStageFull
		expected.Revision = sequence
		expected.UpdatedAt = request.At
		expected.Reservations.Listeners = request.Listeners
		if !reflect.DeepEqual(persisted, expected) {
			return corruptStateError("network state", "1", fmt.Errorf("activation readback aggregate differs from its preflighted projection"))
		}
		if err := validateNetworkDataPlaneActivationRows(before, persistedRows, request, setupToInsert, sequence); err != nil {
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
				fmt.Errorf("data-plane activation allocated revision %d", sequence),
			)
		}
		if err := validateNetworkSequenceExclusivity(tx, sequence); err != nil {
			return err
		}
		result = NetworkMutationResult{Record: persisted, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("activate network data plane: %w", err)
	}
	return result, nil
}

// validateConfirmedNetworkDataPlaneOwnership accepts only an exact helper-confirmed schema-two policy binding.
func validateConfirmedNetworkDataPlaneOwnership(observation ownership.Observation) error {
	if err := validateConfirmedMachineOwnershipObservation(observation); err != nil {
		return fmt.Errorf("confirmed network data-plane ownership: %w", err)
	}
	if observation.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"confirmed network data-plane ownership schema version is %d, want %d",
			observation.Record.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	return nil
}

// networkDataPlaneActivationIdentityOwnership derives the one schema-one record that the confirmed target may replace.
func networkDataPlaneActivationIdentityOwnership(
	target ownership.Observation,
) (ownership.Observation, error) {
	if err := validateConfirmedNetworkDataPlaneOwnership(target); err != nil {
		return ownership.Observation{}, err
	}
	record := target.Record
	record.SchemaVersion = ownership.IdentitySchemaVersion
	record.NetworkPolicyFingerprint = ""
	fingerprint, err := record.Fingerprint()
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("derive identity ownership projection: %w", err)
	}
	return ownership.Observation{Exists: true, Record: record, Fingerprint: fingerprint}, nil
}

// upgradeMachineOwnershipProjectionInTransaction binds the exact identity projection to helper-confirmed policy authority.
func upgradeMachineOwnershipProjectionInTransaction(
	tx *gorm.DB,
	current machineOwnershipProjectionState,
	target ownership.Observation,
	confirmedAt time.Time,
) error {
	expectedSource, err := networkDataPlaneActivationIdentityOwnership(target)
	if err != nil {
		return err
	}
	if current.observation != expectedSource {
		return fmt.Errorf("machine ownership projection differs from the exact schema-one activation source")
	}
	if err := validateStoredTime("machine ownership activation confirmation time", confirmedAt); err != nil {
		return err
	}
	updated := tx.Model(&models.MachineOwnershipProjection{}).
		Where(
			`id = ? AND network_state_id = ? AND ownership_schema_version = ?
				AND installation_id = ? AND owner_identity = ? AND ownership_generation = ?
				AND loopback_pool_prefix = ? AND network_policy_fingerprint IS NULL
				AND ticket_verifier_key = ? AND record_fingerprint = ? AND confirmed_at = ?`,
			current.row.Id,
			current.row.NetworkStateId,
			int(ownership.IdentitySchemaVersion),
			current.row.InstallationId,
			current.row.OwnerIdentity,
			current.row.OwnershipGeneration,
			current.row.LoopbackPoolPrefix,
			current.row.TicketVerifierKey,
			current.row.RecordFingerprint,
			current.row.ConfirmedAt,
		).
		Updates(map[string]any{
			"ownership_schema_version":   int(ownership.NetworkPolicySchemaVersion),
			"network_policy_fingerprint": target.Record.NetworkPolicyFingerprint,
			"record_fingerprint":         target.Fingerprint,
			"confirmed_at":               canonicalNetworkMutationTime(confirmedAt),
		})
	if err := requireOneMutation(updated, "upgrade machine ownership projection", fmt.Sprint(current.row.Id)); err != nil {
		return err
	}

	persisted, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return fmt.Errorf("read upgraded machine ownership projection: %w", err)
	}
	if persisted.observation != target || !persisted.confirmedAt.Equal(confirmedAt) {
		return corruptStateError(
			"machine ownership projection",
			fmt.Sprint(current.row.Id),
			fmt.Errorf("activation upgrade readback differs from the exact confirmed target"),
		)
	}
	expectedRow := current.row
	expectedRow.OwnershipSchemaVersion = int(ownership.NetworkPolicySchemaVersion)
	expectedRow.NetworkPolicyFingerprint = machineOwnershipNetworkPolicyModelValue(target.Record)
	expectedRow.RecordFingerprint = target.Fingerprint
	expectedRow.ConfirmedAt = canonicalNetworkMutationTime(confirmedAt)
	if !reflect.DeepEqual(persisted.row, expectedRow) {
		return corruptStateError(
			"machine ownership projection",
			fmt.Sprint(current.row.Id),
			fmt.Errorf("activation upgrade changed immutable projection facts"),
		)
	}
	return nil
}

// cloneActivateNetworkDataPlaneRequest isolates queued persistence from caller-owned setup proof storage.
func cloneActivateNetworkDataPlaneRequest(request ActivateNetworkDataPlaneRequest) ActivateNetworkDataPlaneRequest {
	request.Setup = slices.Clone(request.Setup)
	request.At = canonicalNetworkMutationTime(request.At)
	for index := range request.Setup {
		request.Setup[index].VerifiedAt = canonicalNetworkMutationTime(request.Setup[index].VerifiedAt)
	}
	request.Listeners.DNS.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.DNS.VerifiedAt)
	request.Listeners.HTTP.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.HTTP.VerifiedAt)
	request.Listeners.HTTPS.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.HTTPS.VerifiedAt)
	return request
}

// insertNetworkDataPlaneActivation appends only the authority absent from the current pre-full aggregate.
func insertNetworkDataPlaneActivation(
	tx *gorm.DB,
	setup []NetworkSetupProof,
	listeners SharedListenerReservations,
) error {
	for _, proof := range setup {
		if err := insertNetworkSetupProof(tx, proof); err != nil {
			return err
		}
	}
	for _, listener := range networkInitializationListeners(listeners) {
		reservation := listener.reservation
		row := models.NetworkSharedListener{
			NetworkStateId:    networkStateSingletonID,
			Kind:              listener.kind,
			Mode:              string(reservation.Mode),
			AdvertisedAddress: reservation.Advertised.Addr().String(),
			AdvertisedPort:    int(reservation.Advertised.Port()),
			BindAddress:       reservation.Bind.Addr().String(),
			BindPort:          int(reservation.Bind.Port()),
			Generation:        int(reservation.Generation),
			VerifiedAt:        reservation.VerifiedAt,
		}
		if err := requireOneCreate(tx.Create(&row), "create network shared listener", listener.kind); err != nil {
			return err
		}
	}
	return nil
}

// networkDataPlaneActivationDifference compares every durable fact supplied by an activation retry.
func networkDataPlaneActivationDifference(rows networkModelRows, request ActivateNetworkDataPlaneRequest) string {
	if len(rows.States) != 1 || rows.States[0].Stage != string(NetworkStageFull) {
		return "network stage"
	}
	if networkDataPlaneSetupDifference(rows.SetupEvidence, request.Setup) != "" {
		return "network setup proofs"
	}
	return networkSharedListenerDifference(rows.Listeners, request.Listeners)
}

// networkDataPlaneSetupDifference compares the complete full-stage proof shape and supplied data-plane facts.
func networkDataPlaneSetupDifference(rows []models.NetworkSetupEvidence, setup []NetworkSetupProof) string {
	if len(rows) != 4 {
		return "network setup proofs"
	}
	byComponent := make(map[string]models.NetworkSetupEvidence, len(rows))
	for _, row := range rows {
		byComponent[row.Component] = row
	}
	for _, component := range []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
		NetworkSetupComponentResolver,
		NetworkSetupComponentLowPorts,
	} {
		row, exists := byComponent[string(component)]
		if !exists || row.NetworkStateId != networkStateSingletonID {
			return "network setup proofs"
		}
	}
	for _, proof := range setup {
		row := byComponent[string(proof.Component)]
		if row.Evidence != proof.Evidence ||
			row.Generation != int(proof.Generation) ||
			!row.VerifiedAt.Equal(proof.VerifiedAt) {
			return "network setup proofs"
		}
	}
	return ""
}

// networkDataPlaneResolverProofDifference requires the full transition to reuse the exact persisted resolver postcondition.
func networkDataPlaneResolverProofDifference(rows []models.NetworkSetupEvidence, expected NetworkSetupProof) string {
	if len(rows) != 3 {
		return "network resolver proof"
	}
	for _, row := range rows {
		if row.Component != string(NetworkSetupComponentResolver) {
			continue
		}
		if row.NetworkStateId != networkStateSingletonID ||
			row.Evidence != expected.Evidence ||
			row.Generation != int(expected.Generation) ||
			!row.VerifiedAt.Equal(expected.VerifiedAt) {
			return "network resolver proof"
		}
		return ""
	}
	return "network resolver proof"
}

// validateNetworkDataPlaneActivationRows proves every pre-existing row remained byte-for-byte stable.
func validateNetworkDataPlaneActivationRows(
	before networkModelRows,
	after networkModelRows,
	request ActivateNetworkDataPlaneRequest,
	setupToInsert []NetworkSetupProof,
	sequence domain.Sequence,
) error {
	if len(before.States) != 1 || len(after.States) != 1 {
		return corruptStateError("network state", "1", fmt.Errorf("activation changed singleton cardinality"))
	}
	expectedRoot := before.States[0]
	expectedRoot.Stage = string(NetworkStageFull)
	expectedRoot.UpdatedAt = request.At
	expectedRoot.Revision = int(sequence)
	if !reflect.DeepEqual(after.States[0], expectedRoot) {
		return corruptStateError("network state", "1", fmt.Errorf("activation changed immutable root facts"))
	}

	for _, rows := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "network pool candidates", before: before.Candidates, after: after.Candidates},
		{name: "loopback address leases", before: before.Leases, after: after.Leases},
		{name: "public endpoint leases", before: before.Endpoints, after: after.Endpoints},
		{name: "network project releases", before: before.Releases, after: after.Releases},
		{name: "network projects", before: before.Projects, after: after.Projects},
		{name: "network release owners", before: before.ReleaseOwners, after: after.ReleaseOwners},
	} {
		if !reflect.DeepEqual(rows.before, rows.after) {
			return corruptStateError("network state", "1", fmt.Errorf("activation changed %s", rows.name))
		}
	}
	if err := validateNetworkDataPlaneSetupRows(before.SetupEvidence, after.SetupEvidence, setupToInsert); err != nil {
		return err
	}
	return validateNetworkDataPlaneListenerRows(before.Listeners, after.Listeners, request.Listeners)
}

// validateNetworkDataPlaneSetupRows proves identity proofs were retained and only the requested proofs were appended.
func validateNetworkDataPlaneSetupRows(
	before []models.NetworkSetupEvidence,
	after []models.NetworkSetupEvidence,
	setup []NetworkSetupProof,
) error {
	if (len(before) != 2 && len(before) != 3) || len(after) != len(before)+len(setup) {
		return corruptStateError("network setup evidence", "activation", fmt.Errorf("activation changed proof cardinality unexpectedly"))
	}
	afterByComponent := make(map[string]models.NetworkSetupEvidence, len(after))
	for _, row := range after {
		afterByComponent[row.Component] = row
	}
	for _, row := range before {
		if persisted, exists := afterByComponent[row.Component]; !exists || !reflect.DeepEqual(persisted, row) {
			return corruptStateError("network setup evidence", row.Component, fmt.Errorf("existing proof changed during activation"))
		}
	}
	for _, proof := range setup {
		row, exists := afterByComponent[string(proof.Component)]
		if !exists || row.Id <= 0 ||
			row.NetworkStateId != networkStateSingletonID ||
			row.Evidence != proof.Evidence ||
			row.Generation != int(proof.Generation) ||
			!row.VerifiedAt.Equal(proof.VerifiedAt) {
			return corruptStateError("network setup evidence", string(proof.Component), fmt.Errorf("appended proof differs from request"))
		}
	}
	return nil
}

// validateNetworkDataPlaneListenerRows proves activation appended exactly the three requested shared listeners.
func validateNetworkDataPlaneListenerRows(
	before []models.NetworkSharedListener,
	after []models.NetworkSharedListener,
	reservations SharedListenerReservations,
) error {
	listeners := networkInitializationListeners(reservations)
	if len(before) != 0 || len(after) != len(listeners) {
		return corruptStateError("network shared listener", "activation", fmt.Errorf("activation changed listener cardinality unexpectedly"))
	}
	afterByKind := make(map[string]models.NetworkSharedListener, len(after))
	for _, row := range after {
		afterByKind[row.Kind] = row
	}
	for _, listener := range listeners {
		row, exists := afterByKind[listener.kind]
		reservation := listener.reservation
		if !exists || row.Id <= 0 ||
			row.NetworkStateId != networkStateSingletonID ||
			row.Mode != string(reservation.Mode) ||
			row.AdvertisedAddress != reservation.Advertised.Addr().String() ||
			row.AdvertisedPort != int(reservation.Advertised.Port()) ||
			row.BindAddress != reservation.Bind.Addr().String() ||
			row.BindPort != int(reservation.Bind.Port()) ||
			row.Generation != int(reservation.Generation) ||
			!row.VerifiedAt.Equal(reservation.VerifiedAt) {
			return corruptStateError("network shared listener", listener.kind, fmt.Errorf("appended listener differs from request"))
		}
	}
	return nil
}
