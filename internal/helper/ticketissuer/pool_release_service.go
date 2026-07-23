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

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketkey"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
)

var (
	// ErrPoolPublicationIndeterminate means the returned result is the only reference that may identify a durably published pool-release capability.
	ErrPoolPublicationIndeterminate = errors.New("pool-release capability publication is indeterminate")
)

// PoolReleaseTarget binds one canonical pool address to its retained loopback observation.
type PoolReleaseTarget struct {
	Address                netip.Addr
	ObservationFingerprint string
}

// PoolReleaseRequest selects one durable global loopback-pool release approval without carrying release authority.
type PoolReleaseRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one durable global pool-release plan.
func (request PoolReleaseRequest) Validate() error {
	return request.OperationID.Validate()
}

// PoolReleasePlan is the immutable authority for one global network-release loopback-pool capability.
type PoolReleasePlan struct {
	Operation          domain.Operation
	OperationRevision  domain.Sequence
	CheckpointRevision domain.Sequence
	TargetOwnership    ownership.Record
	Pool               identity.Pool
	Targets            []PoolReleaseTarget
}

// Validate rejects plans that do not authorize exactly one current complete pool release.
func (plan PoolReleasePlan) Validate() error {
	if err := plan.Operation.Validate(); err != nil {
		return fmt.Errorf("pool release approval operation: %w", err)
	}
	if plan.Operation.Kind != domain.OperationKindNetworkRelease {
		return fmt.Errorf(
			"pool release approval operation kind is %q, want %q",
			plan.Operation.Kind,
			domain.OperationKindNetworkRelease,
		)
	}
	if plan.Operation.ProjectID != "" {
		return errors.New("pool release approval operation must be global")
	}
	if plan.Operation.State != domain.OperationRunning {
		return fmt.Errorf(
			"pool release approval operation state is %q, want %q",
			plan.Operation.State,
			domain.OperationRunning,
		)
	}
	if plan.Operation.Phase != "releasing network runtime" {
		return fmt.Errorf(
			"pool release approval operation phase is %q, want %q",
			plan.Operation.Phase,
			"releasing network runtime",
		)
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("pool release approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return fmt.Errorf("pool release approval checkpoint revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.CheckpointRevision <= plan.OperationRevision {
		return errors.New("pool release approval checkpoint revision must follow the operation revision")
	}
	if err := plan.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("pool release approval target ownership: %w", err)
	}
	if plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"pool release approval target ownership schema is %d, want %d",
			plan.TargetOwnership.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	prefix, addresses, err := validateExactPool(plan.Pool)
	if err != nil {
		return err
	}
	if plan.TargetOwnership.LoopbackPoolPrefix != prefix.String() {
		return errors.New("pool release approval ownership prefix does not match its exact pool")
	}
	if len(plan.Targets) != len(addresses) {
		return fmt.Errorf("pool release approval must contain exactly %d targets", len(addresses))
	}
	for index, address := range addresses {
		target := plan.Targets[index]
		if target.Address != address {
			return errors.New("pool release approval targets do not enumerate the complete pool in canonical order")
		}
		if !canonicalSHA256Fingerprint(target.ObservationFingerprint) {
			return fmt.Errorf("pool release approval target %s observation fingerprint is invalid", address)
		}
	}
	return nil
}

// PoolReleasePlanSource resolves the same immutable pool-release plan before and immediately before publication.
type PoolReleasePlanSource interface {
	// Resolve returns the current global pool-release approval selected by one daemon-owned operation.
	Resolve(context.Context, PoolReleaseRequest) (PoolReleasePlan, error)
}

// PoolReleaseService serializes pool-release publication against durable ownership and loopback revalidation.
type PoolReleaseService struct {
	plans      PoolReleasePlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	loopback   LoopbackObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// poolReleaseDefaultOpeners keeps fixed storage construction replaceable in lifecycle tests.
type poolReleaseDefaultOpeners struct {
	openKeys      func() (defaultKeyStoreCloser, error)
	openPublisher func() (defaultPublisherCloser, error)
}

// NewPoolReleaseService creates a release-only issuer from explicit durable authorities and a read-only loopback observer.
func NewPoolReleaseService(
	plans PoolReleasePlanSource,
	ownershipObserver OwnershipObserver,
	keys KeyLoader,
	publisher Publisher,
	loopbackObserver LoopbackObserver,
	clock helper.Clock,
	entropy io.Reader,
) *PoolReleaseService {
	if plans == nil ||
		ownershipObserver == nil ||
		keys == nil ||
		publisher == nil ||
		loopbackObserver == nil ||
		clock == nil ||
		entropy == nil {
		panic("ticketissuer.NewPoolReleaseService requires every authority dependency")
	}
	return &PoolReleaseService{
		plans:      plans,
		ownership:  ownershipObserver,
		keys:       keys,
		publisher:  publisher,
		loopback:   loopbackObserver,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefaultPoolReleaseService opens fixed user-owned stores around explicit release authorities.
func OpenDefaultPoolReleaseService(
	plans PoolReleasePlanSource,
	ownershipObserver OwnershipObserver,
) (*PoolReleaseService, error) {
	return openDefaultPoolReleaseService(plans, ownershipObserver, defaultPoolReleaseOpeners())
}

// defaultPoolReleaseOpeners binds production issuance to Harbor's fixed user-owned key and ticket paths.
func defaultPoolReleaseOpeners() poolReleaseDefaultOpeners {
	return poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultPoolReleaseService opens both stores as one close-safe ownership unit.
func openDefaultPoolReleaseService(
	plans PoolReleasePlanSource,
	ownershipObserver OwnershipObserver,
	openers poolReleaseDefaultOpeners,
) (*PoolReleaseService, error) {
	if plans == nil {
		return nil, errors.New("open helper pool-release ticket issuer: durable plan source is required")
	}
	if ownershipObserver == nil {
		return nil, errors.New("open helper pool-release ticket issuer: ownership observer is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, errors.New("open helper pool-release ticket issuer: default store openers are incomplete")
	}
	keyStore, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper pool-release ticket issuer key: %w", err)
	}
	if keyStore == nil {
		return nil, errors.New("open helper pool-release ticket issuer key: opener returned nil")
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper pool-release ticket issuer spool: %w", err),
			keyStore.Close(),
		)
	}
	if publisher == nil {
		return nil, errors.Join(
			errors.New("open helper pool-release ticket issuer spool: opener returned nil"),
			keyStore.Close(),
		)
	}
	service := NewPoolReleaseService(
		plans,
		ownershipObserver,
		keyStore,
		publisher,
		loopback.New(),
		helper.SystemClock{},
		rand.Reader,
	)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance leaves the serialized boundary.
func (service *PoolReleaseService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one release capability from stable durable authority and two matching loopback observations.
func (service *PoolReleaseService) Issue(
	ctx context.Context,
	requesterIdentity string,
	request PoolReleaseRequest,
) (PoolResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return PoolResult{}, err
	}
	if err := request.Validate(); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: %w", err)
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return PoolResult{}, errors.New("issue helper pool-release ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return PoolResult{}, err
	}
	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return PoolResult{}, err
	}
	if err := service.observeOwnership(ctx, requesterIdentity, plan); err != nil {
		return PoolResult{}, err
	}
	privateKey, err := service.keys.Load(ctx)
	if err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: load established signing key: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: %w", err)
	}
	expectations, err := service.observeTargets(ctx, plan)
	if err != nil {
		return PoolResult{}, err
	}
	ticket, err := service.buildTicket(plan, expectations, privateKey)
	if err != nil {
		return PoolResult{}, err
	}
	confirmed, err := service.resolvePlan(ctx, request)
	if err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: revalidate approval plan: %w", err)
	}
	if !samePoolReleasePlan(plan, confirmed) {
		return PoolResult{}, errors.New("issue helper pool-release ticket: durable approval plan changed before publication")
	}
	if err := service.observeOwnership(ctx, requesterIdentity, confirmed); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: revalidate ownership: %w", err)
	}
	confirmedExpectations, err := service.observeTargets(ctx, confirmed)
	if err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: revalidate loopback pool: %w", err)
	}
	if !slices.Equal(expectations, confirmedExpectations) {
		return PoolResult{}, errors.New("issue helper pool-release ticket: loopback pool changed before publication")
	}
	reference, publishErr := service.publisher.Publish(ctx, ticket, privateKey)
	result := PoolResult{
		OperationID: plan.Operation.ID,
		Reference:   reference,
		Operation:   helper.OperationReleaseLoopbackPool,
		Pool:        plan.Pool.Prefix(),
		ExpiresAt:   ticket.ExpiresAt,
	}
	if publishErr != nil {
		wrapped := fmt.Errorf("issue helper pool-release ticket: publish capability: %w", publishErr)
		if !errors.Is(publishErr, ticketspool.ErrDurabilityUncertain) {
			return PoolResult{}, wrapped
		}
		if err := result.Validate(ticket.ExpiresAt.Add(-ticketLifetime)); err != nil {
			return PoolResult{}, errors.Join(
				ErrPoolPublicationIndeterminate,
				wrapped,
				fmt.Errorf("issue helper pool-release ticket: invalid durability-uncertain publication result: %w", err),
			)
		}
		return result, errors.Join(ErrPoolPublicationIndeterminate, wrapped)
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return PoolResult{}, fmt.Errorf("issue helper pool-release ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates and isolates source-owned mutable authority on every durable read boundary.
func (service *PoolReleaseService) resolvePlan(
	ctx context.Context,
	request PoolReleaseRequest,
) (PoolReleasePlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return PoolReleasePlan{}, fmt.Errorf("issue helper pool-release ticket: resolve approval plan: %w", err)
	}
	plan, err = clonePoolReleasePlan(plan)
	if err != nil {
		return PoolReleasePlan{}, fmt.Errorf("issue helper pool-release ticket: isolate approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return PoolReleasePlan{}, fmt.Errorf("issue helper pool-release ticket: invalid approval plan: %w", err)
	}
	if plan.Operation.ID != request.OperationID {
		return PoolReleasePlan{}, errors.New(
			"issue helper pool-release ticket: approval plan does not match requested operation",
		)
	}
	return plan, nil
}

// observeOwnership requires the exact approved ownership record and canonical fingerprint to remain current.
func (service *PoolReleaseService) observeOwnership(
	ctx context.Context,
	requesterIdentity string,
	plan PoolReleasePlan,
) error {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return fmt.Errorf("issue helper pool-release ticket: observe ownership projection: %w", err)
	}
	if !observation.Exists {
		return errors.New("issue helper pool-release ticket: ownership projection is absent")
	}
	if observation.Record != plan.TargetOwnership {
		return errors.New("issue helper pool-release ticket: ownership projection differs from the approved target")
	}
	if requesterIdentity != plan.TargetOwnership.OwnerIdentity {
		return errors.New("issue helper pool-release ticket: authenticated requester does not own the approved machine claim")
	}
	fingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return fmt.Errorf("issue helper pool-release ticket: fingerprint approved target ownership: %w", err)
	}
	if observation.Fingerprint != fingerprint {
		return errors.New(
			"issue helper pool-release ticket: ownership projection fingerprint does not match the approved target",
		)
	}
	return nil
}

// observeTargets resolves the complete ordered release expectation vector from current admissible loopback facts.
func (service *PoolReleaseService) observeTargets(
	ctx context.Context,
	plan PoolReleasePlan,
) ([]helper.ExpectedLoopbackIdentity, error) {
	expectations := make([]helper.ExpectedLoopbackIdentity, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		observation, err := service.loopback.Observe(ctx, target.Address)
		if err != nil {
			return nil, fmt.Errorf("issue helper pool-release ticket: observe loopback assignment %s: %w", target.Address, err)
		}
		if observation.Address != target.Address {
			return nil, fmt.Errorf(
				"issue helper pool-release ticket: loopback observation address %s does not match %s",
				observation.Address,
				target.Address,
			)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return nil, fmt.Errorf("issue helper pool-release ticket: fingerprint loopback assignment %s: %w", target.Address, err)
		}
		if !canonicalSHA256Fingerprint(fingerprint) {
			return nil, fmt.Errorf("issue helper pool-release ticket: loopback assignment %s fingerprint is invalid", target.Address)
		}
		expected := helper.ExpectedLoopbackIdentity{
			Address: target.Address.String(),
		}
		switch observation.State {
		case loopback.StateExact:
			if fingerprint != target.ObservationFingerprint {
				return nil, fmt.Errorf(
					"issue helper pool-release ticket: loopback assignment %s does not match its retained observation",
					target.Address,
				)
			}
			expected.ExpectedObservation = helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: fingerprint,
			}
		case loopback.StateAbsent:
			expected.ExpectedObservation = helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: fingerprint,
			}
		default:
			return nil, fmt.Errorf(
				"issue helper pool-release ticket: loopback assignment %s state is %q, want %q or %q",
				target.Address,
				observation.State,
				loopback.StateExact,
				loopback.StateAbsent,
			)
		}
		expectations = append(expectations, expected)
	}
	return expectations, nil
}

