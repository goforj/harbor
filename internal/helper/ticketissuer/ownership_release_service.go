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
	"github.com/goforj/harbor/internal/host/ownership"
)

var (
	// ErrOwnershipReleasePublicationIndeterminate means a release ticket reference may have been durably published and must be reconciled instead of replaced.
	ErrOwnershipReleasePublicationIndeterminate = errors.New("network ownership-release capability publication is indeterminate")
)

// OwnershipReleaseRequest selects one durable global ownership-release approval without carrying release authority.
type OwnershipReleaseRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one durable global ownership-release plan.
func (request OwnershipReleaseRequest) Validate() error {
	return request.OperationID.Validate()
}

// OwnershipReleasePlan is the immutable authority for the terminal global machine-ownership release.
type OwnershipReleasePlan struct {
	Operation                    domain.Operation
	OperationRevision            domain.Sequence
	CheckpointRevision           domain.Sequence
	Mutation                     helper.Operation
	TargetOwnership              ownership.Record
	ExpectedOwnershipFingerprint string
}

// Validate rejects plans that do not authorize exactly one current terminal ownership release.
func (plan OwnershipReleasePlan) Validate() error {
	if err := plan.Operation.Validate(); err != nil {
		return fmt.Errorf("ownership release approval operation: %w", err)
	}
	if plan.Operation.Kind != domain.OperationKindNetworkRelease {
		return fmt.Errorf("ownership release approval operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkRelease)
	}
	if plan.Operation.ProjectID != "" {
		return errors.New("ownership release approval operation must be global")
	}
	if plan.Operation.State != domain.OperationRunning || plan.Operation.Phase != "releasing network runtime" {
		return errors.New("ownership release approval operation is not running global release authority")
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("ownership release approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return fmt.Errorf("ownership release approval checkpoint revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.CheckpointRevision <= plan.OperationRevision {
		return errors.New("ownership release approval checkpoint revision must follow the operation revision")
	}
	if plan.Mutation != helper.OperationReleaseNetworkOwnership {
		return fmt.Errorf("ownership release approval mutation is %q, want %q", plan.Mutation, helper.OperationReleaseNetworkOwnership)
	}
	if err := plan.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("ownership release approval target ownership: %w", err)
	}
	if plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf("ownership release approval target ownership schema is %d, want %d", plan.TargetOwnership.SchemaVersion, ownership.NetworkPolicySchemaVersion)
	}
	fingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return fmt.Errorf("ownership release approval target fingerprint: %w", err)
	}
	if plan.ExpectedOwnershipFingerprint != fingerprint {
		return errors.New("ownership release approval fingerprint does not match target ownership")
	}
	return nil
}

// OwnershipReleasePlanSource resolves the same immutable ownership-release plan before and immediately before publication.
type OwnershipReleasePlanSource interface {
	// Resolve returns the current global ownership-release approval selected by one daemon-owned operation.
	Resolve(context.Context, OwnershipReleaseRequest) (OwnershipReleasePlan, error)
}

// OwnershipReleaseResult exposes only opaque launch metadata for one ownership-release capability.
type OwnershipReleaseResult struct {
	OperationID          domain.OperationID
	OperationRevision    domain.Sequence
	CheckpointRevision   domain.Sequence
	Reference            helper.TicketReference
	Operation            helper.Operation
	OwnershipFingerprint string
	ExpiresAt            time.Time
}

