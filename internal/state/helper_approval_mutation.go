package state

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// HelperApprovalPlanIntent describes one exact lease effect that must receive interactive approval.
type HelperApprovalPlanIntent struct {
	Mutation     helper.Operation
	Lease        identity.Lease
	LeaseState   ticketissuer.LeaseState
	Requirements []hostconflict.SocketRequirement
}

// Validate rejects helper authority that is unbounded, noncanonical, or inconsistent with its lease lifecycle.
func (intent HelperApprovalPlanIntent) Validate() error {
	if intent.Mutation != helper.OperationEnsureLoopbackIdentity && intent.Mutation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("helper approval mutation %q is not allowlisted", intent.Mutation)
	}
	if err := intent.Lease.Validate(); err != nil {
		return err
	}
	if intent.Lease.Address != intent.Lease.Address.Unmap() {
		return fmt.Errorf("helper approval lease address must use canonical IPv4 form")
	}
	switch intent.LeaseState {
	case ticketissuer.LeasePending:
		if intent.Mutation != helper.OperationEnsureLoopbackIdentity {
			return fmt.Errorf("pending helper approval lease requires an ensure mutation")
		}
	case ticketissuer.LeaseActive:
	default:
		return fmt.Errorf("helper approval lease state %q is unsupported", intent.LeaseState)
	}
	if intent.Requirements == nil {
		return fmt.Errorf("helper approval socket requirements must be initialized")
	}
	request, err := hostconflict.NewPreAssignmentRequest(intent.Lease.Address, intent.Requirements)
	if err != nil {
		return err
	}
	if !slices.Equal(request.Requirements(), intent.Requirements) {
		return fmt.Errorf("helper approval socket requirements must be unique canonical order")
	}
	return nil
}

// HelperApprovalPlanRecord is one persisted intent bound to the operation revision that owns it.
type HelperApprovalPlanRecord struct {
	OperationID       domain.OperationID
	OperationRevision domain.Sequence
	Intent            HelperApprovalPlanIntent
	leaseID           null.Int
}

// Validate rejects a plan readback that cannot remain bound to one exact operation revision.
func (record HelperApprovalPlanRecord) Validate() error {
	if err := record.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("helper approval operation revision", record.OperationRevision, false); err != nil {
		return err
	}
	return record.Intent.Validate()
}

// StageProjectNetworkReleaseApprovalRequest identifies one staged unregister release at its exact operation revision.
type StageProjectNetworkReleaseApprovalRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	Phase                     string
	At                        time.Time
}

// Validate rejects a staging request before it enters serialized storage authority.
func (request StageProjectNetworkReleaseApprovalRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Phase) == "" {
		return fmt.Errorf("helper approval phase must not be empty")
	}
	if err := validateStoredTime("helper approval staging time", request.At); err != nil {
		return err
	}
	return nil
}

// ProjectNetworkReleaseApprovalResult couples the approval-state unregister operation with its exact durable release plans.
type ProjectNetworkReleaseApprovalResult struct {
	Operation OperationRecord
	Plans     []HelperApprovalPlanRecord
}

// Validate rejects a result whose plans do not all belong to its approval-state operation revision.
func (result ProjectNetworkReleaseApprovalResult) Validate() error {
	if result.Operation.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("helper approval operation state is %q, want %q", result.Operation.Operation.State, domain.OperationRequiresApproval)
	}
	if len(result.Plans) == 0 {
		return fmt.Errorf("helper approval result requires at least one plan")
	}
	seenAddresses := make(map[string]struct{}, len(result.Plans))
	for index, plan := range result.Plans {
		if err := plan.Validate(); err != nil {
			return err
		}
		if plan.OperationID != result.Operation.Operation.ID || plan.OperationRevision != result.Operation.Revision {
			return fmt.Errorf("helper approval plan does not match its operation revision")
		}
		if plan.Intent.Mutation != helper.OperationReleaseLoopbackIdentity || plan.Intent.LeaseState != ticketissuer.LeaseActive {
			return fmt.Errorf("project network release approval contains a non-release plan")
		}
		if index > 0 && !helperApprovalIntentLess(result.Plans[index-1].Intent, plan.Intent) {
			return fmt.Errorf("helper approval plans are not in unique canonical order")
		}
		address := plan.Intent.Lease.Address.String()
		if _, duplicate := seenAddresses[address]; duplicate {
			return fmt.Errorf("helper approval result address %s is duplicated", address)
		}
		seenAddresses[address] = struct{}{}
	}
	return nil
}

