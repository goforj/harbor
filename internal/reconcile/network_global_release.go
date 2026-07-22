package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
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
	// ReadGlobalNetworkReleasePlan returns the active plan for one exact operation.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error)
	// AdvanceGlobalNetworkReleaseLowPorts commits the independently verified release receipt.
	AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// AdvanceGlobalNetworkReleaseResolver commits the independently verified resolver-release receipt.
	AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// AdvanceGlobalNetworkReleaseTrust commits the independently verified trust-release receipt.
	AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error)
}

// GlobalNetworkReleaseLowPortIssuer issues a bounded low-port release capability.
type GlobalNetworkReleaseLowPortIssuer interface {
	// Issue publishes one release-low-ports capability for its authenticated owner.
	Issue(context.Context, string, ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error)
	// Close releases issuer resources after one publication attempt.
	Close() error
}

// GlobalNetworkReleaseResolverIssuer issues a bounded resolver release capability.
type GlobalNetworkReleaseResolverIssuer interface {
	// Issue publishes one release-resolver capability for its authenticated owner.
	Issue(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error)
	// Close releases issuer resources after one publication attempt.
	Close() error
}

// GlobalNetworkReleaseTrustIssuer issues a bounded trust release capability.
type GlobalNetworkReleaseTrustIssuer interface {
	// Issue publishes one release-trust capability for its authenticated owner.
	Issue(context.Context, string, ticketissuer.TrustRequest) (ticketissuer.TrustResult, error)
	// Close releases issuer resources after one publication attempt.
	Close() error
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
	journal         GlobalNetworkReleaseJournal
	state           GlobalNetworkReleaseStateSource
	projections     GlobalNetworkReleaseProjectionSource
	roots           GlobalNetworkReleaseRootSource
	ownership       OwnershipObserver
	lowPorts        NetworkDataPlaneSetupLowPortObserver
	lowPortPlans    ticketissuer.LowPortPlanSource
	lowPortIssuers  func() (GlobalNetworkReleaseLowPortIssuer, error)
	resolverPlans   ticketissuer.ResolverPlanSource
	resolverIssuers func() (GlobalNetworkReleaseResolverIssuer, error)
	trustPlans      ticketissuer.TrustPlanSource
	trustIssuers    func() (GlobalNetworkReleaseTrustIssuer, error)
	resolver        GlobalNetworkReleaseResolverObserver
	trust           NetworkDataPlaneSetupTrustObserver
	loopback        LoopbackObserver
	runtime         GlobalNetworkReleaseRuntime
	platform        networkplan.Platform
	clock           helper.Clock
	mutex           sync.Mutex
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
	lowPortPlans ticketissuer.LowPortPlanSource,
	lowPortIssuers func() (GlobalNetworkReleaseLowPortIssuer, error),
	resolverPlans ticketissuer.ResolverPlanSource,
	resolverIssuers func() (GlobalNetworkReleaseResolverIssuer, error),
	trustPlans ticketissuer.TrustPlanSource,
	trustIssuers func() (GlobalNetworkReleaseTrustIssuer, error),
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
		nilDependency(lowPortPlans) ||
		nilDependency(lowPortIssuers) ||
		nilDependency(resolverPlans) ||
		nilDependency(resolverIssuers) ||
		nilDependency(trustPlans) ||
		nilDependency(trustIssuers) ||
		nilDependency(resolverObserver) ||
		nilDependency(trustObserver) ||
		nilDependency(loopbackObserver) ||
		nilDependency(runtimeReleaser) ||
		nilDependency(clock) {
		panic("reconcile.NewGlobalNetworkReleaseCoordinator requires every dependency")
	}
	return &GlobalNetworkReleaseCoordinator{
		journal:         journal,
		state:           source,
		projections:     projections,
		roots:           roots,
		ownership:       ownershipObserver,
		lowPorts:        lowPorts,
		lowPortPlans:    lowPortPlans,
		lowPortIssuers:  lowPortIssuers,
		resolverPlans:   resolverPlans,
		resolverIssuers: resolverIssuers,
		trustPlans:      trustPlans,
		trustIssuers:    trustIssuers,
		resolver:        resolverObserver,
		trust:           trustObserver,
		loopback:        loopbackObserver,
		runtime:         runtimeReleaser,
		platform:        platform,
		clock:           clock,
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

// GlobalNetworkReleasePrepareLowPortsRequest selects one owner-approved release-low-ports checkpoint.
type GlobalNetworkReleasePrepareLowPortsRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences preparation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner requesting helper authority.
	RequesterIdentity string
}

// Validate rejects malformed release-low-ports publication input.
func (request GlobalNetworkReleasePrepareLowPortsRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// GlobalNetworkReleaseConfirmLowPortsRequest carries the helper's bounded removal postcondition.
type GlobalNetworkReleaseConfirmLowPortsRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences confirmation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner confirming helper evidence.
	RequesterIdentity string
	// LowPortEvidence proves the low-port release postcondition.
	LowPortEvidence helper.LowPortMutationEvidence
}

// Validate rejects malformed release-low-ports confirmation input.
func (request GlobalNetworkReleaseConfirmLowPortsRequest) Validate() error {
	prepare := GlobalNetworkReleasePrepareLowPortsRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
	}
	if err := prepare.Validate(); err != nil {
		return err
	}
	return validateGlobalNetworkReleaseLowPortEvidence(request.LowPortEvidence)
}

// PrepareLowPorts validates one durable release checkpoint before publishing a removal capability.
func (c *GlobalNetworkReleaseCoordinator) PrepareLowPorts(ctx context.Context, request GlobalNetworkReleasePrepareLowPortsRequest) (ticketissuer.LowPortResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	plan, durable, err := c.releaseLowPortPlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if err := validateGlobalNetworkReleaseLowPortPlan(plan, request.OperationID, request.ExpectedCheckpointRevision, request.RequesterIdentity); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseLowPorts {
		return ticketissuer.LowPortResult{}, fmt.Errorf("release low-port publication requires plan phase %q, found %q", state.GlobalNetworkReleasePlanPhaseLowPorts, durable.Phase)
	}
	issuer, err := c.lowPortIssuers()
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if nilDependency(issuer) {
		return ticketissuer.LowPortResult{}, fmt.Errorf("prepare release low ports: issuer is nil")
	}
	result, issueErr := issuer.Issue(ctx, request.RequesterIdentity, ticketissuer.LowPortRequest{
		OperationID: request.OperationID,
	})
	closeErr := issuer.Close()
	if issueErr != nil || closeErr != nil {
		return result, errors.Join(issueErr, closeErr)
	}
	if err := result.Validate(c.clock.Now().UTC().Round(0)); err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if result.OperationID != request.OperationID || result.Operation != helper.OperationReleaseLowPorts {
		return ticketissuer.LowPortResult{}, fmt.Errorf("prepare release low ports: issuer returned another operation")
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return ticketissuer.LowPortResult{}, err
	}
	if result.PolicyFingerprint != policyFingerprint || result.OwnershipFingerprint != ownershipFingerprint {
		return ticketissuer.LowPortResult{}, fmt.Errorf("prepare release low ports: issuer result belongs to another policy or ownership")
	}
	return result, nil
}

