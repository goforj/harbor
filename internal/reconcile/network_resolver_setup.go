package reconcile

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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
	"github.com/goforj/harbor/internal/trust/certificates"
)

// NetworkResolverSetupOperationJournal owns idempotent resolver staging and exact operation reads.
type NetworkResolverSetupOperationJournal interface {
	// Operation returns one durable operation by its daemon-owned identity.
	Operation(context.Context, domain.OperationID) (state.OperationRecord, error)
	// OperationByIntent returns one durable operation by its client-stable intent identity.
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	// StageNetworkResolverSetup atomically stages or exactly replays one global resolver plan.
	StageNetworkResolverSetup(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error)
}

// NetworkResolverSetupNetworkSource reads the complete durable network root used to derive resolver authority.
type NetworkResolverSetupNetworkSource interface {
	// Network returns the current aggregate and whether it has been initialized.
	Network(context.Context) (state.NetworkRecord, bool, error)
}

// NetworkResolverSetupPlanSource resolves one immutable resolver approval plan.
type NetworkResolverSetupPlanSource interface {
	// Resolve returns the exact revision-bound plan selected by a resolver setup operation.
	Resolve(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error)
}

// NetworkResolverSetupStore commits or exactly replays one independently observed resolver completion.
type NetworkResolverSetupStore interface {
	// CompleteNetworkResolverSetup atomically retires approval authority and activates resolver authority.
	CompleteNetworkResolverSetup(
		context.Context,
		state.CompleteNetworkResolverSetupRequest,
	) (state.CompleteNetworkResolverSetupResult, error)
}

// NetworkResolverSetupRootSource exposes only the public certificate authority used by canonical policy planning.
type NetworkResolverSetupRootSource interface {
	// PublicRoot returns the stable public root retained by the running Harbor generation.
	PublicRoot() (certificates.Root, error)
}

// NetworkResolverSetupIssuer publishes one short-lived capability for an exact durable resolver plan.
type NetworkResolverSetupIssuer interface {
	// Issue publishes a helper resolver capability for the authenticated requester.
	Issue(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error)
	// Close releases the issuer's daemon-owned key and capability stores.
	Close() error
}

// NetworkResolverSetupIssuerFactory opens helper authority only after Prepare validates durable ownership.
type NetworkResolverSetupIssuerFactory func() (NetworkResolverSetupIssuer, error)

// NetworkResolverSetupResolverObserver reads complete native resolver facts without mutation authority.
type NetworkResolverSetupResolverObserver interface {
	// Observe returns every native resolver fact relevant to one immutable request.
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
}

// NetworkResolverSetupStartRequest identifies one idempotent machine-global resolver intent.
type NetworkResolverSetupStartRequest struct {
	OperationID       domain.OperationID
	IntentID          domain.IntentID
	RequesterIdentity string
}

