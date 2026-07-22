package ticketissuer

import (
	"bytes"
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
	platformtrust "github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certificates"
)

var (
	// ErrTrustPublicationIndeterminate means a trust ticket reference may have been durably published and must be reconciled instead of replaced.
	ErrTrustPublicationIndeterminate = errors.New("trust capability publication is indeterminate")
)

// TrustRequest selects one durable trust approval plan without carrying trust authority.
type TrustRequest struct {
	OperationID domain.OperationID
}

// Validate rejects requests that cannot select one daemon-owned approval operation.
func (request TrustRequest) Validate() error {
	return request.OperationID.Validate()
}

// TrustPlan is the immutable public-CA trust authority approved by one durable operation.
type TrustPlan struct {
	OperationID       domain.OperationID
	OperationRevision domain.Sequence
	OperationState    domain.OperationState
	Mutation          helper.Operation
	TargetOwnership   ownership.Record
	Policy            networkpolicy.Policy
	Root              certificates.Root
}

// Validate rejects plans that cannot describe one exact current-user trust mutation.
func (plan TrustPlan) Validate() error {
	if err := plan.OperationID.Validate(); err != nil {
		return err
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("trust approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if plan.OperationState != domain.OperationRequiresApproval {
		return fmt.Errorf("trust approval operation state is %q, want %q", plan.OperationState, domain.OperationRequiresApproval)
	}
	if plan.Mutation != helper.OperationEnsureTrust {
		return fmt.Errorf("trust approval mutation %q is not allowlisted", plan.Mutation)
	}
	if err := plan.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("trust approval target ownership: %w", err)
	}
	if plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"trust approval target ownership schema is %d, want %d",
			plan.TargetOwnership.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	if err := plan.Policy.Validate(); err != nil {
		return fmt.Errorf("trust approval policy: %w", err)
	}
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("trust approval policy fingerprint: %w", err)
	}
	if policyFingerprint != plan.TargetOwnership.NetworkPolicyFingerprint {
		return errors.New("trust approval policy does not match target ownership")
	}
	if _, err := platformtrust.NewRequestForRequester(
		plan.TargetOwnership.InstallationID,
		plan.TargetOwnership.OwnerIdentity,
		plan.Policy.Mechanisms.Trust,
		plan.Root,
	); err != nil {
		return fmt.Errorf("trust approval public root: %w", err)
	}
	if plan.Root.Fingerprint != plan.Policy.AuthorityFingerprint {
		return errors.New("trust approval public root does not match policy authority")
	}
	return nil
}

// TrustPlanSource resolves one exact durable plan before capability publication.
type TrustPlanSource interface {
	// Resolve returns the trust plan owned by one daemon operation.
	Resolve(context.Context, TrustRequest) (TrustPlan, error)
}

// TrustObserver supplies complete native trust facts without mutation authority.
type TrustObserver interface {
	// Observe returns every native trust fact relevant to one immutable request.
	Observe(context.Context, platformtrust.Request) (platformtrust.Observation, error)
}

// TrustResult exposes only opaque launch metadata for one policy-bound trust capability.
type TrustResult struct {
	OperationID          domain.OperationID
	Reference            helper.TicketReference
	Operation            helper.Operation
	PolicyFingerprint    string
	OwnershipFingerprint string
	AuthorityFingerprint string
	Mechanism            networkpolicy.TrustMechanism
	ExpiresAt            time.Time
}

// Validate rejects results that can cross the selected operation or helper lifetime boundary.
func (result TrustResult) Validate(now time.Time) error {
	if err := result.OperationID.Validate(); err != nil {
		return err
	}
	if err := result.Reference.Validate(); err != nil {
		return err
	}
	if result.Operation != helper.OperationEnsureTrust {
		return fmt.Errorf("trust approval result operation %q is unsupported", result.Operation)
	}
	if !canonicalSHA256Fingerprint(result.PolicyFingerprint) {
		return errors.New("trust approval result policy fingerprint is invalid")
	}
	if !canonicalSHA256Fingerprint(result.OwnershipFingerprint) {
		return errors.New("trust approval result ownership fingerprint is invalid")
	}
	if !canonicalSHA256Fingerprint(result.AuthorityFingerprint) {
		return errors.New("trust approval result authority fingerprint is invalid")
	}
	switch result.Mechanism {
	case networkpolicy.DarwinCurrentUserTrust,
		networkpolicy.UbuntuSystemTrust,
		networkpolicy.WindowsCurrentUserTrust:
	default:
		return fmt.Errorf("trust approval result mechanism %q is unsupported", result.Mechanism)
	}
	if result.ExpiresAt.IsZero() || result.ExpiresAt.Location() != time.UTC || !result.ExpiresAt.After(now) {
		return errors.New("trust approval result expiry is invalid")
	}
	if result.ExpiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return errors.New("trust approval result expiry exceeds the protocol bound")
	}
	return nil
}

