package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certroot"
)

// NetworkResolverPolicyMigrationJournal owns durable migration operation reads and staging.
type NetworkResolverPolicyMigrationJournal interface {
	// Operation returns one durable operation by daemon-owned identity.
	Operation(context.Context, domain.OperationID) (state.OperationRecord, error)
	// OperationByIntent returns the operation assigned to a stable client intent.
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	// StageNetworkResolverPolicyMigration records one exact legacy retirement checkpoint.
	StageNetworkResolverPolicyMigration(context.Context, state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error)
}

// NetworkResolverPolicyMigrationPlanSource resolves the committed retirement authority.
type NetworkResolverPolicyMigrationPlanSource interface {
	// Resolve returns the immutable approval plan for one operation.
	Resolve(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error)
}

// NetworkResolverPolicyMigrationOwnershipProjectionSource reads the daemon-owned projected ownership record.
type NetworkResolverPolicyMigrationOwnershipProjectionSource interface {
	// Observe returns the last helper-confirmed ownership projection without reading protected storage.
	Observe(context.Context) (ownership.Observation, error)
}

// NetworkResolverPolicyMigrationStore completes a retirement or restores its exact durable terminal result.
type NetworkResolverPolicyMigrationStore interface {
	// CompleteNetworkResolverPolicyMigration commits helper-correlated retirement evidence.
	CompleteNetworkResolverPolicyMigration(context.Context, state.CompleteNetworkResolverPolicyMigrationRequest) (state.CompleteNetworkResolverPolicyMigrationResult, error)
	// ReplayNetworkResolverPolicyMigration restores a durable terminal result for an exact approval retry.
	ReplayNetworkResolverPolicyMigration(context.Context, domain.OperationID, domain.Sequence) (state.CompleteNetworkResolverPolicyMigrationResult, error)
}

// NetworkResolverPolicyMigrationRootSource exposes the retained public root used by the legacy policy.
type NetworkResolverPolicyMigrationRootSource interface {
	// PublicRoot returns the public root retained by this Harbor generation.
	PublicRoot() (certroot.Root, error)
}

// NetworkResolverPolicyMigrationIssuer publishes one exact retirement capability.
type NetworkResolverPolicyMigrationIssuer interface {
	// Issue publishes a helper resolver capability for the authenticated machine owner.
	Issue(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error)
	// Close releases issuer resources.
	Close() error
}

// NetworkResolverPolicyMigrationIssuerFactory opens the committed policy-migration issuer after durable validation.
type NetworkResolverPolicyMigrationIssuerFactory func() (NetworkResolverPolicyMigrationIssuer, error)

// NetworkResolverPolicyMigrationResolverObserver reads native resolver facts without mutation authority.
type NetworkResolverPolicyMigrationResolverObserver interface {
	// Observe returns complete native facts for one legacy resolver request.
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
}

// NetworkResolverPolicyMigrationStartRequest identifies an idempotent legacy resolver retirement.
type NetworkResolverPolicyMigrationStartRequest struct {
	OperationID       domain.OperationID
	IntentID          domain.IntentID
	RequesterIdentity string
}

// Validate rejects an unscoped migration identity.
func (request NetworkResolverPolicyMigrationStartRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := request.IntentID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkResolverPolicyMigrationPrepareRequest selects one approval checkpoint for its owner.
type NetworkResolverPolicyMigrationPrepareRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects an unscoped approval request.
func (request NetworkResolverPolicyMigrationPrepareRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkResolverPolicyMigrationConfirmRequest carries helper evidence for one retirement checkpoint.
type NetworkResolverPolicyMigrationConfirmRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	ResolverEvidence          helper.ResolverMutationEvidence
}

// Validate rejects malformed retirement confirmation input.
func (request NetworkResolverPolicyMigrationConfirmRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	if err := validateNetworkSetupRequesterIdentity(request.RequesterIdentity); err != nil {
		return err
	}
	if request.ResolverEvidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		return fmt.Errorf("resolver policy migration evidence must prove owned absence")
	}
	return nil
}

