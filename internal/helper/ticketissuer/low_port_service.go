package ticketissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketkey"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/lowport"
)

var (
	// ErrLowPortPublicationIndeterminate means the returned result is the only reference that may identify a published low-port capability.
	ErrLowPortPublicationIndeterminate = errors.New("low-port capability publication is indeterminate")
)

// LowPortRequest selects one durable global data-plane approval without carrying host authority.
type LowPortRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one daemon-owned approval operation.
func (request LowPortRequest) Validate() error {
	return request.OperationID.Validate()
}

// LowPortPlanPurpose identifies the lifecycle that granted a low-port mutation.
type LowPortPlanPurpose string

const (
	// LowPortPlanPurposeDataPlaneSetup permits the setup lifecycle to ensure low-port integration.
	LowPortPlanPurposeDataPlaneSetup LowPortPlanPurpose = "network_data_plane_setup"
	// LowPortPlanPurposeGlobalNetworkRelease permits the global release lifecycle to remove low-port integration.
	LowPortPlanPurposeGlobalNetworkRelease LowPortPlanPurpose = "global_network_release"
)

// LowPortCheckpointPhase identifies the durable checkpoint that fences low-port ticket publication.
type LowPortCheckpointPhase string

const (
	// LowPortCheckpointPhaseSetupApproval identifies the setup approval checkpoint before low-port installation.
	LowPortCheckpointPhaseSetupApproval LowPortCheckpointPhase = "awaiting low-port approval"
	// LowPortCheckpointPhaseGlobalRelease identifies the global release checkpoint after runtime retirement.
	LowPortCheckpointPhaseGlobalRelease LowPortCheckpointPhase = "low_ports"
)

// Validate rejects lifecycle purposes that have no narrow low-port admission contract.
func (purpose LowPortPlanPurpose) Validate() error {
	switch purpose {
	case LowPortPlanPurposeDataPlaneSetup, LowPortPlanPurposeGlobalNetworkRelease:
		return nil
	default:
		return fmt.Errorf("low-port approval purpose %q is unsupported", purpose)
	}
}

// LowPortPlan is the complete durable and native authority approved for one low-port mutation.
type LowPortPlan struct {
	Purpose            LowPortPlanPurpose
	Operation          domain.Operation
	OperationRevision  domain.Sequence
	CheckpointRevision domain.Sequence
	CheckpointPhase    LowPortCheckpointPhase
	Mutation           helper.Operation
	TargetOwnership    ownership.Record
	Policy             networkpolicy.Policy
	NativeRequest      lowport.Request
}

// Validate rejects plans that do not describe one exact low-port lifecycle authority.
func (plan LowPortPlan) Validate() error {
	if err := plan.Operation.Validate(); err != nil {
		return fmt.Errorf("low-port approval operation: %w", err)
	}
	if plan.Operation.ProjectID != "" {
		return errors.New("low-port approval operation must be global")
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("low-port approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if err := plan.Purpose.Validate(); err != nil {
		return err
	}
	if err := validateLowPortPlanLifecycle(plan); err != nil {
		return err
	}
	if err := plan.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("low-port approval target ownership: %w", err)
	}
	if plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"low-port approval target ownership schema is %d, want %d",
			plan.TargetOwnership.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	if err := plan.Policy.Validate(); err != nil {
		return fmt.Errorf("low-port approval policy: %w", err)
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("low-port approval policy fingerprint: %w", err)
	}
	if policyFingerprint != plan.TargetOwnership.NetworkPolicyFingerprint {
		return errors.New("low-port approval policy does not match target ownership")
	}
	if err := plan.NativeRequest.Validate(); err != nil {
		return fmt.Errorf("low-port approval native request: %w", err)
	}
	derived, err := lowport.NewRequest(plan.TargetOwnership, plan.Policy)
	if err != nil {
		return fmt.Errorf("low-port approval authority: %w", err)
	}
	if plan.NativeRequest != derived {
		return errors.New("low-port approval native request does not match policy-bound ownership")
	}
	return nil
}

