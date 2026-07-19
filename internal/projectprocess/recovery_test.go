package projectprocess

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestSettlePriorProcessAcceptsAlreadySettledBirths proves absent and reused PIDs never receive a signal.
func TestSettlePriorProcessAcceptsAlreadySettledBirths(t *testing.T) {
	tests := []struct {
		name        string
		observed    string
		present     bool
		wantOutcome PriorProcessSettlementOutcome
	}{
		{name: "absent", present: false, wantOutcome: PriorProcessSettlementAbsent},
		{name: "replaced", observed: "different-birth", present: true, wantOutcome: PriorProcessSettlementReplaced},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signals := 0
			control := priorProcessRecoveryControl{
				observe: func(int) (string, bool, error) { return test.observed, test.present, nil },
				graceful: func(int, string) (PriorProcessState, error) {
					signals++
					return PriorProcessPresent, nil
				},
				force: func(int, string) (PriorProcessState, error) {
					signals++
					return PriorProcessPresent, nil
				},
			}

			settlement, err := settlePriorProcess(t.Context(), priorProcessTestEvidence(t), time.Millisecond, control)
			if err != nil {
				t.Fatalf("settlePriorProcess() error = %v", err)
			}
			if settlement.Outcome != test.wantOutcome {
				t.Fatalf("settlePriorProcess() outcome = %q, want %q", settlement.Outcome, test.wantOutcome)
			}
			if signals != 0 {
				t.Fatalf("settlePriorProcess() emitted %d signals", signals)
			}
		})
	}
}

// TestSettlePriorProcessObservesGracefulExit proves an exact process can settle without forceful escalation.
func TestSettlePriorProcessObservesGracefulExit(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	running := true
	forceCalls := 0
	observe := func(int) (string, bool, error) {
		return evidence.BirthToken, running, nil
	}
	control := priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, expectedBirth string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, expectedBirth, observe, func(int) error {
				running = false
				return nil
			})
		},
		force: func(int, string) (PriorProcessState, error) {
			forceCalls++
			return PriorProcessPresent, nil
		},
	}

	settlement, err := settlePriorProcess(t.Context(), evidence, time.Second, control)
	if err != nil {
		t.Fatalf("settlePriorProcess() error = %v", err)
	}
	if settlement.Outcome != PriorProcessSettlementTerminated {
		t.Fatalf("settlePriorProcess() outcome = %q", settlement.Outcome)
	}
	if forceCalls != 0 {
		t.Fatalf("settlePriorProcess() force calls = %d", forceCalls)
	}
}

// TestSettlePriorProcessEscalatesAfterGrace proves a process that remains exact receives one bounded forceful signal.
func TestSettlePriorProcessEscalatesAfterGrace(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	running := true
	gracefulCalls := 0
	forceCalls := 0
	observe := func(int) (string, bool, error) {
		return evidence.BirthToken, running, nil
	}
	control := priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, expectedBirth string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, expectedBirth, observe, func(int) error {
				gracefulCalls++
				return nil
			})
		},
		force: func(pid int, expectedBirth string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, expectedBirth, observe, func(int) error {
				forceCalls++
				running = false
				return nil
			})
		},
	}

	settlement, err := settlePriorProcess(t.Context(), evidence, time.Millisecond, control)
	if err != nil {
		t.Fatalf("settlePriorProcess() error = %v", err)
	}
	if settlement.Outcome != PriorProcessSettlementTerminated {
		t.Fatalf("settlePriorProcess() outcome = %q", settlement.Outcome)
	}
	if gracefulCalls != 1 || forceCalls != 1 {
		t.Fatalf("settlePriorProcess() signals = graceful %d, force %d", gracefulCalls, forceCalls)
	}
}

// TestSettlePriorProcessRechecksBirthBeforeSignal proves PID reuse between admission and signaling revokes authority.
func TestSettlePriorProcessRechecksBirthBeforeSignal(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	observations := 0
	signals := 0
	observe := func(int) (string, bool, error) {
		observations++
		if observations == 1 {
			return evidence.BirthToken, true, nil
		}
		return "reused-birth", true, nil
	}
	control := priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, expectedBirth string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, expectedBirth, observe, func(int) error {
				signals++
				return nil
			})
		},
		force: func(int, string) (PriorProcessState, error) {
			signals++
			return PriorProcessPresent, nil
		},
	}

	settlement, err := settlePriorProcess(t.Context(), evidence, time.Second, control)
	if err != nil {
		t.Fatalf("settlePriorProcess() error = %v", err)
	}
	if settlement.Outcome != PriorProcessSettlementReplaced {
		t.Fatalf("settlePriorProcess() outcome = %q", settlement.Outcome)
	}
	if signals != 0 {
		t.Fatalf("settlePriorProcess() emitted %d signals after PID reuse", signals)
	}
}