// ConfirmLowPorts independently verifies removal and advances the release plan to resolver retirement.
func (c *GlobalNetworkReleaseCoordinator) ConfirmLowPorts(ctx context.Context, request GlobalNetworkReleaseConfirmLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	plan, durable, err := c.releaseLowPortPlan(ctx, request.OperationID)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if err := validateGlobalNetworkReleaseLowPortPlan(plan, request.OperationID, request.ExpectedCheckpointRevision, request.RequesterIdentity); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if err := c.observeAbsentReleaseLowPorts(ctx, plan, request.LowPortEvidence); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.LowPortEvidence)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	verifiedAt := c.releaseNow(durable.NetworkUpdatedAt)
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseResolver && durable.LowPortReceipt != nil {
		verifiedAt = durable.LowPortReceipt.VerifiedAt
	}
	return c.journal.AdvanceGlobalNetworkReleaseLowPorts(ctx, state.AdvanceGlobalNetworkReleaseLowPortsRequest{
		OperationID:        request.OperationID,
		CheckpointRevision: plan.CheckpointRevision,
		NetworkRevision:    durable.NetworkRevision,
		Receipt: state.GlobalNetworkReleaseLowPortReceipt{
			SourceCheckpointRevision:          plan.CheckpointRevision,
			LowPortEvidenceDigest:             digest,
			OwnedAbsentObservationFingerprint: request.LowPortEvidence.ObservationFingerprint,
			VerifiedAt:                        verifiedAt,
		},
	})
}

// releaseLowPortPlan re-resolves the plan so replay can validate the committed resolver receipt.
func (c *GlobalNetworkReleaseCoordinator) releaseLowPortPlan(ctx context.Context, operationID domain.OperationID) (ticketissuer.LowPortPlan, state.GlobalNetworkReleasePlanRecord, error) {
	durable, found, err := c.journal.ReadGlobalNetworkReleasePlan(ctx, operationID)
	if err != nil {
		return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("read release low-port plan: %w", err)
	}
	if !found || durable.Operation.Operation.ID != operationID {
		return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release low-port plan does not match operation")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseLowPorts {
		plan, err := c.lowPortPlans.Resolve(ctx, ticketissuer.LowPortRequest{
			OperationID: operationID,
		})
		if err != nil {
			return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
		}
		if err := validateGlobalNetworkReleaseLowPortDurablePlan(plan, durable); err != nil {
			return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
		}
		return plan, durable, nil
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseResolver || durable.LowPortReceipt == nil {
		return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release low-port plan phase is %q", durable.Phase)
	}
	native, err := lowport.NewRequest(durable.Authority.Projection.ConfirmedOwnership.Record, durable.Authority.Policy)
	if err != nil {
		return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("derive release low-port request: %w", err)
	}
	plan := ticketissuer.LowPortPlan{
		Purpose:            ticketissuer.LowPortPlanPurposeGlobalNetworkRelease,
		Operation:          durable.Operation.Operation,
		OperationRevision:  durable.Operation.Revision,
		CheckpointRevision: durable.LowPortReceipt.SourceCheckpointRevision,
		CheckpointPhase:    ticketissuer.LowPortCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseLowPorts,
		TargetOwnership:    durable.Authority.Projection.ConfirmedOwnership.Record,
		Policy:             durable.Authority.Policy,
		NativeRequest:      native,
	}
	if err := validateGlobalNetworkReleaseLowPortDurablePlan(plan, durable); err != nil {
		return ticketissuer.LowPortPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
	}
	return plan, durable, nil
}

// validateGlobalNetworkReleaseLowPortPlan binds a user request to the sole release-only ticket purpose.
func validateGlobalNetworkReleaseLowPortPlan(plan ticketissuer.LowPortPlan, operationID domain.OperationID, checkpoint domain.Sequence, requester string) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("release low-port plan: %w", err)
	}
	if plan.Purpose != ticketissuer.LowPortPlanPurposeGlobalNetworkRelease || plan.Mutation != helper.OperationReleaseLowPorts {
		return fmt.Errorf("release low-port plan is not release_low_ports authority")
	}
	if plan.Operation.ID != operationID {
		return fmt.Errorf("release low-port plan belongs to another operation")
	}
	if plan.CheckpointRevision != checkpoint {
		return &state.StaleRevisionError{
			OperationID: operationID,
			Expected:    checkpoint,
			Actual:      plan.CheckpointRevision,
		}
	}
	if plan.TargetOwnership.OwnerIdentity != requester {
		return fmt.Errorf("authenticated requester does not own release low-port authority")
	}
	return nil
}

