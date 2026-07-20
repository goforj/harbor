package state

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const machineOwnershipProjectionSingletonID = 1

// machineOwnershipProjectionState retains the validated model alongside its canonical domain projection.
type machineOwnershipProjectionState struct {
	row         models.MachineOwnershipProjection
	observation ownership.Observation
	confirmedAt time.Time
}

// MachineOwnershipProjectionSource reads the daemon-owned confirmation used to prepare helper capabilities.
type MachineOwnershipProjectionSource struct {
	projections *models.MachineOwnershipProjectionRepo
}

var _ ticketissuer.OwnershipObserver = (*MachineOwnershipProjectionSource)(nil)

// NewMachineOwnershipProjectionSource creates a read-only source over generated harbord persistence.
func NewMachineOwnershipProjectionSource(
	projections *models.MachineOwnershipProjectionRepo,
) *MachineOwnershipProjectionSource {
	if projections == nil {
		panic("state.NewMachineOwnershipProjectionSource requires a non-nil projection repository")
	}
	return &MachineOwnershipProjectionSource{projections: projections}
}

// Observe returns the confirmed projection after proving it still agrees with the durable network root.
func (source *MachineOwnershipProjectionSource) Observe(
	ctx context.Context,
) (ownership.Observation, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ownership.Observation{}, err
	}
	connection, err := source.projections.WithContext(ctx).Builder()
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("open machine ownership projection: %w", err)
	}

	var observation ownership.Observation
	err = connection.Transaction(func(tx *gorm.DB) error {
		projected, _, readErr := readMachineOwnershipProjectionInTransaction(tx)
		if readErr != nil {
			return readErr
		}
		observation = projected
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("observe machine ownership projection: %w", err)
	}
	return observation, nil
}

// insertMachineOwnershipProjectionInTransaction persists only helper-confirmed authority after the network root exists.
func insertMachineOwnershipProjectionInTransaction(
	tx *gorm.DB,
	observation ownership.Observation,
	confirmedAt time.Time,
) error {
	if err := validateConfirmedMachineOwnershipProjection(observation, confirmedAt); err != nil {
		return err
	}
	generation, err := unsignedToModelInt("machine ownership projection generation", observation.Record.Generation, false)
	if err != nil {
		return err
	}
	row := models.MachineOwnershipProjection{
		Id:                       machineOwnershipProjectionSingletonID,
		NetworkStateId:           networkStateSingletonID,
		OwnershipSchemaVersion:   int(observation.Record.SchemaVersion),
		InstallationId:           observation.Record.InstallationID,
		OwnerIdentity:            observation.Record.OwnerIdentity,
		OwnershipGeneration:      generation,
		LoopbackPoolPrefix:       observation.Record.LoopbackPoolPrefix,
		NetworkPolicyFingerprint: machineOwnershipNetworkPolicyModelValue(observation.Record),
		TicketVerifierKey:        observation.Record.TicketVerifierKey,
		RecordFingerprint:        observation.Fingerprint,
		ConfirmedAt:              canonicalNetworkMutationTime(confirmedAt),
	}
	if err := requireOneCreate(tx.Create(&row), "create machine ownership projection", "1"); err != nil {
		return err
	}
	return nil
}

// readMachineOwnershipProjectionInTransaction reconstructs one projection and checks its network-root binding.
func readMachineOwnershipProjectionInTransaction(
	tx *gorm.DB,
) (ownership.Observation, time.Time, error) {
	state, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return ownership.Observation{}, time.Time{}, err
	}
	return state.observation, state.confirmedAt, nil
}

