//go:build darwin || linux

package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestOwnedUnixSessionObservationResamplesOnlyTransientOmission requires a fresh full census before accepting convergence.
func TestOwnedUnixSessionObservationResamplesOnlyTransientOmission(t *testing.T) {
	for _, outcome := range []string{"zombie", "absent", "complete census", "absent root with descendants"} {
		t.Run(outcome, func(t *testing.T) {
			censuses, births := 0, 0
			observation := unixSessionObservation{
				birth: func(int) (string, bool, error) {
					births++
					if (outcome == "absent" || outcome == "absent root with descendants") && births >= 4 {
						return "", false, nil
					}
					return "exact", true, nil
				},
				members: func(int) ([]unixProcessMember, error) {
					censuses++
					if outcome == "absent root with descendants" && censuses == 2 {
						return []unixProcessMember{{PID: 43, BirthToken: "descendant"}}, nil
					}
					if outcome == "complete census" && censuses == 2 {
						return []unixProcessMember{{PID: 42, BirthToken: "exact"}}, nil
					}
					return nil, nil
				},
				zombie: func(int) (bool, bool, error) { return censuses >= 2, true, nil },
			}
			state, members, err := observeOwnedProcessSessionWithObservation(42, "exact", observation)
			if err != nil {
				t.Fatal(err)
			}
			expected := PriorProcessAbsent
			if outcome == "complete census" || outcome == "absent root with descendants" {
				expected = PriorProcessPresent
				if len(members) != 1 {
					t.Fatal("complete census lost")
				}
				if outcome == "absent root with descendants" && members[0].PID != 43 {
					t.Fatal("surviving descendant lost after root exit")
				}
			} else if len(members) != 0 {
				t.Fatal("empty scope gained signal authority")
			}
			if state != expected || censuses != 2 || births < 4 {
				t.Fatalf("state=%s censuses=%d births=%d", state, censuses, births)
			}
		})
	}
}

// TestOwnedUnixSessionPersistentOmissionRemainsBoundedAndCannotSignal preserves quarantine when an exact live leader stays omitted.
func TestOwnedUnixSessionPersistentOmissionRemainsBoundedAndCannotSignal(t *testing.T) {
	censuses, signals := 0, 0
	observation := unixSessionObservation{
		birth:   func(int) (string, bool, error) { return "exact", true, nil },
		members: func(int) ([]unixProcessMember, error) { censuses++; return nil, nil },
		zombie:  func(int) (bool, bool, error) { return false, true, nil },
	}
	control := priorProcessRecoveryControl{
		observe: observation.birth,
		observeScope: func(pid int, birth string) (PriorProcessState, error) {
			state, _, err := observeOwnedProcessSessionWithObservation(pid, birth, observation)
			return state, err
		},
		graceful: func(int, string) (PriorProcessState, error) { signals++; return PriorProcessPresent, nil },
		force:    func(int, string) (PriorProcessState, error) { signals++; return PriorProcessPresent, nil },
	}
	evidence := domain.ProcessEvidence{PID: 42, BirthToken: "exact", ExecutableIdentity: "/test/owned", ArgumentDigest: strings.Repeat("a", 64)}
	started := time.Now()
	_, err := settlePriorProcess(context.Background(), evidence, time.Second, control)
	if !errors.Is(err, errOwnedUnixSessionLeaderOmitted) || censuses < 2 || signals != 0 {
		t.Fatalf("err=%v censuses=%d signals=%d", err, censuses, signals)
	}
	if elapsed := time.Since(started); elapsed < emptyOwnedUnixSessionObservationWindow || elapsed > time.Second {
		t.Fatalf("omission retry duration=%s", elapsed)
	}
}

// TestOwnedUnixSessionObservationStopsAtBirthDrift never enumerates or signals a replacement root after retry.
func TestOwnedUnixSessionObservationStopsAtBirthDrift(t *testing.T) {
	births, censuses := 0, 0
	observation := unixSessionObservation{
		birth: func(int) (string, bool, error) {
			births++
			if births >= 4 {
				return "replacement", true, nil
			}
			return "exact", true, nil
		},
		members: func(int) ([]unixProcessMember, error) { censuses++; return nil, nil },
		zombie:  func(int) (bool, bool, error) { return false, true, nil },
	}
	state, members, err := observeOwnedProcessSessionWithObservation(42, "exact", observation)
	if err != nil || state != PriorProcessReplaced || len(members) != 0 || censuses != 1 {
		t.Fatalf("state=%s members=%v censuses=%d err=%v", state, members, censuses, err)
	}
}

