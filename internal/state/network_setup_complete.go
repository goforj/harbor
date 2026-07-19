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
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"gorm.io/gorm"
)

const (
	networkSetupCompletionRunningPhase   = "committing"
	networkSetupCompletionSucceededPhase = "completed"

	networkSetupOwnershipEvidencePrefix = "machine-ownership-sha256:"
	networkSetupPoolEvidencePrefix      = "loopback-pool-sha256:"
	networkSetupPoolEvidenceDomain      = "goforj.harbor.network-setup-pool-evidence.v1\x00"
)

// CompleteNetworkSetupRequest contains the correlated helper proof and fresh daemon observations for one staged approval.
type CompleteNetworkSetupRequest struct {
	// OperationID identifies the staged global setup operation.
	OperationID domain.OperationID
	// ExpectedOperationRevision pins the requires-approval revision that authorized the helper exchange.
	ExpectedOperationRevision domain.Sequence
	// ConfirmedOwnership is the complete ownership result confirmed by the correlated helper exchange.
	ConfirmedOwnership ownership.Observation
	// HelperPoolEvidence is the complete exact-eight postcondition returned by the correlated helper exchange.
	HelperPoolEvidence helper.PoolMutationEvidence
	// ObservedPool contains fresh daemon-side observations collected independently after the helper completed.
	ObservedPool []loopback.Observation
	// At is the canonical completion and durable identity-foundation time.
	At time.Time
}

// Validate rejects completion proof that cannot independently establish the exact planned ownership and /29 pool.
func (request CompleteNetworkSetupRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected network setup operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if err := validateStoredTime("network setup completion time", request.At); err != nil {
		return err
	}
	if err := validateConfirmedNetworkSetupOwnership(request.ConfirmedOwnership); err != nil {
		return err
	}
	pool, err := networkSetupIdentityPool(request.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		return fmt.Errorf("confirmed network setup ownership: %w", err)
	}
	if err := validateNetworkSetupPoolPostconditions(pool, request.HelperPoolEvidence, request.ObservedPool); err != nil {
		return err
	}
	return nil
}

// CompleteNetworkSetupResult contains the terminal operation and identity-only network aggregate committed with it.
type CompleteNetworkSetupResult struct {
	// Operation is the succeeded global setup operation.
	Operation OperationRecord
	// Network is the identity foundation and whether this call replayed its exact committed completion.
	Network NetworkMutationResult
}

// Validate rejects completion results whose operation and network revisions do not describe one atomic setup completion.
func (result CompleteNetworkSetupResult) Validate() error {
	if err := result.Operation.Operation.Validate(); err != nil {
		return err
	}
	if result.Operation.Operation.Kind != domain.OperationKindNetworkSetup || result.Operation.Operation.ProjectID != "" {
		return fmt.Errorf("completed network setup operation must be global kind %q", domain.OperationKindNetworkSetup)
	}
	if result.Operation.Operation.State != domain.OperationSucceeded {
		return fmt.Errorf("completed network setup operation state is %q, want %q", result.Operation.Operation.State, domain.OperationSucceeded)
	}
	if result.Operation.Operation.Phase != networkSetupCompletionSucceededPhase {
		return fmt.Errorf("completed network setup operation phase is %q, want %q", result.Operation.Operation.Phase, networkSetupCompletionSucceededPhase)
	}
	if err := result.Network.Validate(); err != nil {
		return err
	}
	if result.Network.Record.Stage != NetworkStageIdentity {
		return fmt.Errorf("completed network setup must create the identity network stage")
	}
	if result.Operation.Revision != result.Network.Record.Revision+1 {
		return fmt.Errorf("completed network setup revisions are not contiguous")
	}
	return nil
}

// CompleteNetworkSetup atomically retires one staged plan, initializes its proven identity foundation, and succeeds its operation.
func (store *Store) CompleteNetworkSetup(
	ctx context.Context,
	request CompleteNetworkSetupRequest,
) (CompleteNetworkSetupResult, error) {
	if err := request.Validate(); err != nil {
		return CompleteNetworkSetupResult{}, fmt.Errorf("complete network setup: %w", err)
	}
	request = cloneCompleteNetworkSetupRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return CompleteNetworkSetupResult{}, err
	}

	var result CompleteNetworkSetupResult
	err := store.mutations.mutate(ctx, "network setup completion", func(tx *gorm.DB) error {
		completed, err := completeNetworkSetupInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = completed
		return nil
	})
	if err != nil {
		return CompleteNetworkSetupResult{}, fmt.Errorf("complete network setup: %w", err)
	}
	return result, nil
}