// Validate rejects identities that cannot select and own one stable resolver setup operation.
func (request NetworkResolverSetupStartRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := request.IntentID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkResolverSetupPrepareRequest selects one exact resolver approval for its authenticated machine owner.
type NetworkResolverSetupPrepareRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects stale-shaped Prepare input before helper authority can be opened.
func (request NetworkResolverSetupPrepareRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkResolverSetupConfirmRequest carries the exact helper postcondition for one revision-bound approval.
type NetworkResolverSetupConfirmRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	ResolverEvidence          helper.ResolverMutationEvidence
}

// Validate rejects malformed or revision-free confirmation identities before durable state is read.
func (request NetworkResolverSetupConfirmRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkResolverSetupEvidence(request.ResolverEvidence)
}

// NetworkResolverSetupCoordinator serializes resolver staging, helper issuance, and proof confirmation.
type NetworkResolverSetupCoordinator struct {
	operations NetworkResolverSetupOperationJournal
	network    NetworkResolverSetupNetworkSource
	plans      NetworkResolverSetupPlanSource
	store      NetworkResolverSetupStore
	roots      NetworkResolverSetupRootSource
	issuers    NetworkResolverSetupIssuerFactory
	ownership  OwnershipObserver
	resolver   NetworkResolverSetupResolverObserver
	platform   networkplan.Platform
	clock      helper.Clock
	mutex      sync.Mutex
}

// NewNetworkResolverSetupCoordinator constructs one policy-bound resolver setup authority.
func NewNetworkResolverSetupCoordinator(
	operations NetworkResolverSetupOperationJournal,
	network NetworkResolverSetupNetworkSource,
	plans NetworkResolverSetupPlanSource,
	store NetworkResolverSetupStore,
	roots NetworkResolverSetupRootSource,
	issuers NetworkResolverSetupIssuerFactory,
	ownershipObserver OwnershipObserver,
	resolverObserver NetworkResolverSetupResolverObserver,
	platform networkplan.Platform,
	clock helper.Clock,
) *NetworkResolverSetupCoordinator {
	return &NetworkResolverSetupCoordinator{
		operations: operations,
		network:    network,
		plans:      plans,
		store:      store,
		roots:      roots,
		issuers:    issuers,
		ownership:  ownershipObserver,
		resolver:   resolverObserver,
		platform:   platform,
		clock:      clock,
	}
}

// CurrentNetworkResolverSetupPlatform returns the host-network profile supported by the running binary.
func CurrentNetworkResolverSetupPlatform() (networkplan.Platform, error) {
	return networkResolverSetupPlatform(runtime.GOOS)
}

// networkResolverSetupPlatform maps only operating systems covered by Harbor's current product profiles.
func networkResolverSetupPlatform(goos string) (networkplan.Platform, error) {
	switch goos {
	case "darwin":
		return networkplan.PlatformMacOS, nil
	case "linux":
		// Harbor's current Linux packaging contract targets Ubuntu 24.04 explicitly.
		return networkplan.PlatformUbuntu2404, nil
	case "windows":
		return networkplan.PlatformWindows11, nil
	default:
		return "", fmt.Errorf("network resolver setup is unsupported on %s", goos)
	}
}

// Start stages a canonical schema-one to schema-two resolver plan or returns the operation already owning its intent.
func (coordinator *NetworkResolverSetupCoordinator) Start(
	ctx context.Context,
	request NetworkResolverSetupStartRequest,
) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}

	existing, err := coordinator.operations.OperationByIntent(ctx, request.IntentID)
	if err == nil {
		if err := validateExistingNetworkResolverSetupOperation(existing, request.IntentID); err != nil {
			return state.OperationRecord{}, fmt.Errorf("start network resolver setup: replay operation: %w", err)
		}
		if existing.Operation.State == domain.OperationSucceeded {
			if _, err := coordinator.terminalAuthority(ctx, 0); err != nil {
				return state.OperationRecord{}, fmt.Errorf("start network resolver setup: replay terminal authority: %w", err)
			}
		}
		return existing, nil
	}
	var missingIntent *state.OperationIntentNotFoundError
	if !errors.As(err, &missingIntent) {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: read operation intent: %w", err)
	}
	if missingIntent.IntentID != request.IntentID {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: missing operation intent differs from request")
	}

	authority, err := coordinator.identityAuthority(ctx, request.RequesterIdentity)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: %w", err)
	}
	operation, err := domain.NewOperation(
		request.OperationID,
		request.IntentID,
		domain.OperationKindNetworkResolverSetup,
		"",
		coordinator.operationTime(coordinator.clock.Now(), time.Time{}),
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: create operation: %w", err)
	}
	staged, err := coordinator.operations.StageNetworkResolverSetup(ctx, state.StageNetworkResolverSetupRequest{
		Operation:                          operation,
		ExpectedNetworkRevision:            authority.network.Revision,
		ExpectedSourceOwnershipFingerprint: authority.source.Fingerprint,
		TargetOwnership:                    authority.target,
		Policy:                             authority.policy,
	})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: stage operation: %w", err)
	}
	if err := validateStagedNetworkResolverSetupOperation(staged, request.IntentID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network resolver setup: stage readback: %w", err)
	}
	return staged, nil
}

