package projectprocess

import (
	"runtime"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestObservePersistedOutputBrokerProcessEvidenceRequiresStableNativeIdentity proves restart adoption rereads more than a durable PID.
func TestObservePersistedOutputBrokerProcessEvidenceRequiresStableNativeIdentity(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("reviewed persisted process identity readers are Unix-only")
	}
	captured, err := CaptureCurrentProcessEvidence()
	if err != nil {
		t.Fatalf("CaptureCurrentProcessEvidence() error = %v", err)
	}
	expected := captured
	observed, err := ObservePersistedOutputBrokerProcessEvidence(expected)
	if err != nil {
		t.Fatalf("ObservePersistedOutputBrokerProcessEvidence() error = %v", err)
	}
	if observed != expected {
		t.Fatalf("observed process evidence = %#v, want %#v", observed, expected)
	}
	mutated := expected
	mutated.ArgumentDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ObservePersistedOutputBrokerProcessEvidence(mutated); err == nil {
		t.Fatal("ObservePersistedOutputBrokerProcessEvidence() accepted argument-digest drift")
	}
	if _, err := ObservePersistedOutputBrokerProcessEvidence(domain.ProcessEvidence{PID: expected.PID}); err == nil {
		t.Fatal("ObservePersistedOutputBrokerProcessEvidence() accepted incomplete evidence")
	}
}
