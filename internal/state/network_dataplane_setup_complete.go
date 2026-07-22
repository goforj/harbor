package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"gorm.io/gorm"
)

const (
	networkDataPlaneSetupTrustConfirmPhase    = "verifying trust"
	networkDataPlaneSetupLowPortApprovalPhase = "awaiting low-port approval"
	networkDataPlaneSetupActivationPhase      = "activating trusted ingress"
	networkDataPlaneSetupCompletedPhase       = "completed"
)

// AdvanceNetworkDataPlaneSetupTrustRequest confirms trust for one exact approval revision.
type AdvanceNetworkDataPlaneSetupTrustRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	Projection                NetworkDataPlaneSetupProjection
	Policy                    networkpolicy.Policy
	TrustEvidenceDigest       string
	TrustVerifiedAt           time.Time
}

// Validate rejects an unbound or malformed trust confirmation.
func (request AdvanceNetworkDataPlaneSetupTrustRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.RequesterIdentity) == "" || strings.TrimSpace(request.RequesterIdentity) != request.RequesterIdentity {
		return fmt.Errorf("network data-plane setup requester identity is invalid")
	}
	if request.Projection.Stage != NetworkStageResolver {
		return fmt.Errorf("network data-plane setup trust requires resolver projection")
	}
	if err := request.Projection.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup trust projection: %w", err)
	}
	if err := request.Policy.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup trust policy: %w", err)
	}
	fingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return err
	}
	if fingerprint != request.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint {
		return fmt.Errorf("network data-plane setup trust policy does not match ownership")
	}
	if err := validateNetworkDataPlaneSetupDigest("trust evidence digest", request.TrustEvidenceDigest); err != nil {
		return err
	}
	return validateStoredTime("network data-plane setup trust verification time", request.TrustVerifiedAt)
}

// StageNetworkDataPlaneActivationRequest stages low-port proof and exact full-network activation facts.
type StageNetworkDataPlaneActivationRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	LowPortEvidenceDigest     string
	Activation                ActivateNetworkDataPlaneRequest
}

// Validate rejects an activation receipt that is not self-contained and replayable.
func (request StageNetworkDataPlaneActivationRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.RequesterIdentity) == "" || strings.TrimSpace(request.RequesterIdentity) != request.RequesterIdentity {
		return fmt.Errorf("network data-plane setup requester identity is invalid")
	}
	if err := validateNetworkDataPlaneSetupDigest("low-port evidence digest", request.LowPortEvidenceDigest); err != nil {
		return err
	}
	return request.Activation.Validate()
}

// CompleteNetworkDataPlaneActivationRequest verifies a completed full activation while its operation remains running.
type CompleteNetworkDataPlaneActivationRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects an unbound activation completion read.
func (request CompleteNetworkDataPlaneActivationRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.RequesterIdentity) == "" || strings.TrimSpace(request.RequesterIdentity) != request.RequesterIdentity {
		return fmt.Errorf("network data-plane setup requester identity is invalid")
	}
	return nil
}

// CompleteNetworkDataPlaneSetupRequest acknowledges a runtime-ready full network as succeeded.
type CompleteNetworkDataPlaneSetupRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	At                        time.Time
}

// Validate rejects a terminal acknowledgement without an exact running operation revision.
func (request CompleteNetworkDataPlaneSetupRequest) Validate() error {
	if err := (CompleteNetworkDataPlaneActivationRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: request.RequesterIdentity}).Validate(); err != nil {
		return err
	}
	return validateStoredTime("network data-plane setup completion time", request.At)
}

// NetworkDataPlaneSetupActivationResult returns the running operation and exact persisted replay authority.
type NetworkDataPlaneSetupActivationResult struct {
	Operation  OperationRecord
	Activation ActivateNetworkDataPlaneRequest
}

// NetworkDataPlaneSetupPlanRecord exposes sanitized recovery authority for one active setup operation.
type NetworkDataPlaneSetupPlanRecord struct {
	Operation             OperationRecord
	Projection            NetworkDataPlaneSetupProjection
	Policy                networkpolicy.Policy
	TrustEvidenceDigest   string
	TrustVerifiedAt       time.Time
	LowPortEvidenceDigest string
	Activation            *ActivateNetworkDataPlaneRequest
}

