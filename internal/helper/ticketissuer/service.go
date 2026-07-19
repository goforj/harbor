package ticketissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketkey"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

const (
	ticketLifetime    = time.Minute
	ticketNonceBytes  = 32
	maximumResultTime = helper.MaxTicketLifetime
)

// LeaseState identifies whether a durable approval plan owns an existing assignment or reserves an absent one.
type LeaseState string

const (
	// LeasePending means the plan reserves an address that has not yet been assigned.
	LeasePending LeaseState = "pending"
	// LeaseActive means durable state already owns the exact assigned address.
	LeaseActive LeaseState = "active"
)

// Request identifies one daemon-selected lease effect inside a durable operation.
type Request struct {
	OperationID domain.OperationID
	LeaseKey    identity.LeaseKey
}

// Validate rejects identities that cannot select one durable approval plan.
func (request Request) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	return request.LeaseKey.Validate()
}

// Plan is the durable, revision-bound authority from which a helper ticket may be derived.
type Plan struct {
	OperationID       domain.OperationID
	OperationRevision domain.Sequence
	OperationState    domain.OperationState
	Mutation          helper.Operation
	Lease             identity.Lease
	LeaseState        LeaseState
	Requirements      []hostconflict.SocketRequirement
}

// Validate rejects approval plans that are not canonical or ready for one exact helper mutation.
func (plan Plan) Validate() error {
	if err := plan.OperationID.Validate(); err != nil {
		return err
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("helper approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.OperationState != domain.OperationRequiresApproval {
		return fmt.Errorf("helper approval operation state is %q, want %q", plan.OperationState, domain.OperationRequiresApproval)
	}
	if plan.Mutation != helper.OperationEnsureLoopbackIdentity && plan.Mutation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("helper approval mutation %q is not allowlisted", plan.Mutation)
	}
	if err := plan.Lease.Validate(); err != nil {
		return err
	}
	if plan.Lease.Address != plan.Lease.Address.Unmap() {
		return fmt.Errorf("helper approval lease address must use canonical IPv4 form")
	}
	switch plan.LeaseState {
	case LeasePending:
		if plan.Mutation != helper.OperationEnsureLoopbackIdentity {
			return fmt.Errorf("pending helper approval lease requires an ensure mutation")
		}
	case LeaseActive:
	default:
		return fmt.Errorf("helper approval lease state %q is unsupported", plan.LeaseState)
	}
	if plan.Requirements == nil {
		return fmt.Errorf("helper approval socket requirements must be initialized")
	}
	request, err := hostconflict.NewPreAssignmentRequest(plan.Lease.Address, plan.Requirements)
	if err != nil {
		return err
	}
	if !slices.Equal(request.Requirements(), plan.Requirements) {
		return fmt.Errorf("helper approval socket requirements must be unique canonical order")
	}
	return nil
}

// Result exposes only the opaque capability and non-secret metadata needed by an interactive launcher.
type Result struct {
	OperationID domain.OperationID
	LeaseKey    identity.LeaseKey
	Reference   helper.TicketReference
	Operation   helper.Operation
	Address     netip.Addr
	ExpiresAt   time.Time
}

// Validate rejects results that could identify a different operation or outlive the helper protocol bound.
func (result Result) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if err := result.LeaseKey.Validate(); err != nil {
		return err
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationEnsureLoopbackIdentity && result.Operation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("helper approval result operation %q is unsupported", result.Operation)
	}
	if !result.Address.Is4() || !result.Address.IsLoopback() || result.Address != result.Address.Unmap() {
		return fmt.Errorf("helper approval result address is not canonical IPv4 loopback")
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return fmt.Errorf("helper approval result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(maximumResultTime)) {
		return fmt.Errorf("helper approval result expiry exceeds the protocol bound")
	}
	return nil
}

// PlanSource resolves the same immutable durable plan before and immediately before ticket publication.
type PlanSource interface {
	// Resolve returns the current approval plan selected by one daemon-owned operation and lease key.
	Resolve(context.Context, Request) (Plan, error)
}

