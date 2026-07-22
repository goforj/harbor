package helper

import (
	"context"
	"errors"
	"net/netip"

	"github.com/goforj/harbor/internal/host/networkpolicy"
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

// Dispatcher validates, consumes, and dispatches one helper ticket.
type Dispatcher struct {
	redeemer    TicketRedeemer
	clock       Clock
	replayGuard ReplayGuard
	loopback    LoopbackIdentityHandler
	resolver    ResolverHandler
	trust       TrustHandler
	lowPorts    LowPortHandler
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
	return &Dispatcher{
		redeemer:    redeemer,
		clock:       clock,
		replayGuard: replayGuard,
		loopback:    loopbackHandler,
		resolver:    resolverHandler,
		trust:       trustHandler,
		lowPorts:    lowPortHandler,
	}
}

// Dispatch admits at most one use of a valid ticket before invoking its operation handler.
func (d *Dispatcher) Dispatch(ctx context.Context, request Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
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

	result := &OperationResult{Operation: ticket.Operation}
	var err error
	switch ticket.Operation {
	case OperationEnsureLoopbackIdentity:
		result.Evidence, err = d.loopback.EnsureLoopbackIdentity(operationContext, ticket)
		if err == nil {
			err = result.Evidence.validate(ticket)
		}
	case OperationEnsureLoopbackPool:
		poolEvidence, poolErr := d.loopback.EnsureLoopbackPool(operationContext, ticket)
		err = poolErr
		if err == nil {
			err = poolEvidence.validate(ticket)
		}
		result.PoolEvidence = &poolEvidence
	case OperationReleaseLoopbackIdentity:
		result.Evidence, err = d.loopback.ReleaseLoopbackIdentity(operationContext, ticket)
		if err == nil {
			err = result.Evidence.validate(ticket)
		}
	case OperationEnsureResolver:
		resolverEvidence, resolverErr := d.resolver.EnsureResolver(operationContext, ticket, redemption.Admission)
		err = resolverErr
		if err == nil {
			err = resolverEvidence.validate(ticket, redemption.Admission)
		}
		result.ResolverEvidence = &resolverEvidence
	case OperationReleaseResolver:
		resolverEvidence, resolverErr := d.resolver.ReleaseResolver(operationContext, ticket, redemption.Admission)
		err = resolverErr
		if err == nil {
			err = resolverEvidence.validate(ticket, redemption.Admission)
		}
		result.ResolverEvidence = &resolverEvidence
	case OperationEnsureTrust:
		trustEvidence, trustErr := d.trust.EnsureTrust(operationContext, ticket)
		err = trustErr
		if err == nil {
			err = trustEvidence.validate(ticket)
		}
		result.TrustEvidence = &trustEvidence
	case OperationReleaseTrust:
		trustEvidence, trustErr := d.trust.ReleaseTrust(operationContext, ticket)
		err = trustErr
		if err == nil {
			err = trustEvidence.validate(ticket)
		}
		result.TrustEvidence = &trustEvidence
	case OperationEnsureLowPorts:
		lowPortEvidence, lowPortErr := d.lowPorts.EnsureLowPorts(operationContext, ticket, redemption.Admission)
		err = lowPortErr
		if err == nil {
			err = lowPortEvidence.validate(ticket, redemption.Admission)
		}
		result.LowPortEvidence = &lowPortEvidence
	case OperationReleaseLowPorts:
		lowPortEvidence, lowPortErr := d.lowPorts.ReleaseLowPorts(operationContext, ticket, redemption.Admission)
		err = lowPortErr
		if err == nil {
			err = lowPortEvidence.validate(ticket, redemption.Admission)
		}
		result.LowPortEvidence = &lowPortEvidence
	default:
		err = newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
	if err != nil {
		return responseForError(err), err
	}

	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result:  result,
	}, nil
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
	if err := e.validateShape(); err != nil {
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
func (e PoolMutationEvidence) validateShape() error {
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
		if evidence.Observation.State != ObservationOwned {
			return errors.New("response pool evidence postcondition is not owned")
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
	}

	return Response{
		Version: ProtocolVersion,
		OK:      false,
		Error:   responseError,
	}
}
