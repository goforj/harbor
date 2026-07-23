package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const (
	networkResolverPolicyMigrationQueuedPhase     = "queued"
	networkResolverPolicyMigrationRunningPhase    = "preparing resolver policy migration"
	networkResolverPolicyMigrationApprovalPhase   = string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval)
	networkResolverPolicyMigrationPlanSingletonID = 1
)

// StageNetworkResolverPolicyMigrationRequest contains the fixed legacy authority and its administrator-trust replacement.
type StageNetworkResolverPolicyMigrationRequest struct {
	Operation                    domain.Operation
	ExpectedNetworkRevision      domain.Sequence
	SourceOwnership              ownership.Observation
	Policy                       networkpolicy.Policy
	ReplacementPolicyFingerprint string
}

// Validate rejects any migration that is not the exact legacy macOS schema-two retirement boundary.
func (request StageNetworkResolverPolicyMigrationRequest) Validate() error {
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	if request.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration {
		return fmt.Errorf("network resolver policy migration operation kind must be %q", domain.OperationKindNetworkResolverPolicyMigration)
	}
	if request.Operation.ProjectID != "" {
		return fmt.Errorf("network resolver policy migration operation must be global")
	}
	if request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("network resolver policy migration operation must be queued")
	}
	if request.Operation.Phase != networkResolverPolicyMigrationQueuedPhase {
		return fmt.Errorf("network resolver policy migration queued phase must be %q", networkResolverPolicyMigrationQueuedPhase)
	}
	if _, err := sequenceToModelInt("network resolver policy migration network revision", request.ExpectedNetworkRevision, false); err != nil {
		return err
	}
	if !request.SourceOwnership.Exists {
		return fmt.Errorf("network resolver policy migration source ownership must exist")
	}
	if err := request.SourceOwnership.Record.Validate(); err != nil {
		return fmt.Errorf("network resolver policy migration source ownership: %w", err)
	}
	sourceFingerprint, err := request.SourceOwnership.Record.Fingerprint()
	if err != nil {
		return fmt.Errorf("network resolver policy migration source ownership fingerprint: %w", err)
	}
	if request.SourceOwnership.Fingerprint != sourceFingerprint {
		return fmt.Errorf("network resolver policy migration source ownership fingerprint is not canonical")
	}
	if request.SourceOwnership.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf("network resolver policy migration source ownership must be schema %d", ownership.NetworkPolicySchemaVersion)
	}
	if err := request.Policy.Validate(); err != nil {
		return err
	}
	if request.Policy.Mechanisms != networkpolicy.LegacyMacOSMechanisms() {
		return fmt.Errorf("network resolver policy migration policy is not legacy macOS")
	}
	policyFingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return err
	}
	if policyFingerprint != request.SourceOwnership.Record.NetworkPolicyFingerprint {
		return fmt.Errorf("network resolver policy migration policy does not match source ownership")
	}
	replacement := request.Policy
	replacement.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
	replacementFingerprint, err := replacement.Fingerprint()
	if err != nil {
		return err
	}
	if request.ReplacementPolicyFingerprint != replacementFingerprint {
		return fmt.Errorf("network resolver policy migration replacement policy fingerprint does not match the administrator policy")
	}
	return nil
}

