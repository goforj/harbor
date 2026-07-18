package loopback

import (
	"fmt"
	"net/netip"
)

// ErrorKind classifies a loopback operation failure without exposing command output.
type ErrorKind string

const (
	// ErrorKindInvalidAddress means a request escaped canonical IPv4 loopback addressing.
	ErrorKindInvalidAddress ErrorKind = "invalid-address"
	// ErrorKindLoopbackMissing means the platform did not report its required native loopback interface.
	ErrorKindLoopbackMissing ErrorKind = "loopback-missing"
	// ErrorKindLoopbackAmbiguous means the platform reported more than one native loopback candidate.
	ErrorKindLoopbackAmbiguous ErrorKind = "loopback-ambiguous"
	// ErrorKindInvalidFacts means platform facts violated the adapter's bounded contract.
	ErrorKindInvalidFacts ErrorKind = "invalid-facts"
	// ErrorKindObservationChanged means host facts no longer match the caller's admitted precondition.
	ErrorKindObservationChanged ErrorKind = "observation-changed"
	// ErrorKindConflict means an assignment is foreign, ambiguous, not a /32, or has incompatible attributes.
	ErrorKindConflict ErrorKind = "assignment-conflict"
	// ErrorKindObserveFailed means the operating system could not be observed safely.
	ErrorKindObserveFailed ErrorKind = "observe-failed"
	// ErrorKindMutationFailed means the operating system rejected an exact mutation.
	ErrorKindMutationFailed ErrorKind = "mutation-failed"
	// ErrorKindVerificationFailed means post-mutation host facts did not match the requested effect.
	ErrorKindVerificationFailed ErrorKind = "verification-failed"
)

// Error is a typed, bounded failure from a loopback operation.
type Error struct {
	Kind        ErrorKind
	Operation   string
	Address     netip.Addr
	State       State
	Observation Observation
	cause       error
}

// Error formats a stable summary without incorporating unbounded platform output.
func (e *Error) Error() string {
	switch {
	case e.Address.IsValid() && e.State != "":
		return fmt.Sprintf("loopback %s %s: %s (%s)", e.Operation, e.Address, e.Kind, e.State)
	case e.Address.IsValid():
		return fmt.Sprintf("loopback %s %s: %s", e.Operation, e.Address, e.Kind)
	default:
		return fmt.Sprintf("loopback %s: %s", e.Operation, e.Kind)
	}
}

// Unwrap preserves the platform cause for programmatic diagnostics.
func (e *Error) Unwrap() error {
	return e.cause
}

// operationError constructs a typed failure while keeping its display bounded.
func operationError(kind ErrorKind, operation string, address netip.Addr, state State, observation Observation, cause error) error {
	return &Error{
		Kind:        kind,
		Operation:   operation,
		Address:     address,
		State:       state,
		Observation: observation,
		cause:       cause,
	}
}