// Prepare validates one exact resolver plan before opening and issuing helper authority.
func (coordinator *NetworkResolverSetupCoordinator) Prepare(
	ctx context.Context,
	request NetworkResolverSetupPrepareRequest,
) (ticketissuer.ResolverResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare network resolver setup approval: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.ResolverResult{}, err
	}

	resolverRequest := ticketissuer.ResolverRequest{OperationID: request.OperationID}
	plan, err := coordinator.plans.Resolve(ctx, resolverRequest)
	if err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare network resolver setup approval: resolve plan: %w", err)
	}
	if err := validateNetworkResolverSetupPlan(plan, request.OperationID, request.ExpectedOperationRevision); err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare network resolver setup approval: %w", err)
	}
	if plan.TargetOwnership.OwnerIdentity != request.RequesterIdentity {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare network resolver setup approval: authenticated requester does not match the approved machine owner")
	}

	result, issueErr := coordinator.issueResolver(ctx, request.RequesterIdentity, resolverRequest)
	if issueErr != nil {
		if errors.Is(issueErr, ticketissuer.ErrResolverPublicationIndeterminate) {
			validationErr := validateNetworkResolverSetupResult(result, plan, coordinator.clock.Now().UTC())
			return result, errors.Join(
				fmt.Errorf("prepare network resolver setup approval: %w", issueErr),
				wrapNetworkResolverSetupError("validate indeterminate helper resolver result", validationErr),
			)
		}
		return result, fmt.Errorf("prepare network resolver setup approval: %w", issueErr)
	}
	if err := validateNetworkResolverSetupResult(result, plan, coordinator.clock.Now().UTC()); err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare network resolver setup approval: %w", err)
	}
	return result, nil
}

// Confirm correlates helper target evidence and independently observes native resolver state before durable completion.
func (coordinator *NetworkResolverSetupCoordinator) Confirm(
	ctx context.Context,
	request NetworkResolverSetupConfirmRequest,
) (state.CompleteNetworkResolverSetupResult, error) {
	if err := request.Validate(); err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.CompleteNetworkResolverSetupResult{}, err
	}

	operation, err := coordinator.operations.Operation(ctx, request.OperationID)
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: read operation: %w", err)
	}
	if err := validateConfirmNetworkResolverSetupOperation(operation, request.OperationID); err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: %w", err)
	}

	var authority networkResolverSetupAuthority
	var at time.Time
	switch operation.Operation.State {
	case domain.OperationRequiresApproval:
		if operation.Revision != request.ExpectedOperationRevision {
			return state.CompleteNetworkResolverSetupResult{}, &state.StaleRevisionError{
				OperationID: request.OperationID,
				Expected:    request.ExpectedOperationRevision,
				Actual:      operation.Revision,
			}
		}
		plan, err := coordinator.plans.Resolve(ctx, ticketissuer.ResolverRequest{OperationID: request.OperationID})
		if err != nil {
			return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: resolve plan: %w", err)
		}
		if err := validateNetworkResolverSetupPlan(plan, request.OperationID, request.ExpectedOperationRevision); err != nil {
			return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: %w", err)
		}
		authority = networkResolverSetupAuthority{policy: plan.Policy, target: plan.TargetOwnership}
		at = coordinator.operationTime(coordinator.clock.Now(), operation.Operation.RequestedAt)
	case domain.OperationSucceeded:
		if operation.Revision < 2 || operation.Revision <= request.ExpectedOperationRevision+1 {
			return state.CompleteNetworkResolverSetupResult{}, &state.StaleRevisionError{
				OperationID: request.OperationID,
				Expected:    request.ExpectedOperationRevision,
				Actual:      operation.Revision,
			}
		}
		authority, err = coordinator.terminalAuthority(ctx, operation.Revision-1)
		if err != nil {
			return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: %w", err)
		}
		at = operation.Operation.FinishedAt.UTC().Round(0)
	default:
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf(
			"confirm network resolver setup approval: operation %q has unsupported state %q",
			request.OperationID,
			operation.Operation.State,
		)
	}

	targetOwnershipFingerprint, err := authority.target.Fingerprint()
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: fingerprint target ownership: %w", err)
	}
	if request.ResolverEvidence.OwnershipFingerprint != targetOwnershipFingerprint {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: helper evidence belongs to another ownership target")
	}
	policyFingerprint, err := authority.policy.Fingerprint()
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: fingerprint resolver policy: %w", err)
	}
	if request.ResolverEvidence.PolicyFingerprint != policyFingerprint {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: helper evidence belongs to another resolver policy")
	}

	observedResolver, err := coordinator.observeExactResolver(ctx, authority.target, authority.policy)
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: %w", err)
	}
	observationFingerprint, err := observedResolver.Fingerprint()
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: fingerprint resolver observation: %w", err)
	}
	if request.ResolverEvidence.ObservationFingerprint != observationFingerprint {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: helper evidence differs from the independently observed resolver")
	}

	result, err := coordinator.store.CompleteNetworkResolverSetup(ctx, state.CompleteNetworkResolverSetupRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ResolverEvidence:          request.ResolverEvidence,
		ObservedResolver:          observedResolver,
		At:                        at,
	})
	if err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: complete setup: %w", err)
	}
	if err := result.Validate(); err != nil {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: completion result is invalid: %w", err)
	}
	if result.Operation.Operation.ID != request.OperationID ||
		result.NetworkRevision <= request.ExpectedOperationRevision+1 ||
		result.Operation.Revision != result.NetworkRevision+1 {
		return state.CompleteNetworkResolverSetupResult{}, fmt.Errorf("confirm network resolver setup approval: completion crossed the requested operation revision")
	}
	return result, nil
}

