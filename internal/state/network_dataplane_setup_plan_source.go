package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/trust/certificates"
	"gorm.io/gorm"
)

// NetworkDataPlaneSetupCertificateRootSource supplies the established public root for trust approval.
type NetworkDataPlaneSetupCertificateRootSource interface {
	// PublicRoot returns the current public certificate authority retained by the daemon.
	PublicRoot() (certificates.Root, error)
}

// NetworkDataPlaneTrustPlanSource resolves trust authority from the current durable resolver-stage state.
type NetworkDataPlaneTrustPlanSource struct {
	network  *models.NetworkStateRepo
	roots    NetworkDataPlaneSetupCertificateRootSource
	platform networkplan.Platform
}

// NetworkDataPlaneTrustPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.TrustPlanSource = (*NetworkDataPlaneTrustPlanSource)(nil)

// NewNetworkDataPlaneTrustPlanSource creates a strict durable source for trust-approval capabilities.
func NewNetworkDataPlaneTrustPlanSource(network *models.NetworkStateRepo, roots NetworkDataPlaneSetupCertificateRootSource, platform networkplan.Platform) *NetworkDataPlaneTrustPlanSource {
	if network == nil || roots == nil {
		panic("state.NewNetworkDataPlaneTrustPlanSource requires a network repository and certificate root source")
	}
	if !supportedNetworkDataPlaneSetupPlatform(platform) {
		panic("state.NewNetworkDataPlaneTrustPlanSource requires a supported host network platform")
	}
	return &NetworkDataPlaneTrustPlanSource{network: network, roots: roots, platform: platform}
}

// Resolve returns authority only while the selected operation is awaiting trust approval.
func (source *NetworkDataPlaneTrustPlanSource) Resolve(ctx context.Context, request ticketissuer.TrustRequest) (ticketissuer.TrustPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve network data-plane trust plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.TrustPlan{}, err
	}
	root, err := source.roots.PublicRoot()
	if err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve network data-plane trust root: %w", err)
	}
	return source.resolve(ctx, request.OperationID, cloneNetworkDataPlaneSetupRoot(root))
}

// NetworkDataPlaneLowPortPlanSource resolves low-port authority from one committed trusted-ingress receipt.
type NetworkDataPlaneLowPortPlanSource struct {
	network *models.NetworkStateRepo
}

// NetworkDataPlaneLowPortPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.LowPortPlanSource = (*NetworkDataPlaneLowPortPlanSource)(nil)

// NewNetworkDataPlaneLowPortPlanSource creates a strict durable source for low-port-approval capabilities.
func NewNetworkDataPlaneLowPortPlanSource(network *models.NetworkStateRepo) *NetworkDataPlaneLowPortPlanSource {
	if network == nil {
		panic("state.NewNetworkDataPlaneLowPortPlanSource requires a network repository")
	}
	return &NetworkDataPlaneLowPortPlanSource{network: network}
}

