package resolver

import "fmt"

// ErrorKind classifies a resolver failure without exposing unbounded native diagnostics.
type ErrorKind string

const (
	// ErrorKindInvalidRequest means caller authority was zero, corrupt, or noncanonical.
	ErrorKindInvalidRequest ErrorKind = "invalid-request"
	// ErrorKindInvalidFacts means a backend returned malformed, unrelated, or unbounded facts.
	ErrorKindInvalidFacts ErrorKind = "invalid-facts"
	// ErrorKindObservationChanged means current facts no longer match the admitted fingerprint.
	ErrorKindObservationChanged ErrorKind = "observation-changed"
	// ErrorKindConflict means foreign or ambiguously owned rules prevent the requested effect.
	ErrorKindConflict ErrorKind = "resolver-conflict"
	// ErrorKindIndeterminate means incomplete native evidence cannot safely authorize a mutation.
	ErrorKindIndeterminate ErrorKind = "resolver-indeterminate"
	// ErrorKindObserveFailed means the native resolver state could not be observed.
	ErrorKindObserveFailed ErrorKind = "observe-failed"
	// ErrorKindMutationFailed means the native platform rejected or interrupted an exact effect.
	ErrorKindMutationFailed ErrorKind = "mutation-failed"
	// ErrorKindVerificationFailed means post-mutation facts did not prove the requested state.
	ErrorKindVerificationFailed ErrorKind = "verification-failed"
)

// Error is a typed, bounded failure from one resolver adapter operation.
type Error struct {
	Kind        ErrorKind
	Operation   string
	Assessment  Assessment
	Observation Observation
	cause       error
}

// Error formats a stable summary without incorporating native command or API output.
func (e *Error) Error() string {
	if e.Assessment.State != "" {
		return fmt.Sprintf("resolver %s: %s (%s/%s)", e.Operation, e.Kind, e.Assessment.State, e.Assessment.Owned)
	}
	return fmt.Sprintf("resolver %s: %s", e.Operation, e.Kind)
}

// Unwrap preserves the native or validation cause for programmatic diagnostics.
func (e *Error) Unwrap() error {
	return e.cause
}

// operationError constructs one typed failure while keeping its display representation bounded.
func operationError(kind ErrorKind, operation string, observation Observation, assessment Assessment, cause error) error {
	return &Error{
		Kind:        kind,
		Operation:   operation,
		Assessment:  assessment,
		Observation: cloneObservation(observation),
		cause:       cause,
	}
}