// networkResolverSetupAuthority retains the exact policy and ownership target needed across one coordinator boundary.
type networkResolverSetupAuthority struct {
	network state.NetworkRecord
	source  ownership.Observation
	policy  networkpolicy.Policy
	target  ownership.Record
}

// identityAuthority derives one immutable policy from the current schema-one machine and certificate roots.
func (coordinator *NetworkResolverSetupCoordinator) identityAuthority(
	ctx context.Context,
	requesterIdentity string,
) (networkResolverSetupAuthority, error) {
	network, initialized, err := coordinator.network.Network(ctx)
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("read network identity: %w", err)
	}
	if !initialized {
		return networkResolverSetupAuthority{}, &state.NetworkNotInitializedError{}
	}
	if err := network.Validate(); err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("network identity is invalid: %w", err)
	}
	if network.Stage != state.NetworkStageIdentity {
		return networkResolverSetupAuthority{}, fmt.Errorf("network resolver setup requires identity stage, found %q", network.Stage)
	}

	source, err := coordinator.ownership.Observe(ctx)
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("observe machine ownership: %w", err)
	}
	if err := validateNetworkResolverSetupOwnership(source, ownership.IdentitySchemaVersion, network); err != nil {
		return networkResolverSetupAuthority{}, err
	}
	if source.Record.OwnerIdentity != requesterIdentity {
		return networkResolverSetupAuthority{}, fmt.Errorf("authenticated requester does not own the machine claim")
	}

	policy, err := coordinator.buildPolicy(network)
	if err != nil {
		return networkResolverSetupAuthority{}, err
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("fingerprint resolver policy: %w", err)
	}
	target := source.Record
	target.SchemaVersion = ownership.NetworkPolicySchemaVersion
	target.NetworkPolicyFingerprint = policyFingerprint
	if err := target.Validate(); err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("construct resolver ownership target: %w", err)
	}
	return networkResolverSetupAuthority{
		network: network,
		source:  source,
		policy:  policy,
		target:  target,
	}, nil
}

// terminalAuthority reconstructs canonical authority and proves it remains the completed resolver projection.
func (coordinator *NetworkResolverSetupCoordinator) terminalAuthority(
	ctx context.Context,
	expectedNetworkRevision domain.Sequence,
) (networkResolverSetupAuthority, error) {
	network, initialized, err := coordinator.network.Network(ctx)
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("read terminal network resolver: %w", err)
	}
	if !initialized {
		return networkResolverSetupAuthority{}, &state.NetworkNotInitializedError{}
	}
	if err := network.Validate(); err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("terminal network resolver is invalid: %w", err)
	}
	if network.Stage != state.NetworkStageResolver && network.Stage != state.NetworkStageFull {
		return networkResolverSetupAuthority{}, fmt.Errorf(
			"terminal network stage is %q, want %q or %q",
			network.Stage,
			state.NetworkStageResolver,
			state.NetworkStageFull,
		)
	}
	if network.Revision < expectedNetworkRevision {
		return networkResolverSetupAuthority{}, &state.NetworkRevisionConflictError{
			Expected: expectedNetworkRevision,
			Actual:   network.Revision,
		}
	}
	policy, err := coordinator.buildPolicy(network)
	if err != nil {
		return networkResolverSetupAuthority{}, err
	}
	projected, err := coordinator.ownership.Observe(ctx)
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("observe terminal machine ownership: %w", err)
	}
	if err := validateNetworkResolverSetupOwnership(projected, ownership.NetworkPolicySchemaVersion, network); err != nil {
		return networkResolverSetupAuthority{}, err
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return networkResolverSetupAuthority{}, fmt.Errorf("fingerprint terminal resolver policy: %w", err)
	}
	if projected.Record.NetworkPolicyFingerprint != policyFingerprint {
		return networkResolverSetupAuthority{}, fmt.Errorf("terminal machine ownership belongs to another resolver policy")
	}
	return networkResolverSetupAuthority{network: network, policy: policy, target: projected.Record}, nil
}

