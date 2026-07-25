package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestResolverDiagnosticClassifiesOnlyFiniteNativeCauses verifies raw platform text cannot cross the helper boundary.
func TestResolverDiagnosticClassifiesOnlyFiniteNativeCauses(t *testing.T) {
	tests := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "none"},
		{name: "deadline", cause: context.DeadlineExceeded, want: "deadline"},
		{name: "canceled", cause: context.Canceled, want: "canceled"},
		{name: "module unavailable", cause: errors.New("Import-Module: The specified module 'DnsClient' was not loaded"), want: "module-unavailable"},
		{name: "cmdlet unavailable", cause: errors.New("Get-DnsClientNrptRule is not recognized"), want: "cmdlet-unavailable"},
		{name: "invalid output", cause: errors.New("decode Windows NRPT snapshot: unexpected end of JSON input"), want: "invalid-output"},
		{name: "unexpected diagnostics", cause: errors.New("Windows NRPT PowerShell wrote unexpected diagnostics"), want: "unexpected-diagnostics"},
		{name: "count precondition", cause: errors.New("NRPT relevant rule count changed before mutation"), want: "precondition-changed"},
		{name: "set precondition", cause: errors.New("NRPT relevant rule set changed before mutation"), want: "precondition-changed"},
		{name: "disabled feature parameters", cause: errors.New("DNSSEC is not configured on the rule"), want: "disabled-feature-parameters"},
		{name: "access denied", cause: errors.New("Access is denied"), want: "access-denied"},
		{name: "other native detail", cause: errors.New("private host detail"), want: "native-failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolverNativeDiagnostic(test.cause); got != test.want {
				t.Fatalf("resolverNativeDiagnostic() = %q, want %q", got, test.want)
			}
		})
	}

	failure := &Error{
		Operation: "ensure",
		Kind:      ErrorKindMutationFailed,
		Assessment: Assessment{
			State: StateAbsent,
			Owned: OwnedStateAbsent,
		},
		cause: context.DeadlineExceeded,
	}
	operation, kind, state, owned, native := failure.ResolverDiagnostic()
	if operation != "ensure" ||
		kind != "mutation-failed" ||
		state != "absent" ||
		owned != "absent" ||
		native != "deadline" {
		t.Fatalf(
			"ResolverDiagnostic() = %q/%q/%q/%q/%q",
			operation,
			kind,
			state,
			owned,
			native,
		)
	}
}

// TestResolverErrorIncludesOnlyFiniteNativeCategories verifies local diagnostics stay useful and bounded.
func TestResolverErrorIncludesOnlyFiniteNativeCategories(t *testing.T) {
	failure := &Error{
		Operation: "observe",
		Kind:      ErrorKindObserveFailed,
		cause:     errors.New("Import-Module: The specified module 'DnsClient' was not loaded from C:\\private"),
	}
	if got, want := failure.Error(), "resolver observe: observe-failed (module-unavailable)"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(failure.Error(), "C:\\private") {
		t.Fatalf("Error() exposed native cause: %q", failure.Error())
	}
}