// TestOwnedUnixSessionObservationDoesNotRetryUnreadableFacts excludes every error except the exact omitted-leader condition.
func TestOwnedUnixSessionObservationDoesNotRetryUnreadableFacts(t *testing.T) {
	for _, failure := range []string{"root", "census", "leader state", "final birth", "leader appeared"} {
		t.Run(failure, func(t *testing.T) {
			births, censuses, leaderStates := 0, 0, 0
			unavailable := errors.New("native observation unavailable")
			observation := unixSessionObservation{
				birth: func(int) (string, bool, error) {
					births++
					if failure == "root" || failure == "final birth" && births == 3 {
						return "", false, unavailable
					}
					return "exact", true, nil
				},
				members: func(int) ([]unixProcessMember, error) {
					censuses++
					if failure == "census" {
						return nil, unavailable
					}
					return nil, nil
				},
				zombie: func(int) (bool, bool, error) {
					leaderStates++
					if failure == "leader state" {
						return false, false, unavailable
					}
					return false, failure != "leader appeared", nil
				},
			}
			_, members, err := observeOwnedProcessSessionWithObservation(42, "exact", observation)
			if err == nil || errors.Is(err, errOwnedUnixSessionLeaderOmitted) || len(members) != 0 || censuses > 1 || leaderStates > 1 {
				t.Fatalf("err=%v censuses=%d leaderStates=%d members=%v", err, censuses, leaderStates, members)
			}
			if failure != "leader appeared" && !errors.Is(err, unavailable) {
				t.Fatalf("native error lost: %v", err)
			}
		})
	}
}

// TestOwnedUnixSessionObservationStopsOnUnreadableRetry never treats a later native failure as the omitted-leader transition.
func TestOwnedUnixSessionObservationStopsOnUnreadableRetry(t *testing.T) {
	for _, boundary := range []string{"root", "census", "leader state"} {
		t.Run(boundary, func(t *testing.T) {
			births, censuses, states := 0, 0, 0
			unavailable := errors.New("native retry observation unavailable")
			observation := unixSessionObservation{
				birth: func(int) (string, bool, error) {
					births++
					if boundary == "root" && births == 4 {
						return "", false, unavailable
					}
					return "exact", true, nil
				},
				members: func(int) ([]unixProcessMember, error) {
					censuses++
					if boundary == "census" && censuses == 2 {
						return nil, unavailable
					}
					return nil, nil
				},
				zombie: func(int) (bool, bool, error) {
					states++
					if boundary == "leader state" && states == 2 {
						return false, false, unavailable
					}
					return false, true, nil
				},
			}
			_, members, err := observeOwnedProcessSessionWithObservation(42, "exact", observation)
			if !errors.Is(err, unavailable) || len(members) != 0 {
				t.Fatalf("members=%v err=%v", members, err)
			}
			expectedBirths, expectedCensuses, expectedStates := 4, 1, 1
			if boundary == "census" {
				expectedCensuses = 2
			}
			if boundary == "leader state" {
				expectedBirths = 5
				expectedCensuses = 2
				expectedStates = 2
			}
			if births != expectedBirths || censuses != expectedCensuses || states != expectedStates {
				t.Fatalf("observation continued after error: births=%d censuses=%d states=%d", births, censuses, states)
			}
		})
	}
}

// TestNativeOwnedUnixSessionSettlementConvergesAcrossLeaderExit covers the actual signal-to-zombie transition with only test-owned sessions.
func TestNativeOwnedUnixSessionSettlementConvergesAcrossLeaderExit(t *testing.T) {
	for index := 0; index < 30; index++ {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			command := exec.Command("/bin/sh", "-c", "sleep 1 & wait")
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); command.Wait() }()
			birth, err := processBirthToken(command.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			evidence := domain.ProcessEvidence{PID: int64(command.Process.Pid), BirthToken: ownedUnixSessionBirthTokenPrefix + birth, ExecutableIdentity: "/bin/sh", ArgumentDigest: strings.Repeat("a", 64)}
			outcome, err := settlePriorProcess(ctx, evidence, 100*time.Millisecond, newPriorProcessRecoveryControl())
			if err != nil {
				t.Fatalf("native settlement: %v", err)
			}
			if outcome.Outcome != PriorProcessSettlementTerminated && outcome.Outcome != PriorProcessSettlementAbsent {
				t.Fatalf("settlement=%s", outcome.Outcome)
			}
		})
	}
}
