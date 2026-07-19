package reconcile

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

const networkSetupPoolAddressCount = 8

// NetworkSetupOperationJournal owns idempotent setup staging and exact operation reads.
type NetworkSetupOperationJournal interface {
	// Operation returns one durable operation by its daemon-owned identity.
	Operation(context.Context, domain.OperationID) (state.OperationRecord, error)
	// OperationByIntent returns one durable operation by its client-stable intent identity.
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	// StageNetworkSetup atomically stages or exactly replays one global setup plan.
	StageNetworkSetup(context.Context, state.StageNetworkSetupRequest) (state.OperationRecord, error)
}

// NetworkSetupPlanSource resolves the immutable pool authority for one approval operation.
type NetworkSetupPlanSource interface {
	// Resolve returns the exact revision-bound pool plan selected by a setup operation.
	Resolve(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error)
}

// NetworkSetupStore commits or exactly replays a fully observed setup completion.
type NetworkSetupStore interface {
	// CompleteNetworkSetup atomically retires setup authority and creates the network foundation.
	CompleteNetworkSetup(context.Context, state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error)
}

// SigningKeyStore loads generation-one helper signing authority and owns its external resources.
type SigningKeyStore interface {
	// LoadOrCreate returns the stable Ed25519 signing identity for this installation.
	LoadOrCreate(context.Context) (ed25519.PrivateKey, error)
	// Close releases the signing-key store without rotating its durable identity.
	Close() error
}

// SigningKeyStoreFactory lazily opens signing authority only for a new setup intent.
type SigningKeyStoreFactory func() (SigningKeyStore, error)

// PoolSelector chooses one exact safe /29 for a caller-authenticated installation owner.
type PoolSelector interface {
	// Select returns one installation-stable pool derived from fresh native observations.
	Select(context.Context, identity.InstallationID, string) (identity.PoolSelection, error)
}

// PoolIssuer publishes one short-lived capability for an exact durable bootstrap plan.
type PoolIssuer interface {
	// Issue publishes a helper pool capability for the authenticated requester.
	Issue(context.Context, string, ticketissuer.PoolRequest) (ticketissuer.PoolResult, error)
	// Close releases the issuer's daemon-owned key and capability stores.
	Close() error
}

// PoolIssuerFactory lazily opens helper authority only after Prepare validates the durable plan.
type PoolIssuerFactory func() (PoolIssuer, error)

// OwnershipObserver reads the daemon-owned confirmed ownership projection for terminal replay.
type OwnershipObserver interface {
	// Observe returns one stable snapshot of ownership previously confirmed through the helper exchange.
	Observe(context.Context) (ownership.Observation, error)
}

// NetworkSetupStartRequest identifies one idempotent machine-global setup intent and its owner.
type NetworkSetupStartRequest struct {
	OperationID       domain.OperationID
	IntentID          domain.IntentID
	InstallationID    identity.InstallationID
	RequesterIdentity string
}

