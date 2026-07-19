package projectprocess

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