// validateGlobalNetworkReleaseLowPortDurablePlan verifies a capability source cannot drift from its journaled authority.
func validateGlobalNetworkReleaseLowPortDurablePlan(plan ticketissuer.LowPortPlan, durable state.GlobalNetworkReleasePlanRecord) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("release low-port plan: %w", err)
	}
	if err := validateGlobalNetworkReleaseOperation(durable.Operation, durable.Operation.Operation.IntentID); err != nil {
		return fmt.Errorf("release low-port operation: %w", err)
	}
	if !sameGlobalNetworkReleaseOperation(plan.Operation, durable.Operation.Operation) || plan.OperationRevision != durable.Operation.Revision {
		return fmt.Errorf("release low-port plan operation differs from durable authority")
	}
	if plan.Purpose != ticketissuer.LowPortPlanPurposeGlobalNetworkRelease ||
		plan.CheckpointPhase != ticketissuer.LowPortCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseLowPorts {
		return fmt.Errorf("release low-port plan purpose differs from durable authority")
	}
	if plan.TargetOwnership != durable.Authority.Projection.ConfirmedOwnership.Record || plan.Policy != durable.Authority.Policy {
		return fmt.Errorf("release low-port plan policy or ownership differs from durable authority")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseLowPorts && plan.CheckpointRevision != durable.CheckpointRevision {
		return fmt.Errorf("release low-port plan checkpoint differs from durable authority")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseResolver &&
		(durable.LowPortReceipt == nil || plan.CheckpointRevision != durable.LowPortReceipt.SourceCheckpointRevision) {
		return fmt.Errorf("release low-port replay checkpoint differs from durable receipt")
	}
	return nil
}

// observeAbsentReleaseLowPorts accepts only complete exact authority-bound owned-absence facts.
func (c *GlobalNetworkReleaseCoordinator) observeAbsentReleaseLowPorts(ctx context.Context, plan ticketissuer.LowPortPlan, evidence helper.LowPortMutationEvidence) error {
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if evidence.PolicyFingerprint != policyFingerprint || evidence.OwnershipFingerprint != ownershipFingerprint {
		return fmt.Errorf("release low-port evidence belongs to another policy or ownership")
	}
	observation, err := c.lowPorts.Observe(ctx, plan.NativeRequest)
	if err != nil {
		return fmt.Errorf("observe release low ports: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("release low-port observation is invalid: %w", err)
	}
	if observation.Request != plan.NativeRequest || !observation.Complete {
		return fmt.Errorf("release low-port observation does not prove the exact complete request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint release low ports: %w", err)
	}
	if fingerprint != evidence.ObservationFingerprint {
		return fmt.Errorf("release low-port evidence differs from independently observed service")
	}
	current, err := observation.State()
	if err != nil {
		return fmt.Errorf("classify release low ports: %w", err)
	}
	if current != lowport.StateAbsent {
		return fmt.Errorf("release low-port state is %q, want absent", current)
	}
	return nil
}

// releaseNow preserves the durable authority's lower time bound for its receipt.
func (c *GlobalNetworkReleaseCoordinator) releaseNow(lower time.Time) time.Time {
	at := c.clock.Now().UTC().Round(0)
	if at.Before(lower) {
		return lower.UTC().Round(0)
	}
	return at
}

// validateGlobalNetworkReleaseLowPortEvidence admits only one owned-absence removal postcondition.
func validateGlobalNetworkReleaseLowPortEvidence(evidence helper.LowPortMutationEvidence) error {
	if !canonicalNetworkDataPlaneFingerprint(evidence.PolicyFingerprint) {
		return fmt.Errorf("release low-port evidence policy fingerprint is invalid")
	}
	if !canonicalNetworkDataPlaneFingerprint(evidence.OwnershipFingerprint) {
		return fmt.Errorf("release low-port evidence ownership fingerprint is invalid")
	}
	if !canonicalNetworkDataPlaneFingerprint(evidence.ObservationFingerprint) {
		return fmt.Errorf("release low-port evidence observation fingerprint is invalid")
	}
	if evidence.Postcondition != helper.LowPortPostconditionOwnedAbsent {
		return fmt.Errorf("release low-port evidence must prove owned_absent")
	}
	return nil
}

// GlobalNetworkReleasePrepareTrustRequest selects one trust-release checkpoint.
type GlobalNetworkReleasePrepareTrustRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences preparation to one retained trust checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner requesting helper authority.
	RequesterIdentity string
}

// GlobalNetworkReleaseTrustPreparation reports whether a trust capability was required for the retained disposition.
type GlobalNetworkReleaseTrustPreparation struct {
	// Disposition identifies whether Harbor owns the trust entry.
	Disposition state.GlobalNetworkReleaseTrustDisposition
	// Ticket is present only when Harbor owns and may remove the trust entry.
	Ticket *ticketissuer.TrustResult
}

// Validate rejects a preparation that could blur owned removal with preexisting preservation.
func (preparation GlobalNetworkReleaseTrustPreparation) Validate(now time.Time) error {
	if err := preparation.Disposition.Validate(); err != nil {
		return err
	}
	switch preparation.Disposition {
	case state.GlobalNetworkReleaseTrustOwned:
		if preparation.Ticket == nil {
			return fmt.Errorf("owned release trust preparation has no ticket")
		}
		if err := preparation.Ticket.Validate(now); err != nil {
			return fmt.Errorf("owned release trust preparation ticket: %w", err)
		}
		if preparation.Ticket.Operation != helper.OperationReleaseTrust {
			return fmt.Errorf("owned release trust preparation ticket is not release_trust authority")
		}
	case state.GlobalNetworkReleaseTrustPreexistingUnowned:
		if preparation.Ticket != nil {
			return fmt.Errorf("preexisting release trust preparation has a ticket")
		}
	}
	return nil
}

// Validate rejects malformed release-trust publication input.
func (request GlobalNetworkReleasePrepareTrustRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// GlobalNetworkReleaseConfirmTrustRequest selects one trust checkpoint and its bounded confirmation evidence.
type GlobalNetworkReleaseConfirmTrustRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences confirmation to one retained trust checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner confirming trust state.
	RequesterIdentity string
	// TrustEvidence proves owned absence when the staged disposition is owned.
	TrustEvidence helper.TrustMutationEvidence
}

// Validate rejects malformed trust confirmation input before disposition-specific validation.
func (request GlobalNetworkReleaseConfirmTrustRequest) Validate() error {
	return (GlobalNetworkReleasePrepareTrustRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
	}).Validate()
}