// readMachineOwnershipProjectionStateInTransaction retains the exact row for atomic preservation checks.
func readMachineOwnershipProjectionStateInTransaction(
	tx *gorm.DB,
) (machineOwnershipProjectionState, error) {
	var projectionRows []models.MachineOwnershipProjection
	if err := tx.Order("id ASC").Limit(2).Find(&projectionRows).Error; err != nil {
		return machineOwnershipProjectionState{}, fmt.Errorf("read machine ownership projection: %w", err)
	}
	if len(projectionRows) != 1 {
		return machineOwnershipProjectionState{}, corruptStateError(
			"machine ownership projection",
			"1",
			fmt.Errorf("found %d rows, expected 1", len(projectionRows)),
		)
	}

	var networkRows []models.NetworkState
	if err := tx.Order("id ASC").Limit(2).Find(&networkRows).Error; err != nil {
		return machineOwnershipProjectionState{}, fmt.Errorf("read network root for machine ownership projection: %w", err)
	}
	if len(networkRows) != 1 {
		return machineOwnershipProjectionState{}, corruptStateError(
			"machine ownership projection",
			"1",
			fmt.Errorf("network root has %d rows, expected 1", len(networkRows)),
		)
	}

	observation, confirmedAt, err := machineOwnershipProjectionFromModel(projectionRows[0])
	if err != nil {
		return machineOwnershipProjectionState{}, err
	}
	if err := requireMachineOwnershipProjectionNetworkRoot(projectionRows[0], networkRows[0], observation.Record); err != nil {
		return machineOwnershipProjectionState{}, err
	}
	return machineOwnershipProjectionState{
		row:         projectionRows[0],
		observation: observation,
		confirmedAt: confirmedAt,
	}, nil
}

// machineOwnershipProjectionFromModel validates every field before exposing projected helper authority.
func machineOwnershipProjectionFromModel(
	row models.MachineOwnershipProjection,
) (ownership.Observation, time.Time, error) {
	key := fmt.Sprint(row.Id)
	if row.Id != machineOwnershipProjectionSingletonID {
		return ownership.Observation{}, time.Time{}, corruptStateError(
			"machine ownership projection",
			key,
			fmt.Errorf("singleton ID is %d, expected 1", row.Id),
		)
	}
	if row.NetworkStateId != networkStateSingletonID {
		return ownership.Observation{}, time.Time{}, corruptStateError(
			"machine ownership projection",
			key,
			fmt.Errorf("network state ID is %d, expected 1", row.NetworkStateId),
		)
	}
	var networkPolicyFingerprint string
	switch row.OwnershipSchemaVersion {
	case int(ownership.IdentitySchemaVersion):
		if row.NetworkPolicyFingerprint != nil {
			return ownership.Observation{}, time.Time{}, corruptStateError(
				"machine ownership projection",
				key,
				fmt.Errorf("schema-%d network policy fingerprint is not NULL", ownership.IdentitySchemaVersion),
			)
		}
	case int(ownership.NetworkPolicySchemaVersion):
		if row.NetworkPolicyFingerprint == nil {
			return ownership.Observation{}, time.Time{}, corruptStateError(
				"machine ownership projection",
				key,
				fmt.Errorf("schema-%d network policy fingerprint is NULL", ownership.NetworkPolicySchemaVersion),
			)
		}
		networkPolicyFingerprint = *row.NetworkPolicyFingerprint
	default:
		return ownership.Observation{}, time.Time{}, corruptStateError(
			"machine ownership projection",
			key,
			fmt.Errorf(
				"ownership schema version is %d, expected %d or %d",
				row.OwnershipSchemaVersion,
				ownership.IdentitySchemaVersion,
				ownership.NetworkPolicySchemaVersion,
			),
		)
	}
	generation, err := positiveNetworkGeneration("machine ownership projection generation", row.OwnershipGeneration)
	if err != nil {
		return ownership.Observation{}, time.Time{}, corruptStateError("machine ownership projection", key, err)
	}
	record := ownership.Record{
		SchemaVersion:            uint32(row.OwnershipSchemaVersion),
		InstallationID:           row.InstallationId,
		OwnerIdentity:            row.OwnerIdentity,
		Generation:               generation,
		LoopbackPoolPrefix:       row.LoopbackPoolPrefix,
		NetworkPolicyFingerprint: networkPolicyFingerprint,
		TicketVerifierKey:        row.TicketVerifierKey,
	}
	if err := record.Validate(); err != nil {
		return ownership.Observation{}, time.Time{}, corruptStateError("machine ownership projection", key, err)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		return ownership.Observation{}, time.Time{}, corruptStateError("machine ownership projection", key, err)
	}
	if row.RecordFingerprint != fingerprint {
		return ownership.Observation{}, time.Time{}, corruptStateError(
			"machine ownership projection",
			key,
			fmt.Errorf("record fingerprint does not match the projected ownership record"),
		)
	}
	if err := validateStoredTime("machine ownership confirmation time", row.ConfirmedAt); err != nil {
		return ownership.Observation{}, time.Time{}, corruptStateError("machine ownership projection", key, err)
	}
	return ownership.Observation{Exists: true, Record: record, Fingerprint: fingerprint}, row.ConfirmedAt, nil
}