// OwnershipObserver supplies the protected machine-global authority pinned during installation.
type OwnershipObserver interface {
	// Observe returns the current protected ownership record and its canonical fingerprint.
	Observe(context.Context) (ownership.Observation, error)
}

// KeyLoader supplies only an established helper-ticket signing identity.
type KeyLoader interface {
	// Load returns the existing private key without creating replacement authority.
	Load(context.Context) (ed25519.PrivateKey, error)
}

// Publisher commits one signed ticket under a fresh opaque single-use reference.
type Publisher interface {
	// Publish signs and durably publishes one validated helper ticket.
	Publish(context.Context, helper.Ticket, ed25519.PrivateKey) (helper.TicketReference, error)
}

// LoopbackObserver supplies the exact assignment facts that bind a loopback mutation.
type LoopbackObserver interface {
	// Observe returns the current native assignment facts for one address.
	Observe(context.Context, netip.Addr) (loopback.Observation, error)
}

// ConflictObserver supplies the route, socket, and policy facts required before assigning an absent address.
type ConflictObserver interface {
	// Observe returns native facts for the authenticated requester's exact pre-assignment request.
	Observe(context.Context, hostconflict.Request, string) (hostconflict.Observation, error)
}

// Service serializes ticket publication against durable-plan and ownership revalidation.
type Service struct {
	plans      PlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	loopback   LoopbackObserver
	conflicts  ConflictObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// New creates an issuer from explicit daemon authorities and native observers.
func New(
	plans PlanSource,
	ownershipObserver OwnershipObserver,
	keys KeyLoader,
	publisher Publisher,
	loopbackObserver LoopbackObserver,
	conflicts ConflictObserver,
	clock helper.Clock,
	entropy io.Reader,
) *Service {
	if plans == nil || ownershipObserver == nil || keys == nil || publisher == nil || loopbackObserver == nil || conflicts == nil || clock == nil || entropy == nil {
		panic("ticketissuer.New requires every authority dependency")
	}
	return &Service{
		plans:      plans,
		ownership:  ownershipObserver,
		keys:       keys,
		publisher:  publisher,
		loopback:   loopbackObserver,
		conflicts:  conflicts,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefault opens the fixed production ownership, key, and ticket stores around one durable plan source.
func OpenDefault(plans PlanSource) (*Service, error) {
	if plans == nil {
		return nil, fmt.Errorf("open helper ticket issuer: durable plan source is required")
	}
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve helper ticket issuer paths: %w", err)
	}
	ownershipStore, err := ownership.NewStore(paths.OwnershipPath)
	if err != nil {
		return nil, fmt.Errorf("open helper ticket issuer ownership: %w", err)
	}
	keyStore, err := ticketkey.OpenDefault()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open helper ticket issuer key: %w", err), ownershipStore.Close())
	}
	publisher, err := ticketspool.OpenDefault()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper ticket issuer spool: %w", err),
			keyStore.Close(),
			ownershipStore.Close(),
		)
	}
	service := New(
		plans,
		ownershipStore,
		keyStore,
		publisher,
		loopback.New(),
		nativeConflictObserver{},
		helper.SystemClock{},
		rand.Reader,
	)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close(), ownershipStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance has left the serialized boundary.
