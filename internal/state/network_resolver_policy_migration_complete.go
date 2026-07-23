package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/resolver"
	"gorm.io/gorm"
)

const (
	networkResolverPolicyMigrationCompletionRunningPhase   = "retiring resolver policy"
	networkResolverPolicyMigrationCompletionSucceededPhase = "completed"
)

// CompleteNetworkResolverPolicyMigrationRequest carries the correlated helper result and independent post-state observation.
type CompleteNetworkResolverPolicyMigrationRequest struct {
	// OperationID identifies the staged policy-migration operation.
	OperationID domain.OperationID
	// ExpectedOperationRevision fences completion to the approval checkpoint.
	ExpectedOperationRevision domain.Sequence
	// ResolverEvidence is the correlated owned-absent helper result.
	ResolverEvidence helper.ResolverMutationEvidence
	// ObservedResolver is an independent fresh legacy-policy observation.
	ObservedResolver resolver.Observation
	// ConfirmedOwnership is the derived schema-one daemon ownership observation.
	ConfirmedOwnership ownership.Observation
	// At is the UTC completion and ownership-confirmation time.
	At time.Time
}

// Validate rejects a completion that cannot prove the exact resolver retirement boundary.
func (request CompleteNetworkResolverPolicyMigrationRequest) Validate() error {
	if err := validateNetworkResolverPolicyMigrationSelection(request.OperationID, request.ExpectedOperationRevision, request.At); err != nil {
		return err
	}
	if err := validateNetworkResolverPolicyMigrationEvidence(request.ResolverEvidence); err != nil {
		return err
	}
	if err := validateNetworkResolverPolicyMigrationPostState(request.ObservedResolver, request.ConfirmedOwnership); err != nil {
		return err
	}
	return nil
}

// RecoverNetworkResolverPolicyMigrationRequest finalizes a retirement whose helper response was lost after it returned an error.
type RecoverNetworkResolverPolicyMigrationRequest struct {
	// OperationID identifies the staged policy-migration operation.
	OperationID domain.OperationID
	// ExpectedOperationRevision fences recovery to the approval checkpoint.
	ExpectedOperationRevision domain.Sequence
	// ObservedResolver independently proves the old owned resolver is absent.
	ObservedResolver resolver.Observation
	// ConfirmedOwnership independently proves the exact derived schema-one projection.
	ConfirmedOwnership ownership.Observation
	// At is the UTC recovery completion time.
	At time.Time
}

// Validate rejects recovery unless fresh observations independently prove the helper's post-state.
func (request RecoverNetworkResolverPolicyMigrationRequest) Validate() error {
	if err := validateNetworkResolverPolicyMigrationSelection(request.OperationID, request.ExpectedOperationRevision, request.At); err != nil {
		return err
	}
	return validateNetworkResolverPolicyMigrationPostState(request.ObservedResolver, request.ConfirmedOwnership)
}

// CompleteNetworkResolverPolicyMigrationResult reports the terminal operation and historical identity revision.
type CompleteNetworkResolverPolicyMigrationResult struct {
	// Operation is the succeeded migration operation.
	Operation OperationRecord
	// NetworkRevision is the identity-stage revision inserted by this completion.
	NetworkRevision domain.Sequence
	// Network is the current identity or later resolver/full aggregate.
	Network NetworkMutationResult
}

// Validate rejects a result whose terminal operation and retained network state cannot describe one completion.
func (result CompleteNetworkResolverPolicyMigrationResult) Validate() error {
	if err := result.Operation.Operation.Validate(); err != nil {
		return err
	}
	if result.Operation.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || result.Operation.Operation.State != domain.OperationSucceeded || result.Operation.Operation.Phase != networkResolverPolicyMigrationCompletionSucceededPhase {
		return fmt.Errorf("result operation is not a succeeded resolver policy migration")
	}
	if err := result.Network.Validate(); err != nil {
		return err
	}
	if result.Operation.Revision != result.NetworkRevision+1 || result.Network.Record.Revision < result.NetworkRevision {
		return fmt.Errorf("result revisions are not contiguous")
	}
	return nil
}

// CompleteNetworkResolverPolicyMigration commits a helper-proven legacy resolver retirement.
func (store *Store) CompleteNetworkResolverPolicyMigration(ctx context.Context, request CompleteNetworkResolverPolicyMigrationRequest) (CompleteNetworkResolverPolicyMigrationResult, error) {
	if err := request.Validate(); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("complete network resolver policy migration: %w", err)
	}
	return store.completeNetworkResolverPolicyMigration(ctx, request.OperationID, request.ExpectedOperationRevision, request.ObservedResolver, request.ConfirmedOwnership, request.At, &request.ResolverEvidence)
}

