package helper

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/platform/trust"
)

// MutationEvidence is the bounded postcondition returned by a loopback identity handler.
type MutationEvidence struct {
	Changed     bool                `json:"changed"`
	Address     string              `json:"address"`
	Observation ExpectedObservation `json:"observation"`
}

// PoolMutationEvidence is the bounded postcondition returned by one complete loopback pool ensure.
type PoolMutationEvidence struct {
	Pool       string             `json:"pool"`
	Identities []MutationEvidence `json:"identities"`
}

// ResolverPostcondition identifies the exact policy-owned state proven after a resolver operation.
type ResolverPostcondition string

const (
	// ResolverPostconditionExact means the one owned resolver rule exactly matches the signed policy.
	ResolverPostconditionExact ResolverPostcondition = "exact"
	// ResolverPostconditionOwnedAbsent means no resolver rule owned by the signed policy remains.
	ResolverPostconditionOwnedAbsent ResolverPostcondition = "owned_absent"
)

// ResolverMutationEvidence is the bounded resolver postcondition returned by the privileged handler.
type ResolverMutationEvidence struct {
	Changed                bool                  `json:"changed"`
	PolicyFingerprint      string                `json:"policy_fingerprint"`
	OwnershipFingerprint   string                `json:"ownership_fingerprint"`
	ObservationFingerprint string                `json:"observation_fingerprint"`
	Postcondition          ResolverPostcondition `json:"postcondition"`
}

// TrustPostcondition identifies the exact public-CA trust state proven after a trust operation.
type TrustPostcondition string

const (
	// TrustPostconditionExact means one Harbor-owned trust entry exactly matches the signed public CA.
	TrustPostconditionExact TrustPostcondition = "exact"
	// TrustPostconditionPreexisting means an identical unowned CA was already trusted and was preserved.
	TrustPostconditionPreexisting TrustPostcondition = "preexisting"
	// TrustPostconditionOwnedAbsent means no trust entry owned by the signed installation remains.
	TrustPostconditionOwnedAbsent TrustPostcondition = "owned_absent"
)

// TrustMutationEvidence is the bounded trust postcondition returned by the privileged handler.
type TrustMutationEvidence struct {
	Changed                bool                         `json:"changed"`
	AuthorityFingerprint   string                       `json:"authority_fingerprint"`
	Mechanism              networkpolicy.TrustMechanism `json:"mechanism"`
	ObservationFingerprint string                       `json:"observation_fingerprint"`
	Postcondition          TrustPostcondition           `json:"postcondition"`
}

// LowPortPostcondition identifies the exact paired low-port service state proven after an operation.
type LowPortPostcondition string

const (
	// LowPortPostconditionExact means the Harbor-owned 80/443 forwarding service exactly matches the signed policy.
	LowPortPostconditionExact LowPortPostcondition = "exact"
	// LowPortPostconditionOwnedAbsent means no low-port service owned by the signed policy remains.
	LowPortPostconditionOwnedAbsent LowPortPostcondition = "owned_absent"
)

// LowPortMutationEvidence is the bounded low-port service postcondition returned by the privileged handler.
type LowPortMutationEvidence struct {
	Changed                bool                 `json:"changed"`
	PolicyFingerprint      string               `json:"policy_fingerprint"`
	OwnershipFingerprint   string               `json:"ownership_fingerprint"`
	ObservationFingerprint string               `json:"observation_fingerprint"`
	Postcondition          LowPortPostcondition `json:"postcondition"`
}

// LoopbackIdentityHandler applies only the loopback operations admitted by this protocol.
type LoopbackIdentityHandler interface {
	// EnsureLoopbackIdentity ensures the ticket's approved address and returns its observed postcondition.
	EnsureLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error)
	// EnsureLoopbackPool ensures all eight ticket-approved addresses and returns their observed postconditions.
	EnsureLoopbackPool(context.Context, Ticket) (PoolMutationEvidence, error)
	// ReleaseLoopbackIdentity releases the ticket's approved address and returns its observed postcondition.
	ReleaseLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error)
}

// LoopbackPoolReleaseHandler applies the additive aggregate pool release operation.
type LoopbackPoolReleaseHandler interface {
	// ReleaseLoopbackPool releases all owned ticket-approved addresses and returns absent postconditions.
	ReleaseLoopbackPool(context.Context, Ticket) (PoolMutationEvidence, error)
}

// UnavailableLoopbackIdentityHandler fails closed until platform mutation adapters are installed.
type UnavailableLoopbackIdentityHandler struct{}

// EnsureLoopbackIdentity rejects ensure operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) EnsureLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	return MutationEvidence{}, ErrMutationUnavailable
}

// EnsureLoopbackPool rejects pool ensure operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) EnsureLoopbackPool(context.Context, Ticket) (PoolMutationEvidence, error) {
	return PoolMutationEvidence{}, ErrMutationUnavailable
}

// ReleaseLoopbackPool rejects pool release operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) ReleaseLoopbackPool(context.Context, Ticket) (PoolMutationEvidence, error) {
	return PoolMutationEvidence{}, ErrMutationUnavailable
}

// ReleaseLoopbackIdentity rejects release operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) ReleaseLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	return MutationEvidence{}, ErrMutationUnavailable
}

