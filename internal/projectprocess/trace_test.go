package projectprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestStartRetainsAndClosesProjectLaunchTrace proves one accepted process leaves immediately reusable diagnostics.
func TestStartRetainsAndClosesProjectLaunchTrace(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-trace",
		SessionID:            "session-trace",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	path := projectLaunchTracePath(checkout)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project launch trace: %v", err)
	}
	for _, expected := range []string{
		"Harbor managed forj dev\n",
		"project=project-trace\n",
		"session=session-trace\n",
		"argument=dev\n",
		"ready\n",
	} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("project launch trace does not contain %q:\n%s", expected, contents)
		}
	}
	if err := os.Rename(path, path+".closed"); err != nil {
		t.Fatalf("rename completed project launch trace: %v", err)
	}
}

// TestStopRemovesProjectLaunchTrace verifies an explicitly requested settled shutdown retires Harbor diagnostics.
func TestStopRemovesProjectLaunchTrace(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "wait")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-trace-stop",
		SessionID:            "session-trace-stop",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := supervisor.Stop(t.Context(), "project-trace-stop", "session-trace-stop"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if _, err := os.Lstat(projectLaunchTracePath(checkout)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project launch trace stat error = %v, want not exist", err)
	}
	if _, err := os.Lstat(filepath.Join(checkout, "_data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trace parent directory stat error = %v, want not exist", err)
	}
}

// TestExternalArtifactRootLifecycleRetainsUnexpectedExitAndRemovesSettledStop verifies cleanup tracks the same confidence boundary as launch traces.
func TestExternalArtifactRootLifecycleRetainsUnexpectedExitAndRemovesSettledStop(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	failedRoot := externalArtifactRootPathForTest(t, "project-artifact-failed", "session-artifact-failed")
	failedCheckout := t.TempDir()
	if err := os.WriteFile(filepath.Join(failedCheckout, "_data"), []byte("block launch trace"), 0o600); err != nil {
		t.Fatalf("block failed launch trace: %v", err)
	}
	_, err := newTestSupervisor(Options{}).Start(t.Context(), StartRequest{
		ProjectID:            "project-artifact-failed",
		SessionID:            "session-artifact-failed",
		CheckoutRoot:         failedCheckout,
		ExternalArtifactRoot: failedRoot,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err == nil {
		t.Fatal("Start() start-failure error = nil")
	}
	if _, err := os.Stat(failedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start failure artifact root stat error = %v, want not exist", err)
	}

	installForjHelper(t, "exit")
	retainedRoot := externalArtifactRootPathForTest(t, "project-artifact-retained", "session-artifact-retained")
	retained := newTestSupervisor(Options{})
	retainedHandle, err := retained.Start(t.Context(), StartRequest{
		ProjectID:            "project-artifact-retained",
		SessionID:            "session-artifact-retained",
		CheckoutRoot:         t.TempDir(),
		ExternalArtifactRoot: retainedRoot,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() unexpected-exit error = %v", err)
	}
	if _, err := retainedHandle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() unexpected-exit error = %v", err)
	}
	if _, err := os.Stat(retainedRoot); err != nil {
		t.Fatalf("unexpected exit removed artifact root: %v", err)
	}

	installForjHelper(t, "wait")
	removedRoot := externalArtifactRootPathForTest(t, "project-artifact-removed", "session-artifact-removed")
	removed := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() { _ = removed.Close(context.Background()) })
	handle, err := removed.Start(t.Context(), StartRequest{
		ProjectID:            "project-artifact-removed",
		SessionID:            "session-artifact-removed",
		CheckoutRoot:         t.TempDir(),
		ExternalArtifactRoot: removedRoot,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() settled-stop error = %v", err)
	}
	if err := removed.Stop(t.Context(), "project-artifact-removed", "session-artifact-removed"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() settled-stop error = %v", err)
	}
	if _, err := os.Stat(removedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settled stop artifact root stat error = %v, want not exist", err)
	}
}

// TestStartArtifactRootCleanupRequiresSettledAcceptedProcess proves post-start setup failures retain artifacts unless the accepted tree settled.
func TestStartArtifactRootCleanupRequiresSettledAcceptedProcess(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	installForjHelper(t, "wait")

	for _, test := range []struct {
		name             string
		cleanupUncertain bool
		wantRoot         bool
	}{
		{
			name:             "uncertain settlement retains root",
			cleanupUncertain: true,
			wantRoot:         true,
		},
		{
			name:     "settled cleanup removes root",
			wantRoot: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectID := domain.ProjectID("project-artifact-post-start-" + strings.ReplaceAll(test.name, " ", "-"))
			sessionID := domain.SessionID("session-artifact-post-start-" + strings.ReplaceAll(test.name, " ", "-"))
			artifactRoot := externalArtifactRootPathForTest(t, projectID, sessionID)
			supervisor := newTestSupervisor(Options{})
			supervisor.afterCommandStart = func() error { return errors.New("post-start setup failed") }
			if test.cleanupUncertain {
				supervisor.terminateAccepted = func(command *exec.Cmd, platform *platformProcess) error {
					return errors.Join(terminateStartedCommand(command, platform), ErrCleanupUncertain)
				}
			}

			_, err := supervisor.Start(t.Context(), StartRequest{
				ProjectID:            projectID,
				SessionID:            sessionID,
				CheckoutRoot:         t.TempDir(),
				ExternalArtifactRoot: artifactRoot,
				EnvironmentOverrides: projectProcessTestEnvironment(),
			})
			if !errors.Is(err, ErrCleanupUncertain) == test.cleanupUncertain {
				t.Fatalf("Start() error = %v, cleanup uncertainty = %t, want %t", err, errors.Is(err, ErrCleanupUncertain), test.cleanupUncertain)
			}
			_, statErr := os.Stat(artifactRoot)
			if test.wantRoot && statErr != nil {
				t.Fatalf("artifact root stat error = %v, want retained root", statErr)
			}
			if !test.wantRoot && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("artifact root stat error = %v, want removed root", statErr)
			}
		})
	}
}

