package reconcile

import (
	"bytes"
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
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
)

// NetworkDataPlaneSetupJournal is the narrow durable lifecycle boundary for trusted ingress setup.
type NetworkDataPlaneSetupJournal interface {
	Operation(context.Context, domain.OperationID) (state.OperationRecord, error)
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	StageNetworkDataPlaneSetup(context.Context, state.StageNetworkDataPlaneSetupRequest) (state.OperationRecord, error)
	AdvanceNetworkDataPlaneSetupTrust(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error)
	StageNetworkDataPlaneActivation(context.Context, state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error)
	CompleteNetworkDataPlaneActivation(context.Context, state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error)
	CompleteNetworkDataPlaneSetup(context.Context, state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error)
	ReadNetworkDataPlaneSetupPlan(context.Context, domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error)
}

// NetworkDataPlaneSetupNetworkSource reads the durable identity needed to build the canonical policy.
type NetworkDataPlaneSetupNetworkSource interface {
	Network(context.Context) (state.NetworkRecord, bool, error)
}

// NetworkDataPlaneSetupProjectionSource replays a policy-bound durable resolver or full projection.
type NetworkDataPlaneSetupProjectionSource interface {
	Resolve(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error)
}

// NetworkDataPlaneSetupStore makes the resolver-to-full durable mutation.
type NetworkDataPlaneSetupStore interface {
	ActivateNetworkDataPlane(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error)
}

// NetworkDataPlaneSetupRootSource supplies the public CA retained by the current runtime generation.
type NetworkDataPlaneSetupRootSource interface {
	PublicRoot() (certificates.Root, error)
}

// NetworkDataPlaneSetupRuntime activates listeners only after durable full authority exists.
type NetworkDataPlaneSetupRuntime interface {
	ActivateNetwork(context.Context, domain.Sequence) error
}

// NetworkDataPlaneSetupEndpointBackfill adds full-stage default HTTP endpoint reservations.
type NetworkDataPlaneSetupEndpointBackfill interface {
	ReconcileFullStageDefaultHTTPEndpoints(context.Context) (state.NetworkRecord, error)
}

// NetworkDataPlaneSetupTrustIssuer issues a bounded trust capability.
type NetworkDataPlaneSetupTrustIssuer interface {
	Issue(context.Context, string, ticketissuer.TrustRequest) (ticketissuer.TrustResult, error)
	Close() error
}

// NetworkDataPlaneSetupLowPortIssuer issues a bounded paired-listener capability.
type NetworkDataPlaneSetupLowPortIssuer interface {
	Issue(context.Context, string, ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error)
	Close() error
}

// NetworkDataPlaneSetupTrustObserver observes public-root trust without mutation authority.
type NetworkDataPlaneSetupTrustObserver interface {
	Observe(context.Context, trust.Request) (trust.Observation, error)
}

// NetworkDataPlaneSetupLowPortObserver observes paired low-port service facts without mutation authority.
type NetworkDataPlaneSetupLowPortObserver interface {
	Observe(context.Context, lowport.Request) (lowport.Observation, error)
}

// NetworkDataPlaneSetupCoordinator serializes durable setup receipts, helper issuance, and runtime activation.
type NetworkDataPlaneSetupCoordinator struct {
	operations     NetworkDataPlaneSetupJournal
	network        NetworkDataPlaneSetupNetworkSource
	projections    NetworkDataPlaneSetupProjectionSource
	store          NetworkDataPlaneSetupStore
	roots          NetworkDataPlaneSetupRootSource
	trustPlans     ticketissuer.TrustPlanSource
	lowPortPlans   ticketissuer.LowPortPlanSource
	trustIssuers   func() (NetworkDataPlaneSetupTrustIssuer, error)
	lowPortIssuers func() (NetworkDataPlaneSetupLowPortIssuer, error)
	ownership      OwnershipObserver
	trust          NetworkDataPlaneSetupTrustObserver
	lowPorts       NetworkDataPlaneSetupLowPortObserver
	runtime        NetworkDataPlaneSetupRuntime
	endpoints      NetworkDataPlaneSetupEndpointBackfill
	platform       networkplan.Platform
	clock          helper.Clock
	mutex          sync.Mutex
}

