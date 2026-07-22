package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certroot"
)

// globalNetworkReleaseRuntimeOperationPhase identifies the only operation state that may own a live release plan.
const globalNetworkReleaseRuntimeOperationPhase = "releasing network runtime"

// GlobalNetworkReleaseJournal owns idempotent global-release staging and recovery reads.
type GlobalNetworkReleaseJournal interface {
	// OperationByIntent returns the operation already assigned to an idempotency intent.
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	// StageGlobalNetworkRelease atomically retains authority before any runtime effect occurs.
	StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error)
	// ReadActiveGlobalNetworkReleasePlan returns the one durable in-progress release plan.
	ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseStateSource supplies the current network and stopped-project revisions.
type GlobalNetworkReleaseStateSource interface {
	// RuntimeState returns a coherent durable network and project snapshot.
	RuntimeState(context.Context) (state.RuntimeState, error)
	// GlobalNetworkReleaseProjectRevisions returns the exact canonical durable project revision set.
	GlobalNetworkReleaseProjectRevisions(context.Context, domain.Sequence) ([]state.NetworkProjectRevision, error)
}

// GlobalNetworkReleaseProjectionSource returns the current full projection for a canonical policy.
type GlobalNetworkReleaseProjectionSource interface {
	// Resolve reads the exact full setup projection bound to policy.
	Resolve(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error)
}

// GlobalNetworkReleaseRootSource supplies the current public root.
type GlobalNetworkReleaseRootSource interface {
	// PublicRoot returns the public root retained by the daemon.
	PublicRoot() (certroot.Root, error)
}

// GlobalNetworkReleaseRuntime releases only the in-process full network runtime.
type GlobalNetworkReleaseRuntime interface {
	// ReleaseNetworkRuntime idempotently advances the durable plan to low ports.
	ReleaseNetworkRuntime(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, error)
}

// GlobalNetworkReleaseResolverObserver observes the exact canonical resolver authority without mutating it.
type GlobalNetworkReleaseResolverObserver interface {
	// Observe returns bounded native resolver facts for one immutable request.
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
}

// GlobalNetworkReleaseCoordinator stages daemon-observed release authority then retires runtime listeners.
type GlobalNetworkReleaseCoordinator struct {
	journal     GlobalNetworkReleaseJournal
	state       GlobalNetworkReleaseStateSource
	projections GlobalNetworkReleaseProjectionSource
	roots       GlobalNetworkReleaseRootSource
	ownership   OwnershipObserver
	lowPorts    NetworkDataPlaneSetupLowPortObserver
	resolver    GlobalNetworkReleaseResolverObserver
	trust       NetworkDataPlaneSetupTrustObserver
	loopback    LoopbackObserver
	runtime     GlobalNetworkReleaseRuntime
	platform    networkplan.Platform
	clock       helper.Clock
	mutex       sync.Mutex
}

// GlobalNetworkReleaseStartRequest identifies a caller intent and daemon-assigned global operation.
type GlobalNetworkReleaseStartRequest struct {
	OperationID       domain.OperationID
	IntentID          domain.IntentID
	RequesterIdentity string
}