// PrepareTrust selects either owned removal authority or confirmation-only preservation.
func (c *GlobalNetworkReleaseCoordinator) PrepareTrust(ctx context.Context, request GlobalNetworkReleasePrepareTrustRequest) (GlobalNetworkReleaseTrustPreparation, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	durable, err := c.releaseTrustDurable(ctx, GlobalNetworkReleaseConfirmTrustRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
	})
	if err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseTrust {
		return GlobalNetworkReleaseTrustPreparation{}, fmt.Errorf("release trust preparation requires plan phase %q", state.GlobalNetworkReleasePlanPhaseTrust)
	}
	if durable.Authority.TrustDisposition == state.GlobalNetworkReleaseTrustPreexistingUnowned {
		preparation := GlobalNetworkReleaseTrustPreparation{
			Disposition: durable.Authority.TrustDisposition,
		}
		return preparation, preparation.Validate(c.clock.Now().UTC())
	}
	plan, err := c.trustPlans.Resolve(ctx, ticketissuer.TrustRequest{
		OperationID: request.OperationID,
	})
	if err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	if err := validateGlobalNetworkReleaseTrustPlan(plan, durable, request); err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	result, issueErr := c.issueReleaseTrust(
		ctx,
		request.RequesterIdentity,
		ticketissuer.TrustRequest{
			OperationID: request.OperationID,
		},
	)
	if issueErr != nil {
		if errors.Is(issueErr, ticketissuer.ErrTrustPublicationIndeterminate) {
			if validationErr := validateGlobalNetworkReleaseTrustResult(result, plan, c.clock.Now().UTC()); validationErr != nil {
				return GlobalNetworkReleaseTrustPreparation{}, fmt.Errorf("validate indeterminate release trust result: %w", validationErr)
			}
			preparation := GlobalNetworkReleaseTrustPreparation{
				Disposition: durable.Authority.TrustDisposition,
				Ticket:      &result,
			}
			if validationErr := preparation.Validate(c.clock.Now().UTC()); validationErr != nil {
				return GlobalNetworkReleaseTrustPreparation{}, validationErr
			}
			return preparation, issueErr
		}
		return GlobalNetworkReleaseTrustPreparation{}, issueErr
	}
	if err := validateGlobalNetworkReleaseTrustResult(result, plan, c.clock.Now().UTC()); err != nil {
		return GlobalNetworkReleaseTrustPreparation{}, err
	}
	preparation := GlobalNetworkReleaseTrustPreparation{
		Disposition: durable.Authority.TrustDisposition,
		Ticket:      &result,
	}
	return preparation, preparation.Validate(c.clock.Now().UTC())
}

// ConfirmTrust independently verifies trust removal or preservation and advances to loopback release.
func (c *GlobalNetworkReleaseCoordinator) ConfirmTrust(ctx context.Context, request GlobalNetworkReleaseConfirmTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	durable, err := c.releaseTrustDurable(ctx, request)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	evidence, err := c.observeReleaseTrust(ctx, durable, request.TrustEvidence)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(evidence)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	checkpoint := durable.CheckpointRevision
	verifiedAt := c.releaseNow(durable.ResolverReceipt.VerifiedAt)
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseLoopbacks {
		checkpoint = durable.TrustReceipt.SourceCheckpointRevision
		verifiedAt = durable.TrustReceipt.VerifiedAt
	}
	return c.journal.AdvanceGlobalNetworkReleaseTrust(ctx, state.AdvanceGlobalNetworkReleaseTrustRequest{
		OperationID:        request.OperationID,
		CheckpointRevision: checkpoint,
		NetworkRevision:    durable.NetworkRevision,
		Receipt: state.GlobalNetworkReleaseTrustReceipt{
			SourceCheckpointRevision: checkpoint,
			Disposition:              durable.Authority.TrustDisposition,
			ConfirmationDigest:       digest,
			ObservationFingerprint:   evidence.ObservationFingerprint,
			VerifiedAt:               verifiedAt,
		},
	})
}

// validateGlobalNetworkReleaseTrustPlan binds a user request to owned release-only trust authority.
func validateGlobalNetworkReleaseTrustPlan(plan ticketissuer.TrustPlan, durable state.GlobalNetworkReleasePlanRecord, request GlobalNetworkReleasePrepareTrustRequest) error {
	if err := validateGlobalNetworkReleaseTrustDurablePlan(plan, durable); err != nil {
		return err
	}
	if plan.CheckpointRevision != request.ExpectedCheckpointRevision {
		return &state.StaleRevisionError{
			OperationID: request.OperationID,
			Expected:    request.ExpectedCheckpointRevision,
			Actual:      plan.CheckpointRevision,
		}
	}
	if plan.TargetOwnership.OwnerIdentity != request.RequesterIdentity {
		return fmt.Errorf("authenticated requester does not own release trust authority")
	}
	return nil
}

// validateGlobalNetworkReleaseTrustDurablePlan verifies the trust capability cannot drift from durable release authority.
func validateGlobalNetworkReleaseTrustDurablePlan(plan ticketissuer.TrustPlan, durable state.GlobalNetworkReleasePlanRecord) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("release trust plan: %w", err)
	}
	if err := validateGlobalNetworkReleaseTrustDurable(durable, durable.Operation.Operation.ID); err != nil {
		return err
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseTrust ||
		durable.Authority.TrustDisposition != state.GlobalNetworkReleaseTrustOwned {
		return fmt.Errorf("release trust plan is not live owned authority")
	}
	if !sameGlobalNetworkReleaseOperation(plan.Operation, durable.Operation.Operation) ||
		plan.OperationRevision != durable.Operation.Revision {
		return fmt.Errorf("release trust plan operation differs from durable authority")
	}
	if plan.Purpose != ticketissuer.TrustPlanPurposeGlobalNetworkRelease ||
		plan.CheckpointPhase != ticketissuer.TrustCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseTrust {
		return fmt.Errorf("release trust plan purpose differs from durable authority")
	}
	if plan.TargetOwnership != durable.Authority.Projection.ConfirmedOwnership.Record ||
		plan.Policy != durable.Authority.Policy ||
		!sameGlobalNetworkReleaseTrustRoot(plan.Root, durable.Authority.Root) {
		return fmt.Errorf("release trust plan target differs from durable authority")
	}
	if durable.ResolverReceipt == nil || durable.LowPortReceipt == nil {
		return fmt.Errorf("release trust plan has no committed predecessor receipts")
	}
	if plan.CheckpointRevision != durable.CheckpointRevision {
		return fmt.Errorf("release trust plan checkpoint differs from durable authority")
	}
	return nil
}

// sameGlobalNetworkReleaseTrustRoot compares root metadata and certificate bytes without pointer identity.
func sameGlobalNetworkReleaseTrustRoot(left certroot.Root, right certroot.Root) bool {
	return left.Fingerprint == right.Fingerprint &&
		left.NotBefore.Equal(right.NotBefore) &&
		left.NotAfter.Equal(right.NotAfter) &&
		bytes.Equal(left.CertificatePEM, right.CertificatePEM)
}