// NewNetworkDataPlaneSetupCoordinator constructs the complete fail-fast trusted-ingress lifecycle.
func NewNetworkDataPlaneSetupCoordinator(operations NetworkDataPlaneSetupJournal, network NetworkDataPlaneSetupNetworkSource, projections NetworkDataPlaneSetupProjectionSource, store NetworkDataPlaneSetupStore, roots NetworkDataPlaneSetupRootSource, trustPlans ticketissuer.TrustPlanSource, lowPortPlans ticketissuer.LowPortPlanSource, trustIssuers func() (NetworkDataPlaneSetupTrustIssuer, error), lowPortIssuers func() (NetworkDataPlaneSetupLowPortIssuer, error), ownership OwnershipObserver, trust NetworkDataPlaneSetupTrustObserver, lowPorts NetworkDataPlaneSetupLowPortObserver, runtimeController NetworkDataPlaneSetupRuntime, endpoints NetworkDataPlaneSetupEndpointBackfill, platform networkplan.Platform, clock helper.Clock) *NetworkDataPlaneSetupCoordinator {
	if nilDependency(operations) || nilDependency(network) || nilDependency(projections) || nilDependency(store) || nilDependency(roots) || nilDependency(trustPlans) || nilDependency(lowPortPlans) || nilDependency(trustIssuers) || nilDependency(lowPortIssuers) || nilDependency(ownership) || nilDependency(trust) || nilDependency(lowPorts) || nilDependency(runtimeController) || nilDependency(endpoints) || nilDependency(clock) {
		panic("reconcile.NewNetworkDataPlaneSetupCoordinator requires every dependency")
	}
	return &NetworkDataPlaneSetupCoordinator{operations: operations, network: network, projections: projections, store: store, roots: roots, trustPlans: trustPlans, lowPortPlans: lowPortPlans, trustIssuers: trustIssuers, lowPortIssuers: lowPortIssuers, ownership: ownership, trust: trust, lowPorts: lowPorts, runtime: runtimeController, endpoints: endpoints, platform: platform, clock: clock}
}

// CurrentNetworkDataPlaneSetupPlatform returns the policy profile for this binary's native host contract.
func CurrentNetworkDataPlaneSetupPlatform() (networkplan.Platform, error) {
	return networkResolverSetupPlatform(runtime.GOOS)
}

// Start stages one global trust approval from the current resolver projection.
func (c *NetworkDataPlaneSetupCoordinator) Start(ctx context.Context, request NetworkDataPlaneSetupStartRequest) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	if existing, err := c.operations.OperationByIntent(ctx, request.IntentID); err == nil {
		if err := validateExistingNetworkDataPlaneSetupOperation(existing, request.IntentID); err != nil {
			return state.OperationRecord{}, err
		}
		if existing.Operation.State == domain.OperationRunning && existing.Operation.Phase == networkDataPlaneSetupActivationPhase {
			plan, found, readErr := c.operations.ReadNetworkDataPlaneSetupPlan(ctx, existing.Operation.ID)
			if readErr != nil || !found {
				return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: read activation receipt: %w", readErr)
			}
			result, resumeErr := c.resumeActivation(ctx, existing, request.RequesterIdentity, plan)
			if resumeErr != nil {
				return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: resume activation: %w", resumeErr)
			}
			return result.Operation, nil
		}
		return existing, nil
	} else {
		var missing *state.OperationIntentNotFoundError
		if !errors.As(err, &missing) {
			return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: read intent: %w", err)
		}
	}
	policy, projection, err := c.resolverAuthority(ctx, request.RequesterIdentity)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: %w", err)
	}
	op, err := domain.NewOperation(request.OperationID, request.IntentID, domain.OperationKindNetworkDataPlaneSetup, "", c.now(time.Time{}))
	if err != nil {
		return state.OperationRecord{}, err
	}
	result, err := c.operations.StageNetworkDataPlaneSetup(ctx, state.StageNetworkDataPlaneSetupRequest{Operation: op, Projection: projection, Policy: policy})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network data-plane setup: %w", err)
	}
	return result, nil
}