// validateLowPortPlanLifecycle keeps setup and release authority disjoint despite sharing native request validation.
func validateLowPortPlanLifecycle(plan LowPortPlan) error {
	switch plan.Purpose {
	case LowPortPlanPurposeDataPlaneSetup:
		if plan.CheckpointRevision != 0 {
			return fmt.Errorf("low-port setup checkpoint revision is %d, want 0", plan.CheckpointRevision)
		}
		if plan.CheckpointPhase != LowPortCheckpointPhaseSetupApproval {
			return fmt.Errorf("low-port setup checkpoint phase is %q, want %q", plan.CheckpointPhase, LowPortCheckpointPhaseSetupApproval)
		}
		if plan.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup {
			return fmt.Errorf("low-port setup operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkDataPlaneSetup)
		}
		if plan.Operation.State != domain.OperationRequiresApproval {
			return fmt.Errorf("low-port setup operation state is %q, want %q", plan.Operation.State, domain.OperationRequiresApproval)
		}
		if plan.Operation.Phase != string(LowPortCheckpointPhaseSetupApproval) {
			return fmt.Errorf("low-port setup operation phase is %q, want %q", plan.Operation.Phase, LowPortCheckpointPhaseSetupApproval)
		}
		if plan.Mutation != helper.OperationEnsureLowPorts {
			return fmt.Errorf("low-port setup mutation is %q, want %q", plan.Mutation, helper.OperationEnsureLowPorts)
		}
	case LowPortPlanPurposeGlobalNetworkRelease:
		if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
			return fmt.Errorf("low-port release checkpoint revision must be between 1 and %d", domain.MaximumSequence)
		}
		if plan.CheckpointPhase != LowPortCheckpointPhaseGlobalRelease {
			return fmt.Errorf("low-port release checkpoint phase is %q, want %q", plan.CheckpointPhase, LowPortCheckpointPhaseGlobalRelease)
		}
		if plan.Operation.Kind != domain.OperationKindNetworkRelease {
			return fmt.Errorf("low-port release operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkRelease)
		}
		if plan.Operation.State != domain.OperationRunning {
			return fmt.Errorf("low-port release operation state is %q, want %q", plan.Operation.State, domain.OperationRunning)
		}
		if plan.Operation.Phase != "releasing network runtime" {
			return fmt.Errorf("low-port release operation phase is %q, want %q", plan.Operation.Phase, "releasing network runtime")
		}
		if plan.Mutation != helper.OperationReleaseLowPorts {
			return fmt.Errorf("low-port release mutation is %q, want %q", plan.Mutation, helper.OperationReleaseLowPorts)
		}
	}
	return nil
}

// LowPortPlanSource resolves one exact durable plan before capability publication.
type LowPortPlanSource interface {
	// Resolve returns the low-port plan owned by one daemon operation.
	Resolve(context.Context, LowPortRequest) (LowPortPlan, error)
}

// LowPortObserver supplies complete native low-port facts without mutation authority.
type LowPortObserver interface {
	// Observe returns every native fact relevant to one immutable low-port request.
	Observe(context.Context, lowport.Request) (lowport.Observation, error)
}

// LowPortResult exposes only opaque launch metadata for one observation-bound low-port capability.
type LowPortResult struct {
	OperationID            domain.OperationID
	Reference              helper.TicketReference
	Operation              helper.Operation
	PolicyFingerprint      string
	OwnershipFingerprint   string
	ObservationFingerprint string
	ExpiresAt              time.Time
}

// Validate rejects results that can cross the selected operation or helper lifetime boundary.
func (result LowPortResult) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationEnsureLowPorts && result.Operation != helper.OperationReleaseLowPorts {
		return fmt.Errorf("low-port approval result operation %q is unsupported", result.Operation)
	}
	if !canonicalSHA256Fingerprint(result.PolicyFingerprint) {
		return errors.New("low-port approval result policy fingerprint is invalid")
	}
	if !canonicalSHA256Fingerprint(result.OwnershipFingerprint) {
		return errors.New("low-port approval result ownership fingerprint is invalid")
	}
	if !canonicalSHA256Fingerprint(result.ObservationFingerprint) {
		return errors.New("low-port approval result observation fingerprint is invalid")
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return errors.New("low-port approval result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return errors.New("low-port approval result expiry exceeds the protocol bound")
	}
	return nil
}

// LowPortService serializes low-port ticket issuance against durable and native revalidation.
type LowPortService struct {
	plans      LowPortPlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	lowPorts   LowPortObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// lowPortDefaultOpeners keeps fixed storage construction replaceable in lifecycle tests.
type lowPortDefaultOpeners struct {
	openKeys      func() (defaultKeyStoreCloser, error)
	openPublisher func() (defaultPublisherCloser, error)
}

// NewLowPortService creates an issuer from explicit durable authorities and one read-only native observer.
func NewLowPortService(
	plans LowPortPlanSource,
	ownershipObserver OwnershipObserver,
	keys KeyLoader,
	publisher Publisher,
	lowPortObserver LowPortObserver,
	clock helper.Clock,
	entropy io.Reader,
) *LowPortService {
	if plans == nil || ownershipObserver == nil || keys == nil || publisher == nil || lowPortObserver == nil || clock == nil || entropy == nil {
		panic("ticketissuer.NewLowPortService requires every authority dependency")
	}
	return &LowPortService{
		plans:      plans,
		ownership:  ownershipObserver,
		keys:       keys,
		publisher:  publisher,
		lowPorts:   lowPortObserver,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefaultLowPortService opens fixed user-owned key and ticket stores around explicit low-port authorities.
func OpenDefaultLowPortService(
	plans LowPortPlanSource,
	ownershipObserver OwnershipObserver,
	lowPortObserver LowPortObserver,
) (*LowPortService, error) {
	return openDefaultLowPortService(plans, ownershipObserver, lowPortObserver, defaultLowPortOpeners())
}

// defaultLowPortOpeners binds production issuance to Harbor's fixed user-owned key and ticket paths.
func defaultLowPortOpeners() lowPortDefaultOpeners {
	return lowPortDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultLowPortService opens both stores as one close-safe ownership unit.
func openDefaultLowPortService(
	plans LowPortPlanSource,
	ownershipObserver OwnershipObserver,
	lowPortObserver LowPortObserver,
	openers lowPortDefaultOpeners,
) (*LowPortService, error) {
	if plans == nil {
		return nil, errors.New("open helper low-port ticket issuer: durable plan source is required")
	}
	if ownershipObserver == nil {
		return nil, errors.New("open helper low-port ticket issuer: ownership observer is required")
	}
	if lowPortObserver == nil {
		return nil, errors.New("open helper low-port ticket issuer: low-port observer is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, errors.New("open helper low-port ticket issuer: default store openers are incomplete")
	}
	keyStore, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper low-port ticket issuer key: %w", err)
	}
	if keyStore == nil {
		return nil, errors.New("open helper low-port ticket issuer key: opener returned nil")
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper low-port ticket issuer spool: %w", err),
			keyStore.Close(),
		)
	}
	if publisher == nil {
		return nil, errors.Join(
			errors.New("open helper low-port ticket issuer spool: opener returned nil"),
			keyStore.Close(),
		)
	}
	service := NewLowPortService(plans, ownershipObserver, keyStore, publisher, lowPortObserver, helper.SystemClock{}, rand.Reader)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance leaves the serialized boundary.
func (service *LowPortService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one low-port capability from a stable plan and two equal complete native observations.
// A result returned with ErrLowPortPublicationIndeterminate is the only reference callers may reconcile.
func (service *LowPortService) Issue(
	ctx context.Context,
	requesterIdentity string,
	request LowPortRequest,
) (LowPortResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return LowPortResult{}, err
	}
	if err := request.Validate(); err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: %w", err)
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return LowPortResult{}, errors.New("issue helper low-port ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return LowPortResult{}, err
	}

	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return LowPortResult{}, err
	}
	owned, err := service.observeOwnership(ctx, requesterIdentity, plan)
	if err != nil {
		return LowPortResult{}, err
	}
	observation, observationFingerprint, err := service.observeLowPorts(ctx, plan.NativeRequest, plan.Mutation)
	if err != nil {
		return LowPortResult{}, err
	}
	privateKey, err := service.keys.Load(ctx)
	if err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: load established signing key: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return LowPortResult{}, err
	}
	privateKey = append(ed25519.PrivateKey(nil), privateKey...)
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: %w", err)
	}
	ticket, err := service.buildTicket(requesterIdentity, plan, observationFingerprint, privateKey)
	if err != nil {
		return LowPortResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return LowPortResult{}, err
	}

	confirmedPlan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: revalidate approval plan: %w", err)
	}
	if !sameLowPortPlan(plan, confirmedPlan) {
		return LowPortResult{}, errors.New("issue helper low-port ticket: durable approval plan changed before publication")
	}
	confirmedOwnership, err := service.observeOwnership(ctx, requesterIdentity, confirmedPlan)
	if err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: revalidate ownership: %w", err)
	}
	if confirmedOwnership != owned {
		return LowPortResult{}, errors.New("issue helper low-port ticket: ownership observation changed before publication")
	}
	confirmedObservation, confirmedFingerprint, err := service.observeLowPorts(ctx, confirmedPlan.NativeRequest, confirmedPlan.Mutation)
	if err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: revalidate native state: %w", err)
	}
	if confirmedFingerprint != observationFingerprint || !sameLowPortObservation(observation, confirmedObservation) {
		return LowPortResult{}, errors.New("issue helper low-port ticket: native observation changed before publication")
	}
	if err := ctx.Err(); err != nil {
		return LowPortResult{}, err
	}

	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: fingerprint target ownership: %w", err)
	}
	publishNow := service.clock.Now().UTC()
	if err := ticket.Validate(publishNow); err != nil {
		return LowPortResult{}, fmt.Errorf("issue helper low-port ticket: constructed ticket expired before publication: %w", err)
	}
	publishContext, cancel := context.WithDeadline(ctx, ticket.ExpiresAt)
	defer cancel()
	reference, publishErr := service.publisher.Publish(publishContext, ticket, privateKey)
	result := LowPortResult{
		OperationID:            plan.Operation.ID,
		Reference:              reference,
		Operation:              plan.Mutation,
		PolicyFingerprint:      plan.TargetOwnership.NetworkPolicyFingerprint,
		OwnershipFingerprint:   ownershipFingerprint,
		ObservationFingerprint: observationFingerprint,
		ExpiresAt:              ticket.ExpiresAt,
	}
	if publishErr != nil {
		wrapped := fmt.Errorf("issue helper low-port ticket: publish capability: %w", publishErr)
		if !errors.Is(publishErr, ticketspool.ErrDurabilityUncertain) {
			return LowPortResult{}, wrapped
		}
		if err := result.Validate(service.clock.Now().UTC()); err != nil {
			return result, errors.Join(
				ErrLowPortPublicationIndeterminate,
				wrapped,
				fmt.Errorf("issue helper low-port ticket: invalid durability-indeterminate publication result: %w", err),
			)
		}
		return result, errors.Join(ErrLowPortPublicationIndeterminate, wrapped)
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return result, errors.Join(
			ErrLowPortPublicationIndeterminate,
			fmt.Errorf("issue helper low-port ticket: published capability has invalid result metadata: %w", err),
		)
	}
	return result, nil
}