// TrustService serializes public-CA trust ticket issuance against durable and native revalidation.
type TrustService struct {
	plans      TrustPlanSource
	ownership  OwnershipObserver
	keys       KeyLoader
	publisher  Publisher
	trust      TrustObserver
	clock      helper.Clock
	entropy    io.Reader
	closeStore func() error

	mutex  sync.Mutex
	closed bool
}

// trustDefaultOpeners keeps fixed storage construction replaceable in lifecycle tests.
type trustDefaultOpeners struct {
	openKeys      func() (defaultKeyStoreCloser, error)
	openPublisher func() (defaultPublisherCloser, error)
}

// NewTrustService creates an issuer from explicit durable authorities and a read-only native trust observer.
func NewTrustService(
	plans TrustPlanSource,
	ownershipObserver OwnershipObserver,
	keys KeyLoader,
	publisher Publisher,
	trustObserver TrustObserver,
	clock helper.Clock,
	entropy io.Reader,
) *TrustService {
	if plans == nil || ownershipObserver == nil || keys == nil || publisher == nil || trustObserver == nil || clock == nil || entropy == nil {
		panic("ticketissuer.NewTrustService requires every authority dependency")
	}
	return &TrustService{
		plans:      plans,
		ownership:  ownershipObserver,
		keys:       keys,
		publisher:  publisher,
		trust:      trustObserver,
		clock:      clock,
		entropy:    entropy,
		closeStore: func() error { return nil },
	}
}

// OpenDefaultTrustService opens fixed user-owned key and ticket stores around explicit trust authorities.
func OpenDefaultTrustService(
	plans TrustPlanSource,
	ownershipObserver OwnershipObserver,
	trustObserver TrustObserver,
) (*TrustService, error) {
	return openDefaultTrustService(plans, ownershipObserver, trustObserver, defaultTrustOpeners())
}

// defaultTrustOpeners binds production issuance to Harbor's fixed user-owned key and ticket paths.
func defaultTrustOpeners() trustDefaultOpeners {
	return trustDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return ticketkey.OpenDefault()
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return ticketspool.OpenDefault()
		},
	}
}

// openDefaultTrustService opens both stores as one close-safe ownership unit.
func openDefaultTrustService(
	plans TrustPlanSource,
	ownershipObserver OwnershipObserver,
	trustObserver TrustObserver,
	openers trustDefaultOpeners,
) (*TrustService, error) {
	if plans == nil {
		return nil, errors.New("open helper trust ticket issuer: durable plan source is required")
	}
	if ownershipObserver == nil {
		return nil, errors.New("open helper trust ticket issuer: ownership observer is required")
	}
	if trustObserver == nil {
		return nil, errors.New("open helper trust ticket issuer: trust observer is required")
	}
	if openers.openKeys == nil || openers.openPublisher == nil {
		return nil, errors.New("open helper trust ticket issuer: default store openers are incomplete")
	}
	keyStore, err := openers.openKeys()
	if err != nil {
		return nil, fmt.Errorf("open helper trust ticket issuer key: %w", err)
	}
	if keyStore == nil {
		return nil, errors.New("open helper trust ticket issuer key: opener returned nil")
	}
	publisher, err := openers.openPublisher()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper trust ticket issuer spool: %w", err),
			keyStore.Close(),
		)
	}
	if publisher == nil {
		return nil, errors.Join(
			errors.New("open helper trust ticket issuer spool: opener returned nil"),
			keyStore.Close(),
		)
	}
	service := NewTrustService(plans, ownershipObserver, keyStore, publisher, trustObserver, helper.SystemClock{}, rand.Reader)
	service.closeStore = func() error {
		return errors.Join(publisher.Close(), keyStore.Close())
	}
	return service, nil
}

// Close releases fixed production stores after all in-flight issuance leaves the serialized boundary.
func (service *TrustService) Close() error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return nil
	}
	service.closed = true
	return service.closeStore()
}