// ReadNetworkDataPlaneSetupPlan restores the exact sanitized recovery receipt for an operation.
func (journal *OperationJournal) ReadNetworkDataPlaneSetupPlan(ctx context.Context, operationID domain.OperationID) (NetworkDataPlaneSetupPlanRecord, bool, error) {
	if err := operationID.Validate(); err != nil {
		return NetworkDataPlaneSetupPlanRecord{}, false, err
	}
	ctx = normalizeContext(ctx)
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return NetworkDataPlaneSetupPlanRecord{}, false, fmt.Errorf("open network data-plane setup plan: %w", err)
	}
	var result NetworkDataPlaneSetupPlanRecord
	found := false
	err = builder.Transaction(func(tx *gorm.DB) error {
		planRow, planFound, readErr := readOptionalNetworkDataPlaneSetupPlan(tx, operationID)
		if readErr != nil {
			return readErr
		}
		row, operationFound, readErr := findOperationByID(tx, operationID)
		if readErr != nil {
			return readErr
		}
		if !operationFound {
			if planFound {
				return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation owner is missing"))
			}
			return nil
		}
		if !planFound {
			return nil
		}
		operation, readErr := operationRecordFromModel(row)
		if readErr != nil {
			return readErr
		}
		if operation.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || operation.Operation.ProjectID != "" {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation owner is not a global network data-plane setup"))
		}
		if readErr := requireNetworkDataPlaneSetupLifecycleHistory(tx, operation); readErr != nil {
			return readErr
		}
		plan, readErr := networkDataPlaneSetupPlanFromRow(planRow, operation)
		if readErr != nil {
			return readErr
		}
		result = NetworkDataPlaneSetupPlanRecord{Operation: plan.Operation, Projection: plan.Authority.Projection, Policy: plan.Authority.Policy, TrustEvidenceDigest: plan.TrustEvidenceDigest, TrustVerifiedAt: plan.TrustVerifiedAt, LowPortEvidenceDigest: plan.LowPortEvidenceDigest}
		if plan.Activation != nil {
			activation := cloneActivateNetworkDataPlaneRequest(*plan.Activation)
			result.Activation = &activation
		}
		found = true
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NetworkDataPlaneSetupPlanRecord{}, false, fmt.Errorf("read network data-plane setup plan: %w", err)
	}
	return result, found, nil
}

// AdvanceNetworkDataPlaneSetupTrust stores resolver authority before low-port approval can be requested.
func (journal *OperationJournal) AdvanceNetworkDataPlaneSetupTrust(ctx context.Context, request AdvanceNetworkDataPlaneSetupTrustRequest) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("advance network data-plane setup trust: %w", err)
	}
	ctx = normalizeContext(ctx)
	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network data-plane setup trust", func(tx *gorm.DB) error {
		current, err := readExpectedNetworkDataPlaneSetupOperation(tx, request.OperationID, request.ExpectedOperationRevision)
		if err != nil {
			return err
		}
		if current.Operation.State != domain.OperationRequiresApproval || current.Operation.Phase != networkDataPlaneSetupApprovalPhase {
			return fmt.Errorf("network data-plane setup trust requires trust approval, found %q/%q", current.Operation.State, current.Operation.Phase)
		}
		authority, err := currentNetworkDataPlaneSetupAuthority(tx, request.Projection, request.Policy)
		if err != nil {
			return err
		}
		if err := requireNetworkDataPlaneSetupRequester(authority, request.RequesterIdentity); err != nil {
			return err
		}
		if request.TrustVerifiedAt.Before(authority.Projection.NetworkUpdatedAt) {
			return fmt.Errorf("network data-plane setup trust verification precedes network authority")
		}
		running, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationRunning, networkDataPlaneSetupTrustConfirmPhase, request.TrustVerifiedAt, nil)
		if err != nil {
			return err
		}
		approval, err := transitionOperationInTransaction(tx, request.OperationID, running.Revision, domain.OperationRequiresApproval, networkDataPlaneSetupLowPortApprovalPhase, request.TrustVerifiedAt, nil)
		if err != nil {
			return err
		}
		payload, digest, err := encodeNetworkDataPlaneSetupAuthority(authority.Projection, authority.Policy)
		if err != nil {
			return err
		}
		row := networkDataPlaneSetupPlanRow{ID: networkDataPlaneSetupPlanSingletonID, OperationID: string(approval.Operation.ID), OperationRevision: int(approval.Revision), Phase: string(networkDataPlaneSetupPlanLowPortApproval), NetworkStateID: networkStateSingletonID, NetworkRevision: int(authority.Projection.NetworkRevision), NetworkUpdatedAt: authority.Projection.NetworkUpdatedAt, AuthorityPayload: payload, AuthorityDigest: digest, TrustEvidenceDigest: request.TrustEvidenceDigest, TrustVerifiedAt: request.TrustVerifiedAt}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create network data-plane setup plan: %w", err)
		}
		result = approval
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("advance network data-plane setup trust: %w", err)
	}
	return result, nil
}