// buildTicket binds every canonical pool address to the freshly resolved release expectation.
func (service *PoolReleaseService) buildTicket(
	plan PoolReleasePlan,
	expectations []helper.ExpectedLoopbackIdentity,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper pool-release ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	ticket := helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                helper.OperationReleaseLoopbackPool,
		InstallationID:           plan.TargetOwnership.InstallationID,
		RequesterIdentity:        plan.TargetOwnership.OwnerIdentity,
		OwnershipGeneration:      plan.TargetOwnership.Generation,
		OwnershipSchemaVersion:   plan.TargetOwnership.SchemaVersion,
		NetworkPolicyFingerprint: plan.TargetOwnership.NetworkPolicyFingerprint,
		ApprovedPool:             plan.TargetOwnership.LoopbackPoolPrefix,
		ExpectedLoopbackPool: &helper.ExpectedLoopbackPool{
			Identities: expectations,
		},
		Nonce:     hex.EncodeToString(nonce),
		ExpiresAt: now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper pool-release ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf(
			"issue helper pool-release ticket: signing key changed during construction: %w",
			err,
		)
	}
	return ticket, nil
}

// clonePoolReleasePlan prevents mutable source-owned target and pool slices from crossing a durable-read boundary.
func clonePoolReleasePlan(plan PoolReleasePlan) (PoolReleasePlan, error) {
	pool, err := identity.NewPool(plan.Pool.Prefix(), plan.Pool.Candidates())
	if err != nil {
		return PoolReleasePlan{}, err
	}
	plan.Operation = cloneLowPortOperation(plan.Operation)
	plan.Pool = pool
	plan.Targets = append([]PoolReleaseTarget(nil), plan.Targets...)
	return plan, nil
}

// samePoolReleasePlan compares every durable authority field before publication.
func samePoolReleasePlan(left PoolReleasePlan, right PoolReleasePlan) bool {
	return sameLowPortOperation(left.Operation, right.Operation) &&
		left.OperationRevision == right.OperationRevision &&
		left.CheckpointRevision == right.CheckpointRevision &&
		left.TargetOwnership == right.TargetOwnership &&
		left.Pool.Prefix() == right.Pool.Prefix() &&
		slices.Equal(left.Pool.Candidates(), right.Pool.Candidates()) &&
		slices.Equal(left.Targets, right.Targets)
}
