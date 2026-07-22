package trust

import (
	"errors"
	"testing"
)

// TestErrorAdministratorTrustDiagnosticExposesOnlyReviewedNativeStatus verifies adapter errors retain no display-derived diagnostic detail.
func TestErrorAdministratorTrustDiagnosticExposesOnlyReviewedNativeStatus(t *testing.T) {
	err := operationError(
		ErrorKindMutationFailed,
		"ensure",
		Observation{},
		Assessment{},
		newAdministratorTrustStatusError("owner-record", -25299),
	)
	trustError, ok := err.(*Error)
	if !ok {
		t.Fatalf("operationError() type = %T", err)
	}
	stage, status, ok := trustError.AdministratorTrustDiagnostic()
	if !ok || stage != "owner-record" || status != -25299 {
		t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, %t", stage, status, ok)
	}

	for _, err := range []*Error{
		{Kind: ErrorKindMutationFailed, Operation: "ensure", cause: errors.New("/private/keychain")},
		{Kind: ErrorKindMutationFailed, Operation: "release", cause: newAdministratorTrustStatusError("set-root", -25299)},
		{Kind: ErrorKindMutationFailed, Operation: "ensure", cause: newAdministratorTrustStatusError("forged-stage", -25299)},
	} {
		if stage, status, ok := err.AdministratorTrustDiagnostic(); ok {
			t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, true", stage, status)
		}
	}
}