// StageNetworkDataPlaneActivation durably records exact activation authority before any runtime activation effect.
func (journal *OperationJournal) StageNetworkDataPlaneActivation(ctx context.Context, request StageNetworkDataPlaneActivationRequest) (NetworkDataPlaneSetupActivationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("stage network data-plane activation: %w", err)
	}
	request.Activation = cloneActivateNetworkDataPlaneRequest(request.Activation)
	ctx = normalizeContext(ctx)
	var result NetworkDataPlaneSetupActivationResult
	err := journal.mutations.mutate(ctx, "network data-plane activation staging", func(tx *gorm.DB) error {
		current, err := readExpectedNetworkDataPlaneSetupOperation(tx, request.OperationID, request.ExpectedOperationRevision)
		if err != nil {
			return err
		}
		row, found, err := readOptionalNetworkDataPlaneSetupPlan(tx, request.OperationID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("network data-plane setup plan is missing")
		}
		plan, err := networkDataPlaneSetupPlanFromRow(row, current)
		if err != nil {
			return err
		}
		if current.Operation.State != domain.OperationRequiresApproval || current.Operation.Phase != networkDataPlaneSetupLowPortApprovalPhase || plan.Phase != networkDataPlaneSetupPlanLowPortApproval {
			return fmt.Errorf("network data-plane setup activation requires low-port approval")
		}
		if err := requireNetworkDataPlaneSetupRequester(plan.Authority, request.RequesterIdentity); err != nil {
			return err
		}
		if err := requireNetworkDataPlaneSetupActivationMatchesAuthority(request.Activation, plan.Authority, plan.TrustVerifiedAt); err != nil {
			return err
		}
		payload, digest, err := encodeNetworkDataPlaneSetupActivation(request.Activation)
		if err != nil {
			return err
		}
		running, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationRunning, networkDataPlaneSetupActivationPhase, request.Activation.At, nil)
		if err != nil {
			return err
		}
		updated := tx.Model(&networkDataPlaneSetupPlanRow{}).Where("id = ? AND operation_id = ? AND operation_revision = ? AND phase = ?", networkDataPlaneSetupPlanSingletonID, string(request.OperationID), int(current.Revision), string(networkDataPlaneSetupPlanLowPortApproval)).Updates(map[string]any{"operation_revision": int(running.Revision), "phase": string(networkDataPlaneSetupPlanActivation), "low_port_evidence_digest": request.LowPortEvidenceDigest, "activation_payload": payload, "activation_digest": digest})
		if err := requireOneMutation(updated, "stage network data-plane activation", string(request.OperationID)); err != nil {
			return err
		}
		result = NetworkDataPlaneSetupActivationResult{Operation: running, Activation: cloneActivateNetworkDataPlaneRequest(request.Activation)}
		return nil
	})
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("stage network data-plane activation: %w", err)
	}
	return result, nil
}

