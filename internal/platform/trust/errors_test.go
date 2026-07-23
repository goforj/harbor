package trust

import (
	"errors"
	"testing"
)

// TestErrorAdministratorTrustDiagnosticExposesOnlyReviewedNativeStatus verifies adapter errors retain no display-derived diagnostic detail.
func TestErrorAdministratorTrustDiagnosticExposesOnlyReviewedNativeStatus(t *testing.T) {
	mutationErr := operationError(
		ErrorKindMutationFailed,
		"ensure",
		Observation{},
		Assessment{},
		newAdministratorTrustStatusError("owner-record", -25299),
	)
	trustError, ok := mutationErr.(*Error)
	if !ok {
		t.Fatalf("operationError() type = %T", mutationErr)
	}
	stage, status, ok := trustError.AdministratorTrustDiagnostic()
	if !ok || stage != "owner-record" || status != -25299 {
		t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, %t", stage, status, ok)
	}

	observationErr := operationError(
		ErrorKindObserveFailed,
		"observe",
		Observation{},
		Assessment{},
		newAdministratorTrustStatusError("snapshot", -25291),
	)
	trustError, ok = observationErr.(*Error)
	if !ok {
		t.Fatalf("operationError() type = %T", observationErr)
	}
	stage, status, ok = trustError.AdministratorTrustDiagnostic()
	if !ok || stage != "snapshot" || status != -25291 {
		t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, %t", stage, status, ok)
	}

	verificationErr := operationError(
		ErrorKindVerificationFailed,
		"ensure",
		Observation{},
		Assessment{},
		observationErr,
	)
	trustError, ok = verificationErr.(*Error)
	if !ok {
		t.Fatalf("operationError() type = %T", verificationErr)
	}
	stage, status, ok = trustError.AdministratorTrustDiagnostic()
	if !ok || stage != "snapshot" || status != -25291 {
		t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, %t", stage, status, ok)
	}

	for _, err := range []*Error{
		{Kind: ErrorKindMutationFailed, Operation: "ensure", cause: errors.New("/private/keychain")},
		{Kind: ErrorKindMutationFailed, Operation: "release", cause: newAdministratorTrustStatusError("set-root", -25299)},
		{Kind: ErrorKindObserveFailed, Operation: "release", cause: newAdministratorTrustStatusError("snapshot", -25291)},
		{Kind: ErrorKindConflict, Operation: "ensure", cause: newAdministratorTrustStatusError("owner-observe", -25291)},
		{Kind: ErrorKindMutationFailed, Operation: "ensure", cause: newAdministratorTrustStatusError("forged-stage", -25299)},
	} {
		if stage, status, ok := err.AdministratorTrustDiagnostic(); ok {
			t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, true", stage, status)
		}
	}
}

// TestAdministratorTrustDiagnosticAcceptsRootStoreStages keeps root-store failures available to the elevated helper.
func TestAdministratorTrustDiagnosticAcceptsRootStoreStages(t *testing.T) {
	for _, stage := range []string{
		"root-store-recheck",
		"root-store-verify",
		"add-system-root",
	} {
		t.Run(stage, func(t *testing.T) {
			err := &Error{
				Kind:      ErrorKindMutationFailed,
				Operation: "ensure",
				cause:     newAdministratorTrustStatusError(stage, -25299),
			}
			gotStage, status, ok := err.AdministratorTrustDiagnostic()
			if !ok || gotStage != stage || status != -25299 {
				t.Fatalf("AdministratorTrustDiagnostic() = %q, %d, %t", gotStage, status, ok)
			}
		})
	}
}
