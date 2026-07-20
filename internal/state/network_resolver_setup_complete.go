package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
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
	networkResolverSetupCompletionRunningPhase   = "committing resolver"
	networkResolverSetupCompletionSucceededPhase = "completed"

	networkResolverSetupEvidencePrefix = "resolver-setup-sha256:"
	networkResolverSetupEvidenceDomain = "goforj.harbor.network-resolver-setup-evidence.v1\x00"
)

// CompleteNetworkResolverSetupRequest contains correlated helper evidence and one fresh daemon resolver observation.
type CompleteNetworkResolverSetupRequest struct {
	// OperationID identifies the staged global resolver setup operation.
	OperationID domain.OperationID
	// ExpectedOperationRevision pins the requires-approval revision that authorized the helper exchange.
	ExpectedOperationRevision domain.Sequence
	// ResolverEvidence is the exact policy-bound postcondition returned by the correlated helper exchange.
	ResolverEvidence helper.ResolverMutationEvidence
	// ObservedResolver is a fresh daemon-side observation collected independently after the helper completed.
	ObservedResolver resolver.Observation
	// At is the canonical completion, ownership confirmation, and resolver-proof time.
	At time.Time
}

// Validate rejects completion evidence that does not independently prove one exact resolver policy.
func (request CompleteNetworkResolverSetupRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"expected network resolver setup operation revision",
		request.ExpectedOperationRevision,
		false,
	); err != nil {
		return err
	}
	if request.ExpectedOperationRevision > domain.MaximumSequence-3 {
		return fmt.Errorf("network resolver setup approval revision must leave room for three contiguous completion revisions")
	}
	if err := validateStoredTime("network resolver setup completion time", request.At); err != nil {
		return err
	}
	if err := validateNetworkResolverSetupCompletionEvidence(request.ResolverEvidence); err != nil {
		return err
	}
	if err := request.ObservedResolver.Validate(); err != nil {
		return fmt.Errorf("observed network resolver: %w", err)
	}
	assessment, err := request.ObservedResolver.Classify()
	if err != nil {
		return fmt.Errorf("classify observed network resolver: %w", err)
	}
	if assessment.State != resolver.StateExact ||
		assessment.Owned != resolver.OwnedStateExact ||
		assessment.ForeignCount != 0 {
		return fmt.Errorf(
			"observed network resolver state is %q with owned state %q and %d foreign claims, want one exact owned rule",
			assessment.State,
			assessment.Owned,
			assessment.ForeignCount,
		)
	}
	if request.ObservedResolver.Request.PolicyFingerprint() != request.ResolverEvidence.PolicyFingerprint {
		return fmt.Errorf("observed network resolver policy does not match helper evidence")
	}
	observationFingerprint, err := request.ObservedResolver.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint observed network resolver: %w", err)
	}
	if observationFingerprint != request.ResolverEvidence.ObservationFingerprint {
		return fmt.Errorf("observed network resolver does not match helper evidence")
	}
	return nil
}

// CompleteNetworkResolverSetupResult contains the terminal operation, its historical resolver revision, and current network aggregate.
type CompleteNetworkResolverSetupResult struct {
	// Operation is the succeeded global resolver setup operation.
	Operation OperationRecord
	// NetworkRevision is the historical resolver-stage revision committed between the two terminal operation edges.
	NetworkRevision domain.Sequence
	// Network is the current aggregate and whether this call replayed the already committed resolver completion.
	Network NetworkMutationResult
}

