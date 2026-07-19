package rpc

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maximumNetworkObservationDetailBytes = 160

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
	// ErrorCodeNetworkObservationFailed reports that Harbor could not inspect one candidate host address.
	ErrorCodeNetworkObservationFailed ErrorCode = "network_observation_failed"
	// ErrorCodePrivilegedHelperRequired reports absent installer-owned privileged networking support.
	ErrorCodePrivilegedHelperRequired ErrorCode = "privileged_helper_required"
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

// NewNetworkObservationWireError uses reviewed dynamic detail only when it satisfies the wire boundary.
func NewNetworkObservationWireError(message string) WireError {
	wireError := NewWireError(ErrorCodeNetworkObservationFailed)
	if !validNetworkObservationMessage(message) {
		return wireError
	}
	wireError.Message = message
	if err := wireError.Validate(); err != nil {
		return NewWireError(ErrorCodeNetworkObservationFailed)
	}

	return wireError
}

// validNetworkObservationMessage accepts only the two reviewed setup-observer message grammars.
func validNetworkObservationMessage(message string) bool {
	fallback := NewWireError(ErrorCodeNetworkObservationFailed).Message
	if message == fallback {
		return true
	}
	stage := ""
	remainder := ""
	for _, candidate := range []string{"loopback assignment", "host conflicts"} {
		prefix := "Harbor could not inspect " + candidate + " for "
		if strings.HasPrefix(message, prefix) {
			stage = candidate
			remainder = strings.TrimPrefix(message, prefix)
			break
		}
	}
	separator := strings.Index(remainder, ": ")
	if stage == "" || separator <= 0 {
		return false
	}
	addressText := remainder[:separator]
	address, err := netip.ParseAddr(addressText)
	if err != nil || address.String() != addressText || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return false
	}
	octets := address.As4()
	if octets[0] != 127 || octets[1] != 77 {
		return false
	}
	detail := remainder[separator+2:]
	if !validNetworkObservationDetail(detail) {
		return false
	}
	if stage == "loopback assignment" {
		return strings.HasPrefix(detail, "loopback observe "+addressText+": ")
	}

	return strings.HasPrefix(detail, "observe Darwin host conflicts: ") ||
		strings.HasPrefix(detail, "observe Linux host conflicts: ") ||
		strings.HasPrefix(detail, "observe Windows host conflicts: ")
}

// validNetworkObservationDetail rejects unsafe text even when a caller directly invokes the reviewed constructor.
func validNetworkObservationDetail(detail string) bool {
	if detail == "" || len(detail) > maximumNetworkObservationDetailBytes || strings.TrimSpace(detail) != detail || !utf8.ValidString(detail) {
		return false
	}
	lowerDetail := strings.ToLower(detail)
	for _, sensitive := range []string{"app_key", "authorization", "credential", "password", "private key", "secret", "token="} {
		if strings.Contains(lowerDetail, sensitive) {
			return false
		}
	}
	for _, character := range detail {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}

	return true
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
	ErrorCodeNetworkObservationFailed: {
		message:   "Harbor could not inspect host networking. Check the daemon log for details.",
		retryable: true,
	},
	ErrorCodePrivilegedHelperRequired: {
		message: "Harbor's privileged networking support is missing. Harbor must install or repair it before setup can finish.",
	},
	ErrorCodeInternal: {message: "Harbor could not complete the request."},
}