// PrepareTrust validates the exact durable trust phase before publishing helper authority.
func (c *NetworkDataPlaneSetupCoordinator) PrepareTrust(ctx context.Context, request NetworkDataPlaneSetupPrepareTrustRequest) (ticketissuer.TrustResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.TrustResult{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.TrustResult{}, err
	}
	plan, err := c.trustPlans.Resolve(ctx, ticketissuer.TrustRequest{OperationID: request.OperationID})
	if err != nil {
		return ticketissuer.TrustResult{}, fmt.Errorf("prepare trust: %w", err)
	}
	if err := c.validateTrustPlan(plan, request.OperationID, request.ExpectedOperationRevision, request.RequesterIdentity); err != nil {
		return ticketissuer.TrustResult{}, err
	}
	issuer, err := c.trustIssuers()
	if err != nil {
		return ticketissuer.TrustResult{}, fmt.Errorf("prepare trust: open issuer: %w", err)
	}
	if nilDependency(issuer) {
		return ticketissuer.TrustResult{}, fmt.Errorf("prepare trust: issuer is nil")
	}
	result, issueErr := issuer.Issue(ctx, request.RequesterIdentity, ticketissuer.TrustRequest{OperationID: request.OperationID})
	closeErr := issuer.Close()
	if issueErr != nil || closeErr != nil {
		return result, errors.Join(issueErr, closeErr)
	}
	if err := result.Validate(c.now(time.Time{})); err != nil {
		return ticketissuer.TrustResult{}, err
	}
	return result, nil
}

// ConfirmTrust independently proves trust, persists its sanitized receipt, and advances to low-port approval.
func (c *NetworkDataPlaneSetupCoordinator) ConfirmTrust(ctx context.Context, request NetworkDataPlaneSetupConfirmTrustRequest) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	plan, err := c.trustPlans.Resolve(ctx, ticketissuer.TrustRequest{OperationID: request.OperationID})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm trust: %w", err)
	}
	if err := c.validateTrustPlan(plan, request.OperationID, request.ExpectedOperationRevision, request.RequesterIdentity); err != nil {
		return state.OperationRecord{}, err
	}
	if request.TrustEvidence.AuthorityFingerprint != plan.Root.Fingerprint {
		return state.OperationRecord{}, fmt.Errorf("confirm trust: evidence belongs to another public root")
	}
	if request.TrustEvidence.Mechanism != plan.Policy.Mechanisms.Trust {
		return state.OperationRecord{}, fmt.Errorf("confirm trust: evidence belongs to another trust mechanism")
	}
	if err := c.observeExactTrust(ctx, plan, request.TrustEvidence); err != nil {
		return state.OperationRecord{}, err
	}
	projection, err := c.projections.Resolve(ctx, plan.Policy)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm trust: resolve projection: %w", err)
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.TrustEvidence)
	if err != nil {
		return state.OperationRecord{}, err
	}
	return c.operations.AdvanceNetworkDataPlaneSetupTrust(ctx, state.AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: request.RequesterIdentity, Projection: projection, Policy: plan.Policy, TrustEvidenceDigest: digest, TrustVerifiedAt: c.now(projection.NetworkUpdatedAt)})
}

// PrepareLowPorts validates one durable low-port approval before capability publication.
func (c *NetworkDataPlaneSetupCoordinator) PrepareLowPorts(ctx context.Context, request NetworkDataPlaneSetupPrepareLowPortsRequest) (ticketissuer.LowPortResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	plan, err := c.lowPortPlans.Resolve(ctx, ticketissuer.LowPortRequest{OperationID: request.OperationID})
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if err := c.validateLowPortPlan(plan, request.OperationID, request.ExpectedOperationRevision, request.RequesterIdentity); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	issuer, err := c.lowPortIssuers()
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if nilDependency(issuer) {
		return ticketissuer.LowPortResult{}, fmt.Errorf("prepare low ports: issuer is nil")
	}
	result, issueErr := issuer.Issue(ctx, request.RequesterIdentity, ticketissuer.LowPortRequest{OperationID: request.OperationID})
	closeErr := issuer.Close()
	if issueErr != nil || closeErr != nil {
		return result, errors.Join(issueErr, closeErr)
	}
	if err := result.Validate(c.now(time.Time{})); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	return result, nil
}