// TestSettlePriorProcessPreservesReplacementAtForceBoundary proves a reused PID is never reported as Harbor-terminated.
func TestSettlePriorProcessPreservesReplacementAtForceBoundary(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	control := priorProcessRecoveryControl{
		observe: func(int) (string, bool, error) { return evidence.BirthToken, true, nil },
		graceful: func(int, string) (PriorProcessState, error) {
			return PriorProcessPresent, errors.New("graceful unavailable")
		},
		force: func(int, string) (PriorProcessState, error) {
			return PriorProcessReplaced, nil
		},
	}

	settlement, err := settlePriorProcess(t.Context(), evidence, time.Second, control)
	if err != nil {
		t.Fatalf("settlePriorProcess() error = %v", err)
	}
	if settlement.Outcome != PriorProcessSettlementReplaced {
		t.Fatalf("settlePriorProcess() outcome = %q", settlement.Outcome)
	}
}

// TestSettlePriorProcessCancellationDoesNotEscalate proves lost caller authority cannot trigger a forceful signal.
func TestSettlePriorProcessCancellationDoesNotEscalate(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	ctx, cancel := context.WithCancel(t.Context())
	forceCalls := 0
	observe := func(int) (string, bool, error) {
		return evidence.BirthToken, true, nil
	}
	control := priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, expectedBirth string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, expectedBirth, observe, func(int) error {
				cancel()
				return nil
			})
		},
		force: func(int, string) (PriorProcessState, error) {
			forceCalls++
			return PriorProcessPresent, nil
		},
	}

	if _, err := settlePriorProcess(ctx, evidence, time.Second, control); !errors.Is(err, context.Canceled) {
		t.Fatalf("settlePriorProcess() error = %v, want cancellation", err)
	}
	if forceCalls != 0 {
		t.Fatalf("settlePriorProcess() force calls after cancellation = %d", forceCalls)
	}
}

// TestSettlePriorProcessSignalFailuresFailClosed proves an optional graceful failure escalates but a force failure is returned.
func TestSettlePriorProcessSignalFailuresFailClosed(t *testing.T) {
	evidence := priorProcessTestEvidence(t)
	gracefulErr := errors.New("graceful unavailable")
	forceErr := errors.New("force denied")
	t.Run("graceful failure falls back to force", func(t *testing.T) {
		running := true
		control := priorProcessRecoveryControl{
			observe: func(int) (string, bool, error) { return evidence.BirthToken, running, nil },
			graceful: func(int, string) (PriorProcessState, error) {
				return PriorProcessPresent, gracefulErr
			},
			force: func(int, string) (PriorProcessState, error) {
				running = false
				return PriorProcessPresent, nil
			},
		}

		settlement, err := settlePriorProcess(t.Context(), evidence, time.Second, control)
		if err != nil || settlement.Outcome != PriorProcessSettlementTerminated {
			t.Fatalf("settlePriorProcess() = %#v, %v", settlement, err)
		}
	})

	t.Run("force failure retains graceful context", func(t *testing.T) {
		control := priorProcessRecoveryControl{
			observe: func(int) (string, bool, error) { return evidence.BirthToken, true, nil },
			graceful: func(int, string) (PriorProcessState, error) {
				return PriorProcessPresent, gracefulErr
			},
			force: func(int, string) (PriorProcessState, error) {
				return PriorProcessPresent, forceErr
			},
		}

		_, err := settlePriorProcess(t.Context(), evidence, time.Second, control)
		if !errors.Is(err, gracefulErr) || !errors.Is(err, forceErr) {
			t.Fatalf("settlePriorProcess() error = %v, want both signal failures", err)
		}
	})
}

// priorProcessTestEvidence returns one fully valid durable authority record for platform-neutral seam tests.
func priorProcessTestEvidence(t *testing.T) domain.ProcessEvidence {
	t.Helper()
	executable, err := filepath.Abs(filepath.Join(t.TempDir(), "forj"))
	if err != nil {
		t.Fatalf("resolve test executable identity: %v", err)
	}
	return domain.ProcessEvidence{
		PID:                1234,
		BirthToken:         "expected-birth",
		ExecutableIdentity: filepath.Clean(executable),
		ArgumentDigest:     strings.Repeat("0", 64),
	}
}
