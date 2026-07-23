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

// TrustPlanPurpose identifies the lifecycle that granted a trust mutation.
type TrustPlanPurpose string

const (
	// TrustPlanPurposeDataPlaneSetup permits the setup lifecycle to ensure public-root trust.
	TrustPlanPurposeDataPlaneSetup TrustPlanPurpose = "network_data_plane_setup"
	// TrustPlanPurposeGlobalNetworkRelease permits the global release lifecycle to remove public-root trust.
	TrustPlanPurposeGlobalNetworkRelease TrustPlanPurpose = "global_network_release"
)

// TrustCheckpointPhase identifies the durable checkpoint that fences trust ticket publication.
type TrustCheckpointPhase string

const (
	// TrustCheckpointPhaseSetupApproval identifies the setup approval checkpoint before trust installation.
	TrustCheckpointPhaseSetupApproval TrustCheckpointPhase = "awaiting trust approval"
	// TrustCheckpointPhaseGlobalRelease identifies the global release checkpoint after resolver retirement.
	TrustCheckpointPhaseGlobalRelease TrustCheckpointPhase = "trust"
)

// Validate rejects lifecycle purposes that have no narrow trust admission contract.
func (purpose TrustPlanPurpose) Validate() error {
	switch purpose {
	case TrustPlanPurposeDataPlaneSetup, TrustPlanPurposeGlobalNetworkRelease:
		return nil
	default:
		return fmt.Errorf("trust approval purpose %q is unsupported", purpose)
	}
}

// TrustPlan is the immutable public-CA trust authority approved by one durable operation.
type TrustPlan struct {
	Purpose            TrustPlanPurpose
	Operation          domain.Operation
	OperationRevision  domain.Sequence
	CheckpointRevision domain.Sequence
	CheckpointPhase    TrustCheckpointPhase
	Mutation           helper.Operation
	TargetOwnership    ownership.Record
	Policy             networkpolicy.Policy
	Root               certificates.Root
}