// validateGlobalNetworkReleaseTrustResult confirms an issuer returned the exact release-trust authority.
func validateGlobalNetworkReleaseTrustResult(result ticketissuer.TrustResult, plan ticketissuer.TrustPlan, now time.Time) error {
	if err := result.Validate(now); err != nil {
		return err
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if result.OperationID != plan.Operation.ID || result.Operation != helper.OperationReleaseTrust {
		return fmt.Errorf("release trust issuer returned another operation")
	}
	if result.PolicyFingerprint != policyFingerprint ||
		result.OwnershipFingerprint != ownershipFingerprint ||
		result.AuthorityFingerprint != plan.Root.Fingerprint ||
		result.Mechanism != plan.Policy.Mechanisms.Trust {
		return fmt.Errorf("release trust issuer returned another authority")
	}
	return nil
}

// issueReleaseTrust opens one issuer only after durable validation and always closes it.
func (c *GlobalNetworkReleaseCoordinator) issueReleaseTrust(ctx context.Context, requester string, request ticketissuer.TrustRequest) (ticketissuer.TrustResult, error) {
	issuer, err := c.trustIssuers()
	if err != nil {
		return ticketissuer.TrustResult{}, fmt.Errorf("open release trust issuer: %w", err)
	}
	if nilDependency(issuer) {
		return ticketissuer.TrustResult{}, fmt.Errorf("prepare release trust: issuer is nil")
	}
	result, issueErr := issuer.Issue(ctx, requester, request)
	closeErr := issuer.Close()
	if issueErr == nil && closeErr == nil {
		return result, nil
	}
	if issueErr == nil || errors.Is(issueErr, ticketissuer.ErrTrustPublicationIndeterminate) {
		return result, errors.Join(ticketissuer.ErrTrustPublicationIndeterminate, issueErr, closeErr)
	}
	return ticketissuer.TrustResult{}, errors.Join(issueErr, closeErr)
}

// releaseTrustDurable validates a live trust checkpoint or its exact loopback replay boundary.
func (c *GlobalNetworkReleaseCoordinator) releaseTrustDurable(ctx context.Context, request GlobalNetworkReleaseConfirmTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	durable, found, err := c.journal.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("read release trust plan: %w", err)
	}
	if !found {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release trust plan does not match durable authority")
	}
	if err := validateGlobalNetworkReleaseTrustDurable(durable, request.OperationID); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if durable.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity != request.RequesterIdentity {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("authenticated requester does not own release trust authority")
	}
	checkpoint := durable.CheckpointRevision
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseLoopbacks {
		checkpoint = durable.TrustReceipt.SourceCheckpointRevision
	}
	if checkpoint != request.ExpectedCheckpointRevision {
		return state.GlobalNetworkReleasePlanRecord{}, &state.StaleRevisionError{
			OperationID: request.OperationID,
			Expected:    request.ExpectedCheckpointRevision,
			Actual:      checkpoint,
		}
	}
	return durable, nil
}