// ResumeProjectNetworkReleaseApprovalRequest removes exact release plans before resuming their unregister operation.
type ResumeProjectNetworkReleaseApprovalRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	Phase                     string
	At                        time.Time
}

// Validate rejects malformed resumption metadata before release authority can be retired.
func (request ResumeProjectNetworkReleaseApprovalRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Phase) == "" {
		return fmt.Errorf("helper approval retirement phase must not be empty")
	}
	if err := validateStoredTime("helper approval retirement time", request.At); err != nil {
		return err
	}
	return nil
}

// StageProjectNetworkReleaseApproval derives and stages every exact active lease owned by one unregister boundary.
func (store *Store) StageProjectNetworkReleaseApproval(
	ctx context.Context,
	request StageProjectNetworkReleaseApprovalRequest,
) (ProjectNetworkReleaseApprovalResult, error) {
	if err := request.Validate(); err != nil {
		return ProjectNetworkReleaseApprovalResult{}, err
	}
	request = cloneStageProjectNetworkReleaseApprovalRequest(request)
	ctx = normalizeContext(ctx)

	var result ProjectNetworkReleaseApprovalResult
	err := store.mutations.mutate(ctx, "project network release approval staging", func(tx *gorm.DB) error {
		current, err := readExpectedHelperApprovalOperation(tx, request.OperationID, request.ExpectedOperationRevision)
		if err != nil {
			return err
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("helper approval plans require a running operation, found %q", current.Operation.State)
		}
		if current.Operation.ProjectID == "" {
			return fmt.Errorf("helper approval plans require a project-scoped operation")
		}
		intents, err := projectNetworkReleaseApprovalIntentsInTransaction(tx, current)
		if err != nil {
			return err
		}

		prepared, err := prepareHelperApprovalPlans(tx, current, intents)
		if err != nil {
			return err
		}
		approval, err := transitionOperationInTransaction(
			tx,
			request.OperationID,
			request.ExpectedOperationRevision,
			domain.OperationRequiresApproval,
			request.Phase,
			request.At,
			nil,
		)
		if err != nil {
			return err
		}
		if err := insertHelperApprovalPlans(tx, approval, prepared); err != nil {
			return err
		}
		plans, err := readHelperApprovalPlanRecordsInTransaction(tx, approval.Operation.ID, approval.Revision)
		if err != nil {
			return err
		}
		if !sameHelperApprovalPlanReadback(plans, approval, prepared) {
			return corruptStateError("helper approval plan", string(approval.Operation.ID), fmt.Errorf("staged plan readback differs from the exact request"))
		}
		result = ProjectNetworkReleaseApprovalResult{Operation: approval, Plans: plans}
		return result.Validate()
	})
	if err != nil {
		return ProjectNetworkReleaseApprovalResult{}, fmt.Errorf("stage project network release approval: %w", err)
	}
	return result, nil
}