// Validate rejects results that could cross the selected operation or helper lifetime boundary.
func (result OwnershipReleaseResult) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if result.OperationRevision == 0 || result.OperationRevision > domain.MaximumSequence {
		return errors.New("ownership release result operation revision is invalid")
	}
	if result.CheckpointRevision == 0 ||
		result.CheckpointRevision > domain.MaximumSequence ||
		result.CheckpointRevision <= result.OperationRevision {
		return errors.New("ownership release result checkpoint revision is invalid")
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationReleaseNetworkOwnership {
		return fmt.Errorf("ownership release result operation %q is unsupported", result.Operation)
	}
	if !canonicalSHA256Fingerprint(result.OwnershipFingerprint) {
		return errors.New("ownership release result ownership fingerprint is invalid")
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return errors.New("ownership release result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return errors.New("ownership release result expiry exceeds the protocol bound")
	}
	return nil
}

// OwnershipReleaseService serializes terminal ownership-release publication against durable projection revalidation.
type OwnershipReleaseService struct {
	plans      OwnershipReleasePlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// ownershipReleaseDefaultOpeners keeps fixed storage construction replaceable in lifecycle tests.
type ownershipReleaseDefaultOpeners struct {
	openKeys      func() (defaultKeyStoreCloser, error)
	openPublisher func() (defaultPublisherCloser, error)
}

// NewOwnershipReleaseService creates an issuer from explicit terminal durable authorities.
func NewOwnershipReleaseService(plans OwnershipReleasePlanSource, ownershipObserver OwnershipObserver, keys KeyLoader, publisher Publisher, clock helper.Clock, entropy io.Reader) *OwnershipReleaseService {
	if plans == nil || ownershipObserver == nil || keys == nil || publisher == nil || clock == nil || entropy == nil {
		panic("ticketissuer.NewOwnershipReleaseService requires every authority dependency")
	}
	return &OwnershipReleaseService{
		plans:     plans,
		ownership: ownershipObserver,
		keys:      keys,
		publisher: publisher,
		clock:     clock,
		entropy:   entropy,
		closeStore: func() error {
			return nil
		},
	}
}

// OpenDefaultOwnershipReleaseService opens fixed user-owned stores around explicit terminal release authorities.
func OpenDefaultOwnershipReleaseService(plans OwnershipReleasePlanSource, ownershipObserver OwnershipObserver) (*OwnershipReleaseService, error) {
	return openDefaultOwnershipReleaseService(plans, ownershipObserver, defaultOwnershipReleaseOpeners())
}

// defaultOwnershipReleaseOpeners binds production issuance to Harbor's fixed user-owned key and ticket paths.
func defaultOwnershipReleaseOpeners() ownershipReleaseDefaultOpeners {
	return ownershipReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultOwnershipReleaseService opens both stores as one close-safe ownership unit.
func openDefaultOwnershipReleaseService(plans OwnershipReleasePlanSource, ownershipObserver OwnershipObserver, openers ownershipReleaseDefaultOpeners) (*OwnershipReleaseService, error) {
	if plans == nil {
		return nil, errors.New("open helper ownership-release ticket issuer: durable plan source is required")
	}
	if ownershipObserver == nil {
		return nil, errors.New("open helper ownership-release ticket issuer: ownership observer is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, errors.New("open helper ownership-release ticket issuer: default store openers are incomplete")
	}
	keys, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper ownership-release ticket issuer key: %w", err)
	}
	if keys == nil {
		return nil, errors.New("open helper ownership-release ticket issuer key: opener returned nil")
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open helper ownership-release ticket issuer spool: %w", err), keys.Close())
	}
	if publisher == nil {
		return nil, errors.Join(errors.New("open helper ownership-release ticket issuer spool: opener returned nil"), keys.Close())
	}
	service := NewOwnershipReleaseService(plans, ownershipObserver, keys, publisher, helper.SystemClock{}, rand.Reader)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keys.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance leaves the serialized boundary.
func (service *OwnershipReleaseService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one ownership-only capability from stable durable authority and the daemon ownership projection.
func (service *OwnershipReleaseService) Issue(ctx context.Context, requesterIdentity string, request OwnershipReleaseRequest) (OwnershipReleaseResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OwnershipReleaseResult{}, err
	}
	if err := request.Validate(); err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: %w", err)
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return OwnershipReleaseResult{}, errors.New("issue helper ownership-release ticket: issuer is closed")
	}
	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return OwnershipReleaseResult{}, err
	}
	if err := service.observeOwnership(ctx, requesterIdentity, plan); err != nil {
		return OwnershipReleaseResult{}, err
	}
	key, err := service.keys.Load(ctx)
	if err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: load established signing key: %w", err)
	}
	if err := requirePinnedKey(key, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: %w", err)
	}
	ticket, err := service.buildTicket(plan, requesterIdentity, key)
	if err != nil {
		return OwnershipReleaseResult{}, err
	}
	confirmed, err := service.resolvePlan(ctx, request)
	if err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: revalidate approval plan: %w", err)
	}
	if plan != confirmed {
		return OwnershipReleaseResult{}, errors.New("issue helper ownership-release ticket: durable approval plan changed before publication")
	}
	if err := service.observeOwnership(ctx, requesterIdentity, confirmed); err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: revalidate ownership: %w", err)
	}
	reference, publishErr := service.publisher.Publish(ctx, ticket, key)
	result := OwnershipReleaseResult{
		OperationID:          plan.Operation.ID,
		OperationRevision:    plan.OperationRevision,
		CheckpointRevision:   plan.CheckpointRevision,
		Reference:            reference,
		Operation:            helper.OperationReleaseNetworkOwnership,
		OwnershipFingerprint: plan.ExpectedOwnershipFingerprint,
		ExpiresAt:            ticket.ExpiresAt,
	}
	if publishErr != nil {
		wrapped := fmt.Errorf("issue helper ownership-release ticket: publish capability: %w", publishErr)
		if !errors.Is(publishErr, ticketspool.ErrDurabilityUncertain) {
			return OwnershipReleaseResult{}, wrapped
		}
		if err := result.Validate(ticket.ExpiresAt.Add(-ticketLifetime)); err != nil {
			return OwnershipReleaseResult{}, errors.Join(ErrOwnershipReleasePublicationIndeterminate, wrapped, fmt.Errorf("issue helper ownership-release ticket: invalid durability-uncertain publication result: %w", err))
		}
		return result, errors.Join(ErrOwnershipReleasePublicationIndeterminate, wrapped)
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return OwnershipReleaseResult{}, fmt.Errorf("issue helper ownership-release ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates one immutable durable plan on every read boundary.
func (service *OwnershipReleaseService) resolvePlan(ctx context.Context, request OwnershipReleaseRequest) (OwnershipReleasePlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return OwnershipReleasePlan{}, fmt.Errorf("issue helper ownership-release ticket: resolve approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return OwnershipReleasePlan{}, fmt.Errorf("issue helper ownership-release ticket: invalid approval plan: %w", err)
	}
	if plan.Operation.ID != request.OperationID {
		return OwnershipReleasePlan{}, errors.New("issue helper ownership-release ticket: approval plan does not match requested operation")
	}
	return plan, nil
}

// observeOwnership requires the exact durable target ownership projection; it deliberately does not observe protected host ownership.
func (service *OwnershipReleaseService) observeOwnership(ctx context.Context, requesterIdentity string, plan OwnershipReleasePlan) error {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return fmt.Errorf("issue helper ownership-release ticket: observe ownership projection: %w", err)
	}
	if !observation.Exists {
		return errors.New("issue helper ownership-release ticket: ownership projection is absent")
	}
	if observation.Record != plan.TargetOwnership {
		return errors.New("issue helper ownership-release ticket: ownership projection differs from the approved target")
	}
	if requesterIdentity != plan.TargetOwnership.OwnerIdentity {
		return errors.New("issue helper ownership-release ticket: authenticated requester does not own the approved machine claim")
	}
	if observation.Fingerprint != plan.ExpectedOwnershipFingerprint {
		return errors.New("issue helper ownership-release ticket: ownership projection fingerprint does not match the approved target")
	}
	return nil
}

// buildTicket binds one exact terminal durable release checkpoint to a fresh ownership-only capability.
func (service *OwnershipReleaseService) buildTicket(plan OwnershipReleasePlan, requesterIdentity string, key ed25519.PrivateKey) (helper.Ticket, error) {
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ownership-release ticket: generate nonce: %w", err)
	}
	now := service.clock.Now().UTC()
	ticket := helper.Ticket{
		Version:                      helper.ProtocolVersion,
		Operation:                    helper.OperationReleaseNetworkOwnership,
		InstallationID:               plan.TargetOwnership.InstallationID,
		RequesterIdentity:            requesterIdentity,
		OwnershipGeneration:          plan.TargetOwnership.Generation,
		OwnershipSchemaVersion:       plan.TargetOwnership.SchemaVersion,
		NetworkPolicyFingerprint:     plan.TargetOwnership.NetworkPolicyFingerprint,
		ApprovedPool:                 plan.TargetOwnership.LoopbackPoolPrefix,
		ReleaseOperationID:           string(plan.Operation.ID),
		ReleaseOperationRevision:     uint64(plan.OperationRevision),
		ReleaseCheckpointRevision:    uint64(plan.CheckpointRevision),
		ExpectedOwnershipFingerprint: plan.ExpectedOwnershipFingerprint,
		Nonce:                        hex.EncodeToString(nonce),
		ExpiresAt:                    now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ownership-release ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(key, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper ownership-release ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}