// buildPolicy binds current certificate identity to one platform profile and the durable loopback pool.
func (coordinator *NetworkResolverSetupCoordinator) buildPolicy(
	network state.NetworkRecord,
) (networkpolicy.Policy, error) {
	root, err := coordinator.roots.PublicRoot()
	if err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("read public certificate root: %w", err)
	}
	policy, err := networkplan.Build(networkplan.Request{
		Platform:             coordinator.platform,
		InstallationID:       network.Ownership.InstallationID,
		Pool:                 network.Pool,
		AuthorityFingerprint: root.Fingerprint,
	})
	if err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("build host network policy: %w", err)
	}
	return policy, nil
}

// observeExactResolver admits only a complete exact rule for the selected ownership and policy.
func (coordinator *NetworkResolverSetupCoordinator) observeExactResolver(
	ctx context.Context,
	target ownership.Record,
	policy networkpolicy.Policy,
) (resolver.Observation, error) {
	request, err := resolver.NewRequest(target.InstallationID, policy)
	if err != nil {
		return resolver.Observation{}, fmt.Errorf("construct resolver observation request: %w", err)
	}
	observation, err := coordinator.resolver.Observe(ctx, request)
	if err != nil {
		return resolver.Observation{}, fmt.Errorf("observe resolver: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return resolver.Observation{}, fmt.Errorf("resolver observation is invalid: %w", err)
	}
	if observation.Request.InstallationID() != request.InstallationID() ||
		observation.Request.PolicyFingerprint() != request.PolicyFingerprint() ||
		observation.Request.Policy() != request.Policy() {
		return resolver.Observation{}, fmt.Errorf("resolver observation belongs to another request")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return resolver.Observation{}, fmt.Errorf("classify resolver observation: %w", err)
	}
	if assessment.State != resolver.StateExact || assessment.Owned != resolver.OwnedStateExact || assessment.ForeignCount != 0 {
		return resolver.Observation{}, fmt.Errorf("resolver state is %q, want exact", assessment.State)
	}
	return observation, nil
}

// issueResolver opens helper authority after plan validation and closes every opened resource before returning.
func (coordinator *NetworkResolverSetupCoordinator) issueResolver(
	ctx context.Context,
	requesterIdentity string,
	request ticketissuer.ResolverRequest,
) (ticketissuer.ResolverResult, error) {
	issuer, err := coordinator.issuers()
	if err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("open helper resolver issuer: %w", err)
	}
	result, issueErr := issuer.Issue(ctx, requesterIdentity, request)
	closeErr := issuer.Close()
	if issueErr == nil && closeErr == nil {
		return result, nil
	}
	if issueErr == nil {
		return result, errors.Join(
			ticketissuer.ErrResolverPublicationIndeterminate,
			wrapNetworkResolverSetupError("close helper resolver issuer after publication", closeErr),
		)
	}
	if errors.Is(issueErr, ticketissuer.ErrResolverPublicationIndeterminate) {
		return result, errors.Join(
			issueErr,
			wrapNetworkResolverSetupError("close helper resolver issuer", closeErr),
		)
	}
	return ticketissuer.ResolverResult{}, errors.Join(
		wrapNetworkResolverSetupError("issue helper resolver ticket", issueErr),
		wrapNetworkResolverSetupError("close helper resolver issuer", closeErr),
	)
}

// operationTime returns a canonical resolver setup instant no earlier than one durable lifecycle boundary.
func (coordinator *NetworkResolverSetupCoordinator) operationTime(now time.Time, lowerBound time.Time) time.Time {
	at := now.UTC().Round(0)
	if !lowerBound.IsZero() && at.Before(lowerBound) {
		return lowerBound.UTC().Round(0)
	}
	return at
}

// wrapNetworkResolverSetupError adds lifecycle context without manufacturing errors for successful resource steps.
func wrapNetworkResolverSetupError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
