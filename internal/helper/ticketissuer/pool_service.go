package ticketissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
)

const poolIdentityCount = 8

// PoolMode identifies whether a durable pool plan creates first-claim authority or repairs established authority.
type PoolMode string

const (
	// PoolModeBootstrap provisions generation-one authority whose absence remains enforced by the elevated redeemer.
	PoolModeBootstrap PoolMode = "bootstrap"
	// PoolModeRepair uses only an already established signing identity.
	PoolModeRepair PoolMode = "repair"
)

// PoolRequest selects one durable machine-level pool approval without carrying its authority.
type PoolRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one durable pool plan.
func (request PoolRequest) Validate() error {
	return request.OperationID.Validate()
}

// PoolPlan is the complete durable authority for one exact pool bootstrap or repair.
type PoolPlan struct {
	OperationID       domain.OperationID
	OperationRevision domain.Sequence
	OperationState    domain.OperationState
	Mode              PoolMode
	Ownership         ownership.Record
	Pool              identity.Pool
}

// Validate rejects pool plans whose ownership and exact-eight address authority disagree.
func (plan PoolPlan) Validate() error {
	if err := plan.OperationID.Validate(); err != nil {
		return err
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("helper pool approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.OperationState != domain.OperationRequiresApproval {
		return fmt.Errorf("helper pool approval operation state is %q, want %q", plan.OperationState, domain.OperationRequiresApproval)
	}
	switch plan.Mode {
	case PoolModeBootstrap, PoolModeRepair:
	default:
		return fmt.Errorf("helper pool approval mode %q is unsupported", plan.Mode)
	}
	if err := plan.Ownership.Validate(); err != nil {
		return fmt.Errorf("helper pool approval ownership is invalid: %w", err)
	}
	if plan.Mode == PoolModeBootstrap && plan.Ownership.Generation != 1 {
		return fmt.Errorf("helper pool approval bootstrap requires ownership generation 1")
	}
	prefix, _, err := validateExactPool(plan.Pool)
	if err != nil {
		return err
	}
	if plan.Ownership.LoopbackPoolPrefix != prefix.String() {
		return fmt.Errorf("helper pool approval ownership prefix does not match its exact pool")
	}
	return nil
}

// PoolPlanSource resolves the same immutable durable pool plan before and immediately before publication.
type PoolPlanSource interface {
	// Resolve returns the current pool approval selected by one daemon-owned operation.
	Resolve(context.Context, PoolRequest) (PoolPlan, error)
}

// PoolKeyStore loads established repair authority and provisions or reloads generation-one bootstrap authority.
type PoolKeyStore interface {
	KeyLoader
	// LoadOrCreate reloads the exact signing identity or atomically publishes one generation-one identity.
	LoadOrCreate(context.Context) (ed25519.PrivateKey, error)
}

// poolKeyStoreCloser releases one production signing-key store after issuance shuts down.
type poolKeyStoreCloser interface {
	PoolKeyStore
	Close() error
}

// poolPublisherCloser releases one production ticket publisher after issuance shuts down.
type poolPublisherCloser interface {
	Publisher
	Close() error
}

// poolDefaultOpeners contains only the daemon-owned stores required by default pool issuance.
type poolDefaultOpeners struct {
	openKeys      func() (poolKeyStoreCloser, error)
	openPublisher func() (poolPublisherCloser, error)
}

// PoolResult exposes only the opaque capability and bounded pool metadata needed by an interactive launcher.
type PoolResult struct {
	OperationID domain.OperationID
	Reference   helper.TicketReference
	Operation   helper.Operation
	Pool        netip.Prefix
	ExpiresAt   time.Time
}

// Validate rejects results that identify another durable operation or escape the pool protocol bounds.
func (result PoolResult) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationEnsureLoopbackPool {
		return fmt.Errorf("helper pool approval result operation %q is unsupported", result.Operation)
	}
	if err := validateExactPrefix(result.Pool); err != nil {
		return fmt.Errorf("helper pool approval result: %w", err)
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return fmt.Errorf("helper pool approval result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("helper pool approval result expiry exceeds the protocol bound")
	}
	return nil
}

// PoolService serializes exact pool publication against durable-plan revalidation and fresh host observations.
type PoolService struct {
	plans      PoolPlanSource
	keys       PoolKeyStore
	publisher  Publisher
	loopback   LoopbackObserver
	conflicts  ConflictObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// NewPoolService creates an issuer whose durable plan supplies every signed ownership dimension.
func NewPoolService(
	plans PoolPlanSource,
	keys PoolKeyStore,
	publisher Publisher,
	loopbackObserver LoopbackObserver,
	conflicts ConflictObserver,
	clock helper.Clock,
	entropy io.Reader,
) *PoolService {
	if plans == nil || keys == nil || publisher == nil || loopbackObserver == nil || conflicts == nil || clock == nil || entropy == nil {
		panic("ticketissuer.NewPoolService requires every authority dependency")
	}
	return &PoolService{
		plans:      plans,
		keys:       keys,
		publisher:  publisher,
		loopback:   loopbackObserver,
		conflicts:  conflicts,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefaultPoolService opens the daemon-owned production key and ticket stores around one durable pool plan source.
func OpenDefaultPoolService(plans PoolPlanSource) (*PoolService, error) {
	return openDefaultPoolService(plans, defaultPoolOpeners())
}

// defaultPoolOpeners binds default issuance only to daemon-owned signing-key and pending-ticket stores.
func defaultPoolOpeners() poolDefaultOpeners {
	return poolDefaultOpeners{
		openKeys: func() (poolKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (poolPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultPoolService opens daemon-owned production stores without crossing the protected ownership boundary.
func openDefaultPoolService(plans PoolPlanSource, openers poolDefaultOpeners) (*PoolService, error) {
	if plans == nil {
		return nil, fmt.Errorf("open helper pool ticket issuer: durable plan source is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, fmt.Errorf("open helper pool ticket issuer: default store openers are incomplete")
	}
	keyStore, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper pool ticket issuer key: %w", err)
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper pool ticket issuer spool: %w", err),
			keyStore.Close(),
		)
	}
	service := NewPoolService(
		plans,
		keyStore,
		publisher,
		loopback.New(),
		nativeConflictObserver{},
		helper.SystemClock{},
		rand.Reader,
	)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight pool issuance has left the serialized boundary.
func (service *PoolService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one exact pool capability from a stable durable plan and protected ownership snapshot.
func (service *PoolService) Issue(ctx context.Context, requesterIdentity string, request PoolRequest) (PoolResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return PoolResult{}, err
	}
	if err := request.Validate(); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: %w", err)
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return PoolResult{}, err
	}

	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return PoolResult{}, err
	}
	if requesterIdentity != plan.Ownership.OwnerIdentity {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: authenticated requester does not match the approved machine owner")
	}

	privateKey, err := service.loadKey(ctx, plan.Mode)
	if err != nil {
		return PoolResult{}, err
	}
	if err := requirePinnedKey(privateKey, plan.Ownership.TicketVerifierKey); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: %w", err)
	}

	ticket, err := service.buildTicket(ctx, plan, privateKey)
	if err != nil {
		return PoolResult{}, err
	}
	confirmedPlan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: revalidate approval plan: %w", err)
	}
	if !samePoolPlan(plan, confirmedPlan) {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: durable approval plan changed before publication")
	}
	reference, err := service.publisher.Publish(ctx, ticket, privateKey)
	if err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: publish capability: %w", err)
	}
	result := PoolResult{
		OperationID: plan.OperationID,
		Reference:   reference,
		Operation:   helper.OperationEnsureLoopbackPool,
		Pool:        plan.Pool.Prefix(),
		ExpiresAt:   ticket.ExpiresAt,
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates and isolates one source result before another authority boundary observes it.
func (service *PoolService) resolvePlan(ctx context.Context, request PoolRequest) (PoolPlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return PoolPlan{}, fmt.Errorf("issue helper pool ticket: resolve approval plan: %w", err)
	}
	plan, err = clonePoolPlan(plan)
	if err != nil {
		return PoolPlan{}, fmt.Errorf("issue helper pool ticket: isolate approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return PoolPlan{}, fmt.Errorf("issue helper pool ticket: invalid approval plan: %w", err)
	}
	if plan.OperationID != request.OperationID {
		return PoolPlan{}, fmt.Errorf("issue helper pool ticket: approval plan does not match its requested identity")
	}
	return plan, nil
}

// loadKey prevents an established-authority repair from silently creating replacement signing authority.
func (service *PoolService) loadKey(ctx context.Context, mode PoolMode) (ed25519.PrivateKey, error) {
	switch mode {
	case PoolModeBootstrap:
		privateKey, err := service.keys.LoadOrCreate(ctx)
		if err != nil {
			return nil, fmt.Errorf("issue helper pool ticket: load or create bootstrap signing key: %w", err)
		}
		return privateKey, nil
	case PoolModeRepair:
		privateKey, err := service.keys.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("issue helper pool ticket: load established signing key: %w", err)
		}
		return privateKey, nil
	default:
		return nil, fmt.Errorf("issue helper pool ticket: pool approval mode %q is unsupported", mode)
	}
}

// buildTicket binds every canonical pool address to a fresh exact assignment observation.
func (service *PoolService) buildTicket(
	ctx context.Context,
	plan PoolPlan,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	_, addresses, err := validateExactPool(plan.Pool)
	if err != nil {
		return helper.Ticket{}, err
	}
	identities := make([]helper.ExpectedLoopbackIdentity, 0, len(addresses))
	for _, address := range addresses {
		observation, err := service.loopback.Observe(ctx, address)
		if err != nil {
			return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: observe loopback assignment %s: %w", address, err)
		}
		if observation.Address != address {
			return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: loopback observation address %s does not match %s", observation.Address, address)
		}

		expectedState := helper.ObservationAbsent
		switch observation.State {
		case loopback.StateAbsent:
		case loopback.StateExact:
			expectedState = helper.ObservationOwned
		default:
			return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: loopback assignment %s state is %q", address, observation.State)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: fingerprint loopback assignment %s: %w", address, err)
		}

		identityEvidence := helper.ExpectedLoopbackIdentity{
			Address: address.String(),
			ExpectedObservation: helper.ExpectedObservation{
				State:       expectedState,
				Fingerprint: fingerprint,
			},
		}
		if expectedState == helper.ObservationAbsent {
			identityEvidence.ExpectedPreAssignment, err = service.observePoolPreAssignment(ctx, plan.Ownership.OwnerIdentity, address)
			if err != nil {
				return helper.Ticket{}, err
			}
		}
		identities = append(identities, identityEvidence)
	}

	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	ticket := helper.Ticket{
		Version:             helper.ProtocolVersion,
		Operation:           helper.OperationEnsureLoopbackPool,
		InstallationID:      plan.Ownership.InstallationID,
		RequesterIdentity:   plan.Ownership.OwnerIdentity,
		OwnershipGeneration: plan.Ownership.Generation,
		ApprovedPool:        plan.Ownership.LoopbackPoolPrefix,
		ExpectedLoopbackPool: &helper.ExpectedLoopbackPool{
			Identities: identities,
		},
		Nonce:     hex.EncodeToString(nonce),
		ExpiresAt: now.Add(helper.MaxTicketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.Ownership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper pool ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}

// observePoolPreAssignment records only route and policy safety because pool setup grants no socket capability.
func (service *PoolService) observePoolPreAssignment(
	ctx context.Context,
	requesterIdentity string,
	address netip.Addr,
) (*helper.ExpectedPreAssignment, error) {
	request, err := hostconflict.NewPreAssignmentRequest(address, nil)
	if err != nil {
		return nil, fmt.Errorf("issue helper pool ticket: construct route-only pre-assignment request %s: %w", address, err)
	}
	observation, err := service.conflicts.Observe(ctx, request, requesterIdentity)
	if err != nil {
		return nil, fmt.Errorf("issue helper pool ticket: observe pre-assignment conflicts %s: %w", address, err)
	}
	if observation.Request.Purpose() != request.Purpose() || observation.Request.Candidate() != address || len(observation.Request.Requirements()) != 0 {
		return nil, fmt.Errorf("issue helper pool ticket: pre-assignment observation does not match route-only request for %s", address)
	}
	assessment, err := observation.Classify()
	if err != nil {
		return nil, fmt.Errorf("issue helper pool ticket: classify pre-assignment conflicts %s: %w", address, err)
	}
	if assessment.State != hostconflict.StateSafe {
		return nil, fmt.Errorf("issue helper pool ticket: pre-assignment state for %s is %q", address, assessment.State)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("issue helper pool ticket: fingerprint pre-assignment conflicts %s: %w", address, err)
	}
	return &helper.ExpectedPreAssignment{
		Fingerprint:  fingerprint,
		Requirements: []helper.SocketRequirement{},
	}, nil
}

// validateExactPool returns the canonical complete /29 address list used by both plans and tickets.
func validateExactPool(pool identity.Pool) (netip.Prefix, []netip.Addr, error) {
	if err := pool.Validate(); err != nil {
		return netip.Prefix{}, nil, fmt.Errorf("helper pool approval pool is invalid: %w", err)
	}
	prefix := pool.Prefix()
	if err := validateExactPrefix(prefix); err != nil {
		return netip.Prefix{}, nil, fmt.Errorf("helper pool approval: %w", err)
	}
	addresses := pool.Candidates()
	if len(addresses) != poolIdentityCount {
		return netip.Prefix{}, nil, fmt.Errorf("helper pool approval must contain exactly eight identities")
	}
	expected := prefix.Addr()
	for _, address := range addresses {
		if address != expected {
			return netip.Prefix{}, nil, fmt.Errorf("helper pool approval identities do not enumerate the complete pool in canonical order")
		}
		expected = expected.Next()
	}
	return prefix, addresses, nil
}

// validateExactPrefix confines aggregate setup authority to one canonical IPv4-loopback /29.
func validateExactPrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Addr() != prefix.Addr().Unmap() || !prefix.Addr().IsLoopback() || prefix.Bits() != 29 || prefix != prefix.Masked() {
		return fmt.Errorf("pool is not a canonical IPv4-loopback /29")
	}
	return nil
}

// clonePoolPlan isolates the source's pool candidates before later durable-plan comparison.
func clonePoolPlan(plan PoolPlan) (PoolPlan, error) {
	pool, err := identity.NewPool(plan.Pool.Prefix(), plan.Pool.Candidates())
	if err != nil {
		return PoolPlan{}, err
	}
	clone := plan
	clone.Pool = pool
	return clone, nil
}

// samePoolPlan compares every durable ownership and exact-address authority field.
func samePoolPlan(left PoolPlan, right PoolPlan) bool {
	return left.OperationID == right.OperationID &&
		left.OperationRevision == right.OperationRevision &&
		left.OperationState == right.OperationState &&
		left.Mode == right.Mode &&
		left.Ownership == right.Ownership &&
		left.Pool.Prefix() == right.Pool.Prefix() &&
		slices.Equal(left.Pool.Candidates(), right.Pool.Candidates())
}