// ResumeProjectNetworkReleaseApproval retires only the complete durable release set before resuming its unregister operation.
func (store *Store) ResumeProjectNetworkReleaseApproval(
	ctx context.Context,
	request ResumeProjectNetworkReleaseApprovalRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, err
	}
	request = cloneResumeProjectNetworkReleaseApprovalRequest(request)
	ctx = normalizeContext(ctx)

	var result OperationRecord
	err := store.mutations.mutate(ctx, "project network release approval resumption", func(tx *gorm.DB) error {
		current, err := readExpectedHelperApprovalOperation(tx, request.OperationID, request.ExpectedOperationRevision)
		if err != nil {
			return err
		}
		if current.Operation.State != domain.OperationRequiresApproval {
			return fmt.Errorf("helper approval plans can retire only from requires-approval state, found %q", current.Operation.State)
		}
		plans, err := readHelperApprovalPlanRecordsInTransaction(tx, current.Operation.ID, current.Revision)
		if err != nil {
			return err
		}
		if len(plans) == 0 {
			return fmt.Errorf("requires-approval operation %q has no helper approval plans", current.Operation.ID)
		}
		intents, err := projectNetworkReleaseApprovalIntentsInTransaction(tx, current)
		if err != nil {
			return err
		}
		prepared, err := prepareHelperApprovalPlans(tx, current, intents)
		if err != nil {
			return err
		}
		if !sameHelperApprovalPlanReadback(plans, current, prepared) {
			return corruptStateError(
				"helper approval plan",
				string(current.Operation.ID),
				fmt.Errorf("durable plans differ from the exact project release set"),
			)
		}
		planIDs, err := helperApprovalPlanIDsInTransaction(tx, current.Operation.ID, current.Revision)
		if err != nil {
			return err
		}
		deleted := tx.Where(
			"operation_id = ? AND operation_revision = ?",
			string(current.Operation.ID),
			int(current.Revision),
		).Delete(&models.HelperApprovalPlan{})
		if deleted.Error != nil {
			return fmt.Errorf("delete helper approval plans: %w", deleted.Error)
		}
		if deleted.RowsAffected != int64(len(plans)) {
			return corruptStateError(
				"helper approval plan",
				string(current.Operation.ID),
				fmt.Errorf("delete affected %d rows, expected %d", deleted.RowsAffected, len(plans)),
			)
		}
		if err := requireRetiredHelperApprovalPlans(tx, current, planIDs); err != nil {
			return err
		}

		transitioned, err := transitionOperationInTransaction(
			tx,
			request.OperationID,
			request.ExpectedOperationRevision,
			domain.OperationRunning,
			request.Phase,
			request.At,
			nil,
		)
		if err != nil {
			return err
		}
		result = transitioned
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("resume project network release approval: %w", err)
	}
	return result, nil
}

// preparedHelperApprovalPlan couples a canonical intent to its optional exact active lease row.
type preparedHelperApprovalPlan struct {
	Intent  HelperApprovalPlanIntent
	LeaseID null.Int
}

// cloneStageProjectNetworkReleaseApprovalRequest removes process-local time metadata before waiting for writer authority.
func cloneStageProjectNetworkReleaseApprovalRequest(
	request StageProjectNetworkReleaseApprovalRequest,
) StageProjectNetworkReleaseApprovalRequest {
	request.At = request.At.UTC().Round(0)
	return request
}

// cloneResumeProjectNetworkReleaseApprovalRequest removes process-local time metadata before waiting for writer authority.
func cloneResumeProjectNetworkReleaseApprovalRequest(
	request ResumeProjectNetworkReleaseApprovalRequest,
) ResumeProjectNetworkReleaseApprovalRequest {
	request.At = request.At.UTC().Round(0)
	return request
}

// compareHelperApprovalIntents establishes project, primary-before-secondary, and address ordering.
func compareHelperApprovalIntents(left HelperApprovalPlanIntent, right HelperApprovalPlanIntent) int {
	if helperApprovalIntentLess(left, right) {
		return -1
	}
	if helperApprovalIntentLess(right, left) {
		return 1
	}
	return 0
}

// helperApprovalIntentLess orders logical lease identities before their immutable address evidence.
func helperApprovalIntentLess(left HelperApprovalPlanIntent, right HelperApprovalPlanIntent) bool {
	if left.Lease.Key.ProjectID != right.Lease.Key.ProjectID {
		return left.Lease.Key.ProjectID < right.Lease.Key.ProjectID
	}
	if left.Lease.Key.Kind() != right.Lease.Key.Kind() {
		return left.Lease.Key.Kind() == identity.LeaseKindPrimary
	}
	if left.Lease.Key.SecondaryID != right.Lease.Key.SecondaryID {
		return left.Lease.Key.SecondaryID < right.Lease.Key.SecondaryID
	}
	return left.Lease.Address.Compare(right.Lease.Address) < 0
}

// readExpectedHelperApprovalOperation loads one exact optimistic operation owner without mutating it.
func readExpectedHelperApprovalOperation(
	tx *gorm.DB,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
) (OperationRecord, error) {
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
	if record.Revision != expectedRevision {
		return OperationRecord{}, &StaleRevisionError{
			OperationID: operationID,
			Expected:    expectedRevision,
			Actual:      record.Revision,
		}
	}
	return record, nil
}