func (service *Service) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives and publishes one capability solely from durable authority and fresh host observations.
func (service *Service) Issue(ctx context.Context, requesterIdentity string, request Request) (Result, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := request.Validate(); err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: %w", err)
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return Result{}, fmt.Errorf("issue helper ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return Result{}, err
	}
	owned, err := service.observeOwnership(ctx, requesterIdentity, plan)
	if err != nil {
		return Result{}, err
	}
	privateKey, err := service.keys.Load(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: load established signing key: %w", err)
	}
	if err := requirePinnedKey(privateKey, owned.Record.TicketVerifierKey); err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: %w", err)
	}

	ticket, err := service.buildTicket(ctx, requesterIdentity, plan, owned, privateKey)
	if err != nil {
		return Result{}, err
	}
	confirmedPlan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: revalidate approval plan: %w", err)
	}
	if !samePlan(plan, confirmedPlan) {
		return Result{}, fmt.Errorf("issue helper ticket: durable approval plan changed before publication")
	}
	confirmedOwnership, err := service.observeOwnership(ctx, requesterIdentity, confirmedPlan)
	if err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: revalidate ownership: %w", err)
	}
	if confirmedOwnership != owned {
		return Result{}, fmt.Errorf("issue helper ticket: machine ownership changed before publication")
	}

	reference, err := service.publisher.Publish(ctx, ticket, privateKey)
	if err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: publish capability: %w", err)
	}
	result := Result{
		OperationID: plan.OperationID,
		LeaseKey:    plan.Lease.Key,
		Reference:   reference,
		Operation:   plan.Mutation,
		Address:     plan.Lease.Address,
		ExpiresAt:   ticket.ExpiresAt,
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return Result{}, fmt.Errorf("issue helper ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates and isolates one source result before another authority boundary observes it.
func (service *Service) resolvePlan(ctx context.Context, request Request) (Plan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return Plan{}, fmt.Errorf("issue helper ticket: resolve approval plan: %w", err)
	}
	plan = clonePlan(plan)
	if err := plan.Validate(); err != nil {
		return Plan{}, fmt.Errorf("issue helper ticket: invalid approval plan: %w", err)
	}
	if plan.OperationID != request.OperationID || plan.Lease.Key != request.LeaseKey {
		return Plan{}, fmt.Errorf("issue helper ticket: approval plan does not match its requested identity")
	}
	return plan, nil
}

// observeOwnership proves the protected machine claim and durable lease describe one authority generation.
func (service *Service) observeOwnership(ctx context.Context, requesterIdentity string, plan Plan) (ownership.Observation, error) {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: observe machine ownership: %w", err)
	}
	if !observation.Exists {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: machine ownership is not claimed")
	}
	if err := observation.Record.Validate(); err != nil {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: invalid machine ownership: %w", err)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return ownership.Observation{}, err
	}
	if observation.Fingerprint != fingerprint {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: machine ownership fingerprint is invalid")
	}
	if requesterIdentity != observation.Record.OwnerIdentity {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: authenticated requester does not own the machine claim")
	}
	wantOwnership, err := identity.NewOwnership(identity.InstallationID(observation.Record.InstallationID), observation.Record.Generation)
	if err != nil {
		return ownership.Observation{}, err
	}
	if plan.Lease.Ownership.InstallationID != wantOwnership.InstallationID ||
		(plan.LeaseState == LeasePending && plan.Lease.Ownership.Generation != wantOwnership.Generation) ||
		(plan.LeaseState == LeaseActive && plan.Lease.Ownership.Generation > wantOwnership.Generation) {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: durable lease does not match machine ownership")
	}
	pool, err := netip.ParsePrefix(observation.Record.LoopbackPoolPrefix)
	if err != nil || !pool.Contains(plan.Lease.Address) {
		return ownership.Observation{}, fmt.Errorf("issue helper ticket: durable lease is outside the machine-owned pool")
	}
	return observation, nil
}

