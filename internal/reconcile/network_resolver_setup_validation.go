package reconcile

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/state"
)

const networkResolverSetupFingerprintBytes = 32

// validateExistingNetworkResolverSetupOperation accepts any valid lifecycle state owned by the exact global intent.
func validateExistingNetworkResolverSetupOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if record.Operation.IntentID != intentID {
		return fmt.Errorf("operation intent readback differs from request")
	}
	if record.Operation.Kind != domain.OperationKindNetworkResolverSetup || record.Operation.ProjectID != "" {
		return &state.IntentConflictError{
			IntentID:            intentID,
			ExistingOperationID: record.Operation.ID,
			ExistingKind:        record.Operation.Kind,
			ExistingProjectID:   record.Operation.ProjectID,
			RequestedKind:       domain.OperationKindNetworkResolverSetup,
			RequestedProjectID:  "",
		}
	}
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("operation is invalid: %w", err)
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return fmt.Errorf("operation revision is invalid: %w", err)
	}
	return nil
}

// validateStagedNetworkResolverSetupOperation binds staging readback to the requested intent and approval boundary.
func validateStagedNetworkResolverSetupOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if err := validateExistingNetworkResolverSetupOperation(record, intentID); err != nil {
		return err
	}
	if record.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("staged operation state is %q, want %q", record.Operation.State, domain.OperationRequiresApproval)
	}
	return nil
}

// validateConfirmNetworkResolverSetupOperation rejects uncorrelated, invalid, or non-resolver operation projections.
func validateConfirmNetworkResolverSetupOperation(record state.OperationRecord, operationID domain.OperationID) error {
	if record.Operation.ID != operationID {
		return fmt.Errorf("operation readback differs from request")
	}
	if record.Operation.Kind != domain.OperationKindNetworkResolverSetup || record.Operation.ProjectID != "" {
		return fmt.Errorf("operation %q is not a global %q operation", operationID, domain.OperationKindNetworkResolverSetup)
	}
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("operation is invalid: %w", err)
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return fmt.Errorf("operation revision is invalid: %w", err)
	}
	return nil
}

// validateNetworkResolverSetupPlan binds a resolved helper plan to one approval revision.
func validateNetworkResolverSetupPlan(
	plan ticketissuer.ResolverPlan,
	operationID domain.OperationID,
	revision domain.Sequence,
) error {
	if plan.Purpose != ticketissuer.ResolverPlanPurposeSetup {
		return fmt.Errorf("network resolver setup plan purpose is %q, want %q", plan.Purpose, ticketissuer.ResolverPlanPurposeSetup)
	}
	if plan.CheckpointRevision != 0 {
		return fmt.Errorf("network resolver setup plan checkpoint revision is %d, want 0", plan.CheckpointRevision)
	}
	if plan.CheckpointPhase != ticketissuer.ResolverCheckpointPhaseSetupApproval {
		return fmt.Errorf("network resolver setup plan checkpoint phase is %q, want %q", plan.CheckpointPhase, ticketissuer.ResolverCheckpointPhaseSetupApproval)
	}
	if plan.Operation.Kind != domain.OperationKindNetworkResolverSetup {
		return fmt.Errorf("network resolver setup plan operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkResolverSetup)
	}
	if plan.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("network resolver setup plan operation state is %q, want %q", plan.Operation.State, domain.OperationRequiresApproval)
	}
	if plan.Operation.Phase != string(ticketissuer.ResolverCheckpointPhaseSetupApproval) {
		return fmt.Errorf("network resolver setup plan operation phase is %q, want %q", plan.Operation.Phase, ticketissuer.ResolverCheckpointPhaseSetupApproval)
	}
	if plan.Mutation != helper.OperationEnsureResolver {
		return fmt.Errorf("network resolver setup plan mutation is %q, want %q", plan.Mutation, helper.OperationEnsureResolver)
	}
	sourceFingerprint, err := networkResolverSetupPlanSourceFingerprint(plan.TargetOwnership)
	if err != nil {
		return fmt.Errorf("derive network resolver setup source ownership fingerprint: %w", err)
	}
	if plan.ExpectedSourceOwnershipFingerprint != sourceFingerprint {
		return fmt.Errorf("network resolver setup plan source ownership fingerprint does not match its target-derived schema-1 record")
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("network resolver setup plan is invalid: %w", err)
	}
	if plan.Operation.ID != operationID {
		return fmt.Errorf("network resolver setup plan belongs to another operation")
	}
	if plan.OperationRevision != revision {
		return &state.StaleRevisionError{OperationID: operationID, Expected: revision, Actual: plan.OperationRevision}
	}
	return nil
}