// Validate rejects unauthenticated or malformed release initiation.
func (request GlobalNetworkReleaseStartRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := request.IntentID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NewGlobalNetworkReleaseCoordinator constructs a fail-closed global release boundary.
func NewGlobalNetworkReleaseCoordinator(
	journal GlobalNetworkReleaseJournal,
	source GlobalNetworkReleaseStateSource,
	projections GlobalNetworkReleaseProjectionSource,
	roots GlobalNetworkReleaseRootSource,
	ownershipObserver OwnershipObserver,
	lowPorts NetworkDataPlaneSetupLowPortObserver,
	resolverObserver GlobalNetworkReleaseResolverObserver,
	trustObserver NetworkDataPlaneSetupTrustObserver,
	loopbackObserver LoopbackObserver,
	runtimeReleaser GlobalNetworkReleaseRuntime,
	platform networkplan.Platform,
	clock helper.Clock,
) *GlobalNetworkReleaseCoordinator {
	if nilDependency(journal) ||
		nilDependency(source) ||
		nilDependency(projections) ||
		nilDependency(roots) ||
		nilDependency(ownershipObserver) ||
		nilDependency(lowPorts) ||
		nilDependency(resolverObserver) ||
		nilDependency(trustObserver) ||
		nilDependency(loopbackObserver) ||
		nilDependency(runtimeReleaser) ||
		nilDependency(clock) {
		panic("reconcile.NewGlobalNetworkReleaseCoordinator requires every dependency")
	}
	return &GlobalNetworkReleaseCoordinator{
		journal:     journal,
		state:       source,
		projections: projections,
		roots:       roots,
		ownership:   ownershipObserver,
		lowPorts:    lowPorts,
		resolver:    resolverObserver,
		trust:       trustObserver,
		loopback:    loopbackObserver,
		runtime:     runtimeReleaser,
		platform:    platform,
		clock:       clock,
	}
}

// CurrentGlobalNetworkReleasePlatform returns this binary's platform policy profile.
func CurrentGlobalNetworkReleasePlatform() (networkplan.Platform, error) {
	return networkResolverSetupPlatform(runtime.GOOS)
}

// Start stages fresh daemon-owned authority and releases runtime listeners through the low-port checkpoint.
func (c *GlobalNetworkReleaseCoordinator) Start(ctx context.Context, request GlobalNetworkReleaseStartRequest) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start global network release: %w", err)
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	existing, err := c.journal.OperationByIntent(ctx, request.IntentID)
	if err == nil {
		if err := validateGlobalNetworkReleaseOperation(existing, request.IntentID); err != nil {
			return state.OperationRecord{}, fmt.Errorf("start global network release: replay: %w", err)
		}
		return c.resume(ctx, existing.Operation.ID, request.RequesterIdentity)
	}
	var missing *state.OperationIntentNotFoundError
	if !errors.As(err, &missing) {
		return state.OperationRecord{}, fmt.Errorf("start global network release: read intent: %w", err)
	}
	authority, err := c.authority(ctx, request.RequesterIdentity)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start global network release: %w", err)
	}
	op, err := domain.NewOperation(request.OperationID, request.IntentID, domain.OperationKindNetworkRelease, "", c.clock.Now().UTC())
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start global network release: create operation: %w", err)
	}
	staged, err := c.journal.StageGlobalNetworkRelease(ctx, state.StageGlobalNetworkReleaseRequest{
		Operation: op,
		Authority: authority.Clone(),
	})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start global network release: stage: %w", err)
	}
	if err := validateGlobalNetworkReleaseOperation(staged, request.IntentID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start global network release: staged operation: %w", err)
	}
	return c.resume(ctx, staged.Operation.ID, request.RequesterIdentity)
}

// Recover resumes an already-staged release after the runtime has installed its cold anchor.
func (c *GlobalNetworkReleaseCoordinator) Recover(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, found, err := c.journal.ReadActiveGlobalNetworkReleasePlan(ctx)
	if err != nil {
		return fmt.Errorf("recover global network release: read active plan: %w", err)
	}
	if !found {
		return nil
	}
	if err := validateGlobalNetworkReleaseOperation(plan.Operation, plan.Operation.Operation.IntentID); err != nil {
		return fmt.Errorf("recover global network release: active plan: %w", err)
	}
	if err := plan.Phase.Validate(); err != nil {
		return fmt.Errorf("recover global network release: active plan phase: %w", err)
	}
	if plan.Phase != state.GlobalNetworkReleasePlanPhaseRuntimeRelease && plan.Phase != state.GlobalNetworkReleasePlanPhaseLowPorts {
		return nil
	}
	if _, err := c.runtime.ReleaseNetworkRuntime(ctx, plan.Operation.Operation.ID); err != nil {
		return fmt.Errorf("recover global network release: release runtime: %w", err)
	}
	return nil
}