// ConfirmLowPorts stages exact full authority, activates it, starts listeners, backfills endpoints, and then succeeds.
func (c *NetworkDataPlaneSetupCoordinator) ConfirmLowPorts(ctx context.Context, request NetworkDataPlaneSetupConfirmLowPortsRequest) (NetworkDataPlaneSetupResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	plan, err := c.lowPortPlans.Resolve(ctx, ticketissuer.LowPortRequest{OperationID: request.OperationID})
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	if err := c.validateLowPortPlan(plan, request.OperationID, request.ExpectedOperationRevision, request.RequesterIdentity); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	if err := c.observeExactLowPorts(ctx, plan, request.LowPortEvidence); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	activation, err := c.activation(ctx, plan, request)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.LowPortEvidence)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	staged, err := c.operations.StageNetworkDataPlaneActivation(ctx, state.StageNetworkDataPlaneActivationRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: request.RequesterIdentity, LowPortEvidenceDigest: digest, Activation: activation})
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	full, err := c.store.ActivateNetworkDataPlane(ctx, staged.Activation)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("activate durable network: %w", err)
	}
	if _, err := c.operations.CompleteNetworkDataPlaneActivation(ctx, state.CompleteNetworkDataPlaneActivationRequest{OperationID: request.OperationID, ExpectedOperationRevision: staged.Operation.Revision, RequesterIdentity: request.RequesterIdentity}); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	if err := c.runtime.ActivateNetwork(ctx, full.Record.Revision); err != nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("activate network runtime: %w", err)
	}
	final, err := c.endpoints.ReconcileFullStageDefaultHTTPEndpoints(ctx)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("backfill default HTTP endpoints: %w", err)
	}
	if err := c.runtime.ActivateNetwork(ctx, final.Revision); err != nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("activate endpoint network runtime: %w", err)
	}
	completed, err := c.operations.CompleteNetworkDataPlaneSetup(ctx, state.CompleteNetworkDataPlaneSetupRequest{OperationID: request.OperationID, ExpectedOperationRevision: staged.Operation.Revision, RequesterIdentity: request.RequesterIdentity, At: c.now(staged.Operation.Operation.RequestedAt)})
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	result := NetworkDataPlaneSetupResult{Operation: completed, Network: state.NetworkMutationResult{Record: final}}
	if err := result.Validate(); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	return result, nil
}

// resumeActivation completes only a receipt that was already staged before a previous response or effect failed.
func (c *NetworkDataPlaneSetupCoordinator) resumeActivation(ctx context.Context, operation state.OperationRecord, requester string, plan state.NetworkDataPlaneSetupPlanRecord) (NetworkDataPlaneSetupResult, error) {
	if plan.Activation == nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("resume network data-plane activation: receipt unavailable")
	}
	if plan.Projection.ConfirmedOwnership.Record.OwnerIdentity != requester {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("resume network data-plane activation: authenticated requester does not match durable ownership")
	}
	full, err := c.store.ActivateNetworkDataPlane(ctx, *plan.Activation)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, fmt.Errorf("resume network data-plane activation: activate durable network: %w", err)
	}
	if _, err := c.operations.CompleteNetworkDataPlaneActivation(ctx, state.CompleteNetworkDataPlaneActivationRequest{OperationID: operation.Operation.ID, ExpectedOperationRevision: operation.Revision, RequesterIdentity: requester}); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	if err := c.runtime.ActivateNetwork(ctx, full.Record.Revision); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	final, err := c.endpoints.ReconcileFullStageDefaultHTTPEndpoints(ctx)
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	if err := c.runtime.ActivateNetwork(ctx, final.Revision); err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	completed, err := c.operations.CompleteNetworkDataPlaneSetup(ctx, state.CompleteNetworkDataPlaneSetupRequest{OperationID: operation.Operation.ID, ExpectedOperationRevision: operation.Revision, RequesterIdentity: requester, At: c.now(operation.Operation.RequestedAt)})
	if err != nil {
		return NetworkDataPlaneSetupResult{}, err
	}
	return NetworkDataPlaneSetupResult{Operation: completed, Network: state.NetworkMutationResult{Record: final}}, nil
}