// resolvePlan validates and isolates one complete durable plan on every read boundary.
func (service *LowPortService) resolvePlan(ctx context.Context, request LowPortRequest) (LowPortPlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return LowPortPlan{}, fmt.Errorf("issue helper low-port ticket: resolve approval plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return LowPortPlan{}, err
	}
	plan = cloneLowPortPlan(plan)
	if err := plan.Validate(); err != nil {
		return LowPortPlan{}, fmt.Errorf("issue helper low-port ticket: invalid approval plan: %w", err)
	}
	if plan.Operation.ID != request.OperationID {
		return LowPortPlan{}, errors.New("issue helper low-port ticket: approval plan does not match requested operation")
	}
	return plan, nil
}

// observeOwnership requires the current confirmed schema-two projection to equal the approved target.
func (service *LowPortService) observeOwnership(
	ctx context.Context,
	requesterIdentity string,
	plan LowPortPlan,
) (ownership.Observation, error) {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper low-port ticket: observe ownership projection: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ownership.Observation{}, err
	}
	if !observation.Exists {
		return ownership.Observation{}, errors.New("issue helper low-port ticket: ownership projection is absent")
	}
	if err := observation.Record.Validate(); err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper low-port ticket: invalid ownership projection: %w", err)
	}
	if observation.Record != plan.TargetOwnership {
		return ownership.Observation{}, errors.New("issue helper low-port ticket: ownership projection differs from the approved target")
	}
	if requesterIdentity != plan.TargetOwnership.OwnerIdentity {
		return ownership.Observation{}, errors.New("issue helper low-port ticket: authenticated requester does not own the approved machine claim")
	}
	fingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper low-port ticket: fingerprint approved target ownership: %w", err)
	}
	if observation.Fingerprint != fingerprint {
		return ownership.Observation{}, errors.New("issue helper low-port ticket: ownership projection fingerprint does not match the approved target")
	}
	return observation, nil
}