// Validate rejects results whose operation and network revisions do not describe one atomic resolver completion.
func (result CompleteNetworkResolverSetupResult) Validate() error {
	if err := result.Operation.Operation.Validate(); err != nil {
		return err
	}
	if result.Operation.Operation.Kind != domain.OperationKindNetworkResolverSetup ||
		result.Operation.Operation.ProjectID != "" {
		return fmt.Errorf(
			"completed network resolver setup operation must be global kind %q",
			domain.OperationKindNetworkResolverSetup,
		)
	}
	if result.Operation.Operation.State != domain.OperationSucceeded {
		return fmt.Errorf(
			"completed network resolver setup operation state is %q, want %q",
			result.Operation.Operation.State,
			domain.OperationSucceeded,
		)
	}
	if result.Operation.Operation.Phase != networkResolverSetupCompletionSucceededPhase {
		return fmt.Errorf(
			"completed network resolver setup operation phase is %q, want %q",
			result.Operation.Operation.Phase,
			networkResolverSetupCompletionSucceededPhase,
		)
	}
	if err := result.Network.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("completed network resolver setup network revision", result.NetworkRevision, false); err != nil {
		return err
	}
	if result.Operation.Revision != result.NetworkRevision+1 {
		return fmt.Errorf("completed network resolver setup revisions are not contiguous")
	}
	if result.Network.Record.Revision < result.NetworkRevision {
		return fmt.Errorf("current network revision precedes the completed resolver revision")
	}
	switch result.Network.Record.Stage {
	case NetworkStageResolver:
		if result.Network.Record.Revision != result.NetworkRevision {
			return fmt.Errorf("resolver-stage network revision differs from the completed resolver revision")
		}
	case NetworkStageFull:
		if result.Network.Record.Revision == result.NetworkRevision {
			return fmt.Errorf("full-stage network must follow the completed resolver revision")
		}
	default:
		return fmt.Errorf("completed network resolver setup requires resolver or later full network stage")
	}
	return nil
}

// NetworkResolverSetupCompletionConflictError reports confirmation facts that differ from the staged or completed authority.
type NetworkResolverSetupCompletionConflictError struct {
	OperationID domain.OperationID
	Difference  string
}

// Error identifies only the non-secret confirmation fact group that differs.
func (err *NetworkResolverSetupCompletionConflictError) Error() string {
	return fmt.Sprintf(
		"network resolver setup operation %q has different %s",
		err.OperationID,
		err.Difference,
	)
}

// CompleteNetworkResolverSetup atomically retires one staged plan, activates proven resolver authority, and succeeds its operation.
func (store *Store) CompleteNetworkResolverSetup(
	ctx context.Context,
	request CompleteNetworkResolverSetupRequest,
) (CompleteNetworkResolverSetupResult, error) {
	if err := request.Validate(); err != nil {
		return CompleteNetworkResolverSetupResult{}, fmt.Errorf("complete network resolver setup: %w", err)
	}
	request = cloneCompleteNetworkResolverSetupRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}

	var result CompleteNetworkResolverSetupResult
	err := store.mutations.mutate(ctx, "network resolver setup completion", func(tx *gorm.DB) error {
		completed, err := completeNetworkResolverSetupInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = completed
		return nil
	})
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, fmt.Errorf("complete network resolver setup: %w", err)
	}
	return result, nil
}

