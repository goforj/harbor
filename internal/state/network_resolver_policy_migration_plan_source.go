package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkResolverPolicyMigrationPlanSource resolves retirement capability authority from its durable singleton plan.
type NetworkResolverPolicyMigrationPlanSource struct {
	plans *models.NetworkResolverPolicyMigrationPlanRepo
}

// NewNetworkResolverPolicyMigrationPlanSource creates a strict source over the policy-migration plan table.
func NewNetworkResolverPolicyMigrationPlanSource(plans *models.NetworkResolverPolicyMigrationPlanRepo) *NetworkResolverPolicyMigrationPlanSource {
	if plans == nil {
		panic("state.NewNetworkResolverPolicyMigrationPlanSource requires a non-nil plan repository")
	}
	return &NetworkResolverPolicyMigrationPlanSource{plans: plans}
}

// Resolve returns a ticket plan only while its operation and every authority dependency remain unchanged.
func (source *NetworkResolverPolicyMigrationPlanSource) Resolve(ctx context.Context, request ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	builder, err := source.plans.WithContext(normalizeContext(ctx)).Builder()
	if err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	var result ticketissuer.ResolverPlan
	err = builder.Transaction(func(tx *gorm.DB) error {
		var operations []models.Operation
		if err := tx.Where("id = ?", string(request.OperationID)).Limit(2).Find(&operations).Error; err != nil {
			return err
		}
		if len(operations) != 1 {
			return corruptNetworkResolverPolicyMigrationPlan(request.OperationID, fmt.Errorf("operation has %d rows", len(operations)))
		}
		operation, err := operationRecordFromModel(operations[0])
		if err != nil {
			return err
		}
		var rows []models.NetworkResolverPolicyMigrationPlan
		if err := tx.Limit(2).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return corruptNetworkResolverPolicyMigrationPlan(request.OperationID, fmt.Errorf("singleton plan has %d rows", len(rows)))
		}
		plan, revision, err := networkResolverPolicyMigrationPlanFromModel(rows[0], operation)
		if err != nil {
			return err
		}
		if operation.Operation.State != domain.OperationRequiresApproval || operation.Operation.Phase != networkResolverPolicyMigrationApprovalPhase || operation.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || operation.Operation.ProjectID != "" || operation.Revision != plan.OperationRevision {
			return corruptNetworkResolverPolicyMigrationPlan(request.OperationID, fmt.Errorf("operation is not the exact approval checkpoint"))
		}
		networkRows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		network, initialized, err := networkRecordFromModels(networkRows)
		if err != nil {
			return err
		}
		if !initialized || network.Stage != NetworkStageResolver || network.Revision != revision || len(networkRows.Listeners) != 0 || len(networkRows.Endpoints) != 0 {
			return corruptNetworkResolverPolicyMigrationPlan(request.OperationID, fmt.Errorf("network authority drifted"))
		}
		projection, _, err := readMachineOwnershipProjectionInTransaction(tx)
		if err != nil {
			return err
		}
		source := plan.TargetOwnership
		if !projection.Exists || projection.Record != source || projection.Fingerprint != plan.ExpectedSourceOwnershipFingerprint {
			return corruptNetworkResolverPolicyMigrationPlan(request.OperationID, fmt.Errorf("machine ownership projection drifted"))
		}
		result = plan
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve network resolver policy migration plan %q: %w", request.OperationID, err)
	}
	return result, nil
}