// ReplayNetworkResolverPolicyMigration restores the durable terminal result for an exact approval retry.
func (store *Store) ReplayNetworkResolverPolicyMigration(ctx context.Context, operationID domain.OperationID, revision domain.Sequence) (CompleteNetworkResolverPolicyMigrationResult, error) {
	if err := operationID.Validate(); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("replay network resolver policy migration: %w", err)
	}
	if _, err := sequenceToModelInt("expected resolver policy migration operation revision", revision, false); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("replay network resolver policy migration: %w", err)
	}
	var result CompleteNetworkResolverPolicyMigrationResult
	err := store.mutations.mutate(normalizeContext(ctx), "network resolver policy migration replay", func(tx *gorm.DB) error {
		operation, found, err := findOperationByID(tx, operationID)
		if err != nil {
			return err
		}
		if !found {
			return &OperationNotFoundError{OperationID: operationID}
		}
		record, err := operationRecordFromModel(operation)
		if err != nil {
			return err
		}
		if record.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || record.Operation.ProjectID != "" || record.Operation.State != domain.OperationSucceeded {
			return fmt.Errorf("network resolver policy migration %q is not completed", operationID)
		}
		history, err := operationHistoryInTransaction(tx, record)
		if err != nil {
			return err
		}
		_, planFound, err := readOptionalNetworkResolverPolicyMigrationPlan(tx, operationID)
		if err != nil {
			return err
		}
		if planFound {
			return corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("succeeded operation retains its singleton plan"))
		}
		replayed, err := replayCompletedNetworkResolverPolicyMigration(tx, record, history, operationID, revision, nil, nil, nil, nil)
		if err != nil {
			return err
		}
		result = replayed
		return nil
	})
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("replay network resolver policy migration: %w", err)
	}
	return result, nil
}

// RecoverNetworkResolverPolicyMigration commits a fresh, independently proven post-state without helper evidence.
func (store *Store) RecoverNetworkResolverPolicyMigration(ctx context.Context, request RecoverNetworkResolverPolicyMigrationRequest) (CompleteNetworkResolverPolicyMigrationResult, error) {
	if err := request.Validate(); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("recover network resolver policy migration: %w", err)
	}
	return store.completeNetworkResolverPolicyMigration(ctx, request.OperationID, request.ExpectedOperationRevision, request.ObservedResolver, request.ConfirmedOwnership, request.At, nil)
}

// completeNetworkResolverPolicyMigration shares the terminal transaction while retaining the distinction between evidence and recovery admission.
func (store *Store) completeNetworkResolverPolicyMigration(ctx context.Context, operationID domain.OperationID, revision domain.Sequence, observed resolver.Observation, confirmed ownership.Observation, at time.Time, evidence *helper.ResolverMutationEvidence) (CompleteNetworkResolverPolicyMigrationResult, error) {
	observed = cloneNetworkResolverPolicyMigrationObservation(observed)
	at = canonicalNetworkMutationTime(at)
	var result CompleteNetworkResolverPolicyMigrationResult
	err := store.mutations.mutate(normalizeContext(ctx), "network resolver policy migration completion", func(tx *gorm.DB) error {
		completed, err := completeNetworkResolverPolicyMigrationInTransaction(tx, operationID, revision, observed, confirmed, at, evidence)
		if err != nil {
			return err
		}
		result = completed
		return nil
	})
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("complete network resolver policy migration: %w", err)
	}
	return result, nil
}