// completeNetworkResolverSetupInTransaction applies or exactly replays plan retirement, resolver activation, and the terminal edge.
func completeNetworkResolverSetupInTransaction(
	tx *gorm.DB,
	request CompleteNetworkResolverSetupRequest,
) (CompleteNetworkResolverSetupResult, error) {
	operation, found, err := findOperationByID(tx, request.OperationID)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if !found {
		return CompleteNetworkResolverSetupResult{}, &OperationNotFoundError{OperationID: request.OperationID}
	}
	record, err := operationRecordFromModel(operation)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if record.Operation.Kind != domain.OperationKindNetworkResolverSetup || record.Operation.ProjectID != "" {
		return CompleteNetworkResolverSetupResult{}, fmt.Errorf(
			"operation %q is not an active global network resolver setup",
			request.OperationID,
		)
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	planRow, planFound, err := readOptionalNetworkResolverSetupPlanForStaging(tx, request.OperationID)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}

	if record.Operation.State == domain.OperationSucceeded {
		if planFound {
			return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
				request.OperationID,
				fmt.Errorf("succeeded operation retains its singleton plan"),
			)
		}
		return replayCompletedNetworkResolverSetup(tx, record, history, request)
	}
	if record.Operation.State != domain.OperationRequiresApproval {
		return CompleteNetworkResolverSetupResult{}, fmt.Errorf(
			"network resolver setup operation %q must require approval, got %q",
			request.OperationID,
			record.Operation.State,
		)
	}
	if record.Revision != request.ExpectedOperationRevision {
		return CompleteNetworkResolverSetupResult{}, &StaleRevisionError{
			OperationID: request.OperationID,
			Expected:    request.ExpectedOperationRevision,
			Actual:      record.Revision,
		}
	}
	if err := requireExactNetworkResolverSetupHistory(record, history); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if !planFound {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupPlan(
			request.OperationID,
			fmt.Errorf("singleton plan is missing"),
		)
	}
	plan, networkRevision, err := networkResolverSetupPlanFromModel(planRow, record)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if err := requireResolvedNetworkResolverSetupAuthority(
		tx,
		request.OperationID,
		networkRevision,
		plan,
	); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if err := requireNetworkResolverSetupCompletionMatchesPlan(request, plan); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if request.At.Before(record.Operation.RequestedAt) {
		return CompleteNetworkResolverSetupResult{}, fmt.Errorf("network resolver setup completion time precedes the operation request")
	}

	expectedOperationRevision, err := sequenceToModelInt(
		"expected network resolver setup operation revision",
		request.ExpectedOperationRevision,
		false,
	)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	expectedNetworkRevision, err := sequenceToModelInt(
		"expected network resolver setup network revision",
		networkRevision,
		false,
	)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	deleted := tx.Where(
		"id = ? AND operation_id = ? AND operation_revision = ? AND network_state_id = ? AND network_revision = ?",
		networkResolverSetupPlanSingletonID,
		string(request.OperationID),
		expectedOperationRevision,
		networkStateSingletonID,
		expectedNetworkRevision,
	).Delete(&models.NetworkResolverSetupPlan{})
	if err := requireOneMutation(deleted, "delete completed network resolver setup plan", string(request.OperationID)); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}

	running, err := transitionOperationInTransaction(
		tx,
		request.OperationID,
		record.Revision,
		domain.OperationRunning,
		networkResolverSetupCompletionRunningPhase,
		request.At,
		nil,
	)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if running.Revision != request.ExpectedOperationRevision+1 {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
			request.OperationID,
			fmt.Errorf("running revision is not contiguous with approval"),
		)
	}

	activation, err := networkResolverSetupActivationRequest(request, plan, networkRevision)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	networkResult, err := activatePlannedNetworkResolverInTransaction(tx, request.OperationID, activation)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if networkResult.Replayed || networkResult.Record.Revision != running.Revision+1 {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
			request.OperationID,
			fmt.Errorf("approval completion did not append one resolver network revision"),
		)
	}

	succeeded, err := transitionOperationInTransaction(
		tx,
		request.OperationID,
		running.Revision,
		domain.OperationSucceeded,
		networkResolverSetupCompletionSucceededPhase,
		request.At,
		nil,
	)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	completedHistory, err := operationHistoryInTransaction(tx, succeeded)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if err := requireExactCompletedNetworkResolverSetupHistory(
		succeeded,
		completedHistory,
		request,
		networkResult.Record.Revision,
	); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	result := CompleteNetworkResolverSetupResult{
		Operation:       succeeded,
		NetworkRevision: networkResult.Record.Revision,
		Network:         networkResult,
	}
	if err := result.Validate(); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	return result, nil
}