// Issue derives one trust capability from a stable plan and two equal native observations.
func (service *TrustService) Issue(
	ctx context.Context,
	requesterIdentity string,
	request TrustRequest,
) (TrustResult, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return TrustResult{}, err
	}
	if err := request.Validate(); err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: %w", err)
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed {
		return TrustResult{}, errors.New("issue helper trust ticket: issuer is closed")
	}
	if err := ctx.Err(); err != nil {
		return TrustResult{}, err
	}
	plan, err := service.resolvePlan(ctx, request)
	if err != nil {
		return TrustResult{}, err
	}
	if err := service.observeOwnership(ctx, requesterIdentity, plan); err != nil {
		return TrustResult{}, err
	}
	privateKey, err := service.keys.Load(ctx)
	if err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: load established signing key: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: %w", err)
	}
	trustRequest, err := platformtrust.NewRequestForRequester(
		plan.TargetOwnership.InstallationID,
		requesterIdentity,
		plan.Policy.Mechanisms.Trust,
		plan.Root,
	)
	if err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: construct trust request: %w", err)
	}
	fingerprint, err := service.observeTrust(ctx, trustRequest)
	if err != nil {
		return TrustResult{}, err
	}
	ticket, err := service.buildTicket(requesterIdentity, plan, fingerprint, privateKey)
	if err != nil {
		return TrustResult{}, err
	}
	confirmed, err := service.resolvePlan(ctx, request)
	if err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: revalidate approval plan: %w", err)
	}
	if !sameTrustPlan(plan, confirmed) {
		return TrustResult{}, errors.New("issue helper trust ticket: durable approval plan changed before publication")
	}
	if err := service.observeOwnership(ctx, requesterIdentity, confirmed); err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: revalidate ownership: %w", err)
	}
	confirmedFingerprint, err := service.observeTrust(ctx, trustRequest)
	if err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: revalidate trust: %w", err)
	}
	if confirmedFingerprint != fingerprint {
		return TrustResult{}, errors.New("issue helper trust ticket: trust observation changed before publication")
	}
	ownershipFingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: fingerprint target ownership: %w", err)
	}
	reference, publishErr := service.publisher.Publish(ctx, ticket, privateKey)
	result := TrustResult{
		OperationID:          plan.OperationID,
		Reference:            reference,
		Operation:            plan.Mutation,
		PolicyFingerprint:    plan.TargetOwnership.NetworkPolicyFingerprint,
		OwnershipFingerprint: ownershipFingerprint,
		AuthorityFingerprint: plan.Root.Fingerprint,
		Mechanism:            plan.Policy.Mechanisms.Trust,
		ExpiresAt:            ticket.ExpiresAt,
	}
	if publishErr != nil {
		wrapped := fmt.Errorf("issue helper trust ticket: publish capability: %w", publishErr)
		if !errors.Is(publishErr, ticketspool.ErrDurabilityUncertain) {
			return TrustResult{}, wrapped
		}
		if err := result.Validate(ticket.ExpiresAt.Add(-ticketLifetime)); err != nil {
			return TrustResult{}, errors.Join(
				ErrTrustPublicationIndeterminate,
				wrapped,
				fmt.Errorf("issue helper trust ticket: invalid durability-uncertain publication result: %w", err),
			)
		}
		return result, errors.Join(ErrTrustPublicationIndeterminate, wrapped)
	}
	if err := result.Validate(service.clock.Now().UTC()); err != nil {
		return TrustResult{}, fmt.Errorf("issue helper trust ticket: invalid result: %w", err)
	}
	return result, nil
}

// resolvePlan validates one immutable durable plan on every read boundary.
func (service *TrustService) resolvePlan(ctx context.Context, request TrustRequest) (TrustPlan, error) {
	plan, err := service.plans.Resolve(ctx, request)
	if err != nil {
		return TrustPlan{}, fmt.Errorf("issue helper trust ticket: resolve approval plan: %w", err)
	}
	plan = cloneTrustPlan(plan)
	if err := plan.Validate(); err != nil {
		return TrustPlan{}, fmt.Errorf("issue helper trust ticket: invalid approval plan: %w", err)
	}
	if plan.OperationID != request.OperationID {
		return TrustPlan{}, errors.New("issue helper trust ticket: approval plan does not match requested operation")
	}
	return plan, nil
}

// observeOwnership requires the current confirmed policy-bound ownership projection to equal the approved target.
func (service *TrustService) observeOwnership(ctx context.Context, requesterIdentity string, plan TrustPlan) error {
	observation, err := service.ownership.Observe(ctx)
	if err != nil {
		return fmt.Errorf("issue helper trust ticket: observe ownership projection: %w", err)
	}
	if !observation.Exists {
		return errors.New("issue helper trust ticket: ownership projection is absent")
	}
	if observation.Record != plan.TargetOwnership {
		return errors.New("issue helper trust ticket: ownership projection differs from the approved target")
	}
	if requesterIdentity != plan.TargetOwnership.OwnerIdentity {
		return errors.New("issue helper trust ticket: authenticated requester does not own the approved machine claim")
	}
	fingerprint, err := plan.TargetOwnership.Fingerprint()
	if err != nil {
		return fmt.Errorf("issue helper trust ticket: fingerprint approved target ownership: %w", err)
	}
	if observation.Fingerprint != fingerprint {
		return errors.New("issue helper trust ticket: ownership projection does not match approved target")
	}
	return nil
}

