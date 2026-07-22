package trust

import (
	"errors"
	"fmt"
)

var (
	// ErrUnavailable reports that the current build has no reviewed native trust-store adapter.
	ErrUnavailable = errors.New("native trust-store adapter is unavailable")
)

// ErrorKind classifies a trust failure without exposing unbounded native diagnostics.
type ErrorKind string

const (
	// ErrorKindInvalidRequest means caller authority was zero, corrupt, or noncanonical.
	ErrorKindInvalidRequest ErrorKind = "invalid-request"
	// ErrorKindInvalidFacts means a backend returned malformed or unrelated facts.
	ErrorKindInvalidFacts ErrorKind = "invalid-facts"
	// ErrorKindObservationChanged means current facts no longer match admitted CAS evidence.
	ErrorKindObservationChanged ErrorKind = "observation-changed"
	// ErrorKindConflict means foreign or ambiguously owned entries prevent the requested effect.
	ErrorKindConflict ErrorKind = "trust-conflict"
	// ErrorKindIndeterminate means incomplete native evidence cannot safely authorize a mutation.
	ErrorKindIndeterminate ErrorKind = "trust-indeterminate"
	// ErrorKindObserveFailed means the native trust store could not be observed.
	ErrorKindObserveFailed ErrorKind = "observe-failed"
	// ErrorKindMutationFailed means the native platform rejected or interrupted an exact effect.
	ErrorKindMutationFailed ErrorKind = "mutation-failed"
	// ErrorKindVerificationFailed means post-mutation facts did not prove the requested state.
	ErrorKindVerificationFailed ErrorKind = "verification-failed"
)

// Error is a typed, bounded failure from one trust adapter operation.
type Error struct {
	Kind        ErrorKind
	Operation   string
	Assessment  Assessment
	Observation Observation
	cause       error
}

// Error formats a stable summary without incorporating native command or API output.
func (err *Error) Error() string {
	if err.Assessment.State != "" {
		return fmt.Sprintf("trust %s: %s (%s/%s)", err.Operation, err.Kind, err.Assessment.State, err.Assessment.Owned)
	}
	return fmt.Sprintf("trust %s: %s", err.Operation, err.Kind)
}

// Unwrap preserves the native or validation cause for programmatic diagnostics.
func (err *Error) Unwrap() error {
	return err.cause
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
