package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

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

// Error formats a stable summary using only finite categories derived from native failures.
func (e *Error) Error() string {
	message := fmt.Sprintf("resolver %s: %s", e.Operation, e.Kind)
	if e.Assessment.State != "" {
		message += fmt.Sprintf(" (%s/%s)", e.Assessment.State, e.Assessment.Owned)
	}
	if native := resolverNativeDiagnostic(e.cause); native != "" {
		message += " (" + native + ")"
	}
	return message
}

// Unwrap preserves the native or validation cause for programmatic diagnostics.
func (e *Error) Unwrap() error {
	return e.cause
}

// ResolverDiagnostic returns only finite adapter and native categories that are
// safe to cross the privileged helper response boundary.
func (e *Error) ResolverDiagnostic() (string, string, string, string, string) {
	return e.Operation,
		string(e.Kind),
		string(e.Assessment.State),
		string(e.Assessment.Owned),
		resolverNativeDiagnostic(e.cause)
}

// resolverNativeDiagnostic classifies native causes without exposing their host-specific text.
func resolverNativeDiagnostic(cause error) string {
	if cause == nil {
		return ""
	}
	message := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(message, "harbor-stage=module-import"):
		return "module-import"
	case strings.Contains(message, "harbor-stage=input"):
		return "input-stage"
	case strings.Contains(message, "harbor-stage=parse"):
		return "parse-stage"
	case strings.Contains(message, "harbor-stage=validation"):
		return "validation-stage"
	case strings.Contains(message, "harbor-stage=enumerate"):
		return "enumeration-stage"
	case strings.Contains(message, "harbor-stage=output"):
		return "output-stage"
	case strings.Contains(message, "harbor-stage=precondition"):
		return "precondition-stage"
	case strings.Contains(message, "harbor-stage=mutation"):
		return "mutation-stage"
	case strings.Contains(message, "harbor-progress=enumerating"):
		return "enumeration-timeout"
	case strings.Contains(message, "harbor-progress=module-imported"):
		return "post-import-timeout"
	case strings.Contains(message, "harbor-progress=importing-dnsclient"):
		return "dnsclient-import-timeout"
	case strings.Contains(message, "harbor-progress=importing-cimcmdlets"):
		return "cimcmdlets-import-timeout"
	case strings.Contains(message, "harbor-progress=importing-utility"):
		return "utility-import-timeout"
	case strings.Contains(message, "harbor-progress=host-started"):
		return "program-start-timeout"
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(cause, context.Canceled):
		return "canceled"
	}
	switch {
	case strings.Contains(message, "locate windows powershell system directory"):
		return "system-directory"
	case strings.Contains(message, "windows nrpt output exceeds"):
		return "output-limit"
	case strings.Contains(message, "windows nrpt diagnostic exceeds"):
		return "diagnostic-limit"
	case strings.Contains(message, "windows nrpt powershell progress sequence is invalid"):
		return "progress-invalid"
	case strings.Contains(message, "execute windows nrpt powershell") &&
		strings.Contains(message, "exit status"):
		return "process-exit"
	case strings.Contains(message, "execute windows nrpt powershell"):
		return "process-start"
	case strings.Contains(message, "specified module 'dnsclient' was not loaded") ||
		strings.Contains(message, "specified module \"dnsclient\" was not loaded") ||
		(strings.Contains(message, "import-module") && strings.Contains(message, "dnsclient")):
		return "module-unavailable"
	case strings.Contains(message, "get-dnsclientnrptrule") &&
		strings.Contains(message, "not recognized"):
		return "cmdlet-unavailable"
	case strings.Contains(message, "decode windows nrpt snapshot") ||
		strings.Contains(message, "snapshot has an invalid size") ||
		strings.Contains(message, "response must contain"):
		return "invalid-output"
	case strings.Contains(message, "wrote unexpected diagnostics"):
		return "unexpected-diagnostics"
	case strings.Contains(message, "nrpt relevant rule count changed before mutation") ||
		strings.Contains(message, "nrpt relevant rule set changed before mutation"):
		return "precondition-changed"
	case strings.Contains(message, "dnssec is not configured on the rule"):
		return "disabled-feature-parameters"
	case strings.Contains(message, "access is denied"):
		return "access-denied"
	default:
		return "native-failure"
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