// Recover resumes only the durable activation phase; approval phases retain their authenticated user boundary.
func (c *NetworkDataPlaneSetupCoordinator) Recover(ctx context.Context, operationID domain.OperationID) (state.OperationRecord, error) {
	if err := operationID.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	operation, err := c.operations.Operation(ctx, operationID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: read operation: %w", err)
	}
	if err := validateExistingNetworkDataPlaneSetupOperation(operation, operation.Operation.IntentID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: %w", err)
	}
	if operation.Operation.State != domain.OperationRunning || operation.Operation.Phase != networkDataPlaneSetupActivationPhase {
		return operation, nil
	}
	plan, found, err := c.operations.ReadNetworkDataPlaneSetupPlan(ctx, operationID)
	if err != nil || !found || plan.Activation == nil {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: activation receipt is unavailable: %w", err)
	}
	requester := plan.Projection.ConfirmedOwnership.Record.OwnerIdentity
	if requester == "" {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: persisted ownership requester is empty")
	}
	result, err := c.resumeActivation(ctx, operation, requester, plan)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("recover network data-plane setup: %w", err)
	}
	return result.Operation, nil
}

// resolverAuthority rebuilds the policy and reads its exact resolver predecessor.
func (c *NetworkDataPlaneSetupCoordinator) resolverAuthority(ctx context.Context, requester string) (networkpolicy.Policy, state.NetworkDataPlaneSetupProjection, error) {
	network, initialized, err := c.network.Network(ctx)
	if err != nil {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, err
	}
	if !initialized || network.Stage != state.NetworkStageResolver {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, fmt.Errorf("network data-plane setup requires resolver stage")
	}
	root, err := c.roots.PublicRoot()
	if err != nil {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, err
	}
	policy, err := networkplan.Build(networkplan.Request{Platform: c.platform, InstallationID: network.Ownership.InstallationID, Pool: network.Pool, AuthorityFingerprint: root.Fingerprint})
	if err != nil {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, err
	}
	projection, err := c.projections.Resolve(ctx, policy)
	if err != nil {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, err
	}
	if projection.ConfirmedOwnership.Record.OwnerIdentity != requester {
		return networkpolicy.Policy{}, state.NetworkDataPlaneSetupProjection{}, fmt.Errorf("authenticated requester does not own resolver authority")
	}
	return policy, projection, nil
}

// activation reobserves all mutable host facts immediately before durable full activation.
func (c *NetworkDataPlaneSetupCoordinator) activation(ctx context.Context, plan ticketissuer.LowPortPlan, request NetworkDataPlaneSetupConfirmLowPortsRequest) (state.ActivateNetworkDataPlaneRequest, error) {
	projection, err := c.projections.Resolve(ctx, plan.Policy)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	ownership, err := c.ownership.Observe(ctx)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	if ownership != projection.ConfirmedOwnership {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("ownership drifted before activation")
	}
	root, err := c.roots.PublicRoot()
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	// The helper-confirmed evidence is already checked before staging; use a fresh accepted observation for drift detection.
	trustRequest, err := trust.NewRequestForRequester(plan.TargetOwnership.InstallationID, request.RequesterIdentity, plan.Policy.Mechanisms.Trust, root)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	observedTrust, err := c.trust.Observe(ctx, trustRequest)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	if err := observedTrust.Validate(); err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("trust activation observation is invalid: %w", err)
	}
	if !sameNetworkDataPlaneSetupTrustRequest(observedTrust.Request, trustRequest) {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("trust activation observation belongs to another request")
	}
	assessment, err := observedTrust.Classify()
	if err != nil || (assessment.State != trust.StateExact && !(assessment.State == trust.StateForeign && assessment.Owned == trust.OwnedStateAbsent && identicalUnownedTrust(observedTrust))) {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("trust drifted before activation")
	}
	observedLowPorts, err := c.lowPorts.Observe(ctx, plan.NativeRequest)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	if err := observedLowPorts.Validate(); err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("low-port activation observation is invalid: %w", err)
	}
	if observedLowPorts.Request.InstallationID() != plan.NativeRequest.InstallationID() ||
		observedLowPorts.Request.OwnerUID() != plan.NativeRequest.OwnerUID() ||
		observedLowPorts.Request.PolicyFingerprint() != plan.NativeRequest.PolicyFingerprint() ||
		observedLowPorts.Request.HTTPUpstream() != plan.NativeRequest.HTTPUpstream() ||
		observedLowPorts.Request.HTTPSUpstream() != plan.NativeRequest.HTTPSUpstream() {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("low-port activation observation belongs to another request")
	}
	lowState, err := observedLowPorts.State()
	if err != nil || lowState != lowport.StateExact {
		return state.ActivateNetworkDataPlaneRequest{}, fmt.Errorf("low ports drifted before activation")
	}
	at := c.now(projection.NetworkUpdatedAt)
	generation := projection.ConfirmedOwnership.Record.Generation
	lowDigest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.LowPortEvidence)
	if err != nil {
		return state.ActivateNetworkDataPlaneRequest{}, err
	}
	return state.ActivateNetworkDataPlaneRequest{ExpectedNetworkRevision: projection.NetworkRevision, ConfirmedOwnership: projection.ConfirmedOwnership, Policy: plan.Policy, Setup: []state.NetworkSetupProof{projection.ResolverProof, {Component: state.NetworkSetupComponentLowPorts, Evidence: lowDigest, Generation: generation, VerifiedAt: at}}, Listeners: listenersForPolicy(plan.Policy, generation, at), At: at}, nil
}