// Validate rejects identities that cannot select and own one stable setup operation.
func (request NetworkSetupStartRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := request.IntentID.Validate(); err != nil {
		return err
	}
	if err := request.InstallationID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkSetupPrepareRequest selects one exact setup approval on behalf of its authenticated owner.
type NetworkSetupPrepareRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects stale-shaped Prepare input before helper authority can be opened.
func (request NetworkSetupPrepareRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkSetupConfirmRequest carries the exact helper postcondition for one revision-bound setup approval.
type NetworkSetupConfirmRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	HelperPoolEvidence        helper.PoolMutationEvidence
}

// Validate rejects malformed or revision-free confirmation identities before durable state is read.
func (request NetworkSetupConfirmRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupHelperEvidence(request.HelperPoolEvidence)
}

// NetworkSetupCoordinator serializes setup staging, helper issuance, and proof confirmation.
type NetworkSetupCoordinator struct {
	operations NetworkSetupOperationJournal
	plans      NetworkSetupPlanSource
	store      NetworkSetupStore
	keys       SigningKeyStoreFactory
	selector   PoolSelector
	issuers    PoolIssuerFactory
	ownership  OwnershipObserver
	loopback   LoopbackObserver
	clock      helper.Clock
	mutex      sync.Mutex
}

// NewNetworkSetupCoordinator constructs one fail-closed machine setup authority.
func NewNetworkSetupCoordinator(
	operations NetworkSetupOperationJournal,
	plans NetworkSetupPlanSource,
	store NetworkSetupStore,
	keys SigningKeyStoreFactory,
	selector PoolSelector,
	issuers PoolIssuerFactory,
	ownershipObserver OwnershipObserver,
	loopbackObserver LoopbackObserver,
	clock helper.Clock,
) *NetworkSetupCoordinator {
	if nilDependency(operations) ||
		nilDependency(plans) ||
		nilDependency(store) ||
		nilDependency(keys) ||
		nilDependency(selector) ||
		nilDependency(issuers) ||
		nilDependency(ownershipObserver) ||
		nilDependency(loopbackObserver) ||
		nilDependency(clock) {
		panic("reconcile.NewNetworkSetupCoordinator requires every authority dependency")
	}
	return &NetworkSetupCoordinator{
		operations: operations,
		plans:      plans,
		store:      store,
		keys:       keys,
		selector:   selector,
		issuers:    issuers,
		ownership:  ownershipObserver,
		loopback:   loopbackObserver,
		clock:      clock,
	}
}

// Start stages a new generation-one setup plan or returns the operation already owning its intent.
func (coordinator *NetworkSetupCoordinator) Start(
	ctx context.Context,
	request NetworkSetupStartRequest,
) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}

	existing, err := coordinator.operations.OperationByIntent(ctx, request.IntentID)
	if err == nil {
		if err := validateExistingNetworkSetupOperation(existing, request.IntentID); err != nil {
			return state.OperationRecord{}, fmt.Errorf("start network setup: replay operation: %w", err)
		}
		return existing, nil
	}
	var missingIntent *state.OperationIntentNotFoundError
	if !errors.As(err, &missingIntent) {
		return state.OperationRecord{}, fmt.Errorf("start network setup: read operation intent: %w", err)
	}
	if missingIntent.IntentID != request.IntentID {
		return state.OperationRecord{}, fmt.Errorf("start network setup: missing operation intent differs from request")
	}

	verifierKey, err := coordinator.loadVerifierKey(ctx)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: %w", err)
	}
	selection, err := coordinator.selector.Select(ctx, request.InstallationID, request.RequesterIdentity)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: select loopback pool: %w", err)
	}
	if err := validateNetworkSetupPool(selection.Pool); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: selected loopback pool: %w", err)
	}

	record := ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     string(request.InstallationID),
		OwnerIdentity:      request.RequesterIdentity,
		Generation:         1,
		LoopbackPoolPrefix: selection.Pool.Prefix().String(),
		TicketVerifierKey:  verifierKey,
	}
	if err := record.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: construct ownership: %w", err)
	}
	operation, err := domain.NewOperation(
		request.OperationID,
		request.IntentID,
		domain.OperationKindNetworkSetup,
		"",
		coordinator.operationTime(coordinator.clock.Now(), time.Time{}),
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: create operation: %w", err)
	}
	staged, err := coordinator.operations.StageNetworkSetup(ctx, state.StageNetworkSetupRequest{
		Operation: operation,
		Ownership: record,
	})
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: stage operation: %w", err)
	}
	if err := validateStagedNetworkSetupOperation(staged, request.IntentID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start network setup: stage readback: %w", err)
	}
	return staged, nil
}

// Prepare validates one exact bootstrap plan before opening and issuing helper authority.
func (coordinator *NetworkSetupCoordinator) Prepare(
	ctx context.Context,
	request NetworkSetupPrepareRequest,
) (ticketissuer.PoolResult, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return ticketissuer.PoolResult{}, err
	}

	poolRequest := ticketissuer.PoolRequest{OperationID: request.OperationID}
	plan, err := coordinator.plans.Resolve(ctx, poolRequest)
	if err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: resolve plan: %w", err)
	}
	if err := validateNetworkSetupPlan(plan, request.OperationID, request.ExpectedOperationRevision); err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: %w", err)
	}
	if plan.Ownership.OwnerIdentity != request.RequesterIdentity {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: authenticated requester does not match the approved machine owner")
	}

	result, err := coordinator.issuePool(ctx, request.RequesterIdentity, poolRequest)
	if err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: %w", err)
	}
	if err := result.Validate(coordinator.clock.Now().UTC()); err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: helper pool result is invalid: %w", err)
	}
	if result.OperationID != request.OperationID ||
		result.Operation != helper.OperationEnsureLoopbackPool ||
		result.Pool != plan.Pool.Prefix() {
		return ticketissuer.PoolResult{}, fmt.Errorf("prepare network setup approval: helper pool result differs from the approved bootstrap")
	}
	return result, nil
}