// StageNetworkResolverPolicyMigration atomically records an approval plan for one exact legacy resolver retirement.
func (journal *OperationJournal) StageNetworkResolverPolicyMigration(ctx context.Context, request StageNetworkResolverPolicyMigrationRequest) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("stage network resolver policy migration: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}
	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network resolver policy migration staging", func(tx *gorm.DB) error {
		if err := requireNetworkResolverPolicyMigrationAuthority(tx, request); err != nil {
			return err
		}
		if _, err := validateRetainedSequenceBounds(tx); err != nil {
			return err
		}
		plan, planFound, err := readOptionalNetworkResolverPolicyMigrationPlan(tx, request.Operation.ID)
		if err != nil {
			return err
		}
		existing, found, err := findOperationByIntent(tx, request.Operation.IntentID)
		if err != nil {
			return err
		}
		if found {
			record, err := operationRecordFromModel(existing)
			if err != nil {
				return err
			}
			if !planFound {
				return corruptNetworkResolverPolicyMigrationPlan(record.Operation.ID, fmt.Errorf("operation exists without its singleton plan"))
			}
			staged, err := replayNetworkResolverPolicyMigration(tx, record, plan, request)
			if err != nil {
				return err
			}
			result = staged
			return nil
		}
		existing, found, err = findOperationByID(tx, request.Operation.ID)
		if err != nil {
			return err
		}
		if found {
			record, err := operationRecordFromModel(existing)
			if err != nil {
				return err
			}
			return &OperationIDConflictError{
				OperationID:       request.Operation.ID,
				ExistingIntentID:  record.Operation.IntentID,
				RequestedIntentID: request.Operation.IntentID,
			}
		}
		if planFound {
			return corruptNetworkResolverPolicyMigrationPlan(request.Operation.ID, fmt.Errorf("singleton plan already belongs to operation %q", plan.OperationId))
		}
		if active, found, err := findActiveNetworkResolverPolicyMigrationOperation(tx); err != nil {
			return err
		} else if found {
			return fmt.Errorf("network resolver policy migration operation %q is already active", active.Operation.ID)
		}
		if err := requireNoActiveProjectLifecycleOperation(tx); err != nil {
			return err
		}
		queued, err := insertQueuedNetworkResolverPolicyMigrationOperation(tx, request.Operation)
		if err != nil {
			return err
		}
		running, err := transitionOperationInTransaction(tx, request.Operation.ID, queued.Revision, domain.OperationRunning, networkResolverPolicyMigrationRunningPhase, request.Operation.RequestedAt, nil)
		if err != nil {
			return err
		}
		approval, err := transitionOperationInTransaction(tx, request.Operation.ID, running.Revision, domain.OperationRequiresApproval, networkResolverPolicyMigrationApprovalPhase, request.Operation.RequestedAt, nil)
		if err != nil {
			return err
		}
		row, err := networkResolverPolicyMigrationPlanModel(approval, request)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create network resolver policy migration plan: %w", err)
		}
		result = approval
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("stage network resolver policy migration: %w", err)
	}
	return result, nil
}

// requireNetworkResolverPolicyMigrationAuthority proves the current resolver-stage root, proof, and ownership are exact.
func requireNetworkResolverPolicyMigrationAuthority(tx *gorm.DB, request StageNetworkResolverPolicyMigrationRequest) error {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return err
	}
	if !initialized || network.Stage != NetworkStageResolver || network.Revision != request.ExpectedNetworkRevision || len(rows.Listeners) != 0 || len(rows.Endpoints) != 0 {
		return fmt.Errorf("network resolver policy migration authority differs from the exact resolver stage")
	}
	projection, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return err
	}
	if !projection.Exists || projection != request.SourceOwnership {
		return fmt.Errorf("network resolver policy migration source ownership differs from projection")
	}
	var proofs []models.NetworkSetupEvidence
	if err := tx.Where("component = ?", "resolver").Order("id ASC").Limit(2).Find(&proofs).Error; err != nil {
		return fmt.Errorf("read network resolver policy migration resolver proof: %w", err)
	}
	if len(proofs) != 1 {
		return fmt.Errorf("network resolver policy migration resolver proof has %d rows, expected 1", len(proofs))
	}
	return nil
}