// NetworkResolverPolicyMigrationCoordinator coordinates only temporary legacy macOS resolver retirement.
type NetworkResolverPolicyMigrationCoordinator struct {
	operations NetworkResolverPolicyMigrationJournal
	network    NetworkResolverSetupNetworkSource
	plans      NetworkResolverPolicyMigrationPlanSource
	store      NetworkResolverPolicyMigrationStore
	roots      NetworkResolverPolicyMigrationRootSource
	issuers    NetworkResolverPolicyMigrationIssuerFactory
	projection NetworkResolverPolicyMigrationOwnershipProjectionSource
	resolver   NetworkResolverPolicyMigrationResolverObserver
	platform   networkplan.Platform
	clock      helper.Clock
	mutex      sync.Mutex
}

// NewNetworkResolverPolicyMigrationCoordinator constructs the narrow legacy retirement coordinator.
func NewNetworkResolverPolicyMigrationCoordinator(operations NetworkResolverPolicyMigrationJournal, network NetworkResolverSetupNetworkSource, plans NetworkResolverPolicyMigrationPlanSource, store NetworkResolverPolicyMigrationStore, roots NetworkResolverPolicyMigrationRootSource, issuers NetworkResolverPolicyMigrationIssuerFactory, projectionSource NetworkResolverPolicyMigrationOwnershipProjectionSource, resolverObserver NetworkResolverPolicyMigrationResolverObserver, platform networkplan.Platform, clock helper.Clock) *NetworkResolverPolicyMigrationCoordinator {
	return &NetworkResolverPolicyMigrationCoordinator{
		operations: operations,
		network:    network,
		plans:      plans,
		store:      store,
		roots:      roots,
		issuers:    issuers,
		projection: projectionSource,
		resolver:   resolverObserver,
		platform:   platform,
		clock:      clock,
	}
}

// Start stages retirement only for the exact current legacy macOS schema-two resolver state and owner.
func (c *NetworkResolverPolicyMigrationCoordinator) Start(ctx context.Context, request NetworkResolverPolicyMigrationStartRequest) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	existing, err := c.operations.OperationByIntent(ctx, request.IntentID)
	if err == nil {
		existing, err = validatePolicyMigrationExisting(existing, request.IntentID)
		if err != nil {
			return state.OperationRecord{}, err
		}
		if existing.Operation.State == domain.OperationSucceeded {
			if err := c.requireProjectedOwner(ctx, request.RequesterIdentity); err != nil {
				return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: validate completed owner: %w", err)
			}
		}
		if existing.Operation.State != domain.OperationRequiresApproval || existing.Operation.Phase != string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval) {
			return existing, nil
		}
		plan, err := c.plan(ctx, existing.Operation.ID, existing.Revision)
		if err != nil {
			return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: inspect replay plan: %w", err)
		}
		if plan.TargetOwnership.OwnerIdentity != request.RequesterIdentity {
			return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: authenticated requester does not match the approved machine owner")
		}
		if err := c.observeReplayResolver(ctx, plan); err != nil {
			return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: inspect replay resolver: %w", err)
		}
		return existing, nil
	}
	var missing *state.OperationIntentNotFoundError
	if !errors.As(err, &missing) {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: read intent: %w", err)
	}
	authority, err := c.legacyAuthority(ctx, request.RequesterIdentity)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: %w", err)
	}
	replacement := authority.policy
	replacement.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
	replacementFingerprint, err := replacement.Fingerprint()
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: fingerprint replacement policy: %w", err)
	}
	op, err := domain.NewOperation(request.OperationID, request.IntentID, domain.OperationKindNetworkResolverPolicyMigration, "", c.migrationTime(time.Time{}))
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: create operation: %w", err)
	}
	staged, err := c.operations.StageNetworkResolverPolicyMigration(ctx, state.StageNetworkResolverPolicyMigrationRequest{
		Operation:                    op,
		ExpectedNetworkRevision:      authority.network.Revision,
		SourceOwnership:              authority.ownership,
		Policy:                       authority.policy,
		ReplacementPolicyFingerprint: replacementFingerprint,
	})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start resolver policy migration: stage operation: %w", err)
	}
	return validatePolicyMigrationStaged(staged, request.IntentID)
}