// Confirm independently observes helper effects before committing or exactly replaying setup completion.
func (coordinator *NetworkSetupCoordinator) Confirm(
	ctx context.Context,
	request NetworkSetupConfirmRequest,
) (state.CompleteNetworkSetupResult, error) {
	if err := request.Validate(); err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: %w", err)
	}
	request.HelperPoolEvidence.Identities = slices.Clone(request.HelperPoolEvidence.Identities)
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.CompleteNetworkSetupResult{}, err
	}

	operation, err := coordinator.operations.Operation(ctx, request.OperationID)
	if err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: read operation: %w", err)
	}
	if err := validateConfirmNetworkSetupOperation(operation, request.OperationID); err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: %w", err)
	}

	var confirmed ownership.Observation
	var pool identity.Pool
	var at time.Time
	switch operation.Operation.State {
	case domain.OperationRequiresApproval:
		if operation.Revision != request.ExpectedOperationRevision {
			return state.CompleteNetworkSetupResult{}, &state.StaleRevisionError{
				OperationID: request.OperationID,
				Expected:    request.ExpectedOperationRevision,
				Actual:      operation.Revision,
			}
		}
		plan, err := coordinator.plans.Resolve(ctx, ticketissuer.PoolRequest{OperationID: request.OperationID})
		if err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: resolve plan: %w", err)
		}
		if err := validateNetworkSetupPlan(plan, request.OperationID, request.ExpectedOperationRevision); err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: %w", err)
		}
		fingerprint, err := plan.Ownership.Fingerprint()
		if err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: fingerprint planned ownership: %w", err)
		}
		confirmed = ownership.Observation{Exists: true, Record: plan.Ownership, Fingerprint: fingerprint}
		pool = plan.Pool
		at = coordinator.operationTime(coordinator.clock.Now(), operation.Operation.RequestedAt)
	case domain.OperationSucceeded:
		if operation.Operation.FinishedAt == nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: succeeded operation has no completion time")
		}
		confirmed, err = coordinator.ownership.Observe(ctx)
		if err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: observe confirmed ownership projection: %w", err)
		}
		if err := validateNetworkSetupOwnershipObservation(confirmed); err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: %w", err)
		}
		pool, err = networkSetupPoolFromOwnership(confirmed.Record)
		if err != nil {
			return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: projected ownership pool: %w", err)
		}
		at = operation.Operation.FinishedAt.UTC().Round(0)
	default:
		return state.CompleteNetworkSetupResult{}, fmt.Errorf(
			"confirm network setup approval: operation %q has unsupported state %q",
			request.OperationID,
			operation.Operation.State,
		)
	}
	if request.HelperPoolEvidence.Pool != pool.Prefix().String() {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: helper evidence belongs to another loopback pool")
	}

	observations, err := coordinator.observePool(ctx, pool)
	if err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: %w", err)
	}
	result, err := coordinator.store.CompleteNetworkSetup(ctx, state.CompleteNetworkSetupRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ConfirmedOwnership:        confirmed,
		HelperPoolEvidence:        request.HelperPoolEvidence,
		ObservedPool:              observations,
		At:                        at,
	})
	if err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: complete setup: %w", err)
	}
	if err := result.Validate(); err != nil {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: completion result is invalid: %w", err)
	}
	if result.Operation.Operation.ID != request.OperationID {
		return state.CompleteNetworkSetupResult{}, fmt.Errorf("confirm network setup approval: completed operation differs from request")
	}
	return result, nil
}

// validateNetworkSetupRequesterIdentity enforces the bounded transport identity shape before external work begins.
func validateNetworkSetupRequesterIdentity(requesterIdentity string) error {
	if requesterIdentity == "" || len(requesterIdentity) > helper.MaximumRequesterIdentityLength {
		return fmt.Errorf("authenticated requester identity is invalid")
	}
	// UID/SID canonicalization remains coupled to ownership and helper validation at their authority boundaries.
	return nil
}