// buildTicket binds one fresh assignment observation and, when absent, one fresh host-safety observation.
func (service *Service) buildTicket(
	ctx context.Context,
	requesterIdentity string,
	plan Plan,
	owned ownership.Observation,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	loopbackObservation, err := service.loopback.Observe(ctx, plan.Lease.Address)
	if err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: observe loopback assignment: %w", err)
	}
	wantState := loopback.StateExact
	expectedState := helper.ObservationOwned
	if plan.LeaseState == LeasePending {
		wantState = loopback.StateAbsent
		expectedState = helper.ObservationAbsent
	}
	if loopbackObservation.State != wantState {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: loopback assignment state is %q, want %q", loopbackObservation.State, wantState)
	}
	loopbackFingerprint, err := loopbackObservation.Fingerprint()
	if err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: fingerprint loopback assignment: %w", err)
	}

	var expectedPreAssignment *helper.ExpectedPreAssignment
	if plan.Mutation == helper.OperationEnsureLoopbackIdentity && plan.LeaseState == LeasePending {
		expectedPreAssignment, err = service.observePreAssignment(ctx, requesterIdentity, plan)
		if err != nil {
			return helper.Ticket{}, err
		}
	}
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	ticket := helper.Ticket{
		Version:             helper.ProtocolVersion,
		Operation:           plan.Mutation,
		InstallationID:      owned.Record.InstallationID,
		RequesterIdentity:   requesterIdentity,
		OwnershipGeneration: owned.Record.Generation,
		ApprovedPool:        owned.Record.LoopbackPoolPrefix,
		ApprovedAddress:     plan.Lease.Address.String(),
		ExpectedObservation: helper.ExpectedObservation{
			State:       expectedState,
			Fingerprint: loopbackFingerprint,
		},
		ExpectedPreAssignment: expectedPreAssignment,
		Nonce:                 hex.EncodeToString(nonce),
		ExpiresAt:             now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, owned.Record.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}

// observePreAssignment requires a fully safe native observation and maps its canonical request into the signed protocol.
func (service *Service) observePreAssignment(ctx context.Context, requesterIdentity string, plan Plan) (*helper.ExpectedPreAssignment, error) {
	request, err := hostconflict.NewPreAssignmentRequest(plan.Lease.Address, plan.Requirements)
	if err != nil {
		return nil, fmt.Errorf("issue helper ticket: construct pre-assignment request: %w", err)
	}
	observation, err := service.conflicts.Observe(ctx, request, requesterIdentity)
	if err != nil {
		return nil, fmt.Errorf("issue helper ticket: observe pre-assignment conflicts: %w", err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		return nil, fmt.Errorf("issue helper ticket: classify pre-assignment conflicts: %w", err)
	}
	if assessment.State != hostconflict.StateSafe {
		return nil, fmt.Errorf("issue helper ticket: pre-assignment state is %q", assessment.State)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("issue helper ticket: fingerprint pre-assignment conflicts: %w", err)
	}
	requirements := make([]helper.SocketRequirement, len(plan.Requirements))
	for index, requirement := range plan.Requirements {
		transport := helper.SocketTransportTCP4
		if requirement.Transport == hostconflict.TransportUDP4 {
			transport = helper.SocketTransportUDP4
		}
		requirements[index] = helper.SocketRequirement{Transport: transport, Port: requirement.Port}
	}
	return &helper.ExpectedPreAssignment{Fingerprint: fingerprint, Requirements: requirements}, nil
}

// requirePinnedKey proves the established private key still corresponds to protected machine ownership.
func requirePinnedKey(privateKey ed25519.PrivateKey, expected string) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("established signing key is invalid")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("established signing public key is invalid")
	}
	encoded := base64.StdEncoding.EncodeToString(publicKey)
	if len(encoded) != len(expected) || subtle.ConstantTimeCompare([]byte(encoded), []byte(expected)) != 1 {
		return fmt.Errorf("established signing key does not match machine ownership")
	}
	return nil
}

// clonePlan isolates source-owned requirement memory from later durable-plan comparisons.
func clonePlan(plan Plan) Plan {
	clone := plan
	clone.Requirements = slices.Clone(plan.Requirements)
	return clone
}

// samePlan compares every durable authority field while preserving canonical requirement order.
func samePlan(left Plan, right Plan) bool {
	return left.OperationID == right.OperationID &&
		left.OperationRevision == right.OperationRevision &&
		left.OperationState == right.OperationState &&
		left.Mutation == right.Mutation &&
		left.Lease == right.Lease &&
		left.LeaseState == right.LeaseState &&
		slices.Equal(left.Requirements, right.Requirements)
}

// normalizeContext keeps optional cancellation scopes on the same code path as live daemon requests.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
