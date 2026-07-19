package projectprocess

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestObservePriorProcessClassifiesMatchingAndReusedBirths proves PID reuse cannot inherit persisted authority.
func TestObservePriorProcessClassifiesMatchingAndReusedBirths(t *testing.T) {
	birthToken, err := processBirthToken(os.Getpid())
	if err != nil {
		t.Fatalf("read current process birth token: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("read current executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("canonicalize current executable: %v", err)
	}
	evidence := domain.ProcessEvidence{
		PID:                int64(os.Getpid()),
		BirthToken:         birthToken,
		ExecutableIdentity: filepath.Clean(executable),
		ArgumentDigest:     strings.Repeat("0", 64),
	}
	supervisor := New(Options{})

	matched, err := supervisor.ObservePriorProcess(t.Context(), evidence)
	if err != nil {
		t.Fatalf("ObservePriorProcess(match) error = %v", err)
	}
	if matched.State != PriorProcessPresent {
		t.Fatalf("ObservePriorProcess(match) = %#v", matched)
	}

	evidence.BirthToken += "-different"
	replaced, err := supervisor.ObservePriorProcess(t.Context(), evidence)
	if err != nil {
		t.Fatalf("ObservePriorProcess(replaced) error = %v", err)
	}
	if replaced.State != PriorProcessReplaced {
		t.Fatalf("ObservePriorProcess(replaced) = %#v", replaced)
	}
}

// TestObservePriorProcessClassifiesAnUnusedPIDAsAbsent proves restart recovery can retire vanished births.
func TestObservePriorProcessClassifiesAnUnusedPIDAsAbsent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("read current executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("canonicalize current executable: %v", err)
	}
	evidence := domain.ProcessEvidence{
		PID:                math.MaxInt32,
		BirthToken:         "unreachable-process-birth",
		ExecutableIdentity: filepath.Clean(executable),
		ArgumentDigest:     strings.Repeat("0", 64),
	}
	observation, err := New(Options{}).ObservePriorProcess(t.Context(), evidence)
	if err != nil {
		t.Fatalf("ObservePriorProcess(absent) error = %v", err)
	}
	if observation.State != PriorProcessAbsent {
		t.Fatalf("ObservePriorProcess(absent) = %#v", observation)
	}
}

// TestObservePriorProcessRejectsCancellationAndInvalidEvidence keeps recovery fail-closed before host observation.
func TestObservePriorProcessRejectsCancellationAndInvalidEvidence(t *testing.T) {
	supervisor := New(Options{})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := supervisor.ObservePriorProcess(canceled, domain.ProcessEvidence{}); err == nil {
		t.Fatal("ObservePriorProcess(canceled) error = nil")
	}
	if _, err := supervisor.ObservePriorProcess(t.Context(), domain.ProcessEvidence{}); err == nil {
		t.Fatal("ObservePriorProcess(invalid) error = nil")
	}
}