// ResolverHandler applies only the policy-bound resolver operations admitted by this protocol.
type ResolverHandler interface {
	// EnsureResolver ensures the signed resolver policy and returns its verified postcondition.
	EnsureResolver(context.Context, Ticket, TicketAdmission) (ResolverMutationEvidence, error)
	// ReleaseResolver removes only the signed policy's owned resolver rule and returns its verified postcondition.
	ReleaseResolver(context.Context, Ticket, TicketAdmission) (ResolverMutationEvidence, error)
}

// TrustHandler applies only the public-CA trust operations admitted by this protocol.
type TrustHandler interface {
	// EnsureTrust ensures the signed public CA trust projection and returns its verified postcondition.
	EnsureTrust(context.Context, Ticket) (TrustMutationEvidence, error)
	// ReleaseTrust removes only the signed installation's owned public CA trust projection.
	ReleaseTrust(context.Context, Ticket) (TrustMutationEvidence, error)
}

// LowPortHandler applies only the policy-bound low-port operations admitted by this protocol.
type LowPortHandler interface {
	// EnsureLowPorts ensures the signed low-port service and returns its verified postcondition.
	EnsureLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error)
	// ReleaseLowPorts removes only the signed policy's owned low-port service and returns its verified postcondition.
	ReleaseLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error)
}

// UnavailableLowPortHandler fails closed until a reviewed platform low-port adapter is installed.
type UnavailableLowPortHandler struct{}

// EnsureLowPorts rejects low-port ensure operations because no mutation authority is installed.
func (UnavailableLowPortHandler) EnsureLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error) {
	return LowPortMutationEvidence{}, ErrMutationUnavailable
}

// ReleaseLowPorts rejects low-port release operations because no mutation authority is installed.
func (UnavailableLowPortHandler) ReleaseLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error) {
	return LowPortMutationEvidence{}, ErrMutationUnavailable
}

// UnavailableTrustHandler fails closed until a reviewed platform trust adapter is installed.
type UnavailableTrustHandler struct{}

// EnsureTrust rejects trust ensure operations because no trust mutation authority is installed.
func (UnavailableTrustHandler) EnsureTrust(context.Context, Ticket) (TrustMutationEvidence, error) {
	return TrustMutationEvidence{}, ErrMutationUnavailable
}

// ReleaseTrust rejects trust release operations because no trust mutation authority is installed.
func (UnavailableTrustHandler) ReleaseTrust(context.Context, Ticket) (TrustMutationEvidence, error) {
	return TrustMutationEvidence{}, ErrMutationUnavailable
}

// Close releases no resources because the unavailable trust handler opens no native authority.
func (UnavailableTrustHandler) Close() error {
	return nil
}

// UnavailableResolverHandler fails closed on platforms without an installed resolver adapter.
type UnavailableResolverHandler struct{}

// EnsureResolver rejects ensure operations because no resolver mutation authority is installed.
func (UnavailableResolverHandler) EnsureResolver(context.Context, Ticket, TicketAdmission) (ResolverMutationEvidence, error) {
	return ResolverMutationEvidence{}, ErrMutationUnavailable
}

// ReleaseResolver rejects release operations because no resolver mutation authority is installed.
func (UnavailableResolverHandler) ReleaseResolver(context.Context, Ticket, TicketAdmission) (ResolverMutationEvidence, error) {
	return ResolverMutationEvidence{}, ErrMutationUnavailable
}

// Close releases no resources because the unavailable handler opens no authority boundary.
func (UnavailableResolverHandler) Close() error {
	return nil
}

// ResponseError is the bounded structured error returned by the helper.
type ResponseError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// OperationResult records the admitted operation and its validated postcondition evidence.
type OperationResult struct {
	Operation        Operation                 `json:"operation"`
	Evidence         MutationEvidence          `json:"evidence,omitzero"`
	PoolEvidence     *PoolMutationEvidence     `json:"pool_evidence,omitempty"`
	ResolverEvidence *ResolverMutationEvidence `json:"resolver_evidence,omitempty"`
	TrustEvidence    *TrustMutationEvidence    `json:"trust_evidence,omitempty"`
	LowPortEvidence  *LowPortMutationEvidence  `json:"low_port_evidence,omitempty"`
}