// validateNetworkSetupHelperEvidence rejects malformed client proof before durable or native observation work begins.
func validateNetworkSetupHelperEvidence(evidence helper.PoolMutationEvidence) error {
	prefix, err := netip.ParsePrefix(evidence.Pool)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() || prefix.Bits() != 29 ||
		prefix != prefix.Masked() || prefix.String() != evidence.Pool {
		return fmt.Errorf("helper pool evidence does not identify a canonical IPv4-loopback /29")
	}
	if len(evidence.Identities) != networkSetupPoolAddressCount {
		return fmt.Errorf("helper pool evidence contains %d identities, want %d", len(evidence.Identities), networkSetupPoolAddressCount)
	}
	address := prefix.Addr()
	for index, identityEvidence := range evidence.Identities {
		if identityEvidence.Address != address.String() {
			return fmt.Errorf("helper pool identity %d does not match address %s", index, address)
		}
		if err := identityEvidence.Observation.Validate(); err != nil {
			return fmt.Errorf("helper pool identity %s observation is invalid: %w", address, err)
		}
		if identityEvidence.Observation.State != helper.ObservationOwned {
			return fmt.Errorf("helper pool identity %s is not owned", address)
		}
		address = address.Next()
	}
	return nil
}

// validateExistingNetworkSetupOperation accepts any valid lifecycle state owned by the exact global setup intent.
func validateExistingNetworkSetupOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if record.Operation.IntentID != intentID {
		return fmt.Errorf("operation intent readback differs from request")
	}
	if record.Operation.Kind != domain.OperationKindNetworkSetup || record.Operation.ProjectID != "" {
		return &state.IntentConflictError{
			IntentID:            intentID,
			ExistingOperationID: record.Operation.ID,
			ExistingKind:        record.Operation.Kind,
			ExistingProjectID:   record.Operation.ProjectID,
			RequestedKind:       domain.OperationKindNetworkSetup,
			RequestedProjectID:  "",
		}
	}
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("operation is invalid: %w", err)
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return fmt.Errorf("operation revision is invalid: %w", err)
	}
	return nil
}

// validateStagedNetworkSetupOperation binds staging readback to the requested intent and approval boundary.
func validateStagedNetworkSetupOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if err := validateExistingNetworkSetupOperation(record, intentID); err != nil {
		return err
	}
	if record.Operation.State != domain.OperationRequiresApproval {
		return fmt.Errorf("staged operation state is %q, want %q", record.Operation.State, domain.OperationRequiresApproval)
	}
	return nil
}

// validateConfirmNetworkSetupOperation rejects uncorrelated, invalid, or non-setup operation projections.
func validateConfirmNetworkSetupOperation(record state.OperationRecord, operationID domain.OperationID) error {
	if record.Operation.ID != operationID {
		return fmt.Errorf("operation readback differs from request")
	}
	if record.Operation.Kind != domain.OperationKindNetworkSetup || record.Operation.ProjectID != "" {
		return fmt.Errorf("operation %q is not a global %q operation", operationID, domain.OperationKindNetworkSetup)
	}
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("operation is invalid: %w", err)
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return fmt.Errorf("operation revision is invalid: %w", err)
	}
	return nil
}

// validateNetworkSetupPlan binds a resolved helper plan to one bootstrap approval revision.
func validateNetworkSetupPlan(plan ticketissuer.PoolPlan, operationID domain.OperationID, revision domain.Sequence) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("network setup plan is invalid: %w", err)
	}
	if plan.OperationID != operationID {
		return fmt.Errorf("network setup plan belongs to another operation")
	}
	if plan.OperationRevision != revision {
		return &state.StaleRevisionError{OperationID: operationID, Expected: revision, Actual: plan.OperationRevision}
	}
	if plan.Mode != ticketissuer.PoolModeBootstrap {
		return fmt.Errorf("network setup plan mode is %q, want %q", plan.Mode, ticketissuer.PoolModeBootstrap)
	}
	return nil
}

