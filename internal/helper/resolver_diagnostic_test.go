package helper

import (
	"errors"
	"testing"
)

// testResolverFailureDiagnostic supplies finite adapter facts without importing the helper-dependent resolver package.
type testResolverFailureDiagnostic struct {
	operation string
	kind      string
	state     string
	owned     string
	native    string
}

// Error implements the error boundary used by responseForError.
func (diagnostic testResolverFailureDiagnostic) Error() string {
	return "resolver diagnostic"
}

// ResolverDiagnostic exposes the test's caller-selected finite categories.
func (diagnostic testResolverFailureDiagnostic) ResolverDiagnostic() (string, string, string, string, string) {
	return diagnostic.operation, diagnostic.kind, diagnostic.state, diagnostic.owned, diagnostic.native
}

// TestResponseForResolverFailureExposesOnlyFiniteDiagnostics verifies useful native categories do not admit arbitrary text.
func TestResponseForResolverFailureExposesOnlyFiniteDiagnostics(t *testing.T) {
	accepted := testResolverFailureDiagnostic{
		operation: "ensure",
		kind:      "mutation-failed",
		state:     "absent",
		owned:     "absent",
		native:    "deadline",
	}
	for _, err := range []error{
		accepted,
		errors.Join(errors.New("outer wrapper"), accepted),
	} {
		response := responseForError(err)
		want := "helper operation failed: resolver ensure mutation-failed absent/absent deadline"
		if response.Error == nil || response.Error.Code != ErrorCodeMutationFailed || response.Error.Message != want {
			t.Fatalf("responseForError(%v) = %#v, want %q", err, response, want)
		}
	}

	for _, native := range []string{
		"module-import",
		"input-stage",
		"parse-stage",
		"validation-stage",
		"enumeration-stage",
		"output-stage",
		"precondition-stage",
		"mutation-stage",
		"enumeration-timeout",
		"post-import-timeout",
		"dnsclient-import-timeout",
		"cimcmdlets-import-timeout",
		"utility-import-timeout",
		"program-start-timeout",
		"system-directory",
		"system-environment",
		"output-limit",
		"diagnostic-limit",
		"progress-invalid",
		"process-exit",
		"process-start",
		"module-unavailable",
		"cmdlet-unavailable",
		"invalid-output",
		"unexpected-diagnostics",
	} {
		diagnostic := accepted
		diagnostic.native = native
		response := responseForError(diagnostic)
		want := "helper operation failed: resolver ensure mutation-failed absent/absent " + native
		if response.Error == nil || response.Error.Message != want {
			t.Fatalf("responseForError(%q) = %#v, want %q", native, response, want)
		}
	}

	for _, forged := range []testResolverFailureDiagnostic{
		{operation: "forged", kind: "mutation-failed"},
		{operation: "observe", kind: "mutation-failed"},
		{operation: "ensure", kind: "forged"},
		{operation: "ensure", kind: "mutation-failed", state: "absent"},
		{operation: "ensure", kind: "mutation-failed", state: "forged", owned: "absent"},
		{operation: "ensure", kind: "mutation-failed", native: "raw host detail"},
	} {
		response := responseForError(forged)
		if response.Error == nil || response.Error.Message != "helper operation failed" {
			t.Fatalf("responseForError(%v) = %#v, want generic failure", forged, response)
		}
	}
}
