package helper

import "errors"

// ErrorCode identifies a stable helper protocol failure.
type ErrorCode string

const (
	// ErrorCodeInvalidJSON indicates that the request was not one strict JSON object.
	ErrorCodeInvalidJSON ErrorCode = "invalid_json"
	// ErrorCodeRequestTooLarge indicates that the request exceeded the protocol bound.
	ErrorCodeRequestTooLarge ErrorCode = "request_too_large"
	// ErrorCodeInvalidTicket indicates that a ticket failed semantic validation.
	ErrorCodeInvalidTicket ErrorCode = "invalid_ticket"
	// ErrorCodeAuthenticationUnavailable indicates that no trusted ticket redemption mechanism is installed.
	ErrorCodeAuthenticationUnavailable ErrorCode = "authentication_unavailable"
	// ErrorCodeAuthenticationFailed indicates that a reference, ticket, or admission binding could not be authenticated.
	ErrorCodeAuthenticationFailed ErrorCode = "authentication_failed"
	// ErrorCodeReplayedTicket indicates that a ticket nonce was already consumed.
	ErrorCodeReplayedTicket ErrorCode = "replayed_ticket"
	// ErrorCodeReplayProtectionUnavailable indicates that durable replay admission is unavailable.
	ErrorCodeReplayProtectionUnavailable ErrorCode = "replay_protection_unavailable"
	// ErrorCodeMutationUnavailable indicates that no platform mutation implementation is installed.
	ErrorCodeMutationUnavailable ErrorCode = "mutation_unavailable"
	// ErrorCodeMutationFailed indicates that an admitted platform mutation failed safely.
	ErrorCodeMutationFailed ErrorCode = "mutation_failed"
)

var (
	// ErrReplay indicates that a replay guard has already consumed the ticket claim.
	ErrReplay = errors.New("helper ticket already consumed")
	// ErrTicketRedemptionUnavailable indicates that no trusted ticket redemption mechanism is installed.
	ErrTicketRedemptionUnavailable = errors.New("helper ticket redemption is unavailable")
	// ErrTicketRedemptionFailed indicates that a reference or its authenticated bindings were rejected.
	ErrTicketRedemptionFailed = errors.New("helper ticket redemption failed")
	// ErrTicketReferenceUnknown indicates that no authenticated ticket exists for a reference.
	ErrTicketReferenceUnknown = errors.New("helper ticket reference is unknown")
	// ErrTicketReferenceStale indicates that a reference can no longer be redeemed.
	ErrTicketReferenceStale = errors.New("helper ticket reference is stale")
	// ErrTicketReferenceRedeemed indicates that a single-use reference was already consumed.
	ErrTicketReferenceRedeemed = errors.New("helper ticket reference was already redeemed")
	// ErrReplayProtectionUnavailable indicates that dispatch cannot safely admit a ticket.
	ErrReplayProtectionUnavailable = errors.New("helper replay protection is unavailable")
	// ErrMutationUnavailable indicates that an operation has no installed platform handler.
	ErrMutationUnavailable = errors.New("helper platform mutation is unavailable")
)

// RequestError is a bounded protocol error safe to return to the invoking client.
type RequestError struct {
	Code    ErrorCode
	Message string
}

// Error returns the stable client-facing helper error message.
func (e *RequestError) Error() string {
	return e.Message
}

// newRequestError constructs a bounded error without exposing underlying host details.
func newRequestError(code ErrorCode, message string) *RequestError {
	return &RequestError{Code: code, Message: message}
}