// Prepare issues only OperationRetireResolver authority for the exact committed migration plan.
func (c *NetworkResolverPolicyMigrationCoordinator) Prepare(ctx context.Context, request NetworkResolverPolicyMigrationPrepareRequest) (ticketissuer.ResolverResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare resolver policy migration: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	plan, err := c.plan(ctx, request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	if plan.TargetOwnership.OwnerIdentity != request.RequesterIdentity {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare resolver policy migration: authenticated requester does not match the approved machine owner")
	}
	return c.issue(ctx, request.RequesterIdentity, ticketissuer.ResolverRequest{OperationID: request.OperationID}, plan)
}

// Confirm binds authenticated helper evidence to fresh resolver absence before atomically retiring the committed plan.
func (c *NetworkResolverPolicyMigrationCoordinator) Confirm(ctx context.Context, request NetworkResolverPolicyMigrationConfirmRequest) (state.CompleteNetworkResolverPolicyMigrationResult, error) {
	if err := request.Validate(); err != nil {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	operation, err := c.operations.Operation(ctx, request.OperationID)
	if err == nil && operation.Operation.State == domain.OperationSucceeded {
		if _, err := validatePolicyMigrationExisting(operation, operation.Operation.IntentID); err != nil {
			return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: validate completed operation: %w", err)
		}
		if err := c.requireProjectedOwner(ctx, request.RequesterIdentity); err != nil {
			return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: validate completed owner: %w", err)
		}
		return c.store.ReplayNetworkResolverPolicyMigration(ctx, request.OperationID, request.ExpectedOperationRevision)
	}
	if err != nil {
		var missing *state.OperationNotFoundError
		if !errors.As(err, &missing) {
			return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: read operation: %w", err)
		}
	}
	plan, err := c.plan(ctx, request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, err
	}
	if plan.TargetOwnership.OwnerIdentity != request.RequesterIdentity {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: authenticated requester does not match the approved machine owner")
	}
	observed, err := c.observeAbsent(ctx, plan)
	if err != nil {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: %w", err)
	}
	confirmed, err := derivedPolicyMigrationOwnership(plan.TargetOwnership)
	if err != nil {
		return state.CompleteNetworkResolverPolicyMigrationResult{}, fmt.Errorf("confirm resolver policy migration: derive post-ownership: %w", err)
	}
	return c.store.CompleteNetworkResolverPolicyMigration(ctx, state.CompleteNetworkResolverPolicyMigrationRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ResolverEvidence:          request.ResolverEvidence,
		ObservedResolver:          observed,
		ConfirmedOwnership:        confirmed,
		At:                        c.migrationTime(plan.Operation.RequestedAt),
	})
}

type resolverPolicyMigrationAuthority struct {
	network   state.NetworkRecord
	ownership ownership.Observation
	policy    networkpolicy.Policy
}

// legacyAuthority reconstructs the sole historical policy from daemon-owned projected schema-two ownership.
func (c *NetworkResolverPolicyMigrationCoordinator) legacyAuthority(ctx context.Context, requester string) (resolverPolicyMigrationAuthority, error) {
	if c.platform != networkplan.PlatformMacOS {
		return resolverPolicyMigrationAuthority{}, fmt.Errorf("legacy resolver policy migration is unsupported on %q", c.platform)
	}
	network, initialized, err := c.network.Network(ctx)
	if err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	if !initialized {
		return resolverPolicyMigrationAuthority{}, &state.NetworkNotInitializedError{}
	}
	if err := network.Validate(); err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	if network.Stage != state.NetworkStageResolver {
		return resolverPolicyMigrationAuthority{}, fmt.Errorf("legacy resolver policy migration requires resolver stage")
	}
	confirmed, err := c.projection.Observe(ctx)
	if err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	if !confirmed.Exists || confirmed.Record.SchemaVersion != ownership.NetworkPolicySchemaVersion || confirmed.Record.OwnerIdentity != requester {
		return resolverPolicyMigrationAuthority{}, fmt.Errorf("projected ownership is not the exact legacy schema-two requester owner")
	}
	root, err := c.roots.PublicRoot()
	if err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	policy, err := networkplan.BuildLegacyMacOS(networkplan.Request{
		Platform:             c.platform,
		InstallationID:       network.Ownership.InstallationID,
		Pool:                 network.Pool,
		AuthorityFingerprint: root.Fingerprint,
	})
	if err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		return resolverPolicyMigrationAuthority{}, err
	}
	if confirmed.Record.NetworkPolicyFingerprint != fingerprint {
		return resolverPolicyMigrationAuthority{}, fmt.Errorf("projected ownership does not match the exact legacy macOS resolver policy")
	}
	return resolverPolicyMigrationAuthority{
		network:   network,
		ownership: confirmed,
		policy:    policy,
	}, nil
}