// projectNetworkReleaseApprovalIntentsInTransaction derives the sole helper effects authorized by one durable unregister marker.
func projectNetworkReleaseApprovalIntentsInTransaction(
	tx *gorm.DB,
	operation OperationRecord,
) ([]HelperApprovalPlanIntent, error) {
	if operation.Operation.Kind != domain.OperationKindProjectUnregister {
		return nil, fmt.Errorf(
			"project network release approval requires operation kind %q, found %q",
			domain.OperationKindProjectUnregister,
			operation.Operation.Kind,
		)
	}
	present, err := inspectNetworkSchema(tx)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("network persistence schema is not installed")
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return nil, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return nil, err
	}
	if !initialized {
		return nil, &NetworkNotInitializedError{}
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return nil, err
	}
	owners, err := validateProjectNetworkReleaseOwners(tx, highWater, rows.Releases)
	if err != nil {
		return nil, err
	}
	marker, exists := networkProjectReleaseRowByOperation(rows.Releases, operation.Operation.ID)
	if !exists {
		return nil, &ProjectNetworkReleaseNotFoundError{
			ProjectID:   operation.Operation.ProjectID,
			OperationID: operation.Operation.ID,
		}
	}
	if marker.State != string(ProjectNetworkReleaseReleasing) {
		return nil, projectNetworkReleaseConflict(
			operation.Operation.ProjectID,
			operation.Operation.ID,
			"release state",
		)
	}
	owner := owners[operation.Operation.ID]
	if owner.operation.Revision != operation.Revision || owner.operation.Operation.State != operation.Operation.State {
		return nil, corruptStateError(
			"network project release",
			string(operation.Operation.ID),
			fmt.Errorf("operation owner differs from the approval revision"),
		)
	}
	release, err := projectNetworkReleaseRecordFromRows(rows, network, marker)
	if err != nil {
		return nil, err
	}
	intents := make([]HelperApprovalPlanIntent, 0, len(release.ActiveLeases))
	for _, active := range release.ActiveLeases {
		intent := HelperApprovalPlanIntent{
			Mutation:     helper.OperationReleaseLoopbackIdentity,
			Lease:        active.Lease,
			LeaseState:   ticketissuer.LeaseActive,
			Requirements: []hostconflict.SocketRequirement{},
		}
		if err := intent.Validate(); err != nil {
			return nil, corruptStateError(
				"network project release",
				string(operation.Operation.ID),
				err,
			)
		}
		intents = append(intents, intent)
	}
	if len(intents) == 0 {
		return nil, corruptStateError(
			"network project release",
			string(operation.Operation.ID),
			fmt.Errorf("release set has no active leases"),
		)
	}
	slices.SortFunc(intents, compareHelperApprovalIntents)
	return intents, nil
}

// prepareHelperApprovalPlans validates complete network authority and resolves exact active lease row identities.
func prepareHelperApprovalPlans(
	tx *gorm.DB,
	operation OperationRecord,
	intents []HelperApprovalPlanIntent,
) ([]preparedHelperApprovalPlan, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("network persistence schema is not installed")
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return nil, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return nil, err
	}
	if !initialized {
		return nil, fmt.Errorf("network state is not initialized")
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return nil, err
	}
	if err := requireHelperApprovalProject(rows.Projects, operation.Operation.ProjectID); err != nil {
		return nil, err
	}

	activeByKey := make(map[identity.LeaseKey]models.LoopbackAddressLease)
	occupiedAddresses := make(map[string]models.LoopbackAddressLease, len(rows.Leases))
	for _, row := range rows.Leases {
		occupiedAddresses[row.Address] = row
		if row.State != "leased" {
			continue
		}
		key, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
		if err != nil {
			return nil, corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
		}
		activeByKey[key] = row
	}

	prepared := make([]preparedHelperApprovalPlan, 0, len(intents))
	for _, intent := range intents {
		if intent.Lease.Key.ProjectID != operation.Operation.ProjectID {
			return nil, fmt.Errorf(
				"helper approval lease project %q does not match operation project %q",
				intent.Lease.Key.ProjectID,
				operation.Operation.ProjectID,
			)
		}
		if err := validateHelperApprovalLeaseOwnership(
			intent.LeaseState,
			intent.Lease.Ownership,
			network.Ownership,
		); err != nil {
			return nil, err
		}
		if !network.Pool.Contains(intent.Lease.Address) {
			return nil, fmt.Errorf("helper approval address %s is not a network pool candidate", intent.Lease.Address)
		}

		plan := preparedHelperApprovalPlan{Intent: intent}
		active, activeKeyExists := activeByKey[intent.Lease.Key]
		occupied, addressOccupied := occupiedAddresses[intent.Lease.Address.String()]
		switch intent.LeaseState {
		case ticketissuer.LeasePending:
			if activeKeyExists {
				return nil, fmt.Errorf("pending helper approval lease key %q/%q is already active", intent.Lease.Key.ProjectID, intent.Lease.Key.SecondaryID)
			}
			if addressOccupied {
				return nil, fmt.Errorf("pending helper approval address %s is occupied by durable lease %d", intent.Lease.Address, occupied.Id)
			}
		case ticketissuer.LeaseActive:
			if !activeKeyExists {
				return nil, fmt.Errorf("active helper approval lease key %q/%q was not found", intent.Lease.Key.ProjectID, intent.Lease.Key.SecondaryID)
			}
			activeLease, err := helperApprovalLeaseFromActiveRow(active)
			if err != nil {
				return nil, err
			}
			if activeLease != intent.Lease {
				return nil, fmt.Errorf("active helper approval lease differs from the exact durable lease")
			}
			if !addressOccupied || occupied.Id != active.Id {
				return nil, corruptStateError("loopback address lease", durableKey(active.Address, active.Id), fmt.Errorf("active address ownership is ambiguous"))
			}
			plan.LeaseID = null.IntFrom(int64(active.Id))
		default:
			return nil, fmt.Errorf("helper approval lease state %q is unsupported", intent.LeaseState)
		}
		prepared = append(prepared, plan)
	}
	return prepared, nil
}