// Validate rejects plans that cannot describe one exact current-user trust mutation.
func (plan TrustPlan) Validate() error {
	if err := plan.Operation.Validate(); err != nil {
		return fmt.Errorf("trust approval operation: %w", err)
	}
	if plan.Operation.ProjectID != "" {
		return errors.New("trust approval operation must be global")
	}
	if plan.OperationRevision == 0 || plan.OperationRevision > domain.MaximumSequence {
		return fmt.Errorf("trust approval operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	if err := plan.Purpose.Validate(); err != nil {
		return err
	}
	if err := validateTrustPlanLifecycle(plan); err != nil {
		return err
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

// validateTrustPlanLifecycle keeps setup and release authority disjoint despite sharing native trust validation.
func validateTrustPlanLifecycle(plan TrustPlan) error {
	switch plan.Purpose {
	case TrustPlanPurposeDataPlaneSetup:
		if plan.CheckpointRevision != 0 {
			return fmt.Errorf("trust setup checkpoint revision is %d, want 0", plan.CheckpointRevision)
		}
		if plan.CheckpointPhase != TrustCheckpointPhaseSetupApproval {
			return fmt.Errorf("trust setup checkpoint phase is %q, want %q", plan.CheckpointPhase, TrustCheckpointPhaseSetupApproval)
		}
		if plan.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup {
			return fmt.Errorf("trust setup operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkDataPlaneSetup)
		}
		if plan.Operation.State != domain.OperationRequiresApproval {
			return fmt.Errorf("trust setup operation state is %q, want %q", plan.Operation.State, domain.OperationRequiresApproval)
		}
		if plan.Operation.Phase != string(TrustCheckpointPhaseSetupApproval) {
			return fmt.Errorf("trust setup operation phase is %q, want %q", plan.Operation.Phase, TrustCheckpointPhaseSetupApproval)
		}
		if plan.Mutation != helper.OperationEnsureTrust {
			return fmt.Errorf("trust setup mutation is %q, want %q", plan.Mutation, helper.OperationEnsureTrust)
		}
	case TrustPlanPurposeGlobalNetworkRelease:
		if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
			return fmt.Errorf("trust release checkpoint revision must be between 1 and %d", domain.MaximumSequence)
		}
		if plan.CheckpointPhase != TrustCheckpointPhaseGlobalRelease {
			return fmt.Errorf("trust release checkpoint phase is %q, want %q", plan.CheckpointPhase, TrustCheckpointPhaseGlobalRelease)
		}
		if plan.Operation.Kind != domain.OperationKindNetworkRelease {
			return fmt.Errorf("trust release operation kind is %q, want %q", plan.Operation.Kind, domain.OperationKindNetworkRelease)
		}
		if plan.Operation.State != domain.OperationRunning {
			return fmt.Errorf("trust release operation state is %q, want %q", plan.Operation.State, domain.OperationRunning)
		}
		if plan.Operation.Phase != "releasing network runtime" {
			return fmt.Errorf("trust release operation phase is %q, want %q", plan.Operation.Phase, "releasing network runtime")
		}
		if plan.Mutation != helper.OperationReleaseTrust {
			return fmt.Errorf("trust release mutation is %q, want %q", plan.Mutation, helper.OperationReleaseTrust)
		}
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
	if result.Operation != helper.OperationEnsureTrust && result.Operation != helper.OperationReleaseTrust {
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
	case networkpolicy.DarwinAdministratorTrust,
		networkpolicy.DarwinCurrentUserTrust,
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
	fingerprint, err := service.observeTrust(ctx, trustRequest, plan.Purpose)
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
	confirmedFingerprint, err := service.observeTrust(ctx, trustRequest, confirmed.Purpose)
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
		OperationID:          plan.Operation.ID,
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
	if plan.Operation.ID != request.OperationID {
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

// observeTrust admits only native trust states that the selected lifecycle may safely mutate.
func (service *TrustService) observeTrust(ctx context.Context, request platformtrust.Request, purpose TrustPlanPurpose) (string, error) {
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
	switch purpose {
	case TrustPlanPurposeDataPlaneSetup:
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
	case TrustPlanPurposeGlobalNetworkRelease:
		if (assessment.State != platformtrust.StateExact || assessment.Owned != platformtrust.OwnedStateExact) &&
			(assessment.State != platformtrust.StateAbsent || assessment.Owned != platformtrust.OwnedStateAbsent) {
			return "", fmt.Errorf("issue helper trust ticket: trust state %q cannot be safely released", assessment.State)
		}
	default:
		return "", fmt.Errorf("issue helper trust ticket: trust purpose %q is unsupported", purpose)
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
	return left.Purpose == right.Purpose &&
		sameTrustOperation(left.Operation, right.Operation) &&
		left.OperationRevision == right.OperationRevision &&
		left.CheckpointRevision == right.CheckpointRevision &&
		left.CheckpointPhase == right.CheckpointPhase &&
		left.Mutation == right.Mutation &&
		left.TargetOwnership == right.TargetOwnership &&
		left.Policy == right.Policy &&
		left.Root.Fingerprint == right.Root.Fingerprint &&
		left.Root.NotBefore.Equal(right.Root.NotBefore) &&
		left.Root.NotAfter.Equal(right.Root.NotAfter) &&
		bytes.Equal(left.Root.CertificatePEM, right.Root.CertificatePEM)
}

// sameTrustOperation compares operation values without treating pointer allocation as authority.
func sameTrustOperation(left domain.Operation, right domain.Operation) bool {
	return left.ID == right.ID &&
		left.IntentID == right.IntentID &&
		left.Kind == right.Kind &&
		left.ProjectID == right.ProjectID &&
		left.State == right.State &&
		left.Phase == right.Phase &&
		left.RequestedAt.Equal(right.RequestedAt) &&
		sameOptionalTrustTime(left.StartedAt, right.StartedAt) &&
		sameOptionalTrustTime(left.FinishedAt, right.FinishedAt) &&
		sameOptionalTrustProblem(left.Problem, right.Problem)
}

// sameOptionalTrustTime compares optional operation timestamps by value.
func sameOptionalTrustTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// sameOptionalTrustProblem compares optional operation failures by value.
func sameOptionalTrustProblem(left *domain.Problem, right *domain.Problem) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