// plan validates the only committed migration authority accepted by prepare and confirm.
func (c *NetworkResolverPolicyMigrationCoordinator) plan(ctx context.Context, id domain.OperationID, revision domain.Sequence) (ticketissuer.ResolverPlan, error) {
	plan, err := c.plans.Resolve(ctx, ticketissuer.ResolverRequest{OperationID: id})
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve resolver policy migration plan: %w", err)
	}
	if err := validatePolicyMigrationPlan(plan, id, revision); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	return plan, nil
}

// observeAbsent rejects any owned, foreign, incomplete, or mixed legacy resolver state.
func (c *NetworkResolverPolicyMigrationCoordinator) observeAbsent(ctx context.Context, plan ticketissuer.ResolverPlan) (resolver.Observation, error) {
	request, err := resolver.NewRequest(plan.TargetOwnership.InstallationID, plan.Policy)
	if err != nil {
		return resolver.Observation{}, err
	}
	observed, err := c.resolver.Observe(ctx, request)
	if err != nil {
		return resolver.Observation{}, err
	}
	if err := observed.Validate(); err != nil {
		return resolver.Observation{}, err
	}
	if observed.Request.InstallationID() != request.InstallationID() || observed.Request.Policy() != request.Policy() {
		return resolver.Observation{}, fmt.Errorf("native resolver observation belongs to another request")
	}
	assessment, err := observed.Classify()
	if err != nil {
		return resolver.Observation{}, err
	}
	if assessment.State != resolver.StateAbsent || assessment.Owned != resolver.OwnedStateAbsent || assessment.ForeignCount != 0 {
		return resolver.Observation{}, fmt.Errorf("native resolver is foreign, ambiguous, mixed, or not absent")
	}
	return observed, nil
}

// observeReplayResolver accepts only exact legacy ownership or complete absence without foreign claims.
func (c *NetworkResolverPolicyMigrationCoordinator) observeReplayResolver(ctx context.Context, plan ticketissuer.ResolverPlan) error {
	request, err := resolver.NewRequest(plan.TargetOwnership.InstallationID, plan.Policy)
	if err != nil {
		return err
	}
	observed, err := c.resolver.Observe(ctx, request)
	if err != nil {
		return err
	}
	if err := observed.Validate(); err != nil {
		return err
	}
	if observed.Request.InstallationID() != request.InstallationID() || observed.Request.Policy() != request.Policy() {
		return fmt.Errorf("native resolver observation belongs to another request")
	}
	assessment, err := observed.Classify()
	if err != nil {
		return err
	}
	if assessment.ForeignCount != 0 || (assessment.State != resolver.StateExact && assessment.State != resolver.StateAbsent) {
		return fmt.Errorf("native resolver is foreign, ambiguous, mixed, or unsupported")
	}
	return nil
}