// completeNetworkSetupInTransaction applies or exactly replays the plan retirement, network foundation, and terminal edge.
func completeNetworkSetupInTransaction(
	tx *gorm.DB,
	request CompleteNetworkSetupRequest,
) (CompleteNetworkSetupResult, error) {
	operation, found, err := findOperationByID(tx, request.OperationID)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if !found {
		return CompleteNetworkSetupResult{}, &OperationNotFoundError{OperationID: request.OperationID}
	}
	record, err := operationRecordFromModel(operation)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if record.Operation.Kind != domain.OperationKindNetworkSetup || record.Operation.ProjectID != "" {
		return CompleteNetworkSetupResult{}, fmt.Errorf(
			"operation %q is not an active global network setup",
			request.OperationID,
		)
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	planRow, planFound, err := readOptionalNetworkSetupPlanForStaging(tx, request.OperationID)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	identityRequest, err := networkIdentityRequestFromSetupCompletion(request)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}

	if record.Operation.State == domain.OperationSucceeded {
		if planFound {
			return CompleteNetworkSetupResult{}, corruptNetworkSetupPlan(
				request.OperationID,
				fmt.Errorf("succeeded operation retains its singleton plan"),
			)
		}
		networkResult, err := initializeNetworkIdentityInTransaction(tx, identityRequest)
		if err != nil {
			return CompleteNetworkSetupResult{}, err
		}
		if !networkResult.Replayed {
			return CompleteNetworkSetupResult{}, corruptStateError(
				"network setup completion",
				string(request.OperationID),
				fmt.Errorf("succeeded operation had no durable network foundation"),
			)
		}
		projectedOwnership, confirmedAt, err := readMachineOwnershipProjectionInTransaction(tx)
		if err != nil {
			return CompleteNetworkSetupResult{}, err
		}
		if err := requireExactCompletedMachineOwnershipProjection(
			projectedOwnership,
			confirmedAt,
			request,
		); err != nil {
			return CompleteNetworkSetupResult{}, err
		}
		if err := requireExactCompletedNetworkSetupHistory(
			record,
			history,
			request,
			networkResult.Record.Revision,
		); err != nil {
			return CompleteNetworkSetupResult{}, err
		}
		result := CompleteNetworkSetupResult{Operation: record, Network: networkResult}
		if err := result.Validate(); err != nil {
			return CompleteNetworkSetupResult{}, err
		}
		return result, nil
	}

	if record.Operation.State != domain.OperationRequiresApproval {
		return CompleteNetworkSetupResult{}, fmt.Errorf(
			"network setup operation %q must require approval, got %q",
			request.OperationID,
			record.Operation.State,
		)
	}
	if record.Revision != request.ExpectedOperationRevision {
		return CompleteNetworkSetupResult{}, &StaleRevisionError{
			OperationID: request.OperationID,
			Expected:    request.ExpectedOperationRevision,
			Actual:      record.Revision,
		}
	}
	if err := requireExactNetworkSetupHistory(record, history); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if !planFound {
		return CompleteNetworkSetupResult{}, corruptNetworkSetupPlan(
			request.OperationID,
			fmt.Errorf("singleton plan is missing"),
		)
	}
	plan, err := networkSetupPoolPlanFromModel(planRow, record)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if err := requireNetworkSetupCompletionMatchesPlan(request, plan.Ownership, plan.Pool); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if err := requireNetworkStateAbsentForStaging(tx); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if request.At.Before(record.Operation.RequestedAt) {
		return CompleteNetworkSetupResult{}, fmt.Errorf("network setup completion time precedes the operation request")
	}
	expectedModelRevision, err := sequenceToModelInt(
		"expected network setup operation revision",
		request.ExpectedOperationRevision,
		false,
	)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}

	deleted := tx.
		Where(
			"id = ? AND operation_id = ? AND operation_revision = ?",
			1,
			string(request.OperationID),
			expectedModelRevision,
		).
		Delete(&models.NetworkSetupPlan{})
	if err := requireOneMutation(deleted, "delete completed network setup plan", string(request.OperationID)); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	running, err := transitionOperationInTransaction(
		tx,
		request.OperationID,
		record.Revision,
		domain.OperationRunning,
		networkSetupCompletionRunningPhase,
		request.At,
		nil,
	)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	networkResult, err := initializeNetworkIdentityInTransaction(tx, identityRequest)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if networkResult.Replayed {
		return CompleteNetworkSetupResult{}, corruptStateError(
			"network setup completion",
			string(request.OperationID),
			fmt.Errorf("approval completion replayed a pre-existing network foundation"),
		)
	}
	if err := insertMachineOwnershipProjectionInTransaction(tx, request.ConfirmedOwnership, request.At); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	succeeded, err := transitionOperationInTransaction(
		tx,
		request.OperationID,
		running.Revision,
		domain.OperationSucceeded,
		networkSetupCompletionSucceededPhase,
		request.At,
		nil,
	)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	completedHistory, err := operationHistoryInTransaction(tx, succeeded)
	if err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	if err := requireExactCompletedNetworkSetupHistory(
		succeeded,
		completedHistory,
		request,
		networkResult.Record.Revision,
	); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	result := CompleteNetworkSetupResult{Operation: succeeded, Network: networkResult}
	if err := result.Validate(); err != nil {
		return CompleteNetworkSetupResult{}, err
	}
	return result, nil
}

