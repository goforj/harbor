package ticketissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketkey"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// ResolverRequest selects one durable resolver approval plan without carrying host authority.
type ResolverRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one daemon-owned approval operation.
func (request ResolverRequest) Validate() error {
	return request.OperationID.Validate()
}

// ResolverPlan is the immutable schema transition and resolver authority approved by one durable operation.
type ResolverPlan struct {
	OperationID                        domain.OperationID
	OperationRevision                  domain.Sequence
	OperationState                     domain.OperationState
	Mutation                           helper.Operation
	ExpectedSourceOwnershipFingerprint string
	TargetOwnership                    ownership.Record
	Policy                             networkpolicy.Policy
}

// Validate rejects plans whose source, target, and policy cannot describe one exact ownership upgrade.
func (plan ResolverPlan) Validate() error {
	if err := plan.OperationID.Validate(); err != nil {
		return err
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("resolver approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.OperationState != domain.OperationRequiresApproval {
		return fmt.Errorf("resolver approval operation state is %q, want %q", plan.OperationState, domain.OperationRequiresApproval)
	}
	if plan.Mutation != helper.OperationEnsureResolver {
		return fmt.Errorf("resolver approval mutation %q is not allowlisted", plan.Mutation)
	}
	if err := plan.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("resolver approval target ownership: %w", err)
	}
	if plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"resolver approval target ownership schema is %d, want %d",
			plan.TargetOwnership.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	if err := plan.Policy.Validate(); err != nil {
		return fmt.Errorf("resolver approval policy: %w", err)
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("resolver approval policy fingerprint: %w", err)
	}
	if policyFingerprint != plan.TargetOwnership.NetworkPolicyFingerprint {
		return fmt.Errorf("resolver approval policy does not match target ownership")
	}
	_, sourceFingerprint, err := resolverPlanSourceOwnership(plan.TargetOwnership)
	if err != nil {
		return err
	}
	if plan.ExpectedSourceOwnershipFingerprint != sourceFingerprint {
		return fmt.Errorf("resolver approval source ownership fingerprint does not match its target-derived schema-1 record")
	}
	return nil
}

// ResolverPlanSource resolves one exact durable plan before capability publication.
type ResolverPlanSource interface {
	// Resolve returns the resolver plan owned by one daemon operation.
	Resolve(context.Context, ResolverRequest) (ResolverPlan, error)
}

// ResolverObserver supplies complete native resolver facts without mutation authority.
type ResolverObserver interface {
	// Observe returns every native resolver fact relevant to one immutable request.
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
}

// ResolverResult exposes only opaque launch metadata for one policy-bound capability.
type ResolverResult struct {
	OperationID       domain.OperationID
	Reference         helper.TicketReference
	Operation         helper.Operation
	PolicyFingerprint string
	ExpiresAt         time.Time
}

// Validate rejects results that can cross the selected operation or helper lifetime boundary.
func (result ResolverResult) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationEnsureResolver {
		return fmt.Errorf("resolver approval result operation %q is unsupported", result.Operation)
	}
	if !canonicalSHA256Fingerprint(result.PolicyFingerprint) {
		return fmt.Errorf("resolver approval result policy fingerprint is invalid")
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return fmt.Errorf("resolver approval result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("resolver approval result expiry exceeds the protocol bound")
	}
	return nil
}