// derivedPolicyMigrationOwnership derives the protected schema-one postcondition from the committed schema-two source.
func derivedPolicyMigrationOwnership(source ownership.Record) (ownership.Observation, error) {
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	fingerprint, err := source.Fingerprint()
	if err != nil {
		return ownership.Observation{}, err
	}
	return ownership.Observation{
		Exists:      true,
		Record:      source,
		Fingerprint: fingerprint,
	}, nil
}

// requireProjectedOwner binds a completed replay to the daemon-owned post-migration ownership projection.
func (c *NetworkResolverPolicyMigrationCoordinator) requireProjectedOwner(ctx context.Context, requester string) error {
	confirmed, err := c.projection.Observe(ctx)
	if err != nil {
		return err
	}
	if !confirmed.Exists || confirmed.Record.OwnerIdentity != requester {
		return fmt.Errorf("authenticated requester does not match the approved machine owner")
	}
	return nil
}

// issue publishes a validated plan and preserves only an indeterminate returned capability.
func (c *NetworkResolverPolicyMigrationCoordinator) issue(ctx context.Context, requester string, request ticketissuer.ResolverRequest, plan ticketissuer.ResolverPlan) (ticketissuer.ResolverResult, error) {
	issuer, err := c.issuers()
	if err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("open resolver policy migration issuer: %w", err)
	}
	result, issueErr := issuer.Issue(ctx, requester, request)
	closeErr := issuer.Close()
	if issueErr == nil && closeErr == nil {
		return result, nil
	}
	if errors.Is(issueErr, ticketissuer.ErrResolverPublicationIndeterminate) || (issueErr == nil && closeErr != nil) {
		return result, errors.Join(ticketissuer.ErrResolverPublicationIndeterminate, issueErr, closeErr)
	}
	return ticketissuer.ResolverResult{}, errors.Join(issueErr, closeErr)
}

// migrationTime keeps writes no earlier than their durable operation boundary.
func (c *NetworkResolverPolicyMigrationCoordinator) migrationTime(lower time.Time) time.Time {
	at := c.clock.Now().UTC().Round(0)
	if !lower.IsZero() && at.Before(lower) {
		return lower.UTC().Round(0)
	}
	return at
}

// validatePolicyMigrationExisting verifies an intent replay cannot select another lifecycle.
func validatePolicyMigrationExisting(record state.OperationRecord, intent domain.IntentID) (state.OperationRecord, error) {
	if record.Operation.IntentID != intent || record.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || record.Operation.ProjectID != "" {
		return state.OperationRecord{}, fmt.Errorf("operation does not match resolver policy migration intent")
	}
	if err := record.Operation.Validate(); err != nil {
		return state.OperationRecord{}, err
	}
	return record, nil
}

// validatePolicyMigrationStaged requires the unique approval checkpoint returned by staging.
func validatePolicyMigrationStaged(record state.OperationRecord, intent domain.IntentID) (state.OperationRecord, error) {
	record, err := validatePolicyMigrationExisting(record, intent)
	if err != nil {
		return state.OperationRecord{}, err
	}
	if record.Operation.State != domain.OperationRequiresApproval || record.Operation.Phase != string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval) {
		return state.OperationRecord{}, fmt.Errorf("staged operation is not awaiting resolver policy migration approval")
	}
	return record, nil
}

// validatePolicyMigrationPlan confines all capability operations to the committed retirement plan.
func validatePolicyMigrationPlan(plan ticketissuer.ResolverPlan, id domain.OperationID, revision domain.Sequence) error {
	if plan.Purpose != ticketissuer.ResolverPlanPurposePolicyMigration || plan.Mutation != helper.OperationRetireResolver || plan.Operation.ID != id {
		return fmt.Errorf("resolver plan is not the requested policy migration retirement")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.OperationRevision != revision {
		return &state.StaleRevisionError{
			OperationID: id,
			Expected:    revision,
			Actual:      plan.OperationRevision,
		}
	}
	return nil
}
