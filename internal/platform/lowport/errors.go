package lowport

import "fmt"

// ErrorKind classifies bounded low-port adapter failures.
type ErrorKind string

const (
	// ErrorKindUnavailable means no reviewed native adapter exists on this platform.
	ErrorKindUnavailable ErrorKind = "unavailable"
	// ErrorKindInvalidRequest means immutable authority was malformed.
	ErrorKindInvalidRequest ErrorKind = "invalid-request"
	// ErrorKindInvalidFacts means a backend emitted malformed or unrelated native facts.
	ErrorKindInvalidFacts ErrorKind = "invalid-facts"
	// ErrorKindObserveFailed means native state could not be safely observed.
	ErrorKindObserveFailed ErrorKind = "observe-failed"
	// ErrorKindObservationChanged means compare-and-swap evidence is stale.
	ErrorKindObservationChanged ErrorKind = "observation-changed"
	// ErrorKindConflict means foreign, ambiguous, or malformed state blocks mutation.
	ErrorKindConflict ErrorKind = "conflict"
	// ErrorKindIndeterminate means incomplete native facts cannot safely authorize a mutation.
	ErrorKindIndeterminate ErrorKind = "indeterminate"
	// ErrorKindMutationFailed means the exact native operation failed.
	ErrorKindMutationFailed ErrorKind = "mutation-failed"
	// ErrorKindVerificationFailed means post-mutation observation did not prove the required state.
	ErrorKindVerificationFailed ErrorKind = "verification-failed"
)

// Error is a typed bounded failure from the low-port adapter.
type Error struct {
	Kind   ErrorKind
	Action string
	cause  error
}

// Error returns a stable summary without native command output.
func (e *Error) Error() string { return fmt.Sprintf("low-port %s: %s", e.Action, e.Kind) }

// Unwrap returns the retained diagnostic cause.
func (e *Error) Unwrap() error { return e.cause }

// LowPortDiagnostic returns only the adapter's finite action and failure classifiers.
func (e *Error) LowPortDiagnostic() (string, string) {
	if e == nil {
		return "", ""
	}
	return e.Action, string(e.Kind)
}

// operationError constructs a bounded adapter error.
func operationError(kind ErrorKind, action string, cause error) error {
	return &Error{Kind: kind, Action: action, cause: cause}
}