// loadVerifierKey opens signing authority only for a missing intent and closes it before pool scanning begins.
func (coordinator *NetworkSetupCoordinator) loadVerifierKey(ctx context.Context) (string, error) {
	store, err := coordinator.keys()
	if err != nil {
		return "", fmt.Errorf("open signing-key store: %w", err)
	}
	if nilDependency(store) {
		return "", fmt.Errorf("open signing-key store: factory returned no store")
	}
	privateKey, loadErr := store.LoadOrCreate(ctx)
	closeErr := store.Close()
	if loadErr != nil || closeErr != nil {
		return "", errors.Join(
			wrapNetworkSetupError("load or create signing key", loadErr),
			wrapNetworkSetupError("close signing-key store", closeErr),
		)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("load or create signing key: private key contains %d bytes, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(publicKey), nil
}

// issuePool opens helper authority only after plan validation and closes it before result admission.
func (coordinator *NetworkSetupCoordinator) issuePool(
	ctx context.Context,
	requesterIdentity string,
	request ticketissuer.PoolRequest,
) (ticketissuer.PoolResult, error) {
	issuer, err := coordinator.issuers()
	if err != nil {
		return ticketissuer.PoolResult{}, fmt.Errorf("open helper pool issuer: %w", err)
	}
	if nilDependency(issuer) {
		return ticketissuer.PoolResult{}, fmt.Errorf("open helper pool issuer: factory returned no issuer")
	}
	result, issueErr := issuer.Issue(ctx, requesterIdentity, request)
	closeErr := issuer.Close()
	if issueErr != nil || closeErr != nil {
		return ticketissuer.PoolResult{}, errors.Join(
			wrapNetworkSetupError("issue helper pool ticket", issueErr),
			wrapNetworkSetupError("close helper pool issuer", closeErr),
		)
	}
	return result, nil
}

// observePool independently reads every canonical address and rejects uncorrelated native results.
func (coordinator *NetworkSetupCoordinator) observePool(
	ctx context.Context,
	pool identity.Pool,
) ([]loopback.Observation, error) {
	if err := validateNetworkSetupPool(pool); err != nil {
		return nil, err
	}
	addresses := pool.Candidates()
	observations := make([]loopback.Observation, 0, len(addresses))
	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observation, err := coordinator.loopback.Observe(ctx, address)
		if err != nil {
			return nil, fmt.Errorf("observe loopback address %s: %w", address, err)
		}
		if observation.Address != address {
			return nil, fmt.Errorf("loopback observation address differs from request")
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

// validateNetworkSetupPool requires the complete canonical address set for one IPv4-loopback /29.
func validateNetworkSetupPool(pool identity.Pool) error {
	if err := pool.Validate(); err != nil {
		return err
	}
	prefix := pool.Prefix()
	if !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() || prefix.Bits() != 29 || prefix != prefix.Masked() {
		return fmt.Errorf("pool prefix %s is not a canonical IPv4-loopback /29", prefix)
	}
	addresses := pool.Candidates()
	if len(addresses) != networkSetupPoolAddressCount {
		return fmt.Errorf("pool contains %d addresses, want %d", len(addresses), networkSetupPoolAddressCount)
	}
	expected := prefix.Addr()
	for index, address := range addresses {
		if address != expected {
			return fmt.Errorf("pool address %d is %s, want %s", index, address, expected)
		}
		expected = expected.Next()
	}
	return nil
}

// networkSetupPoolFromOwnership reconstructs the exact-eight pool retained by terminal ownership projection.
func networkSetupPoolFromOwnership(record ownership.Record) (identity.Pool, error) {
	prefix, err := netip.ParsePrefix(record.LoopbackPoolPrefix)
	if err != nil {
		return identity.Pool{}, err
	}
	if !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() || prefix.Bits() != 29 ||
		prefix != prefix.Masked() || prefix.String() != record.LoopbackPoolPrefix {
		return identity.Pool{}, fmt.Errorf("ownership prefix %q is not a canonical IPv4-loopback /29", record.LoopbackPoolPrefix)
	}
	addresses := make([]netip.Addr, networkSetupPoolAddressCount)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		return identity.Pool{}, err
	}
	if err := validateNetworkSetupPool(pool); err != nil {
		return identity.Pool{}, err
	}
	return pool, nil
}

// validateNetworkSetupOwnershipObservation requires an exact fingerprinted projection before terminal replay observation.
func validateNetworkSetupOwnershipObservation(observation ownership.Observation) error {
	if !observation.Exists {
		return fmt.Errorf("confirmed ownership projection is missing")
	}
	if err := observation.Record.Validate(); err != nil {
		return fmt.Errorf("confirmed ownership projection is invalid: %w", err)
	}
	if observation.Record.Generation != 1 {
		return fmt.Errorf("confirmed ownership generation is %d, want 1", observation.Record.Generation)
	}
	fingerprint, err := observation.Record.Fingerprint()
	if err != nil {
		return err
	}
	if observation.Fingerprint != fingerprint {
		return fmt.Errorf("confirmed ownership projection fingerprint does not match its record")
	}
	return nil
}

// operationTime returns a canonical setup instant no earlier than one durable lifecycle boundary.
func (coordinator *NetworkSetupCoordinator) operationTime(now time.Time, lowerBound time.Time) time.Time {
	at := now.UTC().Round(0)
	if !lowerBound.IsZero() && at.Before(lowerBound) {
		return lowerBound.UTC().Round(0)
	}
	return at
}

// wrapNetworkSetupError adds lifecycle context without manufacturing errors for successful resource steps.
func wrapNetworkSetupError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