// completeNetworkResolverPolicyMigrationInTransaction applies or verifies the fixed terminal lifecycle.
func completeNetworkResolverPolicyMigrationInTransaction(tx *gorm.DB, operationID domain.OperationID, revision domain.Sequence, observed resolver.Observation, confirmed ownership.Observation, at time.Time, evidence *helper.ResolverMutationEvidence) (CompleteNetworkResolverPolicyMigrationResult, error) {
	operation, found, err := findOperationByID(tx, operationID)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if !found {
		return CompleteNetworkResolverPolicyMigrationResult{}, &OperationNotFoundError{
			OperationID: operationID,
		}
	}
	record, err := operationRecordFromModel(operation)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if record.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || record.Operation.ProjectID != "" {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("operation %q is not a global resolver policy migration", operationID)
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	planRow, planFound, err := readOptionalNetworkResolverPolicyMigrationPlan(tx, operationID)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if record.Operation.State == domain.OperationSucceeded {
		if planFound {
			return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("succeeded operation retains its singleton plan"))
		}
		return replayCompletedNetworkResolverPolicyMigration(tx, record, history, operationID, revision, &observed, &confirmed, &at, evidence)
	}
	if record.Operation.State != domain.OperationRequiresApproval || record.Operation.Phase != networkResolverPolicyMigrationApprovalPhase {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("network resolver policy migration %q is not awaiting approval", operationID)
	}
	if record.Revision != revision {
		return CompleteNetworkResolverPolicyMigrationResult{}, &StaleRevisionError{
			OperationID: operationID,
			Expected:    revision,
			Actual:      record.Revision,
		}
	}
	if !planFound {
		return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationPlan(operationID, fmt.Errorf("singleton plan is missing"))
	}
	plan, networkRevision, err := networkResolverPolicyMigrationPlanFromModel(planRow, record)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if err := requireNetworkResolverPolicyMigrationCompletionAuthority(tx, plan, networkRevision, confirmed, observed, evidence, operationID); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if at.Before(record.Operation.RequestedAt) {
		return CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("resolver policy migration completion time precedes operation request")
	}
	deleted := tx.Where("id = ? AND operation_id = ? AND operation_revision = ? AND network_state_id = ? AND network_revision = ?", networkResolverPolicyMigrationPlanSingletonID, string(operationID), int(revision), networkStateSingletonID, int(networkRevision)).Delete(&models.NetworkResolverPolicyMigrationPlan{})
	if err := requireOneMutation(deleted, "delete completed network resolver policy migration plan", string(operationID)); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	running, err := transitionOperationInTransaction(tx, operationID, record.Revision, domain.OperationRunning, networkResolverPolicyMigrationCompletionRunningPhase, at, nil)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	projected, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if err := retireNetworkResolverPolicyMigrationAuthority(tx, operationID, networkRevision, projected, confirmed, at); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	succeeded, err := transitionOperationInTransaction(tx, operationID, running.Revision, domain.OperationSucceeded, networkResolverPolicyMigrationCompletionSucceededPhase, at, nil)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	completedHistory, err := operationHistoryInTransaction(tx, succeeded)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	result, err := replayCompletedNetworkResolverPolicyMigration(tx, succeeded, completedHistory, operationID, revision, &observed, &confirmed, &at, evidence)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	result.Network.Replayed = false
	if err := result.Validate(); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	return result, nil
}

// requireNetworkResolverPolicyMigrationAuthority validates the plan, durable source, helper evidence, and fresh post-state before any write.
func requireNetworkResolverPolicyMigrationCompletionAuthority(tx *gorm.DB, plan ticketissuer.ResolverPlan, networkRevision domain.Sequence, confirmed ownership.Observation, observed resolver.Observation, evidence *helper.ResolverMutationEvidence, operationID domain.OperationID) error {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return err
	}
	if !initialized || network.Stage != NetworkStageResolver || network.Revision != networkRevision || len(rows.Listeners) != 0 || len(rows.Endpoints) != 0 {
		return corruptNetworkResolverPolicyMigrationPlan(operationID, fmt.Errorf("network authority drifted"))
	}
	projected, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return err
	}
	if projected.observation.Record != plan.TargetOwnership || projected.observation.Fingerprint != plan.ExpectedSourceOwnershipFingerprint {
		return corruptNetworkResolverPolicyMigrationPlan(operationID, fmt.Errorf("machine ownership projection drifted"))
	}
	observedFingerprint, err := observed.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint observed resolver: %w", err)
	}
	if evidence != nil && evidence.ObservationFingerprint != observedFingerprint {
		return networkResolverPolicyMigrationCompletionConflict(operationID, "helper observation")
	}
	if confirmed != derivedNetworkResolverPolicyMigrationOwnership(plan.TargetOwnership) {
		return networkResolverPolicyMigrationCompletionConflict(operationID, "confirmed ownership")
	}
	if observed.Request.InstallationID() != plan.TargetOwnership.InstallationID || observed.Request.Policy() != plan.Policy {
		return networkResolverPolicyMigrationCompletionConflict(operationID, "observed resolver authority")
	}
	if evidence != nil && (evidence.PolicyFingerprint != plan.TargetOwnership.NetworkPolicyFingerprint || evidence.OwnershipFingerprint != confirmed.Fingerprint) {
		return networkResolverPolicyMigrationCompletionConflict(operationID, "helper evidence")
	}
	if err := requireOneResolverPolicyMigrationProof(tx, operationID); err != nil {
		return err
	}
	return nil
}