// observeTrust admits only complete trust states that an ensure operation may safely converge.
func (service *TrustService) observeTrust(ctx context.Context, request platformtrust.Request) (string, error) {
	observation, err := service.trust.Observe(ctx, request)
	if err != nil {
		return "", fmt.Errorf("issue helper trust ticket: observe trust: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return "", fmt.Errorf("issue helper trust ticket: invalid trust observation: %w", err)
	}
	if !sameTrustRequest(observation.Request, request) {
		return "", errors.New("issue helper trust ticket: trust observation belongs to another request")
	}
	assessment, err := observation.Classify()
	if err != nil {
		return "", fmt.Errorf("issue helper trust ticket: classify trust observation: %w", err)
	}
	switch assessment.State {
	case platformtrust.StateAbsent, platformtrust.StateExact, platformtrust.StateOwnedDrifted:
	case platformtrust.StateForeign:
		if assessment.Owned != platformtrust.OwnedStateAbsent || !onlyPreexistingIdenticalTrustEntries(observation) {
			return "", fmt.Errorf("issue helper trust ticket: trust state %q cannot be safely ensured", assessment.State)
		}
	case platformtrust.StateAmbiguous, platformtrust.StateIndeterminate:
		return "", fmt.Errorf("issue helper trust ticket: trust state %q cannot be safely ensured", assessment.State)
	default:
		return "", fmt.Errorf("issue helper trust ticket: trust state %q is unsupported", assessment.State)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("issue helper trust ticket: fingerprint trust observation: %w", err)
	}
	return fingerprint, nil
}

// onlyPreexistingIdenticalTrustEntries admits an unowned exact CA without allowing Harbor to claim or replace foreign trust state.
func onlyPreexistingIdenticalTrustEntries(observation platformtrust.Observation) bool {
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

// buildTicket binds target ownership and one exact trust precondition into a fresh capability.
func (service *TrustService) buildTicket(
	requesterIdentity string,
	plan TrustPlan,
	observationFingerprint string,
	privateKey ed25519.PrivateKey,
) (helper.Ticket, error) {
	nonce := make([]byte, ticketNonceBytes)
	if _, err := io.ReadFull(service.entropy, nonce); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper trust ticket: generate nonce: %w", err)
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
		TrustRoot: &helper.TrustRoot{
			CertificatePEM: append([]byte(nil), plan.Root.CertificatePEM...),
			Fingerprint:    plan.Root.Fingerprint,
			NotBefore:      plan.Root.NotBefore,
			NotAfter:       plan.Root.NotAfter,
		},
		ExpectedTrustObservation: &helper.ExpectedTrustObservation{Fingerprint: observationFingerprint},
		Nonce:                    hex.EncodeToString(nonce),
		ExpiresAt:                now.Add(ticketLifetime),
	}
	if err := ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper trust ticket: constructed ticket is invalid: %w", err)
	}
	if err := requirePinnedKey(privateKey, plan.TargetOwnership.TicketVerifierKey); err != nil {
		return helper.Ticket{}, fmt.Errorf("issue helper trust ticket: signing key changed during construction: %w", err)
	}
	return ticket, nil
}

// sameTrustPlan compares all public authority, including copied certificate bytes.
func sameTrustPlan(left TrustPlan, right TrustPlan) bool {
	return left.OperationID == right.OperationID &&
		left.OperationRevision == right.OperationRevision &&
		left.OperationState == right.OperationState &&
		left.Mutation == right.Mutation &&
		left.TargetOwnership == right.TargetOwnership &&
		left.Policy == right.Policy &&
		left.Root.Fingerprint == right.Root.Fingerprint &&
		left.Root.NotBefore.Equal(right.Root.NotBefore) &&
		left.Root.NotAfter.Equal(right.Root.NotAfter) &&
		bytes.Equal(left.Root.CertificatePEM, right.Root.CertificatePEM)
}

// cloneTrustPlan prevents mutable public certificate bytes from crossing a durable-read boundary.
func cloneTrustPlan(plan TrustPlan) TrustPlan {
	plan.Root.CertificatePEM = append([]byte(nil), plan.Root.CertificatePEM...)
	return plan
}

// sameTrustRequest compares the complete immutable request authority, including public certificate validity.
func sameTrustRequest(left platformtrust.Request, right platformtrust.Request) bool {
	if left.InstallationID() != right.InstallationID() ||
		left.RequesterIdentity() != right.RequesterIdentity() ||
		left.Mechanism() != right.Mechanism() {
		return false
	}
	leftRoot := left.Root()
	rightRoot := right.Root()
	return leftRoot.Fingerprint == rightRoot.Fingerprint &&
		leftRoot.NotBefore.Equal(rightRoot.NotBefore) &&
		leftRoot.NotAfter.Equal(rightRoot.NotAfter) &&
		bytes.Equal(leftRoot.CertificatePEM, rightRoot.CertificatePEM)
}