// resume calls the runtime only while the matching durable plan still owns its runtime checkpoint.
func (c *GlobalNetworkReleaseCoordinator) resume(ctx context.Context, operationID domain.OperationID, requester string) (state.OperationRecord, error) {
	plan, found, err := c.journal.ReadActiveGlobalNetworkReleasePlan(ctx)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read active plan: %w", err)
	}
	if !found || plan.Operation.Operation.ID != operationID {
		return state.OperationRecord{}, fmt.Errorf("active release plan does not match operation")
	}
	if err := validateGlobalNetworkReleaseOperation(plan.Operation, plan.Operation.Operation.IntentID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("active release plan: %w", err)
	}
	if err := plan.Phase.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("active release plan phase: %w", err)
	}
	if plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity != requester {
		return state.OperationRecord{}, fmt.Errorf("authenticated requester does not own active release authority")
	}
	if plan.Phase == state.GlobalNetworkReleasePlanPhaseRuntimeRelease || plan.Phase == state.GlobalNetworkReleasePlanPhaseLowPorts {
		if _, err := c.runtime.ReleaseNetworkRuntime(ctx, operationID); err != nil {
			return state.OperationRecord{}, fmt.Errorf("release runtime: %w", err)
		}
	}
	return plan.Operation, nil
}

// authority rebuilds every release fact from independent daemon-owned observations.
func (c *GlobalNetworkReleaseCoordinator) authority(ctx context.Context, requester string) (state.GlobalNetworkReleaseAuthority, error) {
	runtimeState, err := c.state.RuntimeState(ctx)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, err
	}
	if err := runtimeState.Validate(); err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("runtime state: %w", err)
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageFull {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("global network release requires full network")
	}
	root, err := c.roots.PublicRoot()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("public root: %w", err)
	}
	policy, err := networkplan.Build(networkplan.Request{
		Platform:             c.platform,
		InstallationID:       runtimeState.Network.Ownership.InstallationID,
		Pool:                 runtimeState.Network.Pool,
		AuthorityFingerprint: root.Fingerprint,
	})
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("build policy: %w", err)
	}
	projection, err := c.projections.Resolve(ctx, policy)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("full projection: %w", err)
	}
	if projection.Stage != state.NetworkStageFull || projection.ConfirmedOwnership.Record.OwnerIdentity != requester {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("authenticated requester does not own full authority")
	}
	observedOwnership, err := c.ownership.Observe(ctx)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("ownership: %w", err)
	}
	if observedOwnership != projection.ConfirmedOwnership {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("ownership differs from full projection")
	}
	lowRequest, err := lowport.NewRequest(projection.ConfirmedOwnership.Record, policy)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("low-port request: %w", err)
	}
	low, err := c.lowPorts.Observe(ctx, lowRequest)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("low ports: %w", err)
	}
	if low.Request != lowRequest {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("low-port observation belongs to another request")
	}
	lowState, err := low.State()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("classify low ports: %w", err)
	}
	if lowState != lowport.StateExact {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("low ports are not exact")
	}
	lowFingerprint, err := low.Fingerprint()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("fingerprint low ports: %w", err)
	}
	resolverRequest, err := resolver.NewRequest(projection.ConfirmedOwnership.Record.InstallationID, policy)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("resolver request: %w", err)
	}
	resolverObservation, err := c.resolver.Observe(ctx, resolverRequest)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("resolver: %w", err)
	}
	if resolverObservation.Request != resolverRequest {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("resolver observation belongs to another request")
	}
	resolverAssessment, err := resolverObservation.Classify()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("classify resolver: %w", err)
	}
	if resolverAssessment.State != resolver.StateExact {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("resolver is not exact")
	}
	resolverFingerprint, err := resolverObservation.Fingerprint()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("fingerprint resolver: %w", err)
	}
	trustRequest, err := trust.NewRequestForRequester(projection.ConfirmedOwnership.Record.InstallationID, requester, policy.Mechanisms.Trust, root)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("trust request: %w", err)
	}
	trustObservation, err := c.trust.Observe(ctx, trustRequest)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("trust: %w", err)
	}
	if !sameNetworkDataPlaneSetupTrustRequest(trustObservation.Request, trustRequest) {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("trust observation belongs to another request")
	}
	trustAssessment, err := trustObservation.Classify()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("classify trust: %w", err)
	}
	disposition := state.GlobalNetworkReleaseTrustOwned
	if trustAssessment.State != trust.StateExact {
		if trustAssessment.State != trust.StateForeign || trustAssessment.Owned != trust.OwnedStateAbsent || !globalReleaseIdenticalUnownedTrust(trustObservation) {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("trust is not exact owned or identical preexisting")
		}
		disposition = state.GlobalNetworkReleaseTrustPreexistingUnowned
	}
	trustFingerprint, err := trustObservation.Fingerprint()
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("fingerprint trust: %w", err)
	}
	candidates := runtimeState.Network.Pool.Candidates()
	targets := make([]state.GlobalNetworkReleaseLoopbackTarget, 0, len(candidates))
	for _, address := range candidates {
		observation, err := c.loopback.Observe(ctx, address)
		if err != nil {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("loopback %s: %w", address, err)
		}
		if observation.Address != address {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("loopback observation for %s belongs to %s", address, observation.Address)
		}
		if observation.State != loopback.StateExact {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("loopback %s is not exact", address)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("fingerprint loopback %s: %w", address, err)
		}
		targets = append(targets, state.GlobalNetworkReleaseLoopbackTarget{
			Address:                address,
			ObservationFingerprint: fingerprint,
		})
	}
	for _, snapshot := range runtimeState.Snapshot.Projects {
		if snapshot.State != domain.ProjectStopped {
			return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("project %q is not stopped", snapshot.ID)
		}
	}
	projects, err := c.state.GlobalNetworkReleaseProjectRevisions(ctx, runtimeState.Snapshot.Sequence)
	if err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("project revisions: %w", err)
	}
	projects = slices.Clone(projects)
	if len(projects) != len(runtimeState.Snapshot.Projects) {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("project revision set differs from runtime snapshot")
	}
	authority := state.GlobalNetworkReleaseAuthority{
		Projection:                     projection,
		Policy:                         policy,
		Root:                           cloneGlobalReleaseRoot(root),
		ExpectedOwnershipFingerprint:   projection.ConfirmedOwnership.Fingerprint,
		TrustDisposition:               disposition,
		LowPortObservationFingerprint:  lowFingerprint,
		ResolverObservationFingerprint: resolverFingerprint,
		TrustObservationFingerprint:    trustFingerprint,
		LoopbackTargets:                targets,
		ProjectRevisions:               projects,
	}
	if err := authority.Validate(); err != nil {
		return state.GlobalNetworkReleaseAuthority{}, fmt.Errorf("validate global network release authority: %w", err)
	}
	return authority, nil
}