// validateGlobalNetworkReleaseTrustDurable proves a journal result retains the exact trust-phase authority before it can drive host effects.
func validateGlobalNetworkReleaseTrustDurable(durable state.GlobalNetworkReleasePlanRecord, operationID domain.OperationID) error {
	if durable.Operation.Operation.ID != operationID {
		return fmt.Errorf("release trust plan does not match durable authority")
	}
	if err := validateGlobalNetworkReleaseOperation(durable.Operation, durable.Operation.Operation.IntentID); err != nil {
		return fmt.Errorf("release trust operation: %w", err)
	}
	if err := durable.Phase.Validate(); err != nil {
		return fmt.Errorf("release trust plan phase: %w", err)
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseTrust && durable.Phase != state.GlobalNetworkReleasePlanPhaseLoopbacks {
		return fmt.Errorf("release trust plan phase is %q", durable.Phase)
	}
	if err := validateOperationRevision(durable.CheckpointRevision); err != nil {
		return fmt.Errorf("release trust checkpoint revision: %w", err)
	}
	if durable.CheckpointRevision <= durable.Operation.Revision {
		return fmt.Errorf(
			"release trust checkpoint revision %d does not follow operation revision %d",
			durable.CheckpointRevision,
			durable.Operation.Revision,
		)
	}
	if err := validateOperationRevision(durable.NetworkRevision); err != nil {
		return fmt.Errorf("release trust network revision: %w", err)
	}
	if err := durable.Authority.Validate(); err != nil {
		return fmt.Errorf("release trust authority: %w", err)
	}
	if durable.NetworkRevision != durable.Authority.Projection.NetworkRevision ||
		!durable.NetworkUpdatedAt.Equal(durable.Authority.Projection.NetworkUpdatedAt) {
		return fmt.Errorf("release trust network boundary differs from durable authority")
	}
	if durable.LowPortReceipt == nil || durable.ResolverReceipt == nil {
		return fmt.Errorf("release trust plan has no committed predecessor receipts")
	}
	if err := durable.LowPortReceipt.Validate(); err != nil {
		return fmt.Errorf("release trust low-port receipt: %w", err)
	}
	if durable.LowPortReceipt.SourceCheckpointRevision <= durable.Operation.Revision {
		return fmt.Errorf("release trust low-port receipt does not follow operation revision")
	}
	if durable.LowPortReceipt.VerifiedAt.Before(durable.NetworkUpdatedAt) {
		return fmt.Errorf("release trust low-port receipt verification precedes network authority")
	}
	if err := durable.ResolverReceipt.Validate(); err != nil {
		return fmt.Errorf("release trust resolver receipt: %w", err)
	}
	if durable.ResolverReceipt.VerifiedAt.Before(durable.NetworkUpdatedAt) ||
		durable.ResolverReceipt.VerifiedAt.Before(durable.LowPortReceipt.VerifiedAt) {
		return fmt.Errorf("release trust resolver receipt verification precedes durable authority")
	}
	switch durable.Phase {
	case state.GlobalNetworkReleasePlanPhaseTrust:
		if durable.TrustReceipt != nil {
			return fmt.Errorf("release trust phase retains a premature trust receipt")
		}
		if durable.LowPortReceipt.SourceCheckpointRevision+1 >= durable.CheckpointRevision {
			return fmt.Errorf("release trust low-port receipt does not precede the trust checkpoint")
		}
		if durable.ResolverReceipt.SourceCheckpointRevision+1 != durable.CheckpointRevision {
			return fmt.Errorf("release trust resolver receipt source checkpoint does not precede trust checkpoint")
		}
	case state.GlobalNetworkReleasePlanPhaseLoopbacks:
		if durable.TrustReceipt == nil {
			return fmt.Errorf("release loopback phase has no trust receipt")
		}
		if err := durable.TrustReceipt.Validate(); err != nil {
			return fmt.Errorf("release trust receipt: %w", err)
		}
		if durable.TrustReceipt.Disposition != durable.Authority.TrustDisposition {
			return fmt.Errorf("release trust receipt disposition differs from durable authority")
		}
		if durable.LowPortReceipt.SourceCheckpointRevision+1 >= durable.CheckpointRevision {
			return fmt.Errorf("release trust low-port receipt does not precede the loopback checkpoint")
		}
		if durable.ResolverReceipt.SourceCheckpointRevision+1 >= durable.CheckpointRevision {
			return fmt.Errorf("release trust resolver receipt does not precede the loopback checkpoint")
		}
		if durable.TrustReceipt.SourceCheckpointRevision+1 != durable.CheckpointRevision {
			return fmt.Errorf("release trust receipt source checkpoint does not bound loopback replay")
		}
		if durable.TrustReceipt.VerifiedAt.Before(durable.ResolverReceipt.VerifiedAt) {
			return fmt.Errorf("release trust receipt verification precedes resolver receipt")
		}
	}
	return nil
}

// observeReleaseTrust validates disposition-specific bounded evidence against a fresh exact native observation.
func (c *GlobalNetworkReleaseCoordinator) observeReleaseTrust(ctx context.Context, durable state.GlobalNetworkReleasePlanRecord, evidence helper.TrustMutationEvidence) (helper.TrustMutationEvidence, error) {
	if durable.Authority.TrustDisposition == state.GlobalNetworkReleaseTrustPreexistingUnowned &&
		evidence != (helper.TrustMutationEvidence{}) {
		return helper.TrustMutationEvidence{}, fmt.Errorf("preexisting release trust confirmation must not carry helper evidence")
	}
	if durable.Authority.TrustDisposition == state.GlobalNetworkReleaseTrustOwned &&
		(!canonicalNetworkDataPlaneFingerprint(evidence.AuthorityFingerprint) ||
			!canonicalNetworkDataPlaneFingerprint(evidence.ObservationFingerprint) ||
			evidence.AuthorityFingerprint != durable.Authority.Root.Fingerprint ||
			evidence.Mechanism != durable.Authority.Policy.Mechanisms.Trust) {
		return helper.TrustMutationEvidence{}, fmt.Errorf("release trust evidence belongs to another authority")
	}
	native, err := trust.NewRequestForRequester(
		durable.Authority.Projection.ConfirmedOwnership.Record.InstallationID,
		durable.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		durable.Authority.Policy.Mechanisms.Trust,
		durable.Authority.Root,
	)
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("derive release trust request: %w", err)
	}
	observation, err := c.trust.Observe(ctx, native)
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("observe release trust: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("release trust observation is invalid: %w", err)
	}
	if !sameNetworkDataPlaneSetupTrustRequest(observation.Request, native) || !observation.Complete {
		return helper.TrustMutationEvidence{}, fmt.Errorf("release trust observation does not prove the exact complete request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("fingerprint release trust: %w", err)
	}
	if durable.Authority.TrustDisposition == state.GlobalNetworkReleaseTrustOwned && fingerprint != evidence.ObservationFingerprint {
		return helper.TrustMutationEvidence{}, fmt.Errorf("release trust evidence differs from independently observed trust")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("classify release trust: %w", err)
	}
	switch durable.Authority.TrustDisposition {
	case state.GlobalNetworkReleaseTrustOwned:
		if evidence.Postcondition != helper.TrustPostconditionOwnedAbsent || assessment.Owned != trust.OwnedStateAbsent {
			return helper.TrustMutationEvidence{}, fmt.Errorf("release trust does not prove owned absence")
		}
	case state.GlobalNetworkReleaseTrustPreexistingUnowned:
		if fingerprint != durable.Authority.TrustObservationFingerprint ||
			assessment.State != trust.StateForeign ||
			assessment.Owned != trust.OwnedStateAbsent ||
			!globalReleaseIdenticalUnownedTrust(observation) {
			return helper.TrustMutationEvidence{}, fmt.Errorf("release trust does not preserve exact preexisting root")
		}
		evidence = helper.TrustMutationEvidence{
			Changed:                false,
			AuthorityFingerprint:   durable.Authority.Root.Fingerprint,
			Mechanism:              durable.Authority.Policy.Mechanisms.Trust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionPreexisting,
		}
	default:
		return helper.TrustMutationEvidence{}, fmt.Errorf("release trust disposition is unsupported")
	}
	return evidence, nil
}

// GlobalNetworkReleasePrepareResolverRequest selects one owner-approved release-resolver checkpoint.
type GlobalNetworkReleasePrepareResolverRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences preparation to one retained resolver checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner requesting helper authority.
	RequesterIdentity string
}

// Validate rejects malformed release-resolver publication input.
func (request GlobalNetworkReleasePrepareResolverRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// GlobalNetworkReleaseConfirmResolverRequest carries the helper's bounded resolver-removal postcondition.
type GlobalNetworkReleaseConfirmResolverRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID
	// ExpectedCheckpointRevision fences confirmation to one retained resolver checkpoint.
	ExpectedCheckpointRevision domain.Sequence
	// RequesterIdentity identifies the authenticated owner confirming helper evidence.
	RequesterIdentity string
	// ResolverEvidence proves the resolver release postcondition.
	ResolverEvidence helper.ResolverMutationEvidence
}

// Validate rejects malformed release-resolver confirmation input.
func (request GlobalNetworkReleaseConfirmResolverRequest) Validate() error {
	prepare := GlobalNetworkReleasePrepareResolverRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
	}
	if err := prepare.Validate(); err != nil {
		return err
	}
	return validateGlobalNetworkReleaseResolverEvidence(request.ResolverEvidence)
}