// CompleteNetworkDataPlaneActivation proves the full durable activation matches the staged exact replay authority.
func (journal *OperationJournal) CompleteNetworkDataPlaneActivation(ctx context.Context, request CompleteNetworkDataPlaneActivationRequest) (NetworkDataPlaneSetupActivationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("complete network data-plane activation: %w", err)
	}
	ctx = normalizeContext(ctx)
	var result NetworkDataPlaneSetupActivationResult
	err := journal.mutations.mutate(ctx, "network data-plane activation completion", func(tx *gorm.DB) error {
		current, err := readExpectedNetworkDataPlaneSetupOperation(tx, request.OperationID, request.ExpectedOperationRevision)
		if err != nil {
			return err
		}
		row, found, err := readOptionalNetworkDataPlaneSetupPlan(tx, request.OperationID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("network data-plane setup plan is missing")
		}
		plan, err := networkDataPlaneSetupPlanFromRow(row, current)
		if err != nil {
			return err
		}
		if current.Operation.State != domain.OperationRunning || current.Operation.Phase != networkDataPlaneSetupActivationPhase || plan.Phase != networkDataPlaneSetupPlanActivation || plan.Activation == nil {
			return fmt.Errorf("network data-plane setup activation is not running")
		}
		if err := requireNetworkDataPlaneSetupRequester(plan.Authority, request.RequesterIdentity); err != nil {
			return err
		}
		policyFingerprint, err := plan.Authority.Policy.Fingerprint()
		if err != nil {
			return err
		}
		projection, err := resolveNetworkDataPlaneSetupProjection(tx, plan.Authority.Policy, policyFingerprint)
		if err != nil {
			return err
		}
		if projection.Stage != NetworkStageFull || projection.NetworkRevision < plan.Authority.Projection.NetworkRevision {
			return fmt.Errorf("network data-plane setup full activation is not durable")
		}
		if projection.ConfirmedOwnership != plan.Activation.ConfirmedOwnership || projection.LowPortProof != plan.Activation.Setup[1] || projection.Listeners != plan.Activation.Listeners {
			return fmt.Errorf("network data-plane setup full activation differs from staged authority")
		}
		result = NetworkDataPlaneSetupActivationResult{Operation: current, Activation: cloneActivateNetworkDataPlaneRequest(*plan.Activation)}
		return nil
	})
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("complete network data-plane activation: %w", err)
	}
	return result, nil
}

// CompleteNetworkDataPlaneSetup acknowledges success only after CompleteNetworkDataPlaneActivation has proved full authority.
func (journal *OperationJournal) CompleteNetworkDataPlaneSetup(ctx context.Context, request CompleteNetworkDataPlaneSetupRequest) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("complete network data-plane setup: %w", err)
	}
	ctx = normalizeContext(ctx)
	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network data-plane setup completion", func(tx *gorm.DB) error {
		completed, err := journal.completeNetworkDataPlaneSetupInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = completed
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("complete network data-plane setup: %w", err)
	}
	return result, nil
}

// completeNetworkDataPlaneSetupInTransaction repeats the full-authority proof immediately before the terminal edge.
func (journal *OperationJournal) completeNetworkDataPlaneSetupInTransaction(tx *gorm.DB, request CompleteNetworkDataPlaneSetupRequest) (OperationRecord, error) {
	activation, err := journal.completeNetworkDataPlaneActivationInTransaction(tx, CompleteNetworkDataPlaneActivationRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: request.RequesterIdentity})
	if err != nil {
		return OperationRecord{}, err
	}
	completed, err := transitionOperationInTransaction(tx, request.OperationID, activation.Operation.Revision, domain.OperationSucceeded, networkDataPlaneSetupCompletedPhase, request.At, nil)
	if err != nil {
		return OperationRecord{}, err
	}
	deleted := tx.Where("id = ? AND operation_id = ? AND operation_revision = ?", networkDataPlaneSetupPlanSingletonID, string(request.OperationID), int(activation.Operation.Revision)).Delete(&networkDataPlaneSetupPlanRow{})
	if err := requireOneMutation(deleted, "retire network data-plane setup plan", string(request.OperationID)); err != nil {
		return OperationRecord{}, err
	}
	return completed, nil
}