// validateHelperApprovalLeaseOwnership distinguishes historical active effects from new current-generation assignments.
func validateHelperApprovalLeaseOwnership(
	leaseState ticketissuer.LeaseState,
	leaseOwnership identity.Ownership,
	current identity.Ownership,
) error {
	if leaseOwnership.InstallationID != current.InstallationID {
		return fmt.Errorf("helper approval lease belongs to a different Harbor installation")
	}
	switch leaseState {
	case ticketissuer.LeasePending:
		if leaseOwnership.Generation != current.Generation {
			return fmt.Errorf("pending helper approval lease does not use the current ownership generation")
		}
	case ticketissuer.LeaseActive:
		if leaseOwnership.Generation > current.Generation {
			return fmt.Errorf("active helper approval lease ownership generation is newer than the current owner")
		}
	default:
		return fmt.Errorf("helper approval lease state %q is unsupported", leaseState)
	}
	return nil
}

// requireHelperApprovalProject proves the project-scoped operation still has exactly one durable aggregate owner.
func requireHelperApprovalProject(rows []models.Project, projectID domain.ProjectID) error {
	matches := 0
	for _, row := range rows {
		if row.ProjectId == string(projectID) {
			matches++
		}
	}
	if matches != 1 {
		return corruptStateError("project", string(projectID), fmt.Errorf("helper approval owner has %d rows, expected 1", matches))
	}
	return nil
}

// helperApprovalLeaseFromActiveRow reconstructs the exact semantic authority of one validated active row.
func helperApprovalLeaseFromActiveRow(row models.LoopbackAddressLease) (identity.Lease, error) {
	key := durableKey(row.Address, row.Id)
	if row.State != "leased" || !row.ProjectId.Valid || row.ProjectId.String != row.SourceProjectId {
		return identity.Lease{}, corruptStateError("loopback address lease", key, fmt.Errorf("row is not an exact active lease"))
	}
	leaseKey, err := networkLeaseKeyFromModel(domain.ProjectID(row.SourceProjectId), row.Kind, row.SecondaryId)
	if err != nil {
		return identity.Lease{}, corruptStateError("loopback address lease", key, err)
	}
	address, err := parseCanonicalNetworkAddress("active helper approval lease address", row.Address)
	if err != nil {
		return identity.Lease{}, corruptStateError("loopback address lease", key, err)
	}
	generation, err := positiveNetworkGeneration("active helper approval lease ownership generation", row.OwnershipGeneration)
	if err != nil {
		return identity.Lease{}, corruptStateError("loopback address lease", key, err)
	}
	ownership, err := identity.NewOwnership(identity.InstallationID(row.OwnershipInstallationId), generation)
	if err != nil {
		return identity.Lease{}, corruptStateError("loopback address lease", key, err)
	}
	lease := identity.Lease{Key: leaseKey, Address: address, Ownership: ownership}
	if err := lease.Validate(); err != nil {
		return identity.Lease{}, corruptStateError("loopback address lease", key, err)
	}
	return lease, nil
}