// PrepareResolver validates one durable release checkpoint before publishing a resolver-removal capability.
func (c *GlobalNetworkReleaseCoordinator) PrepareResolver(ctx context.Context, request GlobalNetworkReleasePrepareResolverRequest) (ticketissuer.ResolverResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	plan, durable, err := c.releaseResolverPlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	if err := validateGlobalNetworkReleaseResolverPlan(plan, request.OperationID, request.ExpectedCheckpointRevision, request.RequesterIdentity); err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseResolver {
		return ticketissuer.ResolverResult{}, fmt.Errorf("release resolver publication requires plan phase %q, found %q", state.GlobalNetworkReleasePlanPhaseResolver, durable.Phase)
	}
	result, issueErr := c.issueReleaseResolver(ctx, request.RequesterIdentity, ticketissuer.ResolverRequest{
		OperationID: request.OperationID,
	})
	if issueErr != nil {
		if errors.Is(issueErr, ticketissuer.ErrResolverPublicationIndeterminate) {
			if validationErr := validateGlobalNetworkReleaseResolverResult(result, plan, c.clock.Now().UTC()); validationErr != nil {
				return ticketissuer.ResolverResult{}, fmt.Errorf("validate indeterminate release resolver result: %w", validationErr)
			}
			return result, issueErr
		}
		return ticketissuer.ResolverResult{}, issueErr
	}
	if err := validateGlobalNetworkReleaseResolverResult(result, plan, c.clock.Now().UTC()); err != nil {
		return ticketissuer.ResolverResult{}, err
	}
	return result, nil
}

// ConfirmResolver independently verifies removal and advances the release plan to trust retirement.
func (c *GlobalNetworkReleaseCoordinator) ConfirmResolver(ctx context.Context, request GlobalNetworkReleaseConfirmResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	ctx = normalizeContext(ctx)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	plan, durable, err := c.releaseResolverPlan(ctx, request.OperationID)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if err := validateGlobalNetworkReleaseResolverPlan(plan, request.OperationID, request.ExpectedCheckpointRevision, request.RequesterIdentity); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if err := c.observeAbsentReleaseResolver(ctx, plan, request.ResolverEvidence); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.ResolverEvidence)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	verifiedAt := c.releaseNow(durable.LowPortReceipt.VerifiedAt)
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseTrust && durable.ResolverReceipt != nil {
		verifiedAt = durable.ResolverReceipt.VerifiedAt
	}
	return c.journal.AdvanceGlobalNetworkReleaseResolver(ctx, state.AdvanceGlobalNetworkReleaseResolverRequest{
		OperationID:        request.OperationID,
		CheckpointRevision: plan.CheckpointRevision,
		NetworkRevision:    durable.NetworkRevision,
		Receipt: state.GlobalNetworkReleaseResolverReceipt{
			SourceCheckpointRevision:          plan.CheckpointRevision,
			ResolverEvidenceDigest:            digest,
			OwnedAbsentObservationFingerprint: request.ResolverEvidence.ObservationFingerprint,
			VerifiedAt:                        verifiedAt,
		},
	})
}

// releaseResolverPlan resolves a live resolver plan or reconstructs an exact replay plan from its durable receipt.
func (c *GlobalNetworkReleaseCoordinator) releaseResolverPlan(ctx context.Context, operationID domain.OperationID) (ticketissuer.ResolverPlan, state.GlobalNetworkReleasePlanRecord, error) {
	durable, found, err := c.journal.ReadGlobalNetworkReleasePlan(ctx, operationID)
	if err != nil {
		return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("read release resolver plan: %w", err)
	}
	if !found || durable.Operation.Operation.ID != operationID {
		return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release resolver plan does not match operation")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseResolver {
		plan, err := c.resolverPlans.Resolve(ctx, ticketissuer.ResolverRequest{OperationID: operationID})
		if err != nil {
			return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
		}
		if err := validateGlobalNetworkReleaseResolverDurablePlan(plan, durable); err != nil {
			return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
		}
		return plan, durable, nil
	}
	if durable.Phase != state.GlobalNetworkReleasePlanPhaseTrust || durable.ResolverReceipt == nil {
		return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release resolver plan phase is %q", durable.Phase)
	}
	ownershipFingerprint, err := durable.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("fingerprint release resolver ownership: %w", err)
	}
	plan := ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposeGlobalRelease,
		Operation:                          durable.Operation.Operation,
		OperationRevision:                  durable.Operation.Revision,
		CheckpointRevision:                 durable.ResolverReceipt.SourceCheckpointRevision,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhaseGlobalRelease,
		Mutation:                           helper.OperationReleaseResolver,
		ExpectedSourceOwnershipFingerprint: ownershipFingerprint,
		TargetOwnership:                    durable.Authority.Projection.ConfirmedOwnership.Record,
		Policy:                             durable.Authority.Policy,
	}
	if err := validateGlobalNetworkReleaseResolverDurablePlan(plan, durable); err != nil {
		return ticketissuer.ResolverPlan{}, state.GlobalNetworkReleasePlanRecord{}, err
	}
	return plan, durable, nil
}

// validateGlobalNetworkReleaseResolverPlan binds a user request to the sole release-only resolver purpose.
func validateGlobalNetworkReleaseResolverPlan(plan ticketissuer.ResolverPlan, operationID domain.OperationID, checkpoint domain.Sequence, requester string) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("release resolver plan: %w", err)
	}
	if plan.Purpose != ticketissuer.ResolverPlanPurposeGlobalRelease || plan.Mutation != helper.OperationReleaseResolver {
		return fmt.Errorf("release resolver plan is not release_resolver authority")
	}
	if plan.Operation.ID != operationID {
		return fmt.Errorf("release resolver plan belongs to another operation")
	}
	if plan.CheckpointRevision != checkpoint {
		return &state.StaleRevisionError{
			OperationID: operationID,
			Expected:    checkpoint,
			Actual:      plan.CheckpointRevision,
		}
	}
	if plan.TargetOwnership.OwnerIdentity != requester {
		return fmt.Errorf("authenticated requester does not own release resolver authority")
	}
	return nil
}