// replayCompletedNetworkResolverSetup validates a terminal operation without relying on its intentionally deleted plan.
func replayCompletedNetworkResolverSetup(
	tx *gorm.DB,
	record OperationRecord,
	history []OperationTransition,
	request CompleteNetworkResolverSetupRequest,
) (CompleteNetworkResolverSetupResult, error) {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if !initialized || network.Stage != NetworkStageResolver && network.Stage != NetworkStageFull {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
			request.OperationID,
			fmt.Errorf("succeeded operation does not retain resolver or later full network authority"),
		)
	}
	historicalNetworkRevision, err := completedNetworkResolverSetupRevision(history, request.OperationID)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if err := requireExactCompletedNetworkResolverSetupHistory(
		record,
		history,
		request,
		historicalNetworkRevision,
	); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if network.Revision < historicalNetworkRevision ||
		network.Stage == NetworkStageResolver && network.Revision != historicalNetworkRevision ||
		network.Stage == NetworkStageFull && network.Revision == historicalNetworkRevision {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
			request.OperationID,
			fmt.Errorf(
				"current %s network revision %d is inconsistent with completed resolver revision %d",
				network.Stage,
				network.Revision,
				historicalNetworkRevision,
			),
		)
	}
	projection, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if projection.observation.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return CompleteNetworkResolverSetupResult{}, corruptNetworkResolverSetupCompletion(
			request.OperationID,
			fmt.Errorf("resolver-stage completion retains schema-%d ownership", projection.observation.Record.SchemaVersion),
		)
	}
	if projection.observation.Fingerprint != request.ResolverEvidence.OwnershipFingerprint ||
		projection.observation.Record.NetworkPolicyFingerprint != request.ResolverEvidence.PolicyFingerprint ||
		projection.observation.Record.InstallationID != request.ObservedResolver.Request.InstallationID() {
		return CompleteNetworkResolverSetupResult{}, networkResolverSetupCompletionConflict(
			request.OperationID,
			"confirmed ownership",
		)
	}
	if !projection.confirmedAt.Equal(request.At) {
		return CompleteNetworkResolverSetupResult{}, networkResolverSetupCompletionConflict(
			request.OperationID,
			"completion time",
		)
	}
	expectedProof, err := networkResolverSetupCompletionProof(request, projection.observation.Record.Generation)
	if err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if err := requireExactCompletedNetworkResolverProof(
		rows.SetupEvidence,
		expectedProof,
		request.OperationID,
		network.Stage,
	); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	result := CompleteNetworkResolverSetupResult{
		Operation:       record,
		NetworkRevision: historicalNetworkRevision,
		Network: NetworkMutationResult{
			Record:   network,
			Replayed: true,
		},
	}
	if err := result.Validate(); err != nil {
		return CompleteNetworkResolverSetupResult{}, err
	}
	return result, nil
}