// requireExactCompletedMachineOwnershipProjection prevents terminal replay from accepting different helper authority.
func requireExactCompletedMachineOwnershipProjection(
	projected ownership.Observation,
	confirmedAt time.Time,
	request CompleteNetworkSetupRequest,
) error {
	if projected != request.ConfirmedOwnership || !confirmedAt.Equal(request.At) {
		return corruptStateError(
			"network setup completion",
			string(request.OperationID),
			fmt.Errorf("machine ownership projection differs from the exact completed request"),
		)
	}
	return nil
}

// validateConfirmedNetworkSetupOwnership verifies helper-confirmed projection evidence without implying a daemon store read.
func validateConfirmedNetworkSetupOwnership(observation ownership.Observation) error {
	if !observation.Exists {
		return fmt.Errorf("confirmed network setup ownership is missing")
	}
	if err := observation.Record.Validate(); err != nil {
		return fmt.Errorf("confirmed network setup ownership record: %w", err)
	}
	if observation.Record.Generation != networkSetupOwnershipGeneration {
		return fmt.Errorf(
			"confirmed network setup ownership generation is %d, want %d",
			observation.Record.Generation,
			networkSetupOwnershipGeneration,
		)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint confirmed network setup ownership: %w", err)
	}
	if observation.Fingerprint != fingerprint {
		return fmt.Errorf("confirmed network setup ownership fingerprint does not match its record")
	}
	return nil
}

// validateNetworkSetupPoolPostconditions requires matching helper and fresh daemon evidence for every exact /29 address.
func validateNetworkSetupPoolPostconditions(
	pool identity.Pool,
	helperEvidence helper.PoolMutationEvidence,
	observations []loopback.Observation,
) error {
	if helperEvidence.Pool != pool.Prefix().String() {
		return fmt.Errorf("helper network setup pool evidence does not match confirmed ownership")
	}
	if len(helperEvidence.Identities) != networkSetupPoolIdentityCount {
		return fmt.Errorf("helper network setup pool evidence contains %d identities, want %d", len(helperEvidence.Identities), networkSetupPoolIdentityCount)
	}
	if len(observations) != networkSetupPoolIdentityCount {
		return fmt.Errorf("daemon network setup observations contain %d identities, want %d", len(observations), networkSetupPoolIdentityCount)
	}
	addresses := pool.Candidates()
	for index, address := range addresses {
		evidence := helperEvidence.Identities[index]
		if evidence.Address != address.String() {
			return fmt.Errorf("helper network setup pool identity %d does not match address %s", index, address)
		}
		if err := evidence.Observation.Validate(); err != nil {
			return fmt.Errorf("helper network setup pool identity %s: %w", address, err)
		}
		if evidence.Observation.State != helper.ObservationOwned {
			return fmt.Errorf("helper network setup pool identity %s is not owned", address)
		}

		observed := observations[index]
		if observed.Address != address {
			return fmt.Errorf("daemon network setup observation %d does not match address %s", index, address)
		}
		if observed.State != loopback.StateExact {
			return fmt.Errorf("daemon network setup observation %s state is %q, want %q", address, observed.State, loopback.StateExact)
		}
		fingerprint, err := observed.Fingerprint()
		if err != nil {
			return fmt.Errorf("fingerprint daemon network setup observation %s: %w", address, err)
		}
		if evidence.Observation.Fingerprint != fingerprint {
			return fmt.Errorf("helper and daemon network setup evidence differ for address %s", address)
		}
		if index > 0 && observed.Loopback != observations[0].Loopback {
			return fmt.Errorf("daemon network setup observations do not share one native loopback interface")
		}
	}
	return nil
}