// requireOneResolverPolicyMigrationProof ensures retirement consumes exactly the authority staged with the resolver.
func requireOneResolverPolicyMigrationProof(tx *gorm.DB, operationID domain.OperationID) error {
	var rows []models.NetworkSetupEvidence
	if err := tx.Where("network_state_id = ? AND component = ?", networkStateSingletonID, string(NetworkSetupComponentResolver)).Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 {
		return corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("resolver setup evidence has %d rows, expected 1", len(rows)))
	}
	return nil
}

// retireNetworkResolverPolicyMigrationAuthority removes only resolver proof and atomically moves the root to identity authority.
func retireNetworkResolverPolicyMigrationAuthority(tx *gorm.DB, operationID domain.OperationID, revision domain.Sequence, current machineOwnershipProjectionState, target ownership.Observation, at time.Time) error {
	if at.Before(current.confirmedAt) {
		return fmt.Errorf("resolver policy migration completion time precedes ownership confirmation")
	}
	proof := tx.Where("network_state_id = ? AND component = ?", networkStateSingletonID, string(NetworkSetupComponentResolver)).Delete(&models.NetworkSetupEvidence{})
	if err := requireOneMutation(proof, "delete resolver setup evidence", string(operationID)); err != nil {
		return err
	}
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return err
	}
	if sequence == 0 {
		return corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("allocated zero network revision"))
	}
	updated := tx.
		Model(&models.NetworkState{}).
		Where(
			"id = ? AND stage = ? AND revision = ?",
			networkStateSingletonID,
			string(NetworkStageResolver),
			int(revision),
		).
		Updates(map[string]any{
			"stage":      string(NetworkStageIdentity),
			"updated_at": at,
			"revision":   int(sequence),
		})
	if err := requireOneMutation(updated, "retire network resolver policy", "1"); err != nil {
		return err
	}
	return downgradeNetworkResolverPolicyMigrationOwnership(tx, current, target, at)
}

// downgradeNetworkResolverPolicyMigrationOwnership performs the schema-two to derived schema-one CAS without changing identity facts.
func downgradeNetworkResolverPolicyMigrationOwnership(tx *gorm.DB, current machineOwnershipProjectionState, target ownership.Observation, at time.Time) error {
	updated := tx.
		Model(&models.MachineOwnershipProjection{}).
		Where(
			"id = ? AND network_state_id = ? AND ownership_schema_version = ? AND installation_id = ? AND owner_identity = ? AND ownership_generation = ? AND loopback_pool_prefix = ? AND network_policy_fingerprint = ? AND ticket_verifier_key = ? AND record_fingerprint = ? AND confirmed_at = ?",
			current.row.Id,
			current.row.NetworkStateId,
			int(ownership.NetworkPolicySchemaVersion),
			current.row.InstallationId,
			current.row.OwnerIdentity,
			current.row.OwnershipGeneration,
			current.row.LoopbackPoolPrefix,
			current.row.NetworkPolicyFingerprint,
			current.row.TicketVerifierKey,
			current.row.RecordFingerprint,
			current.row.ConfirmedAt,
		).
		Updates(map[string]any{
			"ownership_schema_version":   int(ownership.IdentitySchemaVersion),
			"network_policy_fingerprint": nil,
			"record_fingerprint":         target.Fingerprint,
			"confirmed_at":               at,
		})
	return requireOneMutation(updated, "downgrade machine ownership projection", "1")
}

// replayCompletedNetworkResolverPolicyMigration validates an exact terminal replay and never mutates later setup state.
func replayCompletedNetworkResolverPolicyMigration(tx *gorm.DB, record OperationRecord, history []OperationTransition, operationID domain.OperationID, revision domain.Sequence, observed *resolver.Observation, confirmed *ownership.Observation, at *time.Time, evidence *helper.ResolverMutationEvidence) (CompleteNetworkResolverPolicyMigrationResult, error) {
	if len(history) != 5 || history[2].Sequence != revision || history[3].State != domain.OperationRunning || history[3].Phase != networkResolverPolicyMigrationCompletionRunningPhase || at != nil && !history[3].OccurredAt.Equal(*at) || history[4].State != domain.OperationSucceeded || history[4].Phase != networkResolverPolicyMigrationCompletionSucceededPhase || at != nil && !history[4].OccurredAt.Equal(*at) || record.Revision != history[4].Sequence || history[4].Sequence != history[3].Sequence+2 {
		return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("terminal history differs"))
	}
	historical := history[3].Sequence + 1
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if !initialized || (network.Stage != NetworkStageIdentity && network.Stage != NetworkStageResolver && network.Stage != NetworkStageFull) || network.Revision < historical {
		return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("current network state cannot follow completion"))
	}
	if network.Stage == NetworkStageIdentity && network.Revision != historical {
		return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("identity revision differs from completion"))
	}
	if (network.Stage == NetworkStageResolver || network.Stage == NetworkStageFull) && network.Revision == historical {
		return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("later network stage does not follow completion"))
	}
	projection, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if network.Stage == NetworkStageIdentity && confirmed != nil {
		if projection.observation != *confirmed || at != nil && !projection.confirmedAt.Equal(*at) {
			return CompleteNetworkResolverPolicyMigrationResult{}, networkResolverPolicyMigrationCompletionConflict(operationID, "confirmed ownership")
		}
		if observed == nil {
			return CompleteNetworkResolverPolicyMigrationResult{}, corruptNetworkResolverPolicyMigrationCompletion(operationID, fmt.Errorf("terminal replay lacks resolver observation"))
		}
		if err := validateNetworkResolverPolicyMigrationPostState(*observed, *confirmed); err != nil {
			return CompleteNetworkResolverPolicyMigrationResult{}, err
		}
	}
	result := CompleteNetworkResolverPolicyMigrationResult{
		Operation:       record,
		NetworkRevision: historical,
		Network: NetworkMutationResult{
			Record:   network,
			Replayed: true,
		},
	}
	if err := result.Validate(); err != nil {
		return CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	return result, nil
}