// ResolverService serializes policy-bound issuance against durable and native revalidation.
type ResolverService struct {
	plans      ResolverPlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	resolver   ResolverObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// resolverDefaultOpeners keeps fixed storage construction replaceable in lifecycle tests.
type resolverDefaultOpeners struct {
	openKeys      func() (defaultKeyStoreCloser, error)
	openPublisher func() (defaultPublisherCloser, error)
}

// NewResolverService creates an issuer from explicit durable authorities and a read-only native observer.
func NewResolverService(
	plans ResolverPlanSource,
	ownershipObserver OwnershipObserver,
	keys KeyLoader,
	publisher Publisher,
	resolverObserver ResolverObserver,
	clock helper.Clock,
	entropy io.Reader,
) *ResolverService {
	if plans == nil || ownershipObserver == nil || keys == nil || publisher == nil || resolverObserver == nil || clock == nil || entropy == nil {
		panic("ticketissuer.NewResolverService requires every authority dependency")
	}
	return &ResolverService{
		plans:      plans,
		ownership:  ownershipObserver,
		keys:       keys,
		publisher:  publisher,
		resolver:   resolverObserver,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefaultResolverService opens fixed user-owned key and ticket stores around an explicit platform observer.
func OpenDefaultResolverService(
	plans ResolverPlanSource,
	ownershipObserver OwnershipObserver,
	resolverObserver ResolverObserver,
) (*ResolverService, error) {
	return openDefaultResolverService(plans, ownershipObserver, resolverObserver, defaultResolverOpeners())
}

// defaultResolverOpeners binds production issuance to Harbor's fixed user-owned key and ticket paths.
func defaultResolverOpeners() resolverDefaultOpeners {
	return resolverDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultResolverService opens both stores as one close-safe ownership unit.
func openDefaultResolverService(
	plans ResolverPlanSource,
	ownershipObserver OwnershipObserver,
	resolverObserver ResolverObserver,
	openers resolverDefaultOpeners,
) (*ResolverService, error) {
	if plans == nil {
		return nil, fmt.Errorf("open helper resolver ticket issuer: durable plan source is required")
	}
	if ownershipObserver == nil {
		return nil, fmt.Errorf("open helper resolver ticket issuer: ownership observer is required")
	}
	if resolverObserver == nil {
		return nil, fmt.Errorf("open helper resolver ticket issuer: resolver observer is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, fmt.Errorf("open helper resolver ticket issuer: default store openers are incomplete")
	}
	keyStore, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper resolver ticket issuer key: %w", err)
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper resolver ticket issuer spool: %w", err),
			keyStore.Close(),
		)
	}
	service := NewResolverService(
		plans,
		ownershipObserver,
		keyStore,
		publisher,
		resolverObserver,
		helper.SystemClock{},
		rand.Reader,
	)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance has left the serialized boundary.
func (service *ResolverService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one target-schema capability from a stable plan and two equal native observations.
func (service *ResolverService) Issue(
	ctx context.Context,
	requesterIdentity string,
	request ResolverRequest,
) (ResolverResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ResolverResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: %w", err)
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return ResolverResult{}, err
	}

	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return ResolverResult{}, err
	}
	_, err = service.observeSourceOwnership(ctx, requesterIdentity, plan)
	if err != nil {
		return ResolverResult{}, err
	}
	privateKey, err := service.keys.Load(ctx)
	if err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: load established signing key: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: %w", err)
	}

	resolverRequest, err := resolver.NewRequest(plan.TargetOwnership.InstallationID, plan.Policy)
	if err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: construct resolver request: %w", err)
	}
	observationFingerprint, err := service.observeResolver(ctx, resolverRequest)
	if err != nil {
		return ResolverResult{}, err
	}
	ticket, err := service.buildResolverTicket(requesterIdentity, plan, observationFingerprint, privateKey)
	if err != nil {
		return ResolverResult{}, err
	}

	confirmedPlan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: revalidate approval plan: %w", err)
	}
	if confirmedPlan != plan {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: durable approval plan changed before publication")
	}
	if _, err := service.observeSourceOwnership(ctx, requesterIdentity, confirmedPlan); err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: revalidate ownership: %w", err)
	}
	confirmedResolverFingerprint, err := service.observeResolver(ctx, resolverRequest)
	if err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: revalidate resolver: %w", err)
	}
	if confirmedResolverFingerprint != observationFingerprint {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: resolver observation changed before publication")
	}

	reference, err := service.publisher.Publish(ctx, ticket, privateKey)
	if err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: publish capability: %w", err)
	}
	result := ResolverResult{
		OperationID:       plan.OperationID,
		Reference:         reference,
		Operation:         plan.Mutation,
		PolicyFingerprint: plan.TargetOwnership.NetworkPolicyFingerprint,
		ExpiresAt:         ticket.ExpiresAt,
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return ResolverResult{}, fmt.Errorf("issue helper resolver ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates one immutable durable plan on every read boundary.
func (service *ResolverService) resolvePlan(ctx context.Context, request ResolverRequest) (ResolverPlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return ResolverPlan{}, fmt.Errorf("issue helper resolver ticket: resolve approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return ResolverPlan{}, fmt.Errorf("issue helper resolver ticket: invalid approval plan: %w", err)
	}
	if plan.OperationID != request.OperationID {
		return ResolverPlan{}, fmt.Errorf("issue helper resolver ticket: approval plan does not match its requested operation")
	}
	return plan, nil
}

// observeSourceOwnership requires the daemon projection to remain the exact schema-1 source until confirmation commits.
func (service *ResolverService) observeSourceOwnership(
	ctx context.Context,
	requesterIdentity string,
	plan ResolverPlan,
) (ownership.Observation, error) {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: observe ownership projection: %w", err)
	}
	if !observation.Exists {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: ownership projection is absent")
	}
	if err := observation.Record.Validate(); err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: ownership projection: %w", err)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return ownership.Observation{}, err
	}
	if observation.Fingerprint != fingerprint || fingerprint != plan.ExpectedSourceOwnershipFingerprint {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: ownership projection does not match the approved schema-1 source")
	}
	source, _, err := resolverPlanSourceOwnership(plan.TargetOwnership)
	if err != nil {
		return ownership.Observation{}, err
	}
	if observation.Record != source {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: ownership projection differs from the approved schema-1 source")
	}
	if requesterIdentity != source.OwnerIdentity {
		return ownership.Observation{}, fmt.Errorf("issue helper resolver ticket: authenticated requester does not own the machine claim")
	}
	return observation, nil
}