// Resolve returns authority only while the selected operation is awaiting low-port approval.
func (source *NetworkDataPlaneLowPortPlanSource) Resolve(ctx context.Context, request ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve network data-plane low-port plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.LowPortPlan{}, err
	}
	connection, err := source.network.WithContext(ctx).Builder()
	if err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("open network data-plane low-port plan: %w", err)
	}
	var result ticketissuer.LowPortPlan
	err = connection.Transaction(func(tx *gorm.DB) error {
		plan, err := resolveNetworkDataPlaneSetupApproval(tx, request.OperationID, networkDataPlaneSetupLowPortApprovalPhase)
		if err != nil {
			return err
		}
		native, err := lowport.NewRequest(plan.Authority.Projection.ConfirmedOwnership.Record, plan.Authority.Policy)
		if err != nil {
			return corruptNetworkDataPlaneSetupPlan(request.OperationID, fmt.Errorf("derive low-port request: %w", err))
		}
		result = ticketissuer.LowPortPlan{Operation: plan.Operation.Operation, OperationRevision: plan.Operation.Revision, Mutation: helper.OperationEnsureLowPorts, TargetOwnership: plan.Authority.Projection.ConfirmedOwnership.Record, Policy: plan.Authority.Policy, NativeRequest: native}
		if err := result.Validate(); err != nil {
			return corruptNetworkDataPlaneSetupPlan(request.OperationID, err)
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve network data-plane low-port plan %q: %w", request.OperationID, err)
	}
	return result, nil
}

// resolve rereads all durable trust dependencies together after obtaining the established root.
func (source *NetworkDataPlaneTrustPlanSource) resolve(ctx context.Context, operationID domain.OperationID, root certificates.Root) (ticketissuer.TrustPlan, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.TrustPlan{}, err
	}
	connection, err := source.network.WithContext(ctx).Builder()
	if err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("open network data-plane trust plan: %w", err)
	}
	var result ticketissuer.TrustPlan
	err = connection.Transaction(func(tx *gorm.DB) error {
		operation, err := readNetworkDataPlaneSetupOperation(tx, operationID)
		if err != nil {
			return err
		}
		if err := requireExactActiveNetworkDataPlaneSetupOperation(tx, operation); err != nil {
			return err
		}
		if operation.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || operation.Operation.ProjectID != "" || operation.Operation.State != domain.OperationRequiresApproval || operation.Operation.Phase != networkDataPlaneSetupApprovalPhase {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation is not awaiting trust approval"))
		}
		if err := requireNetworkDataPlaneSetupLifecycleHistory(tx, operation); err != nil {
			return err
		}
		if _, found, err := readOptionalNetworkDataPlaneSetupPlan(tx, operationID); err != nil {
			return err
		} else if found {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("trust approval retains a post-trust receipt"))
		}
		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		network, initialized, err := networkRecordFromModels(rows)
		if err != nil {
			return corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		if !initialized || network.Stage != NetworkStageResolver {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("current network stage is not resolver"))
		}
		policy, err := networkplan.Build(networkplan.Request{Platform: source.platform, InstallationID: network.Ownership.InstallationID, Pool: network.Pool, AuthorityFingerprint: root.Fingerprint})
		if err != nil {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("build canonical network policy: %w", err))
		}
		fingerprint, err := policy.Fingerprint()
		if err != nil {
			return corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		projection, err := resolveNetworkDataPlaneSetupProjection(tx, policy, fingerprint)
		if err != nil {
			return corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		if projection.Stage != NetworkStageResolver {
			return corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("current network stage is %q, expected resolver", projection.Stage))
		}
		result = ticketissuer.TrustPlan{OperationID: operation.Operation.ID, OperationRevision: operation.Revision, OperationState: operation.Operation.State, Mutation: helper.OperationEnsureTrust, TargetOwnership: projection.ConfirmedOwnership.Record, Policy: policy, Root: root}
		if err := result.Validate(); err != nil {
			return corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve network data-plane trust plan %q: %w", operationID, err)
	}
	return result, nil
}

// resolveNetworkDataPlaneSetupApproval validates the exact selected approval boundary and all current durable dependencies.
func resolveNetworkDataPlaneSetupApproval(tx *gorm.DB, operationID domain.OperationID, phase string) (networkDataPlaneSetupPlan, error) {
	operation, err := readNetworkDataPlaneSetupOperation(tx, operationID)
	if err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	if err := requireExactActiveNetworkDataPlaneSetupOperation(tx, operation); err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || operation.Operation.ProjectID != "" {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation is not a global network data-plane setup"))
	}
	if operation.Operation.State != domain.OperationRequiresApproval || operation.Operation.Phase != phase {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation is %q/%q, expected requires_approval/%q", operation.Operation.State, operation.Operation.Phase, phase))
	}
	if operation.Revision == 0 || operation.Revision > domain.MaximumSequence {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation revision is outside the durable sequence range"))
	}
	if err := requireNetworkDataPlaneSetupLifecycleHistory(tx, operation); err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	row, found, err := readOptionalNetworkDataPlaneSetupPlan(tx, operationID)
	if err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	if !found {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("committed authority receipt is missing"))
	}
	plan, err := networkDataPlaneSetupPlanFromRow(row, operation)
	if err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	if plan.Phase != networkDataPlaneSetupPlanLowPortApproval {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("receipt phase is %q, expected low-port approval", plan.Phase))
	}
	fingerprint, err := plan.Authority.Policy.Fingerprint()
	if err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	current, err := resolveNetworkDataPlaneSetupProjection(tx, plan.Authority.Policy, fingerprint)
	if err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if current.Stage != NetworkStageResolver || !sameNetworkDataPlaneSetupProjection(current, plan.Authority.Projection) {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("current resolver authority differs from committed receipt"))
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return networkDataPlaneSetupPlan{}, err
	}
	return plan, nil
}

// supportedNetworkDataPlaneSetupPlatform admits only profiles that can build a canonical host policy.
func supportedNetworkDataPlaneSetupPlatform(platform networkplan.Platform) bool {
	switch platform {
	case networkplan.PlatformMacOS, networkplan.PlatformUbuntu2404, networkplan.PlatformWindows11:
		return true
	default:
		return false
	}
}

// cloneNetworkDataPlaneSetupRoot prevents a caller receiving a plan from mutating root-source memory.
func cloneNetworkDataPlaneSetupRoot(root certificates.Root) certificates.Root {
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	return root
}