// observeLowPorts admits only complete nonforeign states that the requested mutation may safely converge.
func (service *LowPortService) observeLowPorts(
	ctx context.Context,
	request lowport.Request,
	mutation helper.Operation,
) (lowport.Observation, string, error) {
	observation, err := service.lowPorts.Observe(ctx, request)
	if err != nil {
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: observe native state: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return lowport.Observation{}, "", err
	}
	observation = cloneLowPortObservation(observation)
	if err := observation.Validate(); err != nil {
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: invalid native observation: %w", err)
	}
	if observation.Request != request {
		return lowport.Observation{}, "", errors.New("issue helper low-port ticket: native observation belongs to another request")
	}
	state, err := observation.State()
	if err != nil {
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: invalid native observation: %w", err)
	}
	verb := "converged"
	if mutation == helper.OperationEnsureLowPorts {
		verb = "ensured"
	} else if mutation == helper.OperationReleaseLowPorts {
		verb = "released"
	}
	switch state {
	case lowport.StateAbsent, lowport.StateExact:
	case lowport.StateOwnedDrifted:
		if mutation != helper.OperationEnsureLowPorts {
			return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: owned-drifted native state cannot be safely %s", verb)
		}
	case lowport.StateForeign:
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: foreign native state cannot be safely %s", verb)
	case lowport.StateAmbiguous:
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: ambiguous native state cannot be safely %s", verb)
	case lowport.StateIndeterminate:
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: indeterminate native state cannot be safely %s", verb)
	default:
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: native state %q is unsupported", state)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return lowport.Observation{}, "", fmt.Errorf("issue helper low-port ticket: fingerprint native observation: %w", err)
	}
	if !canonicalSHA256Fingerprint(fingerprint) {
		return lowport.Observation{}, "", errors.New("issue helper low-port ticket: native observation fingerprint is not canonical")
	}
	return observation, fingerprint, nil
}