// requireNetworkSetupCompletionMatchesPlan binds independently confirmed proof to every immutable durable plan field.
func requireNetworkSetupCompletionMatchesPlan(
	request CompleteNetworkSetupRequest,
	plannedOwnership ownership.Record,
	plannedPool identity.Pool,
) error {
	if request.ConfirmedOwnership.Record != plannedOwnership {
		return fmt.Errorf("confirmed network setup ownership does not match the durable plan")
	}
	if request.HelperPoolEvidence.Pool != plannedPool.Prefix().String() ||
		request.ConfirmedOwnership.Record.LoopbackPoolPrefix != plannedPool.Prefix().String() {
		return fmt.Errorf("confirmed network setup pool does not match the durable plan")
	}
	return nil
}

// networkIdentityRequestFromSetupCompletion converts only validated proof into the existing identity-foundation contract.
func networkIdentityRequestFromSetupCompletion(
	request CompleteNetworkSetupRequest,
) (InitializeNetworkIdentityRequest, error) {
	pool, err := networkSetupIdentityPool(request.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		return InitializeNetworkIdentityRequest{}, err
	}
	networkOwnership, err := identity.NewOwnership(
		identity.InstallationID(request.ConfirmedOwnership.Record.InstallationID),
		request.ConfirmedOwnership.Record.Generation,
	)
	if err != nil {
		return InitializeNetworkIdentityRequest{}, err
	}
	poolEvidence, err := networkSetupPoolCompletionEvidence(request)
	if err != nil {
		return InitializeNetworkIdentityRequest{}, err
	}
	initialization := InitializeNetworkIdentityRequest{
		ExpectedNetworkRevision: 0,
		Ownership:               networkOwnership,
		Pool:                    pool,
		PoolGeneration:          request.ConfirmedOwnership.Record.Generation,
		Setup: []NetworkSetupProof{
			{
				Component:  NetworkSetupComponentMachineOwnership,
				Evidence:   networkSetupOwnershipEvidencePrefix + request.ConfirmedOwnership.Fingerprint,
				Generation: request.ConfirmedOwnership.Record.Generation,
				VerifiedAt: request.At,
			},
			{
				Component:  NetworkSetupComponentLoopbackPool,
				Evidence:   poolEvidence,
				Generation: request.ConfirmedOwnership.Record.Generation,
				VerifiedAt: request.At,
			},
		},
		At: request.At,
	}
	if err := initialization.Validate(); err != nil {
		return InitializeNetworkIdentityRequest{}, fmt.Errorf("derive network identity foundation from setup completion: %w", err)
	}
	return initialization, nil
}

// networkSetupPoolCompletionEvidence fingerprints every helper result and matching independent daemon observation.
func networkSetupPoolCompletionEvidence(request CompleteNetworkSetupRequest) (string, error) {
	payload := []byte(networkSetupPoolEvidenceDomain)
	payload = append(payload, request.HelperPoolEvidence.Pool...)
	payload = append(payload, 0)
	for index, evidence := range request.HelperPoolEvidence.Identities {
		if evidence.Changed {
			payload = append(payload, 1)
		} else {
			payload = append(payload, 0)
		}
		payload = append(payload, evidence.Address...)
		payload = append(payload, 0)
		payload = append(payload, evidence.Observation.State...)
		payload = append(payload, 0)
		payload = append(payload, evidence.Observation.Fingerprint...)
		payload = append(payload, 0)
		fingerprint, err := request.ObservedPool[index].Fingerprint()
		if err != nil {
			return "", fmt.Errorf("fingerprint daemon network setup observation %d: %w", index, err)
		}
		payload = append(payload, fingerprint...)
		payload = append(payload, 0)
	}
	digest := sha256.Sum256(payload)
	return networkSetupPoolEvidencePrefix + hex.EncodeToString(digest[:]), nil
}