// activatePlannedNetworkResolverInTransaction upgrades only the identity authority already validated from a staged plan.
func activatePlannedNetworkResolverInTransaction(
	tx *gorm.DB,
	operationID domain.OperationID,
	request ActivateNetworkResolverRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	before, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	current, initialized, err := networkRecordFromModels(before)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if !initialized {
		return NetworkMutationResult{}, &NetworkNotInitializedError{}
	}
	if current.Stage != NetworkStageIdentity {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"1",
			fmt.Errorf("planned resolver completion found stage %q", current.Stage),
		)
	}
	if current.Revision != request.ExpectedNetworkRevision {
		return NetworkMutationResult{}, &NetworkRevisionConflictError{
			Expected: request.ExpectedNetworkRevision,
			Actual:   current.Revision,
		}
	}
	if len(before.Listeners) != 0 || len(before.Endpoints) != 0 {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"identity-stage",
			fmt.Errorf("identity-stage network must not contain listener or endpoint reservations"),
		)
	}
	projected, err := readMachineOwnershipProjectionStateInTransaction(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if projected.observation.Record.SchemaVersion != ownership.IdentitySchemaVersion {
		return NetworkMutationResult{}, corruptStateError(
			"machine ownership projection",
			fmt.Sprint(projected.row.Id),
			fmt.Errorf("identity-stage network retains schema-%d ownership", projected.observation.Record.SchemaVersion),
		)
	}
	expectedSource, err := networkDataPlaneActivationIdentityOwnership(request.ConfirmedOwnership)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if projected.observation != expectedSource {
		return NetworkMutationResult{}, networkResolverSetupCompletionConflict(
			operationID,
			"machine ownership projection",
		)
	}
	if request.At.Before(current.UpdatedAt) || request.At.Before(projected.confirmedAt) {
		return NetworkMutationResult{}, fmt.Errorf("network resolver setup completion time precedes durable identity authority")
	}

	if err := insertNetworkSetupProof(tx, request.Resolver); err != nil {
		return NetworkMutationResult{}, err
	}
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return NetworkMutationResult{}, err
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
	if err := requireOneMutation(updated, "complete planned network resolver", "1"); err != nil {
		return NetworkMutationResult{}, err
	}
	if err := upgradeMachineOwnershipProjectionInTransaction(
		tx,
		projected,
		request.ConfirmedOwnership,
		request.At,
	); err != nil {
		return NetworkMutationResult{}, err
	}

	after, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("read completed planned network resolver: %w", err)
	}
	persisted, exists, err := networkRecordFromModels(after)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if !exists {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"1",
			fmt.Errorf("aggregate is missing after planned resolver completion"),
		)
	}
	expected := current
	expected.Stage = NetworkStageResolver
	expected.Revision = sequence
	expected.UpdatedAt = request.At
	if !reflect.DeepEqual(persisted, expected) {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"1",
			fmt.Errorf("planned resolver completion readback differs from its preflighted projection"),
		)
	}
	if difference := networkResolverActivationDifference(after, request); difference != "" {
		return NetworkMutationResult{}, corruptStateError(
			"network state",
			"1",
			fmt.Errorf("planned resolver completion readback differs in %s", difference),
		)
	}
	if err := validateNetworkResolverActivationRows(before, after, request, sequence); err != nil {
		return NetworkMutationResult{}, err
	}
	finalHighWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return NetworkMutationResult{}, err
	}
	if finalHighWater != sequence {
		return NetworkMutationResult{}, corruptStateError(
			"Harbor sequence",
			fmt.Sprint(finalHighWater),
			fmt.Errorf("planned resolver completion allocated revision %d", sequence),
		)
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

// requireNetworkResolverSetupCompletionMatchesPlan binds helper and daemon evidence to every immutable planned authority field.
func requireNetworkResolverSetupCompletionMatchesPlan(
	request CompleteNetworkResolverSetupRequest,
	plan ticketissuer.ResolverPlan,
) error {
	targetFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if request.ResolverEvidence.PolicyFingerprint != plan.TargetOwnership.NetworkPolicyFingerprint {
		return networkResolverSetupCompletionConflict(request.OperationID, "resolver policy")
	}
	if request.ResolverEvidence.OwnershipFingerprint != targetFingerprint {
		return networkResolverSetupCompletionConflict(request.OperationID, "target ownership")
	}
	if request.ObservedResolver.Request.InstallationID() != plan.TargetOwnership.InstallationID ||
		request.ObservedResolver.Request.Policy() != plan.Policy {
		return networkResolverSetupCompletionConflict(request.OperationID, "observed resolver authority")
	}
	return nil
}

// networkResolverSetupActivationRequest converts validated planned authority into the existing resolver-stage mutation contract.
func networkResolverSetupActivationRequest(
	request CompleteNetworkResolverSetupRequest,
	plan ticketissuer.ResolverPlan,
	networkRevision domain.Sequence,
) (ActivateNetworkResolverRequest, error) {
	confirmedOwnership := ownership.Observation{
		Exists:      true,
		Record:      plan.TargetOwnership,
		Fingerprint: request.ResolverEvidence.OwnershipFingerprint,
	}
	proof, err := networkResolverSetupCompletionProof(request, plan.TargetOwnership.Generation)
	if err != nil {
		return ActivateNetworkResolverRequest{}, err
	}
	activation := ActivateNetworkResolverRequest{
		ExpectedNetworkRevision: networkRevision,
		ConfirmedOwnership:      confirmedOwnership,
		Policy:                  plan.Policy,
		Resolver:                proof,
		At:                      request.At,
	}
	if err := activation.Validate(); err != nil {
		return ActivateNetworkResolverRequest{}, fmt.Errorf("derive resolver activation from setup completion: %w", err)
	}
	return activation, nil
}

// networkResolverSetupCompletionProof fingerprints the complete helper result and matching independent observation.
func networkResolverSetupCompletionProof(
	request CompleteNetworkResolverSetupRequest,
	generation uint64,
) (NetworkSetupProof, error) {
	observationFingerprint, err := request.ObservedResolver.Fingerprint()
	if err != nil {
		return NetworkSetupProof{}, fmt.Errorf("fingerprint resolver setup completion observation: %w", err)
	}
	payload := []byte(networkResolverSetupEvidenceDomain)
	if request.ResolverEvidence.Changed {
		payload = append(payload, 1)
	} else {
		payload = append(payload, 0)
	}
	for _, value := range []string{
		request.ResolverEvidence.PolicyFingerprint,
		request.ResolverEvidence.OwnershipFingerprint,
		request.ResolverEvidence.ObservationFingerprint,
		string(request.ResolverEvidence.Postcondition),
		observationFingerprint,
	} {
		payload = append(payload, value...)
		payload = append(payload, 0)
	}
	digest := sha256.Sum256(payload)
	proof := NetworkSetupProof{
		Component:  NetworkSetupComponentResolver,
		Evidence:   networkResolverSetupEvidencePrefix + hex.EncodeToString(digest[:]),
		Generation: generation,
		VerifiedAt: request.At,
	}
	if err := proof.Validate(); err != nil {
		return NetworkSetupProof{}, fmt.Errorf("derive resolver setup completion proof: %w", err)
	}
	return proof, nil
}

// requireExactCompletedNetworkResolverProof distinguishes missing durable authority from a conflicting retry.
func requireExactCompletedNetworkResolverProof(
	rows []models.NetworkSetupEvidence,
	expected NetworkSetupProof,
	operationID domain.OperationID,
	stage NetworkStage,
) error {
	wantCount := 3
	if stage == NetworkStageFull {
		wantCount = 4
	}
	if len(rows) != wantCount {
		return corruptNetworkResolverSetupCompletion(
			operationID,
			fmt.Errorf("%s-stage network has %d setup proofs, expected %d", stage, len(rows), wantCount),
		)
	}
	var found *models.NetworkSetupEvidence
	for index := range rows {
		if rows[index].Component != string(NetworkSetupComponentResolver) {
			continue
		}
		if found != nil {
			return corruptNetworkResolverSetupCompletion(
				operationID,
				fmt.Errorf("resolver-stage network has duplicate resolver proofs"),
			)
		}
		found = &rows[index]
	}
	if found == nil {
		return corruptNetworkResolverSetupCompletion(operationID, fmt.Errorf("resolver-stage network is missing its resolver proof"))
	}
	if found.NetworkStateId != networkStateSingletonID {
		return corruptNetworkResolverSetupCompletion(operationID, fmt.Errorf("resolver proof belongs to another network root"))
	}
	if found.Evidence != expected.Evidence ||
		found.Generation != int(expected.Generation) ||
		!found.VerifiedAt.Equal(expected.VerifiedAt) {
		return networkResolverSetupCompletionConflict(operationID, "resolver proof")
	}
	return nil
}

// completedNetworkResolverSetupRevision recovers the historical network revision held between terminal operation edges.
func completedNetworkResolverSetupRevision(
	history []OperationTransition,
	operationID domain.OperationID,
) (domain.Sequence, error) {
	if len(history) != 5 {
		return 0, corruptNetworkResolverSetupCompletion(
			operationID,
			fmt.Errorf("operation has %d transitions, expected 5", len(history)),
		)
	}
	runningRevision := history[3].Sequence
	if runningRevision == 0 || runningRevision >= domain.MaximumSequence {
		return 0, corruptNetworkResolverSetupCompletion(
			operationID,
			fmt.Errorf("completion running revision cannot precede one network revision"),
		)
	}
	return runningRevision + 1, nil
}

// requireExactCompletedNetworkResolverSetupHistory proves the completion edges surround exactly one network revision.
func requireExactCompletedNetworkResolverSetupHistory(
	record OperationRecord,
	history []OperationTransition,
	request CompleteNetworkResolverSetupRequest,
	networkRevision domain.Sequence,
) error {
	if len(history) != 5 {
		return corruptNetworkResolverSetupCompletion(
			record.Operation.ID,
			fmt.Errorf("operation has %d transitions, expected 5", len(history)),
		)
	}
	approval := record
	approval.Operation.State = domain.OperationRequiresApproval
	approval.Operation.Phase = networkResolverSetupApprovalPhase
	approval.Operation.FinishedAt = nil
	approval.Revision = history[2].Sequence
	if err := requireExactNetworkResolverSetupHistory(approval, history[:3]); err != nil {
		return corruptNetworkResolverSetupCompletion(record.Operation.ID, err)
	}
	if history[2].Sequence != request.ExpectedOperationRevision {
		return networkResolverSetupCompletionConflict(request.OperationID, "approval revision")
	}
	if history[3].State != domain.OperationRunning ||
		history[3].Phase != networkResolverSetupCompletionRunningPhase ||
		!history[3].OccurredAt.Equal(request.At) ||
		history[3].Sequence != request.ExpectedOperationRevision+1 {
		return networkResolverSetupCompletionConflict(request.OperationID, "running transition")
	}
	if networkRevision != history[3].Sequence+1 {
		return corruptNetworkResolverSetupCompletion(
			record.Operation.ID,
			fmt.Errorf("resolver network revision does not follow its running transition"),
		)
	}
	if history[4].State != domain.OperationSucceeded ||
		history[4].Phase != networkResolverSetupCompletionSucceededPhase ||
		!history[4].OccurredAt.Equal(request.At) ||
		history[4].Sequence != networkRevision+1 {
		return networkResolverSetupCompletionConflict(request.OperationID, "succeeded transition")
	}
	if record.Operation.State != domain.OperationSucceeded ||
		record.Operation.Phase != networkResolverSetupCompletionSucceededPhase ||
		record.Revision != history[4].Sequence {
		return corruptNetworkResolverSetupCompletion(
			record.Operation.ID,
			fmt.Errorf("operation does not match its succeeded transition"),
		)
	}
	return nil
}

// validateNetworkResolverSetupCompletionEvidence confines confirmation to canonical exact resolver postconditions.
func validateNetworkResolverSetupCompletionEvidence(evidence helper.ResolverMutationEvidence) error {
	for _, candidate := range []struct {
		name  string
		value string
	}{
		{name: "policy", value: evidence.PolicyFingerprint},
		{name: "ownership", value: evidence.OwnershipFingerprint},
		{name: "observation", value: evidence.ObservationFingerprint},
	} {
		decoded, err := hex.DecodeString(candidate.value)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != candidate.value {
			return fmt.Errorf("network resolver setup evidence %s fingerprint is invalid", candidate.name)
		}
	}
	if evidence.Postcondition != helper.ResolverPostconditionExact {
		return fmt.Errorf("network resolver setup evidence must prove the exact resolver policy")
	}
	return nil
}

// cloneCompleteNetworkResolverSetupRequest isolates nested native facts before waiting for writer authority.
func cloneCompleteNetworkResolverSetupRequest(
	request CompleteNetworkResolverSetupRequest,
) CompleteNetworkResolverSetupRequest {
	request.ObservedResolver.Rules = slices.Clone(request.ObservedResolver.Rules)
	for index := range request.ObservedResolver.Rules {
		request.ObservedResolver.Rules[index].Servers = slices.Clone(request.ObservedResolver.Rules[index].Servers)
		if request.ObservedResolver.Rules[index].Owner != nil {
			owner := *request.ObservedResolver.Rules[index].Owner
			request.ObservedResolver.Rules[index].Owner = &owner
		}
	}
	request.At = canonicalNetworkMutationTime(request.At)
	return request
}

// networkResolverSetupCompletionConflict constructs one request-versus-authority conflict without exposing evidence.
func networkResolverSetupCompletionConflict(
	operationID domain.OperationID,
	difference string,
) error {
	return &NetworkResolverSetupCompletionConflictError{
		OperationID: operationID,
		Difference:  difference,
	}
}

// corruptNetworkResolverSetupCompletion gives every impossible terminal shape one typed corruption identity.
func corruptNetworkResolverSetupCompletion(operationID domain.OperationID, cause error) error {
	return corruptStateError("network resolver setup completion", string(operationID), cause)
}
