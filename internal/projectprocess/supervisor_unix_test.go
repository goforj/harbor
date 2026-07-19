//go:build !windows

package projectprocess

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStopEscalatesAcrossUnixProcessGroup verifies a graceful root exit cannot leave an ignoring descendant alive.
func TestStopEscalatesAcrossUnixProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/grandchild.pid"
	t.Setenv(helperPIDFileEnvironment, pidFile)
	installForjHelper(t, "tree")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-tree",
		SessionID:            "session-tree",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	grandchildPID := waitForHelperPID(t, pidFile)
	if err := supervisor.Stop(t.Context(), "project-tree", "session-tree"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	result, ok := handle.Result()
	if !ok || !result.StopRequested {
		t.Fatalf("Result() = %#v, %t", result, ok)
	}
	if processExists(grandchildPID) {
		t.Fatalf("grandchild PID %d survived Stop()", grandchildPID)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestUnexpectedRootExitKillsUnixDescendants verifies a crashing forj process cannot orphan its managed tools.
func TestUnexpectedRootExitKillsUnixDescendants(t *testing.T) {
	pidFile := t.TempDir() + "/orphan.pid"
	t.Setenv(helperPIDFileEnvironment, pidFile)
	installForjHelper(t, "orphan")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-orphan",
		SessionID:            "session-orphan",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result, err := handle.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.ExitCode != 23 || result.StopRequested {
		t.Fatalf("Wait() result = %#v", result)
	}
	grandchildPID := waitForHelperPID(t, pidFile)
	if processExists(grandchildPID) {
		t.Fatalf("grandchild PID %d survived unexpected root exit", grandchildPID)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// waitForHelperPID waits until the process-tree helper publishes a parseable descendant PID.
func waitForHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, conversionErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if conversionErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper PID was not published to %s", path)
	return 0
}

// processExists reports whether Unix still has a process identity for the PID.
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