// requireExactActiveNetworkDataPlaneSetupOperation rejects a selected operation that is no longer the global owner.
func requireExactActiveNetworkDataPlaneSetupOperation(tx *gorm.DB, operation OperationRecord) error {
	active, found, err := findActiveNetworkDataPlaneSetupOperation(tx)
	if err != nil {
		return err
	}
	if !found || active.Operation.ID != operation.Operation.ID || active.Revision != operation.Revision {
		return corruptNetworkDataPlaneSetupPlan(operation.Operation.ID, fmt.Errorf("selected operation is not the current global setup operation"))
	}
	return nil
}

// readNetworkDataPlaneSetupOperation reads exactly one requested operation without trusting primary-key enforcement.
func readNetworkDataPlaneSetupOperation(tx *gorm.DB, operationID domain.OperationID) (OperationRecord, error) {
	var rows []models.Operation
	if err := tx.Where("id = ?", string(operationID)).Order("revision ASC").Limit(2).Find(&rows).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("read network data-plane setup operation: %w", err)
	}
	if len(rows) != 1 {
		return OperationRecord{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation has %d rows, expected 1", len(rows)))
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	return record, nil
}

// NetworkDataPlaneSetupActivePhase classifies the recoverable lifecycle boundary of an active setup operation.
type NetworkDataPlaneSetupActivePhase string

const (
	// NetworkDataPlaneSetupPhaseTrustApproval denotes a pending trust capability.
	NetworkDataPlaneSetupPhaseTrustApproval NetworkDataPlaneSetupActivePhase = "trust_approval"
	// NetworkDataPlaneSetupPhaseLowPortApproval denotes a pending low-port capability.
	NetworkDataPlaneSetupPhaseLowPortApproval NetworkDataPlaneSetupActivePhase = "low_port_approval"
	// NetworkDataPlaneSetupPhaseActivation denotes a durably staged activation that startup recovery may resume.
	NetworkDataPlaneSetupPhaseActivation NetworkDataPlaneSetupActivePhase = "activation"
)

// NetworkDataPlaneSetupActiveOperation is the sole global setup operation together with its typed recovery boundary.
type NetworkDataPlaneSetupActiveOperation struct {
	// Operation is the exact durable global operation and revision selected by the query.
	Operation OperationRecord
	// Phase is the typed lifecycle boundary that determines whether recovery may activate it.
	Phase NetworkDataPlaneSetupActivePhase
}

// ActiveNetworkDataPlaneSetupOperation reads the sole active global setup operation for startup recovery.
// Approval phases are returned so callers can deliberately restrict automatic recovery to activation.
func (journal *OperationJournal) ActiveNetworkDataPlaneSetupOperation(ctx context.Context) (NetworkDataPlaneSetupActiveOperation, bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkDataPlaneSetupActiveOperation{}, false, err
	}
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return NetworkDataPlaneSetupActiveOperation{}, false, fmt.Errorf("open active network data-plane setup operation: %w", err)
	}
	var result NetworkDataPlaneSetupActiveOperation
	found := false
	err = builder.Transaction(func(tx *gorm.DB) error {
		record, active, err := findActiveNetworkDataPlaneSetupOperation(tx)
		if err != nil || !active {
			return err
		}
		if err := requireNetworkDataPlaneSetupLifecycleHistory(tx, record); err != nil {
			return err
		}
		phase, err := networkDataPlaneSetupActivePhase(record)
		if err != nil {
			return err
		}
		result = NetworkDataPlaneSetupActiveOperation{Operation: record, Phase: phase}
		found = true
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NetworkDataPlaneSetupActiveOperation{}, false, fmt.Errorf("read active network data-plane setup operation: %w", err)
	}
	return result, found, nil
}

// networkDataPlaneSetupActivePhase rejects in-progress and malformed boundaries because recovery has no safe action for them.
func networkDataPlaneSetupActivePhase(record OperationRecord) (NetworkDataPlaneSetupActivePhase, error) {
	if record.Operation.State != domain.OperationRequiresApproval && record.Operation.State != domain.OperationRunning {
		return "", corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("operation state %q is not active", record.Operation.State))
	}
	switch record.Operation.Phase {
	case networkDataPlaneSetupApprovalPhase:
		return NetworkDataPlaneSetupPhaseTrustApproval, nil
	case networkDataPlaneSetupLowPortApprovalPhase:
		return NetworkDataPlaneSetupPhaseLowPortApproval, nil
	case networkDataPlaneSetupActivationPhase:
		return NetworkDataPlaneSetupPhaseActivation, nil
	default:
		return "", corruptNetworkDataPlaneSetupOperation(record.Operation.ID, fmt.Errorf("operation phase %q is not recoverable", record.Operation.Phase))
	}
}
