package helper

import (
	"context"
	"errors"
)

// MutationEvidence is the bounded postcondition returned by a loopback identity handler.
type MutationEvidence struct {
	Changed     bool                `json:"changed"`
	Address     string              `json:"address"`
	Observation ExpectedObservation `json:"observation"`
}

// LoopbackIdentityHandler applies only the two loopback operations admitted by this protocol.
type LoopbackIdentityHandler interface {
	// EnsureLoopbackIdentity ensures the ticket's approved address and returns its observed postcondition.
	EnsureLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error)
	// ReleaseLoopbackIdentity releases the ticket's approved address and returns its observed postcondition.
	ReleaseLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error)
}

// UnavailableLoopbackIdentityHandler fails closed until platform mutation adapters are installed.
type UnavailableLoopbackIdentityHandler struct{}

// EnsureLoopbackIdentity rejects ensure operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) EnsureLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	return MutationEvidence{}, ErrMutationUnavailable
}

// ReleaseLoopbackIdentity rejects release operations because this seed contains no OS mutation authority.
func (UnavailableLoopbackIdentityHandler) ReleaseLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	return MutationEvidence{}, ErrMutationUnavailable
}

// ResponseError is the bounded structured error returned by the helper.
type ResponseError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// OperationResult records the admitted operation and its validated postcondition evidence.
type OperationResult struct {
	Operation Operation        `json:"operation"`
	Evidence  MutationEvidence `json:"evidence"`
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
	handler     LoopbackIdentityHandler
}

// NewDispatcher constructs a dispatcher whose dependencies must fail closed themselves.
func NewDispatcher(redeemer TicketRedeemer, clock Clock, replayGuard ReplayGuard, handler LoopbackIdentityHandler) *Dispatcher {
	if redeemer == nil {
		panic("helper.NewDispatcher requires a non-nil ticket redeemer")
	}
	if clock == nil {
		panic("helper.NewDispatcher requires a non-nil clock")
	}
	if replayGuard == nil {
		panic("helper.NewDispatcher requires a non-nil replay guard")
	}
	if handler == nil {
		panic("helper.NewDispatcher requires a non-nil loopback identity handler")
	}
	return &Dispatcher{redeemer: redeemer, clock: clock, replayGuard: replayGuard, handler: handler}
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

	var (
		evidence MutationEvidence
		err      error
	)
	switch ticket.Operation {
	case OperationEnsureLoopbackIdentity:
		evidence, err = d.handler.EnsureLoopbackIdentity(operationContext, ticket)
	case OperationReleaseLoopbackIdentity:
		evidence, err = d.handler.ReleaseLoopbackIdentity(operationContext, ticket)
	default:
		err = newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
	if err != nil {
		return responseForError(err), err
	}
	if err := evidence.validate(ticket); err != nil {
		return responseForError(err), err
	}

	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result: &OperationResult{
			Operation: ticket.Operation,
			Evidence:  evidence,
		},
	}, nil
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