// insertHelperApprovalPlans persists each plan and its canonical requirements under the new operation revision.
func insertHelperApprovalPlans(
	tx *gorm.DB,
	operation OperationRecord,
	plans []preparedHelperApprovalPlan,
) error {
	operationRevision, err := sequenceToModelInt("helper approval operation revision", operation.Revision, false)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		ownershipGeneration, err := unsignedToModelInt(
			"helper approval ownership generation",
			plan.Intent.Lease.Ownership.Generation,
			false,
		)
		if err != nil {
			return err
		}
		row := models.HelperApprovalPlan{
			OperationId:             string(operation.Operation.ID),
			OperationRevision:       operationRevision,
			NetworkStateId:          networkStateSingletonID,
			Mutation:                string(plan.Intent.Mutation),
			LeaseState:              string(plan.Intent.LeaseState),
			ProjectId:               string(plan.Intent.Lease.Key.ProjectID),
			Kind:                    string(plan.Intent.Lease.Key.Kind()),
			SecondaryId:             plan.Intent.Lease.Key.SecondaryID,
			Address:                 plan.Intent.Lease.Address.String(),
			OwnershipInstallationId: string(plan.Intent.Lease.Ownership.InstallationID),
			OwnershipGeneration:     ownershipGeneration,
			LoopbackAddressLeaseId:  plan.LeaseID,
		}
		created := tx.Select(helperApprovalPlanInsertColumns()).Create(&row)
		if created.Error != nil {
			return fmt.Errorf("insert helper approval plan for %q/%q: %w", row.ProjectId, row.SecondaryId, created.Error)
		}
		if created.RowsAffected != 1 || row.Id <= 0 {
			return corruptStateError("helper approval plan", row.OperationId, fmt.Errorf("insert did not return one positive row identity"))
		}
		for _, requirement := range plan.Intent.Requirements {
			requirementRow := models.HelperApprovalPlanSocketRequirement{
				HelperApprovalPlanId: row.Id,
				Transport:            string(requirement.Transport),
				Port:                 int(requirement.Port),
			}
			inserted := tx.Create(&requirementRow)
			if inserted.Error != nil {
				return fmt.Errorf("insert helper approval socket requirement: %w", inserted.Error)
			}
			if inserted.RowsAffected != 1 || requirementRow.Id <= 0 {
				return corruptStateError("helper approval socket requirement", fmt.Sprint(row.Id), fmt.Errorf("insert did not return one positive row identity"))
			}
		}
	}
	return nil
}

// helperApprovalPlanInsertColumns excludes generated fields that are not part of the durable migration contract.
func helperApprovalPlanInsertColumns() []string {
	return []string{
		"operation_id",
		"operation_revision",
		"network_state_id",
		"mutation",
		"lease_state",
		"project_id",
		"kind",
		"secondary_id",
		"address",
		"ownership_installation_id",
		"ownership_generation",
		"loopback_address_lease_id",
	}
}

// readHelperApprovalPlanRecordsInTransaction reconstructs every plan and requirement in canonical lease order.
func readHelperApprovalPlanRecordsInTransaction(
	tx *gorm.DB,
	operationID domain.OperationID,
	operationRevision domain.Sequence,
) ([]HelperApprovalPlanRecord, error) {
	modelRevision, err := sequenceToModelInt("helper approval operation revision", operationRevision, false)
	if err != nil {
		return nil, err
	}
	var rows []models.HelperApprovalPlan
	if err := tx.
		Select(helperApprovalPlanReadColumns()).
		Where("operation_id = ? AND operation_revision = ?", string(operationID), modelRevision).
		Order("project_id ASC").
		Order("kind ASC").
		Order("secondary_id ASC").
		Order("address ASC").
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read helper approval plans: %w", err)
	}
	planIDs := make([]int, 0, len(rows))
	seenPlanIDs := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		if row.Id <= 0 {
			return nil, corruptStateError("helper approval plan", string(operationID), fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenPlanIDs[row.Id]; duplicate {
			return nil, corruptStateError("helper approval plan", fmt.Sprint(row.Id), fmt.Errorf("database ID is duplicated"))
		}
		seenPlanIDs[row.Id] = struct{}{}
		planIDs = append(planIDs, row.Id)
	}

	requirements, err := readHelperApprovalRequirementsByPlan(tx, planIDs)
	if err != nil {
		return nil, err
	}
	records := make([]HelperApprovalPlanRecord, 0, len(rows))
	for _, row := range rows {
		record, err := helperApprovalPlanRecordFromModel(row, requirements[row.Id])
		if err != nil {
			return nil, err
		}
		if record.OperationID != operationID || record.OperationRevision != operationRevision {
			return nil, corruptStateError("helper approval plan", fmt.Sprint(row.Id), fmt.Errorf("operation owner differs from the read scope"))
		}
		records = append(records, record)
	}
	return records, nil
}

