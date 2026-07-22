package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkDataPlaneSetupProjection is the complete durable predecessor authority for full-network setup.
type NetworkDataPlaneSetupProjection struct {
	Stage              NetworkStage
	NetworkRevision    domain.Sequence
	NetworkUpdatedAt   time.Time
	ResolverProof      NetworkSetupProof
	LowPortProof       NetworkSetupProof
	Listeners          SharedListenerReservations
	ConfirmedOwnership ownership.Observation
}

// NetworkDataPlaneSetupPolicyFingerprintMismatchError identifies persisted ownership bound to another otherwise valid policy.
type NetworkDataPlaneSetupPolicyFingerprintMismatchError struct {
	Expected string
	Actual   string
}

// Error reports the bounded policy-ownership mismatch without classifying storage or corruption failures as compatible state.
func (err *NetworkDataPlaneSetupPolicyFingerprintMismatchError) Error() string {
	return fmt.Sprintf("network data-plane setup policy fingerprint does not match confirmed ownership (expected %q, actual %q)", err.Expected, err.Actual)
}

// Validate rejects projections that cannot authorize resolver-to-full setup or exact full-stage replay.
func (projection NetworkDataPlaneSetupProjection) Validate() error {
	switch projection.Stage {
	case NetworkStageResolver, NetworkStageFull:
	default:
		return fmt.Errorf(
			"network data-plane setup stage is %q, expected %q or %q",
			projection.Stage,
			NetworkStageResolver,
			NetworkStageFull,
		)
	}
	if _, err := sequenceToModelInt("network data-plane setup revision", projection.NetworkRevision, false); err != nil {
		return err
	}
	if err := validateStoredTime("network data-plane setup update time", projection.NetworkUpdatedAt); err != nil {
		return err
	}
	if projection.ResolverProof.Component != NetworkSetupComponentResolver {
		return fmt.Errorf(
			"network data-plane setup resolver proof is %q, expected %q",
			projection.ResolverProof.Component,
			NetworkSetupComponentResolver,
		)
	}
	if err := projection.ResolverProof.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup resolver proof: %w", err)
	}
	if projection.ResolverProof.VerifiedAt.After(projection.NetworkUpdatedAt) {
		return fmt.Errorf("network data-plane setup resolver proof verification time must not be after the network update time")
	}
	switch projection.Stage {
	case NetworkStageResolver:
		if projection.LowPortProof != (NetworkSetupProof{}) {
			return fmt.Errorf("resolver-stage network data-plane setup projection must not contain low-port authority")
		}
		if projection.Listeners != (SharedListenerReservations{}) {
			return fmt.Errorf("resolver-stage network data-plane setup projection must not contain listener authority")
		}
	case NetworkStageFull:
		if projection.LowPortProof.Component != NetworkSetupComponentLowPorts {
			return fmt.Errorf(
				"network data-plane setup low-port proof is %q, expected %q",
				projection.LowPortProof.Component,
				NetworkSetupComponentLowPorts,
			)
		}
		if err := projection.LowPortProof.Validate(); err != nil {
			return fmt.Errorf("network data-plane setup low-port proof: %w", err)
		}
		if projection.LowPortProof.VerifiedAt.After(projection.NetworkUpdatedAt) {
			return fmt.Errorf("network data-plane setup low-port proof verification time must not be after the network update time")
		}
		if err := projection.Listeners.Validate(); err != nil {
			return fmt.Errorf("network data-plane setup listeners: %w", err)
		}
		for _, candidate := range []struct {
			name        string
			reservation ListenerReservation
		}{
			{name: "DNS", reservation: projection.Listeners.DNS},
			{name: "HTTP", reservation: projection.Listeners.HTTP},
			{name: "HTTPS", reservation: projection.Listeners.HTTPS},
		} {
			if candidate.reservation.VerifiedAt.After(projection.NetworkUpdatedAt) {
				return fmt.Errorf(
					"network data-plane setup %s listener verification time must not be after the network update time",
					candidate.name,
				)
			}
		}
	}
	if err := validateConfirmedNetworkDataPlaneOwnership(projection.ConfirmedOwnership); err != nil {
		return err
	}
	return nil
}

// NetworkDataPlaneSetupProjectionSource reads the current policy-bound predecessor for full-network setup.
type NetworkDataPlaneSetupProjectionSource struct {
	network *models.NetworkStateRepo
}