// completeNetworkDataPlaneActivationInTransaction shares the completion proof with terminal acknowledgement.
func (journal *OperationJournal) completeNetworkDataPlaneActivationInTransaction(tx *gorm.DB, request CompleteNetworkDataPlaneActivationRequest) (NetworkDataPlaneSetupActivationResult, error) {
	current, err := readExpectedNetworkDataPlaneSetupOperation(tx, request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	row, found, err := readOptionalNetworkDataPlaneSetupPlan(tx, request.OperationID)
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	if !found {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("network data-plane setup plan is missing")
	}
	plan, err := networkDataPlaneSetupPlanFromRow(row, current)
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	if current.Operation.State != domain.OperationRunning || current.Operation.Phase != networkDataPlaneSetupActivationPhase || plan.Phase != networkDataPlaneSetupPlanActivation || plan.Activation == nil {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("network data-plane setup activation is not running")
	}
	if err := requireNetworkDataPlaneSetupRequester(plan.Authority, request.RequesterIdentity); err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	policyFingerprint, err := plan.Authority.Policy.Fingerprint()
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	projection, err := resolveNetworkDataPlaneSetupProjection(tx, plan.Authority.Policy, policyFingerprint)
	if err != nil {
		return NetworkDataPlaneSetupActivationResult{}, err
	}
	if projection.Stage != NetworkStageFull || projection.NetworkRevision < plan.Authority.Projection.NetworkRevision {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("network data-plane setup full activation is not durable")
	}
	if projection.ConfirmedOwnership != plan.Activation.ConfirmedOwnership || projection.LowPortProof != plan.Activation.Setup[1] || projection.Listeners != plan.Activation.Listeners {
		return NetworkDataPlaneSetupActivationResult{}, fmt.Errorf("network data-plane setup full activation differs from staged authority")
	}
	return NetworkDataPlaneSetupActivationResult{Operation: current, Activation: cloneActivateNetworkDataPlaneRequest(*plan.Activation)}, nil
}

// readExpectedNetworkDataPlaneSetupOperation loads and checks one exact data-plane operation revision.
func readExpectedNetworkDataPlaneSetupOperation(tx *gorm.DB, operationID domain.OperationID, revision domain.Sequence) (OperationRecord, error) {
	row, found, err := findOperationByID(tx, operationID)
	if err != nil {
		return OperationRecord{}, err
	}
	if !found {
		return OperationRecord{}, &OperationNotFoundError{OperationID: operationID}
	}
	record, err := operationRecordFromModel(row)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || record.Operation.ProjectID != "" {
		return OperationRecord{}, fmt.Errorf("operation %q is not network data-plane setup", operationID)
	}
	if record.Revision != revision {
		return OperationRecord{}, &StaleRevisionError{OperationID: operationID, Expected: revision, Actual: record.Revision}
	}
	if err := requireNetworkDataPlaneSetupLifecycleHistory(tx, record); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// requireNetworkDataPlaneSetupLifecycleHistory rejects generic transitions injected between fixed setup edges.
func requireNetworkDataPlaneSetupLifecycleHistory(tx *gorm.DB, record OperationRecord) error {
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return err
	}
	expected := []struct {
		state domain.OperationState
		phase string
	}{
		{domain.OperationQueued, networkDataPlaneSetupQueuedPhase},
		{domain.OperationRunning, networkDataPlaneSetupRunningPhase},
		{domain.OperationRequiresApproval, networkDataPlaneSetupApprovalPhase},
	}
	switch record.Operation.Phase {
	case networkDataPlaneSetupApprovalPhase:
	case networkDataPlaneSetupLowPortApprovalPhase:
		expected = append(expected, struct {
			state domain.OperationState
			phase string
		}{domain.OperationRunning, networkDataPlaneSetupTrustConfirmPhase}, struct {
			state domain.OperationState
			phase string
		}{domain.OperationRequiresApproval, networkDataPlaneSetupLowPortApprovalPhase})
	case networkDataPlaneSetupActivationPhase:
		expected = append(expected, struct {
			state domain.OperationState
			phase string
		}{domain.OperationRunning, networkDataPlaneSetupTrustConfirmPhase}, struct {
			state domain.OperationState
			phase string
		}{domain.OperationRequiresApproval, networkDataPlaneSetupLowPortApprovalPhase}, struct {
			state domain.OperationState
			phase string
		}{domain.OperationRunning, networkDataPlaneSetupActivationPhase})
	default:
		return corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("operation phase %q is not a lifecycle boundary", record.Operation.Phase))
	}
	if len(history) != len(expected) {
		return corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("operation has %d transitions, expected %d", len(history), len(expected)))
	}
	for index, want := range expected {
		if history[index].State != want.state || history[index].Phase != want.phase {
			return corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("transition %d differs from fixed lifecycle", index+1))
		}
		if index > 0 && history[index].Sequence <= history[index-1].Sequence {
			return corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("transition sequence is not monotonic"))
		}
	}
	return nil
}