// machineOwnershipNetworkPolicyModelValue preserves SQL NULL as the only schema-one policy representation.
func machineOwnershipNetworkPolicyModelValue(record ownership.Record) *string {
	if record.SchemaVersion == ownership.IdentitySchemaVersion {
		return nil
	}
	value := record.NetworkPolicyFingerprint
	return &value
}

// validateConfirmedMachineOwnershipObservation accepts only an exact helper-confirmed record fingerprint pair.
func validateConfirmedMachineOwnershipObservation(observation ownership.Observation) error {
	if !observation.Exists {
		return fmt.Errorf("machine ownership projection requires confirmed authority")
	}
	if err := observation.Record.Validate(); err != nil {
		return fmt.Errorf("machine ownership projection record: %w", err)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return err
	}
	if observation.Fingerprint != fingerprint {
		return fmt.Errorf("machine ownership projection fingerprint does not match its record")
	}
	return nil
}

// requireMachineOwnershipProjectionNetworkRoot prevents the daemon projection from drifting from its durable network owner.
func requireMachineOwnershipProjectionNetworkRoot(
	projection models.MachineOwnershipProjection,
	root models.NetworkState,
	record ownership.Record,
) error {
	validatedRoot, _, networkOwnership, pool, err := networkRootFromModel(root)
	if err != nil {
		return err
	}
	expectedSchemaVersion := ownership.IdentitySchemaVersion
	if NetworkStage(validatedRoot.Stage) == NetworkStageResolver ||
		NetworkStage(validatedRoot.Stage) == NetworkStageFull {
		expectedSchemaVersion = ownership.NetworkPolicySchemaVersion
	}
	if record.SchemaVersion != expectedSchemaVersion {
		return corruptStateError(
			"machine ownership projection",
			fmt.Sprint(projection.Id),
			fmt.Errorf(
				"%s-stage network retains schema-%d ownership, expected schema %d",
				validatedRoot.Stage,
				record.SchemaVersion,
				expectedSchemaVersion,
			),
		)
	}
	prefix, err := netip.ParsePrefix(record.LoopbackPoolPrefix)
	if err != nil {
		return corruptStateError("machine ownership projection", fmt.Sprint(projection.Id), err)
	}
	if projection.NetworkStateId != root.Id ||
		string(networkOwnership.InstallationID) != record.InstallationID ||
		networkOwnership.Generation != record.Generation ||
		pool != prefix {
		return corruptStateError(
			"machine ownership projection",
			fmt.Sprint(projection.Id),
			fmt.Errorf("projected ownership differs from the durable network root"),
		)
	}
	return nil
}

// validateConfirmedMachineOwnershipProjection accepts only a complete helper-confirmed record and canonical timestamp.
func validateConfirmedMachineOwnershipProjection(
	observation ownership.Observation,
	confirmedAt time.Time,
) error {
	if err := validateConfirmedMachineOwnershipObservation(observation); err != nil {
		return err
	}
	if err := validateStoredTime("machine ownership confirmation time", confirmedAt); err != nil {
		return err
	}
	return nil
}