// readOptionalNetworkResolverPolicyMigrationPlan reads the singleton without trusting its database constraint.
func readOptionalNetworkResolverPolicyMigrationPlan(tx *gorm.DB, operationID domain.OperationID) (models.NetworkResolverPolicyMigrationPlan, bool, error) {
	var rows []models.NetworkResolverPolicyMigrationPlan
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkResolverPolicyMigrationPlan{}, false, fmt.Errorf("read network resolver policy migration plan: %w", err)
	}
	if len(rows) > 1 {
		return models.NetworkResolverPolicyMigrationPlan{}, false, corruptNetworkResolverPolicyMigrationPlan(operationID, fmt.Errorf("singleton plan has %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return models.NetworkResolverPolicyMigrationPlan{}, false, nil
	}
	if rows[0].Id != networkResolverPolicyMigrationPlanSingletonID {
		return models.NetworkResolverPolicyMigrationPlan{}, false, corruptNetworkResolverPolicyMigrationPlan(operationID, fmt.Errorf("singleton ID is %d, expected %d", rows[0].Id, networkResolverPolicyMigrationPlanSingletonID))
	}
	return rows[0], true, nil
}

// replayNetworkResolverPolicyMigration accepts only the exact request that produced the fixed staging lifecycle.
func replayNetworkResolverPolicyMigration(tx *gorm.DB, record OperationRecord, row models.NetworkResolverPolicyMigrationPlan, request StageNetworkResolverPolicyMigrationRequest) (OperationRecord, error) {
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if len(history) != 3 || record.Operation.ID != request.Operation.ID || record.Operation.IntentID != request.Operation.IntentID || record.Operation.Kind != request.Operation.Kind || record.Operation.ProjectID != request.Operation.ProjectID || !record.Operation.RequestedAt.Equal(request.Operation.RequestedAt) {
		return OperationRecord{}, corruptNetworkResolverPolicyMigrationPlan(record.Operation.ID, fmt.Errorf("replay differs from the exact staged operation"))
	}
	expectedStates := [...]domain.OperationState{domain.OperationQueued, domain.OperationRunning, domain.OperationRequiresApproval}
	expectedPhases := [...]string{networkResolverPolicyMigrationQueuedPhase, networkResolverPolicyMigrationRunningPhase, networkResolverPolicyMigrationApprovalPhase}
	for index := range history {
		if history[index].State != expectedStates[index] || history[index].Phase != expectedPhases[index] || !history[index].OccurredAt.Equal(record.Operation.RequestedAt) {
			return OperationRecord{}, corruptNetworkResolverPolicyMigrationPlan(record.Operation.ID, fmt.Errorf("transition %d differs from the staged lifecycle", index+1))
		}
		if index > 0 && history[index].Sequence != history[index-1].Sequence+1 {
			return OperationRecord{}, corruptNetworkResolverPolicyMigrationPlan(record.Operation.ID, fmt.Errorf("operation revisions are not contiguous"))
		}
	}
	if record.Operation.State != domain.OperationRequiresApproval || record.Operation.Phase != networkResolverPolicyMigrationApprovalPhase || record.Revision != history[2].Sequence {
		return OperationRecord{}, corruptNetworkResolverPolicyMigrationPlan(record.Operation.ID, fmt.Errorf("operation does not match its approval transition"))
	}
	plan, revision, err := networkResolverPolicyMigrationPlanFromModel(row, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if revision != request.ExpectedNetworkRevision || plan.ExpectedSourceOwnershipFingerprint != request.SourceOwnership.Fingerprint || plan.TargetOwnership != request.SourceOwnership.Record || plan.Policy != request.Policy || plan.ReplacementPolicyFingerprint != request.ReplacementPolicyFingerprint {
		return OperationRecord{}, fmt.Errorf("network resolver policy migration plan for operation %q differs from the exact authority request", record.Operation.ID)
	}
	return record, nil
}

// findActiveNetworkResolverPolicyMigrationOperation returns the one active migration owner.
func findActiveNetworkResolverPolicyMigrationOperation(tx *gorm.DB) (OperationRecord, bool, error) {
	var rows []models.Operation
	if err := tx.
		Where("kind = ? AND project_id IS NULL AND state IN ?", domain.OperationKindNetworkResolverPolicyMigration, []domain.OperationState{
			domain.OperationQueued,
			domain.OperationRunning,
			domain.OperationRequiresApproval,
		}).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, false, err
	}
	if len(rows) == 0 {
		return OperationRecord{}, false, nil
	}
	if len(rows) != 1 {
		return OperationRecord{}, false, corruptNetworkResolverPolicyMigrationPlan("global", fmt.Errorf("multiple active network resolver policy migrations"))
	}
	record, err := operationRecordFromModel(rows[0])
	return record, err == nil, err
}

// insertQueuedNetworkResolverPolicyMigrationOperation appends the fixed initial operation edge.
func insertQueuedNetworkResolverPolicyMigrationOperation(tx *gorm.DB, operation domain.Operation) (OperationRecord, error) {
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return OperationRecord{}, err
	}
	row, err := operationModelFromDomain(operation, sequence)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return OperationRecord{}, err
	}
	transition, err := operationTransitionModelFromDomain(OperationTransition{
		OperationID: operation.ID,
		Ordinal:     1,
		State:       domain.OperationQueued,
		Phase:       operation.Phase,
		OccurredAt:  operation.RequestedAt,
		Sequence:    sequence,
	})
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&transition).Error; err != nil {
		return OperationRecord{}, err
	}
	return OperationRecord{
		Operation: operation,
		Revision:  sequence,
	}, nil
}