// NewNetworkDataPlaneSetupProjectionSource creates a strict read-only source over generated network persistence.
func NewNetworkDataPlaneSetupProjectionSource(
	network *models.NetworkStateRepo,
) *NetworkDataPlaneSetupProjectionSource {
	if network == nil {
		panic("state.NewNetworkDataPlaneSetupProjectionSource requires a non-nil network state repository")
	}
	return &NetworkDataPlaneSetupProjectionSource{network: network}
}

// Resolve returns current resolver authority after matching a freshly reconstructed canonical policy.
func (source *NetworkDataPlaneSetupProjectionSource) Resolve(
	ctx context.Context,
	policy networkpolicy.Policy,
) (NetworkDataPlaneSetupProjection, error) {
	ctx = normalizeContext(ctx)
	if err := policy.Validate(); err != nil {
		return NetworkDataPlaneSetupProjection{}, fmt.Errorf("resolve network data-plane setup projection: policy: %w", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, fmt.Errorf("resolve network data-plane setup projection: fingerprint policy: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	connection, err := source.network.WithContext(ctx).Builder()
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, fmt.Errorf("open network data-plane setup projection: %w", err)
	}

	var result NetworkDataPlaneSetupProjection
	err = connection.Transaction(func(tx *gorm.DB) error {
		resolved, resolveErr := resolveNetworkDataPlaneSetupProjection(tx, policy, policyFingerprint)
		if resolveErr != nil {
			return resolveErr
		}
		result = resolved
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, fmt.Errorf("resolve network data-plane setup projection: %w", err)
	}
	return result, nil
}

// resolveNetworkDataPlaneSetupProjection revalidates every persisted predecessor in one database instant.
func resolveNetworkDataPlaneSetupProjection(
	tx *gorm.DB,
	policy networkpolicy.Policy,
	policyFingerprint string,
) (NetworkDataPlaneSetupProjection, error) {
	root, revision, err := readNetworkDataPlaneSetupRoot(tx)
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	stage := NetworkStage(root.Stage)
	if stage != NetworkStageResolver && stage != NetworkStageFull {
		return NetworkDataPlaneSetupProjection{}, fmt.Errorf(
			"network data-plane setup requires %q or %q stage; current stage is %q",
			NetworkStageResolver,
			NetworkStageFull,
			stage,
		)
	}

	resolverProof, lowPortProof, err := readNetworkDataPlaneSetupProofs(tx, root, stage)
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	listeners, err := readNetworkDataPlaneSetupListeners(tx, root, stage)
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	if stage == NetworkStageFull {
		if err := validateNetworkDataPlanePolicyListeners(policy, listeners); err != nil {
			return NetworkDataPlaneSetupProjection{}, corruptStateError(
				"network shared listener",
				"aggregate",
				err,
			)
		}
	}
	confirmedOwnership, confirmedAt, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	if confirmedOwnership.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return NetworkDataPlaneSetupProjection{}, corruptStateError(
			"machine ownership projection",
			fmt.Sprint(machineOwnershipProjectionSingletonID),
			fmt.Errorf(
				"network data-plane setup requires schema-%d ownership, found schema %d",
				ownership.NetworkPolicySchemaVersion,
				confirmedOwnership.Record.SchemaVersion,
			),
		)
	}
	if confirmedOwnership.Record.NetworkPolicyFingerprint != policyFingerprint {
		return NetworkDataPlaneSetupProjection{}, &NetworkDataPlaneSetupPolicyFingerprintMismatchError{
			Expected: policyFingerprint,
			Actual:   confirmedOwnership.Record.NetworkPolicyFingerprint,
		}
	}
	if confirmedAt.After(root.UpdatedAt) {
		return NetworkDataPlaneSetupProjection{}, corruptStateError(
			"machine ownership projection",
			fmt.Sprint(machineOwnershipProjectionSingletonID),
			fmt.Errorf("confirmation time is after the current network state update time"),
		)
	}
	if resolverProof.VerifiedAt.After(confirmedAt) {
		return NetworkDataPlaneSetupProjection{}, corruptStateError(
			"network setup evidence",
			string(NetworkSetupComponentResolver),
			fmt.Errorf("resolver proof verification time is after policy-bound ownership confirmation"),
		)
	}

	highWater, err := readSnapshotSequence(tx)
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	if err := validateVisibleSequence(highWater, revision, "network state", nil); err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	if err := validateNetworkSequenceExclusivity(tx, revision); err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}

	result := NetworkDataPlaneSetupProjection{
		Stage:              stage,
		NetworkRevision:    revision,
		NetworkUpdatedAt:   root.UpdatedAt,
		ResolverProof:      resolverProof,
		LowPortProof:       lowPortProof,
		Listeners:          listeners,
		ConfirmedOwnership: confirmedOwnership,
	}
	if err := result.Validate(); err != nil {
		return NetworkDataPlaneSetupProjection{}, corruptStateError(
			"network data-plane setup projection",
			fmt.Sprint(networkStateSingletonID),
			err,
		)
	}
	return result, nil
}

// readNetworkDataPlaneSetupRoot reads and validates the exact network singleton without projecting unrelated routes.
func readNetworkDataPlaneSetupRoot(
	tx *gorm.DB,
) (models.NetworkState, domain.Sequence, error) {
	var rows []models.NetworkState
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkState{}, 0, fmt.Errorf("read network data-plane setup root: %w", err)
	}
	if len(rows) == 0 {
		return models.NetworkState{}, 0, fmt.Errorf("network data-plane setup requires initialized network state")
	}
	if len(rows) != 1 {
		return models.NetworkState{}, 0, corruptStateError(
			"network state",
			fmt.Sprint(networkStateSingletonID),
			fmt.Errorf("singleton contains %d rows, expected 1", len(rows)),
		)
	}
	root, revision, _, _, err := networkRootFromModel(rows[0])
	if err != nil {
		return models.NetworkState{}, 0, err
	}
	return root, revision, nil
}