// Response is the versioned one-shot helper response envelope.
type Response struct {
	Version uint16           `json:"version"`
	OK      bool             `json:"ok"`
	Result  *OperationResult `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

// AdmittedTrustOperation is one trust ticket operation that passed redemption, clock, and replay admission.
type AdmittedTrustOperation struct {
	ticket Ticket
}

// RequesterIdentity returns the authenticated requester identity bound to the admitted trust ticket.
func (admitted AdmittedTrustOperation) RequesterIdentity() string {
	return admitted.ticket.RequesterIdentity
}

// TrustMechanism returns the trust scope authenticated by the admitted ticket policy.
func (admitted AdmittedTrustOperation) TrustMechanism() networkpolicy.TrustMechanism {
	return admitted.ticket.NetworkPolicy.Mechanisms.Trust
}

// ExecuteTrust invokes exactly the trust handler selected by the already-admitted trust operation.
func (admitted AdmittedTrustOperation) ExecuteTrust(ctx context.Context, handler TrustHandler) (OperationResult, error) {
	if handler == nil {
		return OperationResult{}, ErrMutationUnavailable
	}
	result := OperationResult{Operation: admitted.ticket.Operation}
	var evidence TrustMutationEvidence
	var err error
	switch admitted.ticket.Operation {
	case OperationEnsureTrust:
		evidence, err = handler.EnsureTrust(ctx, admitted.ticket)
	case OperationReleaseTrust:
		evidence, err = handler.ReleaseTrust(ctx, admitted.ticket)
	default:
		return OperationResult{}, newRequestError(ErrorCodeInvalidTicket, "admitted operation is not a trust operation")
	}
	if err != nil {
		return OperationResult{}, err
	}
	if err := evidence.validate(admitted.ticket); err != nil {
		return OperationResult{}, err
	}
	result.TrustEvidence = &evidence
	return result, nil
}

// AdmittedResolverOperation is one resolver ticket operation that passed redemption, clock, and replay admission.
type AdmittedResolverOperation struct {
	ticket    Ticket
	admission TicketAdmission
}

// RequesterIdentity returns the authenticated requester identity bound to the admitted resolver ticket.
func (admitted AdmittedResolverOperation) RequesterIdentity() string {
	return admitted.ticket.RequesterIdentity
}

// ExecuteResolver invokes exactly the resolver handler selected by the already-admitted resolver operation.
func (admitted AdmittedResolverOperation) ExecuteResolver(ctx context.Context, handler ResolverHandler) (OperationResult, error) {
	if handler == nil {
		return OperationResult{}, ErrMutationUnavailable
	}
	result := OperationResult{Operation: admitted.ticket.Operation}
	var evidence ResolverMutationEvidence
	var err error
	switch admitted.ticket.Operation {
	case OperationEnsureResolver:
		evidence, err = handler.EnsureResolver(ctx, admitted.ticket, admitted.admission)
	case OperationReleaseResolver:
		evidence, err = handler.ReleaseResolver(ctx, admitted.ticket, admitted.admission)
	default:
		return OperationResult{}, newRequestError(ErrorCodeInvalidTicket, "admitted operation is not a resolver operation")
	}
	if err != nil {
		return OperationResult{}, err
	}
	if err := evidence.validate(admitted.ticket, admitted.admission); err != nil {
		return OperationResult{}, err
	}
	result.ResolverEvidence = &evidence
	return result, nil
}

// AdmittedLowPortOperation is one low-port ticket operation that passed redemption, clock, and replay admission.
type AdmittedLowPortOperation struct {
	ticket    Ticket
	admission TicketAdmission
}

// RequesterIdentity returns the authenticated requester identity bound to the admitted low-port ticket.
func (admitted AdmittedLowPortOperation) RequesterIdentity() string {
	return admitted.ticket.RequesterIdentity
}

// ExecuteLowPorts invokes exactly the low-port handler selected by the already-admitted low-port operation.
func (admitted AdmittedLowPortOperation) ExecuteLowPorts(ctx context.Context, handler LowPortHandler) (OperationResult, error) {
	if handler == nil {
		return OperationResult{}, ErrMutationUnavailable
	}
	result := OperationResult{Operation: admitted.ticket.Operation}
	var evidence LowPortMutationEvidence
	var err error
	switch admitted.ticket.Operation {
	case OperationEnsureLowPorts:
		evidence, err = handler.EnsureLowPorts(ctx, admitted.ticket, admitted.admission)
	case OperationReleaseLowPorts:
		evidence, err = handler.ReleaseLowPorts(ctx, admitted.ticket, admitted.admission)
	default:
		return OperationResult{}, newRequestError(ErrorCodeInvalidTicket, "admitted operation is not a low-port operation")
	}
	if err != nil {
		return OperationResult{}, err
	}
	if err := evidence.validate(admitted.ticket, admitted.admission); err != nil {
		return OperationResult{}, err
	}
	result.LowPortEvidence = &evidence
	return result, nil
}

// AdmittedLoopbackOperation is one loopback ticket operation that passed redemption, clock, and replay admission.
type AdmittedLoopbackOperation struct {
	ticket Ticket
}

// RequesterIdentity returns the authenticated requester identity bound to the admitted loopback ticket.
func (admitted AdmittedLoopbackOperation) RequesterIdentity() string {
	return admitted.ticket.RequesterIdentity
}

// ExecuteLoopback invokes exactly the loopback handler selected by the already-admitted loopback operation.
func (admitted AdmittedLoopbackOperation) ExecuteLoopback(ctx context.Context, handler LoopbackIdentityHandler) (OperationResult, error) {
	if handler == nil {
		return OperationResult{}, ErrMutationUnavailable
	}
	result := OperationResult{Operation: admitted.ticket.Operation}
	switch admitted.ticket.Operation {
	case OperationEnsureLoopbackIdentity:
		evidence, err := handler.EnsureLoopbackIdentity(ctx, admitted.ticket)
		if err != nil {
			return OperationResult{}, err
		}
		if err := evidence.validate(admitted.ticket); err != nil {
			return OperationResult{}, err
		}
		result.Evidence = evidence
	case OperationReleaseLoopbackIdentity:
		evidence, err := handler.ReleaseLoopbackIdentity(ctx, admitted.ticket)
		if err != nil {
			return OperationResult{}, err
		}
		if err := evidence.validate(admitted.ticket); err != nil {
			return OperationResult{}, err
		}
		result.Evidence = evidence
	case OperationEnsureLoopbackPool, OperationReleaseLoopbackPool:
		poolHandler, supported := handler.(LoopbackPoolReleaseHandler)
		if admitted.ticket.Operation == OperationEnsureLoopbackPool {
			evidence, err := handler.EnsureLoopbackPool(ctx, admitted.ticket)
			if err != nil {
				return OperationResult{}, err
			}
			if err := evidence.validate(admitted.ticket); err != nil {
				return OperationResult{}, err
			}
			result.PoolEvidence = &evidence
			return result, nil
		}
		if !supported {
			return OperationResult{}, ErrMutationUnavailable
		}
		evidence, err := poolHandler.ReleaseLoopbackPool(ctx, admitted.ticket)
		if err != nil {
			return OperationResult{}, err
		}
		if err := evidence.validate(admitted.ticket); err != nil {
			return OperationResult{}, err
		}
		result.PoolEvidence = &evidence
	default:
		return OperationResult{}, newRequestError(ErrorCodeInvalidTicket, "admitted operation is not a loopback operation")
	}
	return result, nil
}

// AdmittedTrustExecutor executes one already-admitted trust operation.
type AdmittedTrustExecutor func(context.Context, AdmittedTrustOperation) (OperationResult, error)

// AdmittedResolverExecutor executes one already-admitted resolver operation.
type AdmittedResolverExecutor func(context.Context, AdmittedResolverOperation) (OperationResult, error)

// AdmittedLowPortExecutor executes one already-admitted low-port operation.
type AdmittedLowPortExecutor func(context.Context, AdmittedLowPortOperation) (OperationResult, error)

// AdmittedLoopbackExecutor executes one already-admitted loopback operation.
type AdmittedLoopbackExecutor func(context.Context, AdmittedLoopbackOperation) (OperationResult, error)

// AdmittedOperationExecutors supplies the operation-family callbacks reached only after admission.
type AdmittedOperationExecutors struct {
	// Trust executes authenticated trust ticket operations.
	Trust AdmittedTrustExecutor
	// Resolver executes authenticated resolver ticket operations.
	Resolver AdmittedResolverExecutor
	// LowPorts executes authenticated low-port ticket operations.
	LowPorts AdmittedLowPortExecutor
	// Loopback executes authenticated loopback ticket operations.
	Loopback AdmittedLoopbackExecutor
}

// handlerAdmittedOperationExecutors retains the established handler graph behind the admitted-operation boundary.
type handlerAdmittedOperationExecutors struct {
	loopback LoopbackIdentityHandler
	resolver ResolverHandler
	trust    TrustHandler
	lowPorts LowPortHandler
}

// NewAdmittedOperationExecutors constructs the production callbacks from the reviewed operation-family handlers.
func NewAdmittedOperationExecutors(loopback LoopbackIdentityHandler, resolver ResolverHandler, trust TrustHandler, lowPorts LowPortHandler) AdmittedOperationExecutors {
	if loopback == nil || resolver == nil || trust == nil || lowPorts == nil {
		panic("helper.NewAdmittedOperationExecutors requires non-nil handlers")
	}
	handlers := handlerAdmittedOperationExecutors{
		loopback: loopback,
		resolver: resolver,
		trust:    trust,
		lowPorts: lowPorts,
	}
	return AdmittedOperationExecutors{
		Trust:    handlers.executeTrust,
		Resolver: handlers.executeResolver,
		LowPorts: handlers.executeLowPorts,
		Loopback: handlers.executeLoopback,
	}
}

// executeTrust executes one admitted trust operation through the configured handler.
func (executor handlerAdmittedOperationExecutors) executeTrust(ctx context.Context, admitted AdmittedTrustOperation) (OperationResult, error) {
	return admitted.ExecuteTrust(ctx, executor.trust)
}

// executeResolver executes one admitted resolver operation through the configured handler.
func (executor handlerAdmittedOperationExecutors) executeResolver(ctx context.Context, admitted AdmittedResolverOperation) (OperationResult, error) {
	return admitted.ExecuteResolver(ctx, executor.resolver)
}

// executeLowPorts executes one admitted low-port operation through the configured handler.
func (executor handlerAdmittedOperationExecutors) executeLowPorts(ctx context.Context, admitted AdmittedLowPortOperation) (OperationResult, error) {
	return admitted.ExecuteLowPorts(ctx, executor.lowPorts)
}

// executeLoopback executes one admitted loopback operation through the configured handler.
func (executor handlerAdmittedOperationExecutors) executeLoopback(ctx context.Context, admitted AdmittedLoopbackOperation) (OperationResult, error) {
	return admitted.ExecuteLoopback(ctx, executor.loopback)
}

// Dispatcher validates, consumes, and dispatches one helper ticket.
type Dispatcher struct {
	redeemer             TicketRedeemer
	clock                Clock
	replayGuard          ReplayGuard
	executors            AdmittedOperationExecutors
	detachTrustAdmission bool
}

// NewDispatcher constructs a dispatcher with resolver, trust, and low-port effects intentionally unavailable.
func NewDispatcher(redeemer TicketRedeemer, clock Clock, replayGuard ReplayGuard, handler LoopbackIdentityHandler) *Dispatcher {
	return NewDispatcherWithResolverAndTrust(redeemer, clock, replayGuard, handler, UnavailableResolverHandler{}, UnavailableTrustHandler{})
}

// NewDispatcherWithResolver constructs a dispatcher with an explicit resolver handler and unavailable trust and low-port effects.
func NewDispatcherWithResolver(
	redeemer TicketRedeemer,
	clock Clock,
	replayGuard ReplayGuard,
	loopbackHandler LoopbackIdentityHandler,
	resolverHandler ResolverHandler,
) *Dispatcher {
	return NewDispatcherWithResolverAndTrust(
		redeemer,
		clock,
		replayGuard,
		loopbackHandler,
		resolverHandler,
		UnavailableTrustHandler{},
	)
}

// NewDispatcherWithResolverAndTrust constructs a dispatcher with explicit resolver and trust authorities.
func NewDispatcherWithResolverAndTrust(
	redeemer TicketRedeemer,
	clock Clock,
	replayGuard ReplayGuard,
	loopbackHandler LoopbackIdentityHandler,
	resolverHandler ResolverHandler,
	trustHandler TrustHandler,
) *Dispatcher {
	if redeemer == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil ticket redeemer")
	}
	if clock == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil clock")
	}
	if replayGuard == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil replay guard")
	}
	if loopbackHandler == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil loopback identity handler")
	}
	if resolverHandler == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil resolver handler")
	}
	if trustHandler == nil {
		panic("helper.NewDispatcherWithResolverAndTrust requires a non-nil trust handler")
	}
	return NewDispatcherWithResolverTrustAndLowPorts(redeemer, clock, replayGuard, loopbackHandler, resolverHandler, trustHandler, UnavailableLowPortHandler{})
}

// NewDispatcherWithResolverTrustAndLowPorts constructs a dispatcher with explicit resolver, trust, and low-port authorities.
func NewDispatcherWithResolverTrustAndLowPorts(
	redeemer TicketRedeemer,
	clock Clock,
	replayGuard ReplayGuard,
	loopbackHandler LoopbackIdentityHandler,
	resolverHandler ResolverHandler,
	trustHandler TrustHandler,
	lowPortHandler LowPortHandler,
) *Dispatcher {
	if redeemer == nil || clock == nil || replayGuard == nil || loopbackHandler == nil || resolverHandler == nil || trustHandler == nil || lowPortHandler == nil {
		panic("helper.NewDispatcherWithResolverTrustAndLowPorts requires non-nil dependencies")
	}
	return NewDispatcherWithAdmittedOperationExecutors(
		redeemer,
		clock,
		replayGuard,
		NewAdmittedOperationExecutors(
			loopbackHandler,
			resolverHandler,
			trustHandler,
			lowPortHandler,
		),
	)
}

// NewDispatcherWithAdmittedOperationExecutors constructs a dispatcher that delegates only after ticket admission is complete.
func NewDispatcherWithAdmittedOperationExecutors(redeemer TicketRedeemer, clock Clock, replayGuard ReplayGuard, executors AdmittedOperationExecutors) *Dispatcher {
	if redeemer == nil || clock == nil || replayGuard == nil || executors.Trust == nil || executors.Resolver == nil || executors.LowPorts == nil || executors.Loopback == nil {
		panic("helper.NewDispatcherWithAdmittedOperationExecutors requires non-nil dependencies")
	}
	return &Dispatcher{
		redeemer:    redeemer,
		clock:       clock,
		replayGuard: replayGuard,
		executors:   executors,
	}
}

// NewOneShotDispatcherWithAdmittedOperationExecutors constructs a single-request dispatcher that detaches root admission references before trust execution.
func NewOneShotDispatcherWithAdmittedOperationExecutors(redeemer TicketRedeemer, clock Clock, replayGuard ReplayGuard, executors AdmittedOperationExecutors) *Dispatcher {
	dispatcher := NewDispatcherWithAdmittedOperationExecutors(redeemer, clock, replayGuard, executors)
	dispatcher.detachTrustAdmission = true
	return dispatcher
}

// Dispatch admits at most one use of a valid ticket before invoking its operation handler.
func (d *Dispatcher) Dispatch(ctx context.Context, request Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d.redeemer == nil || d.replayGuard == nil {
		err := newRequestError(ErrorCodeAuthenticationFailed, "helper admission authority is no longer available")
		return responseForError(err), err
	}
	if err := request.Validate(); err != nil {
		return responseForError(err), err
	}

	redemptionContext, cancelRedemption := context.WithTimeout(ctx, MaxTicketRedemptionDuration)
	redemption, redemptionErr := d.redeemer.Redeem(redemptionContext, request.TicketReference)
	cancelRedemption()
	if redemptionErr != nil {
		redemptionErr = normalizeRedemptionError(redemptionErr)
		return responseForError(redemptionErr), redemptionErr
	}
	if err := redemption.validate(request.TicketReference); err != nil {
		return responseForError(err), err
	}

	ticket := redemption.Ticket
	now := d.clock.Now().UTC()
	if err := ticket.Validate(now); err != nil {
		return responseForError(err), err
	}
	operationContext, cancel := context.WithTimeout(ctx, ticket.ExpiresAt.Sub(now))
	defer cancel()

	claim := ReplayClaim{
		Key: ReplayKey{
			InstallationID:      ticket.InstallationID,
			OwnershipGeneration: ticket.OwnershipGeneration,
			Nonce:               ticket.Nonce,
		},
		ExpiresAt: ticket.ExpiresAt,
	}
	if err := d.replayGuard.Consume(operationContext, claim); err != nil {
		return responseForError(err), err
	}

	var result OperationResult
	var err error
	switch ticket.Operation {
	case OperationEnsureTrust, OperationReleaseTrust:
		if d.detachTrustAdmission {
			d.redeemer = nil
			d.replayGuard = nil
		}
		result, err = d.executors.Trust(operationContext, AdmittedTrustOperation{ticket: ticket})
	case OperationEnsureResolver, OperationReleaseResolver:
		result, err = d.executors.Resolver(operationContext, AdmittedResolverOperation{
			ticket:    ticket,
			admission: redemption.Admission,
		})
	case OperationEnsureLowPorts, OperationReleaseLowPorts:
		result, err = d.executors.LowPorts(operationContext, AdmittedLowPortOperation{
			ticket:    ticket,
			admission: redemption.Admission,
		})
	case OperationEnsureLoopbackIdentity,
		OperationEnsureLoopbackPool,
		OperationReleaseLoopbackPool,
		OperationReleaseLoopbackIdentity:
		result, err = d.executors.Loopback(operationContext, AdmittedLoopbackOperation{ticket: ticket})
	default:
		err = newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
	if err == nil {
		err = validateAdmittedOperationResult(ticket, redemption.Admission, result)
	}
	if err != nil {
		return responseForError(err), err
	}

	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result:  &result,
	}, nil
}

// validateAdmittedOperationResult preserves ticket-to-result correlation for injected family callbacks.
func validateAdmittedOperationResult(ticket Ticket, admission TicketAdmission, result OperationResult) error {
	if result.Operation != ticket.Operation {
		return newRequestError(ErrorCodeMutationFailed, "admitted operation result does not match the ticket operation")
	}
	if err := validateOperationResult(result); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "admitted operation result is invalid")
	}
	switch ticket.Operation {
	case OperationEnsureLoopbackIdentity, OperationReleaseLoopbackIdentity:
		return result.Evidence.validate(ticket)
	case OperationEnsureLoopbackPool, OperationReleaseLoopbackPool:
		return result.PoolEvidence.validate(ticket)
	case OperationEnsureResolver, OperationReleaseResolver:
		return result.ResolverEvidence.validate(ticket, admission)
	case OperationEnsureTrust, OperationReleaseTrust:
		return result.TrustEvidence.validate(ticket)
	case OperationEnsureLowPorts, OperationReleaseLowPorts:
		return result.LowPortEvidence.validate(ticket, admission)
	default:
		return newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
}

// validate prevents a resolver adapter from returning evidence for another policy or postcondition.
func (e ResolverMutationEvidence) validate(ticket Ticket, admission TicketAdmission) error {
	if err := e.validateShape(ticket.Operation); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "resolver mutation evidence is invalid")
	}
	if e.PolicyFingerprint != ticket.NetworkPolicyFingerprint {
		return newRequestError(ErrorCodeMutationFailed, "resolver mutation evidence policy does not match the approved policy")
	}
	if e.OwnershipFingerprint != admission.TargetOwnershipFingerprint {
		return newRequestError(ErrorCodeMutationFailed, "resolver mutation evidence ownership does not match the approved target")
	}
	return nil
}

// validateShape enforces the standalone resolver response shape before ticket correlation.
func (e ResolverMutationEvidence) validateShape(operation Operation) error {
	if !validFingerprint(e.PolicyFingerprint) ||
		!validFingerprint(e.OwnershipFingerprint) ||
		!validFingerprint(e.ObservationFingerprint) {
		return errors.New("resolver mutation evidence fingerprints are invalid")
	}
	want := ResolverPostconditionExact
	if operation == OperationReleaseResolver {
		want = ResolverPostconditionOwnedAbsent
	} else if operation != OperationEnsureResolver {
		return errors.New("resolver mutation evidence operation is unsupported")
	}
	if e.Postcondition != want {
		return errors.New("resolver mutation evidence state does not match the operation")
	}
	return nil
}

// validate prevents a trust adapter from returning evidence for another CA, mechanism, or postcondition.
func (e TrustMutationEvidence) validate(ticket Ticket) error {
	if err := e.validateShape(ticket.Operation); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "trust mutation evidence is invalid")
	}
	if ticket.NetworkPolicy == nil || ticket.TrustRoot == nil {
		return newRequestError(ErrorCodeMutationFailed, "trust mutation evidence has no approved authority")
	}
	if e.AuthorityFingerprint != ticket.TrustRoot.Fingerprint || e.AuthorityFingerprint != ticket.NetworkPolicy.AuthorityFingerprint {
		return newRequestError(ErrorCodeMutationFailed, "trust mutation evidence authority does not match the approved CA")
	}
	if e.Mechanism != ticket.NetworkPolicy.Mechanisms.Trust {
		return newRequestError(ErrorCodeMutationFailed, "trust mutation evidence mechanism does not match the approved policy")
	}
	return nil
}

// validate prevents a low-port adapter from returning evidence for another policy or protected ownership target.
func (e LowPortMutationEvidence) validate(ticket Ticket, admission TicketAdmission) error {
	if err := e.validateShape(ticket.Operation); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "low-port mutation evidence is invalid")
	}
	if e.PolicyFingerprint != ticket.NetworkPolicyFingerprint || e.OwnershipFingerprint != admission.TargetOwnershipFingerprint {
		return newRequestError(ErrorCodeMutationFailed, "low-port mutation evidence does not match approved authority")
	}
	if ticket.Operation == OperationEnsureLowPorts && e.Postcondition != LowPortPostconditionExact || ticket.Operation == OperationReleaseLowPorts && e.Postcondition != LowPortPostconditionOwnedAbsent {
		return newRequestError(ErrorCodeMutationFailed, "low-port mutation evidence postcondition does not match operation")
	}
	return nil
}

// validateShape enforces the standalone low-port response shape before ticket correlation.
func (e LowPortMutationEvidence) validateShape(operation Operation) error {
	if !validFingerprint(e.PolicyFingerprint) || !validFingerprint(e.OwnershipFingerprint) || !validFingerprint(e.ObservationFingerprint) {
		return errors.New("low-port mutation evidence fingerprints are invalid")
	}
	if operation == OperationEnsureLowPorts && e.Postcondition == LowPortPostconditionExact {
		return nil
	}
	if operation == OperationReleaseLowPorts && e.Postcondition == LowPortPostconditionOwnedAbsent {
		return nil
	}
	return errors.New("low-port mutation evidence postcondition is invalid")
}

// validateShape enforces the standalone trust response shape before ticket correlation.
func (e TrustMutationEvidence) validateShape(operation Operation) error {
	if !validFingerprint(e.AuthorityFingerprint) || !validFingerprint(e.ObservationFingerprint) {
		return errors.New("trust mutation evidence fingerprints are invalid")
	}
	if e.Mechanism != networkpolicy.DarwinCurrentUserTrust &&
		e.Mechanism != networkpolicy.DarwinAdministratorTrust &&
		e.Mechanism != networkpolicy.UbuntuSystemTrust &&
		e.Mechanism != networkpolicy.WindowsCurrentUserTrust {
		return errors.New("trust mutation evidence mechanism is unsupported")
	}
	switch operation {
	case OperationEnsureTrust:
		if e.Postcondition != TrustPostconditionExact && e.Postcondition != TrustPostconditionPreexisting {
			return errors.New("trust mutation evidence ensure postcondition is invalid")
		}
	case OperationReleaseTrust:
		if e.Postcondition != TrustPostconditionOwnedAbsent {
			return errors.New("trust mutation evidence release postcondition is invalid")
		}
	default:
		return errors.New("trust mutation evidence operation is unsupported")
	}
	return nil
}

// normalizeRedemptionError keeps adapter details opaque while preserving stable reference outcomes for callers.
func normalizeRedemptionError(err error) error {
	if errors.Is(err, ErrTicketRedemptionUnavailable) || errors.Is(err, ErrTicketRedemptionFailed) {
		return err
	}
	return errors.Join(ErrTicketRedemptionFailed, err)
}

// validate prevents a platform adapter from returning evidence for another address or state.
func (e MutationEvidence) validate(ticket Ticket) error {
	if e.Address != ticket.ApprovedAddress {
		return newRequestError(ErrorCodeMutationFailed, "mutation evidence address does not match the approved address")
	}
	if err := e.Observation.Validate(); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "mutation evidence observation is invalid")
	}
	expectedState := ObservationOwned
	if ticket.Operation == OperationReleaseLoopbackIdentity {
		expectedState = ObservationAbsent
	}
	if e.Observation.State != expectedState {
		return newRequestError(ErrorCodeMutationFailed, "mutation evidence state does not match the operation")
	}
	return nil
}

// validate prevents a platform adapter from returning pool evidence outside the ticket's exact address authority.
func (e PoolMutationEvidence) validate(ticket Ticket) error {
	if err := e.validateShape(ticket.Operation); err != nil {
		return newRequestError(ErrorCodeMutationFailed, "pool mutation evidence is invalid")
	}
	if e.Pool != ticket.ApprovedPool {
		return newRequestError(ErrorCodeMutationFailed, "pool mutation evidence does not match the approved pool")
	}
	if ticket.ExpectedLoopbackPool == nil || len(ticket.ExpectedLoopbackPool.Identities) != loopbackPoolIdentities {
		return newRequestError(ErrorCodeMutationFailed, "pool mutation evidence does not match the ticket authority")
	}
	for index, evidence := range e.Identities {
		if evidence.Address != ticket.ExpectedLoopbackPool.Identities[index].Address {
			return newRequestError(ErrorCodeMutationFailed, "pool mutation evidence address does not match the ticket authority")
		}
	}
	return nil
}

// validateShape enforces the standalone canonical response shape without requiring the redeemed ticket.
func (e PoolMutationEvidence) validateShape(operation Operation) error {
	pool, err := netip.ParsePrefix(e.Pool)
	if err != nil || !pool.Addr().Is4() || !pool.Addr().IsLoopback() || pool.Bits() != loopbackPoolPrefixBits || pool != pool.Masked() || pool.String() != e.Pool {
		return errors.New("response pool evidence pool is not a canonical IPv4 loopback /29")
	}
	if len(e.Identities) != loopbackPoolIdentities {
		return errors.New("response pool evidence must contain exactly eight identities")
	}

	address := pool.Addr()
	for _, evidence := range e.Identities {
		if evidence.Address != address.String() || !validApprovedAddress(evidence.Address) {
			return errors.New("response pool evidence identities are not in canonical address order")
		}
		if err := evidence.Observation.Validate(); err != nil {
			return errors.New("response pool evidence observation is invalid")
		}
		want := ObservationOwned
		if operation == OperationReleaseLoopbackPool {
			want = ObservationAbsent
		}
		if evidence.Observation.State != want {
			return errors.New("response pool evidence postcondition does not match the operation")
		}
		address = address.Next()
	}
	return nil
}

// responseForError maps internal admission outcomes to fixed messages without leaking host details.
func responseForError(err error) Response {
	responseError := &ResponseError{
		Code:    ErrorCodeMutationFailed,
		Message: "helper operation failed",
	}

	var requestError *RequestError
	switch {
	case errors.As(err, &requestError):
		responseError.Code = requestError.Code
		responseError.Message = requestError.Message
	case errors.Is(err, ErrTicketRedemptionUnavailable):
		responseError.Code = ErrorCodeAuthenticationUnavailable
		responseError.Message = "helper ticket redemption is unavailable"
	case errors.Is(err, ErrTicketRedemptionFailed):
		responseError.Code = ErrorCodeAuthenticationFailed
		responseError.Message = "helper ticket redemption failed"
	case errors.Is(err, ErrReplay):
		responseError.Code = ErrorCodeReplayedTicket
		responseError.Message = "helper ticket was already consumed"
	case errors.Is(err, ErrReplayProtectionUnavailable):
		responseError.Code = ErrorCodeReplayProtectionUnavailable
		responseError.Message = "helper replay protection is unavailable"
	case errors.Is(err, ErrMutationUnavailable):
		responseError.Code = ErrorCodeMutationUnavailable
		responseError.Message = "helper platform mutation is unavailable"
	default:
		var trustError *trust.Error
		if errors.As(err, &trustError) {
			if stage, status, ok := trustError.AdministratorTrustDiagnostic(); ok {
				if message, ok := administratorTrustDiagnosticMessage(stage, status); ok {
					responseError.Message = message
				}
			} else if message, ok := trustFailureDiagnosticMessage(trustError); ok {
				responseError.Message = message
			}
		}
	}

	return Response{
		Version: ProtocolVersion,
		OK:      false,
		Error:   responseError,
	}
}

// trustFailureDiagnosticMessage exposes only the adapter's finite operation, kind, and classification enums.
func trustFailureDiagnosticMessage(err *trust.Error) (string, bool) {
	if err == nil || !validTrustFailureOperationKind(err.Operation, err.Kind) {
		return "", false
	}
	state := err.Assessment.State
	owned := err.Assessment.Owned
	if state == "" && owned == "" {
		return fmt.Sprintf("helper operation failed: trust %s %s", err.Operation, err.Kind), true
	}
	if !validTrustFailureAssessment(state, owned) {
		return "", false
	}
	return fmt.Sprintf("helper operation failed: trust %s %s %s/%s", err.Operation, err.Kind, state, owned), true
}

// validTrustFailureOperationKind rejects arbitrary text even when a caller constructs the exported adapter error shape directly.
func validTrustFailureOperationKind(operation string, kind trust.ErrorKind) bool {
	switch operation {
	case "observe":
		return kind == trust.ErrorKindInvalidRequest ||
			kind == trust.ErrorKindObserveFailed ||
			kind == trust.ErrorKindInvalidFacts
	case "ensure", "release":
		return kind == trust.ErrorKindInvalidRequest ||
			kind == trust.ErrorKindInvalidFacts ||
			kind == trust.ErrorKindObservationChanged ||
			kind == trust.ErrorKindConflict ||
			kind == trust.ErrorKindIndeterminate ||
			kind == trust.ErrorKindMutationFailed ||
			kind == trust.ErrorKindVerificationFailed
	default:
		return false
	}
}

// validTrustFailureAssessment accepts only the finite state pairs produced by trust classification.
func validTrustFailureAssessment(state trust.State, owned trust.OwnedState) bool {
	switch state {
	case trust.StateAbsent, trust.StateExact, trust.StateOwnedDrifted,
		trust.StateForeign, trust.StateAmbiguous, trust.StateIndeterminate:
	default:
		return false
	}
	switch owned {
	case trust.OwnedStateAbsent, trust.OwnedStateExact,
		trust.OwnedStateDrifted, trust.OwnedStateAmbiguous:
		return true
	default:
		return false
	}
}

// administratorTrustDiagnosticMessage independently validates the only native facts safe to expose in a helper response.
func administratorTrustDiagnosticMessage(stage string, status int) (string, bool) {
	if status < -(1<<31) || status > (1<<31)-1 {
		return "", false
	}
	switch stage {
	case "snapshot",
		"owner-observe",
		"owner-recheck",
		"root-store-recheck",
		"root-store-verify",
		"root-recheck",
		"add-system-root",
		"owner-record",
		"root-recheck-after-marker",
		"set-root":
		return fmt.Sprintf("helper operation failed: administrator trust %s OSStatus %d", stage, status), true
	default:
		return "", false
	}
}