// TestDownRemovesProjectLaunchTrace verifies reset clears diagnostics only after its owned process scope settles.
func TestDownRemovesProjectLaunchTrace(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "exit")
	if trace, err := openProjectLaunchTrace(checkout, "project-trace-down", "session-trace-down", time.Now()); err != nil {
		t.Fatalf("openProjectLaunchTrace() error = %v", err)
	} else if err := trace.Close(); err != nil {
		t.Fatalf("close project launch trace: %v", err)
	}
	supervisor := newTestSupervisor(Options{})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Down(t.Context(), DownRequest{CheckoutRoot: checkout}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if _, err := os.Lstat(projectLaunchTracePath(checkout)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project launch trace stat error = %v, want not exist", err)
	}
}

// TestFailedDownRetainsProjectLaunchTrace preserves diagnostics when reset did not complete successfully.
func TestFailedDownRetainsProjectLaunchTrace(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "down-fail")
	if trace, err := openProjectLaunchTrace(checkout, "project-trace-down-failed", "session-trace-down-failed", time.Now()); err != nil {
		t.Fatalf("openProjectLaunchTrace() error = %v", err)
	} else if err := trace.Close(); err != nil {
		t.Fatalf("close project launch trace: %v", err)
	}
	supervisor := newTestSupervisor(Options{})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Down(t.Context(), DownRequest{CheckoutRoot: checkout}); err == nil {
		t.Fatal("Down() error = nil, want reset failure")
	}
	if _, err := os.Lstat(projectLaunchTracePath(checkout)); err != nil {
		t.Fatalf("project launch trace stat error = %v, want retained trace", err)
	}
}

// TestStartRollbackRemovesProjectLaunchTrace verifies a process rejected before acceptance leaves no diagnostic residue.
func TestStartRollbackRemovesProjectLaunchTrace(t *testing.T) {
	checkout := t.TempDir()
	supervisor := newTestSupervisor(Options{})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-trace-rollback",
		SessionID:            "session-trace-rollback",
		CheckoutRoot:         checkout,
		GoForjExecutable:     checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err == nil {
		t.Fatal("Start() error = nil, want launch failure")
	}
	if _, err := os.Lstat(projectLaunchTracePath(checkout)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project launch trace stat error = %v, want not exist", err)
	}
}