// buildTicket binds policy ownership and one exact native precondition into a fresh capability.
func (service *LowPortService) buildTicket(
	requesterIdentity string,
	plan LowPortPlan,
	observationFingerprint string,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper low-port ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	policy := plan.Policy
	ticket := helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                plan.Mutation,
		InstallationID:           plan.TargetOwnership.InstallationID,
		RequesterIdentity:        requesterIdentity,
		OwnershipGeneration:      plan.TargetOwnership.Generation,
		OwnershipSchemaVersion:   plan.TargetOwnership.SchemaVersion,
		NetworkPolicyFingerprint: plan.TargetOwnership.NetworkPolicyFingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             plan.TargetOwnership.LoopbackPoolPrefix,
		ExpectedLowPortObservation: &helper.ExpectedLowPortObservation{
			Fingerprint: observationFingerprint,
		},
		Nonce:     hex.EncodeToString(nonce),
		ExpiresAt: now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper low-port ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper low-port ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}

// cloneLowPortPlan isolates operation pointer fields from source-owned storage before revalidation.
func cloneLowPortPlan(plan LowPortPlan) LowPortPlan {
	plan.Operation = cloneLowPortOperation(plan.Operation)
	return plan
}

// cloneLowPortOperation copies every optional operation value so a source cannot mutate retained authority.
func cloneLowPortOperation(operation domain.Operation) domain.Operation {
	clone := operation
	if operation.StartedAt != nil {
		startedAt := *operation.StartedAt
		clone.StartedAt = &startedAt
	}
	if operation.FinishedAt != nil {
		finishedAt := *operation.FinishedAt
		clone.FinishedAt = &finishedAt
	}
	if operation.Problem != nil {
		problem := *operation.Problem
		clone.Problem = &problem
	}
	return clone
}