// networkResolverPolicyMigrationPlanModel preserves the complete legacy policy because helper tickets cannot carry ambient authority.
func networkResolverPolicyMigrationPlanModel(operation OperationRecord, request StageNetworkResolverPolicyMigrationRequest) (models.NetworkResolverPolicyMigrationPlan, error) {
	post := request.SourceOwnership.Record
	post.SchemaVersion = ownership.IdentitySchemaVersion
	post.NetworkPolicyFingerprint = ""
	postFingerprint, err := post.Fingerprint()
	if err != nil {
		return models.NetworkResolverPolicyMigrationPlan{}, err
	}
	return models.NetworkResolverPolicyMigrationPlan{
		Id:                             networkResolverPolicyMigrationPlanSingletonID,
		OperationId:                    string(operation.Operation.ID),
		OperationKind:                  string(operation.Operation.Kind),
		OperationState:                 string(operation.Operation.State),
		OperationPhase:                 operation.Operation.Phase,
		OperationRevision:              int(operation.Revision),
		NetworkStateId:                 networkStateSingletonID,
		NetworkRevision:                int(request.ExpectedNetworkRevision),
		SourceOwnershipSchemaVersion:   int(request.SourceOwnership.Record.SchemaVersion),
		SourceOwnershipFingerprint:     request.SourceOwnership.Fingerprint,
		SourceInstallationId:           request.SourceOwnership.Record.InstallationID,
		SourceOwnerIdentity:            request.SourceOwnership.Record.OwnerIdentity,
		SourceOwnershipGeneration:      int(request.SourceOwnership.Record.Generation),
		SourceLoopbackPoolPrefix:       request.SourceOwnership.Record.LoopbackPoolPrefix,
		SourceNetworkPolicyFingerprint: request.SourceOwnership.Record.NetworkPolicyFingerprint,
		SourceTicketVerifierKey:        request.SourceOwnership.Record.TicketVerifierKey,
		PostOwnershipFingerprint:       postFingerprint,
		ReplacementPolicyFingerprint:   request.ReplacementPolicyFingerprint,
		PolicySuffix:                   request.Policy.Suffix,
		PolicyAuthorityFingerprint:     request.Policy.AuthorityFingerprint,
		PolicyResolverMechanism:        string(request.Policy.Mechanisms.Resolver),
		PolicyLowPortsMechanism:        string(request.Policy.Mechanisms.LowPorts),
		PolicyTrustMechanism:           string(request.Policy.Mechanisms.Trust),
		PolicyDnsAdvertisedAddress:     request.Policy.DNS.Advertised.Addr().String(),
		PolicyDnsAdvertisedPort:        int(request.Policy.DNS.Advertised.Port()),
		PolicyDnsBindAddress:           request.Policy.DNS.Bind.Addr().String(),
		PolicyDnsBindPort:              int(request.Policy.DNS.Bind.Port()),
		PolicyHttpAdvertisedAddress:    request.Policy.HTTP.Advertised.Addr().String(),
		PolicyHttpAdvertisedPort:       int(request.Policy.HTTP.Advertised.Port()),
		PolicyHttpBindAddress:          request.Policy.HTTP.Bind.Addr().String(),
		PolicyHttpBindPort:             int(request.Policy.HTTP.Bind.Port()),
		PolicyHttpsAdvertisedAddress:   request.Policy.HTTPS.Advertised.Addr().String(),
		PolicyHttpsAdvertisedPort:      int(request.Policy.HTTPS.Advertised.Port()),
		PolicyHttpsBindAddress:         request.Policy.HTTPS.Bind.Addr().String(),
		PolicyHttpsBindPort:            int(request.Policy.HTTPS.Bind.Port()),
	}, nil
}