// validateGlobalNetworkReleaseResolverDurablePlan verifies resolver authority cannot drift from the journaled release plan.
func validateGlobalNetworkReleaseResolverDurablePlan(plan ticketissuer.ResolverPlan, durable state.GlobalNetworkReleasePlanRecord) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("release resolver plan: %w", err)
	}
	if err := validateGlobalNetworkReleaseOperation(durable.Operation, durable.Operation.Operation.IntentID); err != nil {
		return fmt.Errorf("release resolver operation: %w", err)
	}
	if !sameGlobalNetworkReleaseOperation(plan.Operation, durable.Operation.Operation) || plan.OperationRevision != durable.Operation.Revision {
		return fmt.Errorf("release resolver plan operation differs from durable authority")
	}
	if plan.Purpose != ticketissuer.ResolverPlanPurposeGlobalRelease ||
		plan.CheckpointPhase != ticketissuer.ResolverCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseResolver {
		return fmt.Errorf("release resolver plan purpose differs from durable authority")
	}
	if plan.TargetOwnership != durable.Authority.Projection.ConfirmedOwnership.Record || plan.Policy != durable.Authority.Policy {
		return fmt.Errorf("release resolver plan policy or ownership differs from durable authority")
	}
	if durable.LowPortReceipt == nil {
		return fmt.Errorf("release resolver plan has no committed low-port receipt")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseResolver && plan.CheckpointRevision != durable.CheckpointRevision {
		return fmt.Errorf("release resolver plan checkpoint differs from durable authority")
	}
	if durable.Phase == state.GlobalNetworkReleasePlanPhaseTrust &&
		(durable.ResolverReceipt == nil || plan.CheckpointRevision != durable.ResolverReceipt.SourceCheckpointRevision) {
		return fmt.Errorf("release resolver replay checkpoint differs from durable receipt")
	}
	return nil
}

// sameGlobalNetworkReleaseOperation compares operation facts without treating equivalent pointer allocations as authority drift.
func sameGlobalNetworkReleaseOperation(left domain.Operation, right domain.Operation) bool {
	return left.ID == right.ID &&
		left.IntentID == right.IntentID &&
		left.Kind == right.Kind &&
		left.ProjectID == right.ProjectID &&
		left.State == right.State &&
		left.Phase == right.Phase &&
		left.RequestedAt.Equal(right.RequestedAt) &&
		sameGlobalNetworkReleaseOperationTime(left.StartedAt, right.StartedAt) &&
		sameGlobalNetworkReleaseOperationTime(left.FinishedAt, right.FinishedAt) &&
		sameGlobalNetworkReleaseOperationProblem(left.Problem, right.Problem)
}

// sameGlobalNetworkReleaseOperationTime compares optional lifecycle times by timestamp value.
func sameGlobalNetworkReleaseOperationTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// sameGlobalNetworkReleaseOperationProblem compares optional operation problems by immutable value.
func sameGlobalNetworkReleaseOperationProblem(left *domain.Problem, right *domain.Problem) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// validateGlobalNetworkReleaseResolverResult confirms the issuer returned the exact planned mutation authority.
func validateGlobalNetworkReleaseResolverResult(result ticketissuer.ResolverResult, plan ticketissuer.ResolverPlan, now time.Time) error {
	if err := result.Validate(now); err != nil {
		return err
	}
	if result.OperationID != plan.Operation.ID || result.Operation != helper.OperationReleaseResolver {
		return fmt.Errorf("release resolver issuer returned another operation")
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if result.PolicyFingerprint != policyFingerprint || result.OwnershipFingerprint != ownershipFingerprint {
		return fmt.Errorf("release resolver issuer result belongs to another policy or ownership")
	}
	return nil
}

// observeAbsentReleaseResolver accepts complete owned-absence facts and preserves unrelated foreign resolver rules.
func (c *GlobalNetworkReleaseCoordinator) observeAbsentReleaseResolver(ctx context.Context, plan ticketissuer.ResolverPlan, evidence helper.ResolverMutationEvidence) error {
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return err
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return err
	}
	if evidence.PolicyFingerprint != policyFingerprint || evidence.OwnershipFingerprint != ownershipFingerprint {
		return fmt.Errorf("release resolver evidence belongs to another policy or ownership")
	}
	native, err := resolver.NewRequest(plan.TargetOwnership.InstallationID, plan.Policy)
	if err != nil {
		return fmt.Errorf("derive release resolver request: %w", err)
	}
	observation, err := c.resolver.Observe(ctx, native)
	if err != nil {
		return fmt.Errorf("observe release resolver: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("release resolver observation is invalid: %w", err)
	}
	if observation.Request != native || !observation.Complete {
		return fmt.Errorf("release resolver observation does not prove the exact complete request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint release resolver: %w", err)
	}
	if fingerprint != evidence.ObservationFingerprint {
		return fmt.Errorf("release resolver evidence differs from independently observed resolver")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return fmt.Errorf("classify release resolver: %w", err)
	}
	if assessment.Owned != resolver.OwnedStateAbsent {
		return fmt.Errorf("release resolver owned state is %q, want absent", assessment.Owned)
	}
	return nil
}

// validateGlobalNetworkReleaseResolverEvidence admits only one owned-absence resolver postcondition.
func validateGlobalNetworkReleaseResolverEvidence(evidence helper.ResolverMutationEvidence) error {
	if !canonicalNetworkDataPlaneFingerprint(evidence.PolicyFingerprint) ||
		!canonicalNetworkDataPlaneFingerprint(evidence.OwnershipFingerprint) ||
		!canonicalNetworkDataPlaneFingerprint(evidence.ObservationFingerprint) {
		return fmt.Errorf("release resolver evidence fingerprint is invalid")
	}
	if evidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		return fmt.Errorf("release resolver evidence must prove owned_absent")
	}
	return nil
}

// issueReleaseResolver opens helper authority after durable validation and closes it after every publication attempt.
func (c *GlobalNetworkReleaseCoordinator) issueReleaseResolver(ctx context.Context, requester string, request ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
	issuer, err := c.resolverIssuers()
	if err != nil {
		return ticketissuer.ResolverResult{}, fmt.Errorf("open release resolver issuer: %w", err)
	}
	if nilDependency(issuer) {
		return ticketissuer.ResolverResult{}, fmt.Errorf("prepare release resolver: issuer is nil")
	}
	result, issueErr := issuer.Issue(ctx, requester, request)
	closeErr := issuer.Close()
	if issueErr == nil && closeErr == nil {
		return result, nil
	}
	if issueErr == nil {
		return result, errors.Join(ticketissuer.ErrResolverPublicationIndeterminate, closeErr)
	}
	if errors.Is(issueErr, ticketissuer.ErrResolverPublicationIndeterminate) {
		return result, errors.Join(issueErr, closeErr)
	}
	return ticketissuer.ResolverResult{}, errors.Join(issueErr, closeErr)
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