// sameLowPortPlan compares every durable, policy, ownership, and native request field.
func sameLowPortPlan(left LowPortPlan, right LowPortPlan) bool {
	return sameLowPortOperation(left.Operation, right.Operation) &&
		left.Purpose == right.Purpose &&
		left.OperationRevision == right.OperationRevision &&
		left.CheckpointRevision == right.CheckpointRevision &&
		left.CheckpointPhase == right.CheckpointPhase &&
		left.Mutation == right.Mutation &&
		left.TargetOwnership == right.TargetOwnership &&
		left.Policy == right.Policy &&
		left.NativeRequest == right.NativeRequest
}

// sameLowPortOperation compares operation values without treating pointer allocation as authority.
func sameLowPortOperation(left domain.Operation, right domain.Operation) bool {
	return left.ID == right.ID &&
		left.IntentID == right.IntentID &&
		left.Kind == right.Kind &&
		left.ProjectID == right.ProjectID &&
		left.State == right.State &&
		left.Phase == right.Phase &&
		left.RequestedAt.Equal(right.RequestedAt) &&
		sameOptionalLowPortTime(left.StartedAt, right.StartedAt) &&
		sameOptionalLowPortTime(left.FinishedAt, right.FinishedAt) &&
		sameOptionalLowPortProblem(left.Problem, right.Problem)
}

// sameOptionalLowPortTime compares optional operation timestamps by value.
func sameOptionalLowPortTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// sameOptionalLowPortProblem compares optional operation failures by value.
func sameOptionalLowPortProblem(left *domain.Problem, right *domain.Problem) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// cloneLowPortObservation prevents an observer-owned artifact slice from changing retained CAS authority.
func cloneLowPortObservation(observation lowport.Observation) lowport.Observation {
	observation.Artifacts = slices.Clone(observation.Artifacts)
	return observation
}

// sameLowPortObservation compares every native fact through the platform's order-independent canonical digest.
func sameLowPortObservation(left lowport.Observation, right lowport.Observation) bool {
	if left.Request != right.Request || left.Complete != right.Complete {
		return false
	}
	leftFingerprint, leftErr := left.Fingerprint()
	rightFingerprint, rightErr := right.Fingerprint()
	return leftErr == nil && rightErr == nil && leftFingerprint == rightFingerprint
}
