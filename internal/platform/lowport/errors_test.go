package lowport

import "testing"

// TestErrorLowPortDiagnosticExposesOnlyFiniteClassifiers pins the helper-facing diagnostic boundary.
func TestErrorLowPortDiagnosticExposesOnlyFiniteClassifiers(t *testing.T) {
	err := &Error{
		Action: "ensure",
		Kind:   ErrorKindVerificationFailed,
		cause:  testNativeError("native detail must remain private"),
	}
	action, kind := err.LowPortDiagnostic()
	if action != "ensure" || kind != "verification-failed" {
		t.Fatalf("LowPortDiagnostic() = %q, %q", action, kind)
	}

	var absent *Error
	action, kind = absent.LowPortDiagnostic()
	if action != "" || kind != "" {
		t.Fatalf("nil LowPortDiagnostic() = %q, %q", action, kind)
	}
}

// testNativeError supplies private detail that must not cross the diagnostic boundary.
type testNativeError string

// Error implements error for the retained native-detail fixture.
func (err testNativeError) Error() string {
	return string(err)
}