// listenersForPolicy derives durable listener receipts from canonical policy instead of mutable runtime facts.
func listenersForPolicy(policy networkpolicy.Policy, generation uint64, at time.Time) state.SharedListenerReservations {
	reservation := func(listener networkpolicy.Listener) state.ListenerReservation {
		mode := state.ListenerModeRedirect
		if listener.Advertised == listener.Bind {
			mode = state.ListenerModeDirect
		}
		return state.ListenerReservation{Mode: mode, Advertised: listener.Advertised, Bind: listener.Bind, Generation: generation, VerifiedAt: at}
	}
	return state.SharedListenerReservations{DNS: reservation(policy.DNS), HTTP: reservation(policy.HTTP), HTTPS: reservation(policy.HTTPS)}
}

// now returns a canonical time that cannot precede an established durable boundary.
func (c *NetworkDataPlaneSetupCoordinator) now(lower time.Time) time.Time {
	at := c.clock.Now().UTC().Round(0)
	if !lower.IsZero() && at.Before(lower) {
		return lower.UTC().Round(0)
	}
	return at
}

// validateExistingNetworkDataPlaneSetupOperation verifies an intent replay remains within this global lifecycle.
func validateExistingNetworkDataPlaneSetupOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if record.Operation.IntentID != intentID || record.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || record.Operation.ProjectID != "" {
		return fmt.Errorf("network data-plane setup replay does not match the requested global intent")
	}
	if err := record.Operation.Validate(); err != nil {
		return err
	}
	return validateOperationRevision(record.Revision)
}

// validateTrustPlan binds issuer authority to the authenticated trust approval revision.
func (c *NetworkDataPlaneSetupCoordinator) validateTrustPlan(plan ticketissuer.TrustPlan, operationID domain.OperationID, revision domain.Sequence, requester string) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("trust plan: %w", err)
	}
	if plan.OperationID != operationID {
		return fmt.Errorf("trust plan belongs to another operation")
	}
	if plan.OperationRevision != revision {
		return &state.StaleRevisionError{OperationID: operationID, Expected: revision, Actual: plan.OperationRevision}
	}
	if plan.TargetOwnership.OwnerIdentity != requester {
		return fmt.Errorf("authenticated requester does not match trust approval owner")
	}
	return nil
}

// validateLowPortPlan binds issuer authority to the authenticated schema-two low-port approval revision.
func (c *NetworkDataPlaneSetupCoordinator) validateLowPortPlan(plan ticketissuer.LowPortPlan, operationID domain.OperationID, revision domain.Sequence, requester string) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("low-port plan: %w", err)
	}
	if plan.Operation.ID != operationID {
		return fmt.Errorf("low-port plan belongs to another operation")
	}
	if plan.OperationRevision != revision {
		return &state.StaleRevisionError{OperationID: operationID, Expected: revision, Actual: plan.OperationRevision}
	}
	if plan.TargetOwnership.OwnerIdentity != requester {
		return fmt.Errorf("authenticated requester does not match low-port approval owner")
	}
	return nil
}