// derivedNetworkResolverPolicyMigrationOwnership removes only policy-bound schema-two authority.
func derivedNetworkResolverPolicyMigrationOwnership(source ownership.Record) ownership.Observation {
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	fingerprint, _ := source.Fingerprint()
	return ownership.Observation{
		Exists:      true,
		Record:      source,
		Fingerprint: fingerprint,
	}
}

// validateNetworkResolverPolicyMigrationSelection validates shared completion fencing facts.
func validateNetworkResolverPolicyMigrationSelection(id domain.OperationID, revision domain.Sequence, at time.Time) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected resolver policy migration operation revision", revision, false); err != nil {
		return err
	}
	return validateStoredTime("resolver policy migration completion time", at)
}

// validateNetworkResolverPolicyMigrationEvidence confines normal completion to OperationRetireResolver semantics.
func validateNetworkResolverPolicyMigrationEvidence(evidence helper.ResolverMutationEvidence) error {
	if evidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		return fmt.Errorf("resolver policy migration evidence must prove owned absence")
	}
	for _, value := range []string{evidence.PolicyFingerprint, evidence.OwnershipFingerprint, evidence.ObservationFingerprint} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
			return fmt.Errorf("resolver policy migration evidence fingerprint is invalid")
		}
	}
	return nil
}

// validateNetworkResolverPolicyMigrationPostState fails closed unless legacy ownership is absent and derived schema-one ownership is exact.
func validateNetworkResolverPolicyMigrationPostState(observed resolver.Observation, confirmed ownership.Observation) error {
	if err := observed.Validate(); err != nil {
		return fmt.Errorf("observed resolver: %w", err)
	}
	assessment, err := observed.Classify()
	if err != nil {
		return err
	}
	if assessment.State != resolver.StateAbsent || assessment.Owned != resolver.OwnedStateAbsent || assessment.ForeignCount != 0 {
		return fmt.Errorf("observed resolver is not exact owned absence")
	}
	if err := validateConfirmedMachineOwnershipObservation(confirmed); err != nil {
		return err
	}
	if confirmed.Record.SchemaVersion != ownership.IdentitySchemaVersion {
		return fmt.Errorf("confirmed ownership is not schema one")
	}
	return nil
}

// cloneNetworkResolverPolicyMigrationObservation isolates caller-owned native observation slices.
func cloneNetworkResolverPolicyMigrationObservation(observed resolver.Observation) resolver.Observation {
	observed.Rules = slices.Clone(observed.Rules)
	for index := range observed.Rules {
		observed.Rules[index].Servers = slices.Clone(observed.Rules[index].Servers)
		if observed.Rules[index].Owner != nil {
			owner := *observed.Rules[index].Owner
			observed.Rules[index].Owner = &owner
		}
	}
	return observed
}

// networkResolverPolicyMigrationCompletionConflict reports a non-secret mismatch with durable migration authority.
func networkResolverPolicyMigrationCompletionConflict(operationID domain.OperationID, difference string) error {
	return &NetworkResolverSetupCompletionConflictError{
		OperationID: operationID,
		Difference:  difference,
	}
}

// corruptNetworkResolverPolicyMigrationCompletion identifies impossible terminal shapes.
func corruptNetworkResolverPolicyMigrationCompletion(operationID domain.OperationID, cause error) error {
	return corruptStateError("network resolver policy migration completion", string(operationID), cause)
}