// helperApprovalPlanReadColumns keeps readback aligned to the migration-owned authority fields.
func helperApprovalPlanReadColumns() []string {
	return append([]string{"id"}, helperApprovalPlanInsertColumns()...)
}

// readHelperApprovalRequirementsByPlan validates all requirement rows selected by the staged plan identities.
func readHelperApprovalRequirementsByPlan(
	tx *gorm.DB,
	planIDs []int,
) (map[int][]hostconflict.SocketRequirement, error) {
	result := make(map[int][]hostconflict.SocketRequirement, len(planIDs))
	if len(planIDs) == 0 {
		return result, nil
	}
	knownPlans := make(map[int]struct{}, len(planIDs))
	for _, planID := range planIDs {
		knownPlans[planID] = struct{}{}
		result[planID] = []hostconflict.SocketRequirement{}
	}
	var rows []models.HelperApprovalPlanSocketRequirement
	if err := tx.
		Where("helper_approval_plan_id IN ?", planIDs).
		Order("helper_approval_plan_id ASC").
		Order("transport ASC").
		Order("port ASC").
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read helper approval socket requirements: %w", err)
	}
	seenIDs := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(fmt.Sprintf("%d/%s/%d", row.HelperApprovalPlanId, row.Transport, row.Port), row.Id)
		if row.Id <= 0 {
			return nil, corruptStateError("helper approval socket requirement", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return nil, corruptStateError("helper approval socket requirement", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if _, exists := knownPlans[row.HelperApprovalPlanId]; !exists {
			return nil, corruptStateError("helper approval socket requirement", key, fmt.Errorf("plan ID is outside the read scope"))
		}
		if row.Port <= 0 || row.Port > 65535 {
			return nil, corruptStateError("helper approval socket requirement", key, fmt.Errorf("port is outside 1-65535"))
		}
		requirement := hostconflict.SocketRequirement{
			Transport: hostconflict.Transport(row.Transport),
			Port:      uint16(row.Port),
		}
		if err := requirement.Validate(); err != nil {
			return nil, corruptStateError("helper approval socket requirement", key, err)
		}
		result[row.HelperApprovalPlanId] = append(result[row.HelperApprovalPlanId], requirement)
	}
	return result, nil
}

// helperApprovalPlanRecordFromModel validates and converts one row without trusting SQLite coercions.
func helperApprovalPlanRecordFromModel(
	row models.HelperApprovalPlan,
	requirements []hostconflict.SocketRequirement,
) (HelperApprovalPlanRecord, error) {
	key := durableKey(row.OperationId+"/"+row.ProjectId+"/"+row.SecondaryId, row.Id)
	if row.Id <= 0 {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, fmt.Errorf("database ID must be positive"))
	}
	operationID := domain.OperationID(row.OperationId)
	if err := operationID.Validate(); err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	operationRevision := domain.Sequence(row.OperationRevision)
	if _, err := sequenceToModelInt("helper approval operation revision", operationRevision, false); err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	if row.NetworkStateId != networkStateSingletonID {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, networkStateSingletonID))
	}
	leaseKey, err := networkLeaseKeyFromModel(domain.ProjectID(row.ProjectId), row.Kind, row.SecondaryId)
	if err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	address, err := parseCanonicalNetworkAddress("helper approval address", row.Address)
	if err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	generation, err := positiveNetworkGeneration("helper approval ownership generation", row.OwnershipGeneration)
	if err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	ownership, err := identity.NewOwnership(identity.InstallationID(row.OwnershipInstallationId), generation)
	if err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	intent := HelperApprovalPlanIntent{
		Mutation: helper.Operation(row.Mutation),
		Lease: identity.Lease{
			Key:       leaseKey,
			Address:   address,
			Ownership: ownership,
		},
		LeaseState:   ticketissuer.LeaseState(row.LeaseState),
		Requirements: slices.Clone(requirements),
	}
	if err := intent.Validate(); err != nil {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, err)
	}
	if intent.LeaseState == ticketissuer.LeasePending && row.LoopbackAddressLeaseId.Valid {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, fmt.Errorf("pending plan references an active lease"))
	}
	if intent.LeaseState == ticketissuer.LeaseActive && (!row.LoopbackAddressLeaseId.Valid || row.LoopbackAddressLeaseId.Int64 <= 0) {
		return HelperApprovalPlanRecord{}, corruptStateError("helper approval plan", key, fmt.Errorf("active plan has no positive lease identity"))
	}
	return HelperApprovalPlanRecord{
		OperationID:       operationID,
		OperationRevision: operationRevision,
		Intent:            intent,
		leaseID:           row.LoopbackAddressLeaseId,
	}, nil
}