// observeResolver admits only complete, nonforeign resolver states that an ensure operation may safely converge.
func (service *ResolverService) observeResolver(ctx context.Context, request resolver.Request) (string, error) {
	observation, err := service.resolver.Observe(ctx, request)
	if err != nil {
		return "", fmt.Errorf("issue helper resolver ticket: observe resolver: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return "", fmt.Errorf("issue helper resolver ticket: invalid resolver observation: %w", err)
	}
	if observation.Request.InstallationID() != request.InstallationID() ||
		observation.Request.PolicyFingerprint() != request.PolicyFingerprint() ||
		observation.Request.Policy() != request.Policy() {
		return "", fmt.Errorf("issue helper resolver ticket: resolver observation belongs to another request")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return "", fmt.Errorf("issue helper resolver ticket: classify resolver observation: %w", err)
	}
	switch assessment.State {
	case resolver.StateAbsent, resolver.StateExact, resolver.StateOwnedDrifted:
	case resolver.StateForeign, resolver.StateAmbiguous, resolver.StateIndeterminate:
		return "", fmt.Errorf("issue helper resolver ticket: resolver state %q cannot be safely ensured", assessment.State)
	default:
		return "", fmt.Errorf("issue helper resolver ticket: resolver state %q is unsupported", assessment.State)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("issue helper resolver ticket: fingerprint resolver observation: %w", err)
	}
	return fingerprint, nil
}

// buildResolverTicket binds target ownership and one exact native resolver precondition into a fresh capability.
func (service *ResolverService) buildResolverTicket(
	requesterIdentity string,
	plan ResolverPlan,
	observationFingerprint string,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper resolver ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	policy := plan.Policy
	ticket := helper.Ticket{
		Version:                     helper.ProtocolVersion,
		Operation:                   plan.Mutation,
		InstallationID:              plan.TargetOwnership.InstallationID,
		RequesterIdentity:           requesterIdentity,
		OwnershipGeneration:         plan.TargetOwnership.Generation,
		OwnershipSchemaVersion:      plan.TargetOwnership.SchemaVersion,
		NetworkPolicyFingerprint:    plan.TargetOwnership.NetworkPolicyFingerprint,
		NetworkPolicy:               &policy,
		ApprovedPool:                plan.TargetOwnership.LoopbackPoolPrefix,
		ExpectedResolverObservation: &helper.ExpectedResolverObservation{Fingerprint: observationFingerprint},
		Nonce:                       hex.EncodeToString(nonce),
		ExpiresAt:                   now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper resolver ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper resolver ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}

// resolverPlanSourceOwnership derives the only schema-1 record that may precede one target policy binding.
func resolverPlanSourceOwnership(target ownership.Record) (ownership.Record, string, error) {
	source := target
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	if err := source.Validate(); err != nil {
		return ownership.Record{}, "", fmt.Errorf("resolver approval source ownership: %w", err)
	}
	fingerprint, err := source.Fingerprint()
	if err != nil {
		return ownership.Record{}, "", err
	}
	return source, fingerprint, nil
}

// canonicalSHA256Fingerprint accepts only the lowercase fixed-width spelling used by policy evidence.
func canonicalSHA256Fingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