// networkResolverSetupPlanSourceFingerprint derives the schema-one ownership fingerprint that setup must transition from.
func networkResolverSetupPlanSourceFingerprint(target ownership.Record) (string, error) {
	source := target
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	if err := source.Validate(); err != nil {
		return "", err
	}
	return source.Fingerprint()
}

// validateNetworkResolverSetupResult binds helper launch metadata to the exact durable policy and target.
func validateNetworkResolverSetupResult(
	result ticketissuer.ResolverResult,
	plan ticketissuer.ResolverPlan,
	now time.Time,
) error {
	if err := result.Validate(now); err != nil {
		return fmt.Errorf("helper resolver result is invalid: %w", err)
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint approved resolver policy: %w", err)
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint approved resolver ownership: %w", err)
	}
	if result.OperationID != plan.Operation.ID ||
		result.Operation != helper.OperationEnsureResolver ||
		result.PolicyFingerprint != policyFingerprint ||
		result.OwnershipFingerprint != ownershipFingerprint {
		return fmt.Errorf("helper resolver result differs from the approved policy transition")
	}
	return nil
}

// validateNetworkResolverSetupEvidence rejects helper postconditions outside one exact resolver ensure.
func validateNetworkResolverSetupEvidence(evidence helper.ResolverMutationEvidence) error {
	if !validNetworkResolverSetupFingerprint(evidence.PolicyFingerprint) {
		return fmt.Errorf("resolver evidence policy fingerprint is invalid")
	}
	if !validNetworkResolverSetupFingerprint(evidence.OwnershipFingerprint) {
		return fmt.Errorf("resolver evidence ownership fingerprint is invalid")
	}
	if !validNetworkResolverSetupFingerprint(evidence.ObservationFingerprint) {
		return fmt.Errorf("resolver evidence observation fingerprint is invalid")
	}
	if evidence.Postcondition != helper.ResolverPostconditionExact {
		return fmt.Errorf("resolver evidence must prove the exact resolver policy")
	}
	return nil
}

// validateNetworkResolverSetupOwnership binds one protected projection to the current durable network identity.
func validateNetworkResolverSetupOwnership(
	observation ownership.Observation,
	schemaVersion uint32,
	network state.NetworkRecord,
) error {
	if !observation.Exists {
		return fmt.Errorf("machine ownership projection is missing")
	}
	if err := observation.Record.Validate(); err != nil {
		return fmt.Errorf("machine ownership projection is invalid: %w", err)
	}
	if observation.Record.SchemaVersion != schemaVersion {
		return fmt.Errorf("machine ownership schema is %d, want %d", observation.Record.SchemaVersion, schemaVersion)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint machine ownership projection: %w", err)
	}
	if observation.Fingerprint != fingerprint {
		return fmt.Errorf("machine ownership projection fingerprint does not match its record")
	}
	if observation.Record.InstallationID != string(network.Ownership.InstallationID) ||
		observation.Record.Generation != network.Ownership.Generation ||
		observation.Record.LoopbackPoolPrefix != network.Pool.Prefix().String() {
		return fmt.Errorf("machine ownership projection differs from the durable network identity")
	}
	return nil
}

// validNetworkResolverSetupFingerprint accepts only the canonical lowercase SHA-256 representation.
func validNetworkResolverSetupFingerprint(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil &&
		len(decoded) == networkResolverSetupFingerprintBytes &&
		hex.EncodeToString(decoded) == value
}
