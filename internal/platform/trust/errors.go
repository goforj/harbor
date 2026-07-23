package trust

import (
	"errors"
	"fmt"
)

var (
	// ErrUnavailable reports that the current build has no reviewed native trust-store adapter.
	ErrUnavailable = errors.New("native trust-store adapter is unavailable")
	// errNativeObservationChanged reports a successful native recheck that no longer matches admitted facts.
	errNativeObservationChanged = errors.New("native trust observation changed before mutation")
	// errNativeMutationConflict reports facts that cannot identify one exact safe native effect.
	errNativeMutationConflict = errors.New("native trust mutation conflicts with current facts")
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

// AdministratorTrustDiagnostic returns a reviewed administrator observation, ensure, or release failure, when this error wraps one.
func (err *Error) AdministratorTrustDiagnostic() (string, int, bool) {
	if err == nil || !administratorTrustDiagnosticBoundary(err.Kind, err.Operation) {
		return "", 0, false
	}
	var cause *administratorTrustStatusError
	if !errors.As(err.cause, &cause) || !validAdministratorTrustStage(cause.stage) ||
		(err.Operation == "release" && cause.stage != "release-remove") {
		return "", 0, false
	}
	return cause.stage, cause.status, true
}

// administratorTrustDiagnosticBoundary limits native diagnostics to trust observations and the reviewed mutation paths that consume them.
func administratorTrustDiagnosticBoundary(kind ErrorKind, operation string) bool {
	switch operation {
	case "observe":
		return kind == ErrorKindObserveFailed
	case "ensure":
		return kind == ErrorKindMutationFailed || kind == ErrorKindVerificationFailed
	case "release":
		return kind == ErrorKindMutationFailed
	default:
		return false
	}
}

// administratorTrustStatusError retains only reviewed native status facts until the trust adapter classifies them.
type administratorTrustStatusError struct {
	stage  string
	status int
}

// Error returns a fixed local summary so native detail remains available only through the typed accessor.
func (err *administratorTrustStatusError) Error() string {
	return "administrator trust native mutation failed"
}

// newAdministratorTrustStatusError binds a native status to one reviewed administrator trust step.
func newAdministratorTrustStatusError(stage string, status int) error {
	return &administratorTrustStatusError{
		stage:  stage,
		status: status,
	}
}

// validAdministratorTrustStage rejects arbitrary native-operation labels from diagnostic propagation.
func validAdministratorTrustStage(stage string) bool {
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
		"set-root",
		"release-remove":
		return true
	default:
		return false
	}
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