// observeExactTrust independently accepts only an owned exact root or a byte-identical unowned preexisting root.
func (c *NetworkDataPlaneSetupCoordinator) observeExactTrust(ctx context.Context, plan ticketissuer.TrustPlan, evidence helper.TrustMutationEvidence) error {
	request, err := trust.NewRequestForRequester(plan.TargetOwnership.InstallationID, plan.TargetOwnership.OwnerIdentity, plan.Policy.Mechanisms.Trust, plan.Root)
	if err != nil {
		return fmt.Errorf("construct trust observation: %w", err)
	}
	observation, err := c.trust.Observe(ctx, request)
	if err != nil {
		return fmt.Errorf("observe trust: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("trust observation is invalid: %w", err)
	}
	if !sameNetworkDataPlaneSetupTrustRequest(observation.Request, request) {
		return fmt.Errorf("trust observation belongs to another request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint trust observation: %w", err)
	}
	if fingerprint != evidence.ObservationFingerprint {
		return fmt.Errorf("trust evidence differs from independently observed trust")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return fmt.Errorf("classify trust: %w", err)
	}
	if assessment.State == trust.StateExact {
		return nil
	}
	if assessment.State == trust.StateForeign && assessment.Owned == trust.OwnedStateAbsent && evidence.Postcondition == helper.TrustPostconditionPreexisting && identicalUnownedTrust(observation) {
		return nil
	}
	return fmt.Errorf("trust state is %q, not exact owned or identical preexisting", assessment.State)
}

// sameNetworkDataPlaneSetupTrustRequest compares complete canonical trust authority returned by an observer.
func sameNetworkDataPlaneSetupTrustRequest(left trust.Request, right trust.Request) bool {
	leftRoot := left.Root()
	rightRoot := right.Root()
	return left.InstallationID() == right.InstallationID() &&
		left.RequesterIdentity() == right.RequesterIdentity() &&
		left.Mechanism() == right.Mechanism() &&
		leftRoot.Fingerprint == rightRoot.Fingerprint &&
		leftRoot.NotBefore.Equal(rightRoot.NotBefore) &&
		leftRoot.NotAfter.Equal(rightRoot.NotAfter) &&
		bytes.Equal(leftRoot.CertificatePEM, rightRoot.CertificatePEM)
}

// identicalUnownedTrust permits the preexisting branch only for an exact copy of the selected public CA.
func identicalUnownedTrust(observation trust.Observation) bool {
	found := false
	for _, entry := range observation.Entries {
		if entry.Owner != nil {
			return false
		}
		if entry.CertificateFingerprint != observation.Request.AuthorityFingerprint() {
			continue
		}
		if !entry.NativeExact {
			return false
		}
		found = true
	}
	return found
}

// observeExactLowPorts independently accepts only the complete schema-two paired service postcondition.
func (c *NetworkDataPlaneSetupCoordinator) observeExactLowPorts(ctx context.Context, plan ticketissuer.LowPortPlan, evidence helper.LowPortMutationEvidence) error {
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if evidence.PolicyFingerprint != policyFingerprint || evidence.OwnershipFingerprint != ownershipFingerprint {
		return fmt.Errorf("low-port evidence belongs to another schema-two policy")
	}
	observation, err := c.lowPorts.Observe(ctx, plan.NativeRequest)
	if err != nil {
		return fmt.Errorf("observe low ports: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("low-port observation is invalid: %w", err)
	}
	if observation.Request.InstallationID() != plan.NativeRequest.InstallationID() ||
		observation.Request.OwnerUID() != plan.NativeRequest.OwnerUID() ||
		observation.Request.PolicyFingerprint() != plan.NativeRequest.PolicyFingerprint() ||
		observation.Request.HTTPUpstream() != plan.NativeRequest.HTTPUpstream() ||
		observation.Request.HTTPSUpstream() != plan.NativeRequest.HTTPSUpstream() {
		return fmt.Errorf("low-port observation belongs to another request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint low ports: %w", err)
	}
	if fingerprint != evidence.ObservationFingerprint {
		return fmt.Errorf("low-port evidence differs from independently observed service")
	}
	current, err := observation.State()
	if err != nil {
		return fmt.Errorf("classify low ports: %w", err)
	}
	if current != lowport.StateExact {
		return fmt.Errorf("low-port state is %q, want exact", current)
	}
	return nil
}