// networkResolverPolicyMigrationPlanFromModel reconstructs the exact legacy policy and schema-two source authority.
func networkResolverPolicyMigrationPlanFromModel(row models.NetworkResolverPolicyMigrationPlan, operation OperationRecord) (ticketissuer.ResolverPlan, domain.Sequence, error) {
	if row.Id != networkResolverPolicyMigrationPlanSingletonID || row.OperationId != string(operation.Operation.ID) || row.OperationKind != string(domain.OperationKindNetworkResolverPolicyMigration) || row.OperationState != string(domain.OperationRequiresApproval) || row.OperationPhase != networkResolverPolicyMigrationApprovalPhase || row.NetworkStateId != networkStateSingletonID {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, fmt.Errorf("singleton ownership differs"))
	}
	if row.OperationRevision <= 0 || domain.Sequence(row.OperationRevision) > domain.MaximumSequence || domain.Sequence(row.OperationRevision) != operation.Revision {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, fmt.Errorf("operation revision differs from owner"))
	}
	if row.NetworkRevision <= 0 || domain.Sequence(row.NetworkRevision) > domain.MaximumSequence {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, fmt.Errorf("network revision is outside the durable sequence range"))
	}
	generation, err := positiveNetworkGeneration("resolver policy migration source ownership generation", row.SourceOwnershipGeneration)
	if err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, err)
	}
	source := ownership.Record{
		SchemaVersion:            uint32(row.SourceOwnershipSchemaVersion),
		InstallationID:           row.SourceInstallationId,
		OwnerIdentity:            row.SourceOwnerIdentity,
		Generation:               generation,
		LoopbackPoolPrefix:       row.SourceLoopbackPoolPrefix,
		NetworkPolicyFingerprint: row.SourceNetworkPolicyFingerprint,
		TicketVerifierKey:        row.SourceTicketVerifierKey,
	}
	if err := source.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, err)
	}
	sourceFingerprint, err := source.Fingerprint()
	if err != nil || sourceFingerprint != row.SourceOwnershipFingerprint {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, fmt.Errorf("source ownership fingerprint differs"))
	}
	post := source
	post.SchemaVersion = ownership.IdentitySchemaVersion
	post.NetworkPolicyFingerprint = ""
	postFingerprint, err := post.Fingerprint()
	if err != nil || postFingerprint != row.PostOwnershipFingerprint {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, fmt.Errorf("post-retirement ownership fingerprint differs"))
	}
	setupRow := models.NetworkResolverSetupPlan{
		PolicySuffix:                 row.PolicySuffix,
		PolicyAuthorityFingerprint:   row.PolicyAuthorityFingerprint,
		PolicyResolverMechanism:      row.PolicyResolverMechanism,
		PolicyLowPortsMechanism:      row.PolicyLowPortsMechanism,
		PolicyTrustMechanism:         row.PolicyTrustMechanism,
		PolicyDnsAdvertisedAddress:   row.PolicyDnsAdvertisedAddress,
		PolicyDnsAdvertisedPort:      row.PolicyDnsAdvertisedPort,
		PolicyDnsBindAddress:         row.PolicyDnsBindAddress,
		PolicyDnsBindPort:            row.PolicyDnsBindPort,
		PolicyHttpAdvertisedAddress:  row.PolicyHttpAdvertisedAddress,
		PolicyHttpAdvertisedPort:     row.PolicyHttpAdvertisedPort,
		PolicyHttpBindAddress:        row.PolicyHttpBindAddress,
		PolicyHttpBindPort:           row.PolicyHttpBindPort,
		PolicyHttpsAdvertisedAddress: row.PolicyHttpsAdvertisedAddress,
		PolicyHttpsAdvertisedPort:    row.PolicyHttpsAdvertisedPort,
		PolicyHttpsBindAddress:       row.PolicyHttpsBindAddress,
		PolicyHttpsBindPort:          row.PolicyHttpsBindPort,
	}
	policy, err := networkResolverSetupPolicyFromModel(setupRow)
	if err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, err)
	}
	plan := ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposePolicyMigration,
		Operation:                          operation.Operation,
		OperationRevision:                  operation.Revision,
		CheckpointRevision:                 0,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval,
		Mutation:                           helper.OperationRetireResolver,
		ExpectedSourceOwnershipFingerprint: row.SourceOwnershipFingerprint,
		ReplacementPolicyFingerprint:       row.ReplacementPolicyFingerprint,
		TargetOwnership:                    source,
		Policy:                             policy,
	}
	if err := plan.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverPolicyMigrationPlan(operation.Operation.ID, err)
	}
	return plan, domain.Sequence(row.NetworkRevision), nil
}

// corruptNetworkResolverPolicyMigrationPlan identifies malformed durable migration authority.
func corruptNetworkResolverPolicyMigrationPlan(operationID domain.OperationID, cause error) error {
	return corruptStateError("network resolver policy migration plan", string(operationID), cause)
}

var _ ticketissuer.ResolverPlanSource = (*NetworkResolverPolicyMigrationPlanSource)(nil)
