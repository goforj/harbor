package rpc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestNewWireErrorFromCauseNeverSerializesCause verifies filesystem paths,
// credentials, and other internal diagnostics remain daemon-local.
func TestNewWireErrorFromCauseNeverSerializesCause(t *testing.T) {
	const secret = "token=secret-value /Users/person/private/project"
	wireError := NewWireErrorFromCause(ErrorCodeInternal, errors.New(secret))
	encoded, err := json.Marshal(wireError)
	if err != nil {
		t.Fatalf("marshal wire error: %v", err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private/project") {
		t.Fatalf("wire error exposed cause: %s", encoded)
	}
}

// TestPrivilegedHelperWireErrorsDistinguishMissingFromUnsafeInstallation keeps desktop repair guidance specific without serializing local paths.
func TestPrivilegedHelperWireErrorsDistinguishMissingFromUnsafeInstallation(t *testing.T) {
	t.Parallel()

	const secret = "APP_KEY=secret /Users/person/private"
	missing := NewWireErrorFromCause(ErrorCodePrivilegedHelperRequired, errors.New(secret))
	unsafe := NewWireErrorFromCause(ErrorCodePrivilegedHelperUnsafe, errors.New(secret))
	if missing.Code == unsafe.Code || missing.Message == unsafe.Message {
		t.Fatalf("privileged helper errors are not distinct: missing %#v, unsafe %#v", missing, unsafe)
	}
	for _, wireError := range []WireError{missing, unsafe} {
		encoded, err := json.Marshal(wireError)
		if err != nil {
			t.Fatalf("marshal privileged helper error: %v", err)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "/Users/person/private") {
			t.Fatalf("privileged helper error exposed local cause: %s", encoded)
		}
	}
}

// TestWireErrorRejectsMultilineAndInvisibleMessages verifies a peer-facing error
// cannot forge adjacent log or UI lines with Unicode separators and controls.
func TestWireErrorRejectsMultilineAndInvisibleMessages(t *testing.T) {
	for _, message := range []string{
		"line one\nline two",
		"line one\u2028line two",
		"line one\u2029line two",
		"hidden\u2060format",
		string([]byte{0xff}),
	} {
		wireError := WireError{Code: ErrorCodeInternal, Message: message}
		if err := wireError.Validate(); err == nil {
			t.Fatalf("message %q accepted", message)
		}
	}
}

// TestWireErrorAcceptsUnknownAdditiveCode verifies older clients can surface a
// future machine-readable failure without silently reclassifying it.
func TestWireErrorAcceptsUnknownAdditiveCode(t *testing.T) {
	wireError := WireError{Code: "future_state", Message: "The operation needs a newer client."}
	if err := wireError.Validate(); err != nil {
		t.Fatalf("validate future code: %v", err)
	}
}

// TestNewNetworkObservationWireErrorAcceptsOnlyBoundedSingleLineDetail verifies the dynamic reviewed category fails closed.
func TestNewNetworkObservationWireErrorAcceptsOnlyBoundedSingleLineDetail(t *testing.T) {
	const reviewed = "Harbor could not inspect host conflicts for 127.77.10.8: observe Darwin host conflicts: route selection is unavailable."
	if got := NewNetworkObservationWireError(reviewed); got.Code != ErrorCodeNetworkObservationFailed || got.Message != reviewed || !got.Retryable {
		t.Fatalf("reviewed network observation error = %#v", got)
	}

	fallback := NewWireError(ErrorCodeNetworkObservationFailed)
	for _, unsafe := range []string{
		"",
		"host inspection failed\nAPP_KEY=secret",
		"host inspection failed\u2028APP_KEY=secret",
		"Harbor could not inspect host conflicts for 127.77.10.8: observe Darwin host conflicts: APP_KEY=secret-value",
		"Harbor could not inspect host conflicts for 127.78.10.8: observe Darwin host conflicts: route selection failed",
		"Harbor could not inspect host conflicts for 127.77.10.8: database unavailable",
		strings.Repeat("x", 257),
		string([]byte{0xff}),
	} {
		if got := NewNetworkObservationWireError(unsafe); got != fallback {
			t.Fatalf("unsafe network observation message %q produced %#v, want fallback %#v", unsafe, got, fallback)
		}
	}
}