// readNetworkDataPlaneSetupProofs reconstructs the replayable proofs from the stage's exact proof set.
func readNetworkDataPlaneSetupProofs(
	tx *gorm.DB,
	root models.NetworkState,
	stage NetworkStage,
) (NetworkSetupProof, NetworkSetupProof, error) {
	var rows []models.NetworkSetupEvidence
	if err := tx.Order("component ASC").Order("id ASC").Limit(5).Find(&rows).Error; err != nil {
		return NetworkSetupProof{}, NetworkSetupProof{}, fmt.Errorf("read network data-plane setup evidence: %w", err)
	}
	wantRows := 3
	if stage == NetworkStageFull {
		wantRows = 4
	}
	if len(rows) != wantRows {
		return NetworkSetupProof{}, NetworkSetupProof{}, corruptStateError(
			"network setup evidence",
			string(stage)+"-stage",
			fmt.Errorf("found %d rows, expected %d", len(rows), wantRows),
		)
	}
	if err := validateNetworkSetupEvidence(rows, stage, root.Id, root.UpdatedAt); err != nil {
		return NetworkSetupProof{}, NetworkSetupProof{}, err
	}
	var resolverProof NetworkSetupProof
	var lowPortProof NetworkSetupProof
	for _, row := range rows {
		component := NetworkSetupComponent(row.Component)
		if component != NetworkSetupComponentResolver && component != NetworkSetupComponentLowPorts {
			continue
		}
		generation, err := positiveNetworkGeneration("network data-plane setup evidence generation", row.Generation)
		if err != nil {
			return NetworkSetupProof{}, NetworkSetupProof{}, corruptStateError(
				"network setup evidence",
				durableKey(row.Component, row.Id),
				err,
			)
		}
		proof := NetworkSetupProof{
			Component:  component,
			Evidence:   row.Evidence,
			Generation: generation,
			VerifiedAt: row.VerifiedAt,
		}
		if err := proof.Validate(); err != nil {
			return NetworkSetupProof{}, NetworkSetupProof{}, corruptStateError(
				"network setup evidence",
				durableKey(row.Component, row.Id),
				err,
			)
		}
		switch component {
		case NetworkSetupComponentResolver:
			resolverProof = proof
		case NetworkSetupComponentLowPorts:
			lowPortProof = proof
		}
	}
	return resolverProof, lowPortProof, nil
}

// readNetworkDataPlaneSetupListeners reconstructs exact full-stage sockets and rejects any pre-full claim.
func readNetworkDataPlaneSetupListeners(
	tx *gorm.DB,
	root models.NetworkState,
	stage NetworkStage,
) (SharedListenerReservations, error) {
	var rows []models.NetworkSharedListener
	if err := tx.Order("kind ASC").Order("id ASC").Limit(4).Find(&rows).Error; err != nil {
		return SharedListenerReservations{}, fmt.Errorf("read network data-plane setup listeners: %w", err)
	}
	wantRows := 0
	if stage == NetworkStageFull {
		wantRows = 3
	}
	if len(rows) != wantRows {
		return SharedListenerReservations{}, corruptStateError(
			"network shared listener",
			string(stage)+"-stage",
			fmt.Errorf("found %d rows, expected %d", len(rows), wantRows),
		)
	}
	return networkListenersForStage(rows, stage, root.Id, root.UpdatedAt)
}
