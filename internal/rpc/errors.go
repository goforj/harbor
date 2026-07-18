package rpc

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ErrorCode identifies a stable machine-readable failure category.
type ErrorCode string

const (
	// ErrorCodeInvalidHandshake reports malformed negotiation input.
	ErrorCodeInvalidHandshake ErrorCode = "invalid_handshake"
	// ErrorCodeUnsupportedProtocol reports that peer protocol ranges do not overlap.
	ErrorCodeUnsupportedProtocol ErrorCode = "unsupported_protocol"
	// ErrorCodeInvalidRequest reports an invalid bounded domain request.
	ErrorCodeInvalidRequest ErrorCode = "invalid_request"
	// ErrorCodeDeadlineExceeded reports that request work exceeded its deadline.
	ErrorCodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	// ErrorCodeCancelled reports explicit client cancellation.
	ErrorCodeCancelled ErrorCode = "cancelled"
	// ErrorCodeNotFound reports that a requested domain object does not exist.
	ErrorCodeNotFound ErrorCode = "not_found"
	// ErrorCodeConflict reports that current state prevents the requested transition.
	ErrorCodeConflict ErrorCode = "conflict"
	// ErrorCodePermissionDenied reports that the authenticated peer lacks authority.
	ErrorCodePermissionDenied ErrorCode = "permission_denied"
	// ErrorCodeUnavailable reports a temporary daemon or dependency outage.
	ErrorCodeUnavailable ErrorCode = "unavailable"
	// ErrorCodeInternal reports an unexpected daemon failure without exposing its cause.
	ErrorCodeInternal ErrorCode = "internal"
)

// WireError is the redaction-safe error shape sent to an IPC peer. Diagnostic
// causes stay in daemon-owned logs and are deliberately absent from this type.
type WireError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
}

// Error returns the safe peer-facing message.
func (e WireError) Error() string {
	return e.Message
}

// Validate accepts future additive error codes while keeping the stable shape
// bounded and safe to display as one line.
func (e WireError) Validate() error {
	if err := validateWireToken("error code", string(e.Code), maxCapabilityLength); err != nil {
		return err
	}
	if e.Message == "" {
		return fmt.Errorf("error message is required")
	}
	if len(e.Message) > 256 {
		return fmt.Errorf("error message exceeds 256 bytes")
	}
	if !utf8.ValidString(e.Message) {
		return fmt.Errorf("error message is not valid UTF-8")
	}
	for _, character := range e.Message {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return fmt.Errorf("error message contains a control character")
		}
	}

	return nil
}

// NewWireError creates a failure using only reviewed, redaction-safe text.
func NewWireError(code ErrorCode) WireError {
	specification, ok := wireErrorSpecifications[code]
	if !ok {
		code = ErrorCodeInternal
		specification = wireErrorSpecifications[code]
	}

	return WireError{
		Code:      code,
		Message:   specification.message,
		Retryable: specification.retryable,
	}
}

// NewWireErrorFromCause converts an internal failure without copying its text
// across the IPC boundary.
func NewWireErrorFromCause(code ErrorCode, cause error) WireError {
	_ = cause

	return NewWireError(code)
}

// wireErrorSpecification records reviewed text and retry behavior for an error code.
type wireErrorSpecification struct {
	message   string
	retryable bool
}

var wireErrorSpecifications = map[ErrorCode]wireErrorSpecification{
	ErrorCodeInvalidHandshake:    {message: "The connection handshake is invalid."},
	ErrorCodeUnsupportedProtocol: {message: "Upgrade Harbor or this client so their protocol ranges overlap."},
	ErrorCodeInvalidRequest:      {message: "The request is invalid."},
	ErrorCodeDeadlineExceeded:    {message: "The request deadline was exceeded.", retryable: true},
	ErrorCodeCancelled:           {message: "The request was cancelled."},
	ErrorCodeNotFound:            {message: "The requested item was not found."},
	ErrorCodeConflict:            {message: "The request conflicts with current state."},
	ErrorCodePermissionDenied:    {message: "The request is not permitted."},
	ErrorCodeUnavailable:         {message: "Harbor is temporarily unavailable.", retryable: true},
	ErrorCodeInternal:            {message: "Harbor could not complete the request."},
}