// currentNetworkDataPlaneSetupAuthority reconstructs the current resolver predecessor for the first durable receipt.
func currentNetworkDataPlaneSetupAuthority(tx *gorm.DB, expected NetworkDataPlaneSetupProjection, policy networkpolicy.Policy) (networkDataPlaneSetupAuthority, error) {
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return networkDataPlaneSetupAuthority{}, err
	}
	current, err := resolveNetworkDataPlaneSetupProjection(tx, policy, fingerprint)
	if err != nil {
		return networkDataPlaneSetupAuthority{}, err
	}
	if current.Stage != NetworkStageResolver {
		return networkDataPlaneSetupAuthority{}, fmt.Errorf("network data-plane setup trust requires resolver stage, found %q", current.Stage)
	}
	if !sameNetworkDataPlaneSetupProjection(current, expected) {
		return networkDataPlaneSetupAuthority{}, fmt.Errorf("network data-plane setup trust projection differs from the current projection")
	}
	authority := networkDataPlaneSetupAuthority{Projection: current, Policy: policy}
	if err := authority.Validate(); err != nil {
		return networkDataPlaneSetupAuthority{}, err
	}
	return authority, nil
}

// sameNetworkDataPlaneSetupProjection compares durable facts while treating equal instants as equal across callers.
func sameNetworkDataPlaneSetupProjection(left NetworkDataPlaneSetupProjection, right NetworkDataPlaneSetupProjection) bool {
	return left.Stage == right.Stage &&
		left.NetworkRevision == right.NetworkRevision &&
		left.NetworkUpdatedAt.Equal(right.NetworkUpdatedAt) &&
		sameNetworkDataPlaneSetupResolverProof(left.ResolverProof, right.ResolverProof) &&
		sameNetworkDataPlaneSetupResolverProof(left.LowPortProof, right.LowPortProof) &&
		sameNetworkDataPlaneSetupListeners(left.Listeners, right.Listeners) &&
		left.ConfirmedOwnership == right.ConfirmedOwnership
}

// sameNetworkDataPlaneSetupListeners compares every listener fact without time representation identity.
func sameNetworkDataPlaneSetupListeners(left SharedListenerReservations, right SharedListenerReservations) bool {
	return sameNetworkDataPlaneSetupListener(left.DNS, right.DNS) &&
		sameNetworkDataPlaneSetupListener(left.HTTP, right.HTTP) &&
		sameNetworkDataPlaneSetupListener(left.HTTPS, right.HTTPS)
}

// sameNetworkDataPlaneSetupListener compares one shared socket reservation exactly.
func sameNetworkDataPlaneSetupListener(left ListenerReservation, right ListenerReservation) bool {
	return left.Mode == right.Mode &&
		left.Advertised == right.Advertised &&
		left.Bind == right.Bind &&
		left.Generation == right.Generation &&
		left.VerifiedAt.Equal(right.VerifiedAt)
}

// requireNetworkDataPlaneSetupRequester binds every mutation to the exact durable ownership identity.
func requireNetworkDataPlaneSetupRequester(authority networkDataPlaneSetupAuthority, requester string) error {
	if authority.Projection.ConfirmedOwnership.Record.OwnerIdentity != requester {
		return fmt.Errorf("network data-plane setup requester identity does not match durable ownership")
	}
	return nil
}