// sameHelperApprovalPlanReadback compares the exact canonical request to its persisted projection.
func sameHelperApprovalPlanReadback(
	read []HelperApprovalPlanRecord,
	operation OperationRecord,
	want []preparedHelperApprovalPlan,
) bool {
	if len(read) != len(want) {
		return false
	}
	for index := range read {
		if read[index].OperationID != operation.Operation.ID || read[index].OperationRevision != operation.Revision {
			return false
		}
		if !sameHelperApprovalIntent(read[index].Intent, want[index].Intent) {
			return false
		}
		if read[index].leaseID != want[index].LeaseID {
			return false
		}
	}
	return true
}

// sameHelperApprovalIntent compares immutable authority fields while treating requirement slices by value.
func sameHelperApprovalIntent(left HelperApprovalPlanIntent, right HelperApprovalPlanIntent) bool {
	return left.Mutation == right.Mutation &&
		left.Lease == right.Lease &&
		left.LeaseState == right.LeaseState &&
		slices.Equal(left.Requirements, right.Requirements)
}

// helperApprovalPlanIDsInTransaction returns the exact positive row identities retired by one operation revision.
func helperApprovalPlanIDsInTransaction(
	tx *gorm.DB,
	operationID domain.OperationID,
	operationRevision domain.Sequence,
) ([]int, error) {
	modelRevision, err := sequenceToModelInt("helper approval operation revision", operationRevision, false)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID int `gorm:"column:id"`
	}
	if err := tx.Model(&models.HelperApprovalPlan{}).
		Select("id").
		Where("operation_id = ? AND operation_revision = ?", string(operationID), modelRevision).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read helper approval plan identities: %w", err)
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.ID <= 0 {
			return nil, corruptStateError("helper approval plan", string(operationID), fmt.Errorf("database ID must be positive"))
		}
		ids = append(ids, row.ID)
	}
	return ids, nil
}

// requireRetiredHelperApprovalPlans proves neither plan nor cascaded requirement authority survived deletion.
func requireRetiredHelperApprovalPlans(
	tx *gorm.DB,
	operation OperationRecord,
	planIDs []int,
) error {
	var planCount int64
	if err := tx.Model(&models.HelperApprovalPlan{}).
		Where("operation_id = ? AND operation_revision = ?", string(operation.Operation.ID), int(operation.Revision)).
		Count(&planCount).Error; err != nil {
		return fmt.Errorf("verify retired helper approval plans: %w", err)
	}
	if planCount != 0 {
		return corruptStateError("helper approval plan", string(operation.Operation.ID), fmt.Errorf("%d plan rows survived retirement", planCount))
	}
	if len(planIDs) == 0 {
		return nil
	}
	var requirementCount int64
	if err := tx.Model(&models.HelperApprovalPlanSocketRequirement{}).
		Where("helper_approval_plan_id IN ?", planIDs).
		Count(&requirementCount).Error; err != nil {
		return fmt.Errorf("verify retired helper approval socket requirements: %w", err)
	}
	if requirementCount != 0 {
		return corruptStateError("helper approval plan", string(operation.Operation.ID), fmt.Errorf("%d socket requirement rows survived retirement", requirementCount))
	}
	return nil
}
