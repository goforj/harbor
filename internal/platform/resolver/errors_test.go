package resolver

import (
	"context"
	"errors"
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