// globalReleaseIdenticalUnownedTrust permits preservation only of byte-identical unowned public-root facts.
func globalReleaseIdenticalUnownedTrust(observation trust.Observation) bool {
	found := false
	for _, entry := range observation.Entries {
		if entry.Owner != nil {
			return false
		}
		if entry.CertificateFingerprint == observation.Request.AuthorityFingerprint() {
			if !entry.NativeExact {
				return false
			}
			found = true
		}
	}
	return found
}

// cloneGlobalReleaseRoot prevents backend-owned public certificate bytes escaping into durable authority.
func cloneGlobalReleaseRoot(root certroot.Root) certroot.Root {
	root.CertificatePEM = bytes.Clone(root.CertificatePEM)
	return root
}

// validateGlobalNetworkReleaseOperation rejects an intent replay that is not the exact global release operation.
func validateGlobalNetworkReleaseOperation(record state.OperationRecord, intent domain.IntentID) error {
	if record.Operation.IntentID != intent ||
		record.Operation.Kind != domain.OperationKindNetworkRelease ||
		record.Operation.ProjectID != "" {
		return fmt.Errorf("operation does not match global release intent")
	}
	if err := record.Operation.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return err
	}
	if record.Operation.State != domain.OperationRunning || record.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return fmt.Errorf(
			"operation is %q/%q, expected %q/%q",
			record.Operation.State,
			record.Operation.Phase,
			domain.OperationRunning,
			globalNetworkReleaseRuntimeOperationPhase,
		)
	}
	return nil
}