// TestRemoveProjectLaunchTracePreservesProjectFiles verifies cleanup never removes project-owned siblings or non-empty parents.
func TestRemoveProjectLaunchTracePreservesProjectFiles(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "_data", "harbor"), 0o700); err != nil {
		t.Fatalf("create trace directory: %v", err)
	}
	if err := os.WriteFile(projectLaunchTracePath(checkout), []byte("trace"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	projectFile := filepath.Join(checkout, "_data", "project-owned")
	if err := os.WriteFile(projectFile, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	if err := removeProjectLaunchTrace(checkout); err != nil {
		t.Fatalf("removeProjectLaunchTrace() error = %v", err)
	}
	if contents, err := os.ReadFile(projectFile); err != nil || string(contents) != "preserve" {
		t.Fatalf("project file = %q, %v", contents, err)
	}
	if _, err := os.Lstat(filepath.Join(checkout, "_data")); err != nil {
		t.Fatalf("non-empty _data stat error = %v", err)
	}
}

// TestRemoveProjectLaunchTraceRejectsLinkedDirectory prevents cleanup from resolving Harbor's path through a project link.
func TestRemoveProjectLaunchTraceRejectsLinkedDirectory(t *testing.T) {
	checkout := t.TempDir()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, "_data"), 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(checkout, "_data", "harbor")); err != nil {
		t.Skipf("create diagnostic directory symlink: %v", err)
	}
	if err := removeProjectLaunchTrace(checkout); err == nil || !strings.Contains(err.Error(), "direct directory") {
		t.Fatalf("removeProjectLaunchTrace() error = %v", err)
	}
}

// TestDownFencesCheckoutUntilTraceCleanup verifies reset admission remains owned when diagnostic cleanup is unsafe.
func TestDownFencesCheckoutUntilTraceCleanup(t *testing.T) {
	checkout := t.TempDir()
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, "_data"), 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	link := filepath.Join(checkout, "_data", "harbor")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("create diagnostic directory symlink: %v", err)
	}
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err := supervisor.Down(t.Context(), DownRequest{CheckoutRoot: checkout}); err == nil {
		t.Fatal("Down() error = nil, want trace cleanup failure")
	}
	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-trace-fence",
		SessionID:            "session-trace-fence",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrResetInProgress) {
		t.Fatalf("Start() error = %v, want ErrResetInProgress", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove unsafe trace link: %v", err)
	}
	if err := supervisor.Down(t.Context(), DownRequest{CheckoutRoot: checkout}); err != nil {
		t.Fatalf("retry Down() error = %v", err)
	}
}

// TestOutputRelayTraceIgnoresBlockedCaller proves diagnostic progress never depends on a terminal or UI writer.
func TestOutputRelayTraceIgnoresBlockedCaller(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forj-dev.log")
	trace, err := newProjectLaunchTrace(path, 1024)
	if err != nil {
		t.Fatalf("newProjectLaunchTrace() error = %v", err)
	}
	writer := newBlockingWriter()
	defer close(writer.release)
	relay := newOutputRelayWithTrace(writer, writer, trace, 4)
	relay.offer(outputStreamStdout, []byte("first\n"))
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("caller writer was not reached")
	}
	relay.offer(outputStreamStderr, []byte("second\n"))
	relay.finish()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project launch trace: %v", err)
	}
	if string(contents) != "first\nsecond\n" {
		t.Fatalf("project launch trace = %q", contents)
	}
}

// TestProjectLaunchTraceBoundsOutput preserves the diagnostic prefix and one visible truncation marker.
func TestProjectLaunchTraceBoundsOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forj-dev.log")
	const maximumBytes = 128
	trace, err := newProjectLaunchTrace(path, maximumBytes)
	if err != nil {
		t.Fatalf("newProjectLaunchTrace() error = %v", err)
	}
	body := strings.Repeat("diagnostic-output-", 32)
	written, err := trace.Write([]byte(body))
	if err != nil || written != len(body) {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if _, err := trace.Write([]byte("ignored")); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}
	if err := trace.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project launch trace: %v", err)
	}
	if len(contents) != maximumBytes {
		t.Fatalf("project launch trace bytes = %d, want %d", len(contents), maximumBytes)
	}
	if strings.Count(string(contents), projectLaunchTraceTruncated) != 1 {
		t.Fatalf("project launch trace truncation marker count = %d", strings.Count(string(contents), projectLaunchTraceTruncated))
	}
}

// TestProjectLaunchTraceRejectsIndirectDestination prevents a project symlink from redirecting owned diagnostics.
func TestProjectLaunchTraceRejectsIndirectDestination(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.log")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	path := filepath.Join(root, "forj-dev.log")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("create diagnostic symlink: %v", err)
	}
	if _, err := newProjectLaunchTrace(path, 1024); err == nil || !strings.Contains(err.Error(), "direct regular file") {
		t.Fatalf("newProjectLaunchTrace() error = %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve" {
		t.Fatalf("symlink target = %q, %v", contents, err)
	}
}