// requireExactCompletedNetworkSetupHistory proves the two completion edges surround exactly one network revision.
func requireExactCompletedNetworkSetupHistory(
	record OperationRecord,
	history []OperationTransition,
	request CompleteNetworkSetupRequest,
	networkRevision domain.Sequence,
) error {
	if len(history) != 5 {
		return fmt.Errorf("completed network setup operation %q has %d transitions, expected 5", record.Operation.ID, len(history))
	}
	if err := requireExactNetworkSetupHistory(record, history[:3]); err != nil {
		return err
	}
	if history[2].Sequence != request.ExpectedOperationRevision {
		return fmt.Errorf("network setup completion does not follow the expected approval revision")
	}
	if history[3].State != domain.OperationRunning || history[3].Phase != networkSetupCompletionRunningPhase ||
		!history[3].OccurredAt.Equal(request.At) || history[3].Sequence != request.ExpectedOperationRevision+1 {
		return fmt.Errorf("network setup completion running edge differs from the exact request")
	}
	if networkRevision != history[3].Sequence+1 {
		return fmt.Errorf("network setup identity revision does not follow its running edge")
	}
	if history[4].State != domain.OperationSucceeded || history[4].Phase != networkSetupCompletionSucceededPhase ||
		!history[4].OccurredAt.Equal(request.At) || history[4].Sequence != networkRevision+1 {
		return fmt.Errorf("network setup completion succeeded edge differs from the exact request")
	}
	return nil
}

// cloneCompleteNetworkSetupRequest isolates every nested proof slice and platform fact before waiting for writer authority.
func cloneCompleteNetworkSetupRequest(request CompleteNetworkSetupRequest) CompleteNetworkSetupRequest {
	request.HelperPoolEvidence.Identities = slices.Clone(request.HelperPoolEvidence.Identities)
	request.ObservedPool = slices.Clone(request.ObservedPool)
	for index := range request.ObservedPool {
		request.ObservedPool[index] = cloneNetworkSetupLoopbackObservation(request.ObservedPool[index])
	}
	request.At = canonicalNetworkMutationTime(request.At)
	return request
}

// cloneNetworkSetupLoopbackObservation deep-copies mutable assignment slices and platform-specific fact pointers.
func cloneNetworkSetupLoopbackObservation(observation loopback.Observation) loopback.Observation {
	observation.Assignments = slices.Clone(observation.Assignments)
	for index := range observation.Assignments {
		if observation.Assignments[index].Linux != nil {
			linux := *observation.Assignments[index].Linux
			observation.Assignments[index].Linux = &linux
		}
		if observation.Assignments[index].Windows != nil {
			windows := *observation.Assignments[index].Windows
			observation.Assignments[index].Windows = &windows
		}
	}
	return observation
}

// requireNoNetworkSetupPlanForDirectInitialization prevents public initialization from bypassing staged approval.
func requireNoNetworkSetupPlanForDirectInitialization(tx *gorm.DB) error {
	table := (&models.NetworkSetupPlan{}).TableName()
	var objects []networkSchemaObject
	if err := tx.Raw(
		"SELECT name, type FROM sqlite_master WHERE name = ? ORDER BY type ASC",
		table,
	).Scan(&objects).Error; err != nil {
		return fmt.Errorf("inspect network setup plan schema: %w", err)
	}
	if len(objects) == 0 {
		return nil
	}
	if len(objects) != 1 || objects[0].Type != "table" {
		return corruptStateError("network setup plan", "schema", fmt.Errorf("expected one table object"))
	}
	var rows []models.NetworkSetupPlan
	if err := tx.Order("id ASC").Limit(1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read network setup plan before direct initialization: %w", err)
	}
	if len(rows) != 0 {
		return fmt.Errorf("direct network initialization is blocked by staged setup operation %q", rows[0].OperationId)
	}
	return nil
}
