package projectprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewOutputBrokerProcessLauncherRejectsUntrustedExecutablePaths keeps production discovery out of PATH and cwd semantics.
func TestNewOutputBrokerProcessLauncherRejectsUntrustedExecutablePaths(t *testing.T) {
	for name, path := range map[string]string{
		"relative":  "outputbroker",
		"missing":   filepath.Join(t.TempDir(), "outputbroker"),
		"directory": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewOutputBrokerProcessLauncher(path); err == nil {
				t.Fatal("NewOutputBrokerProcessLauncher() accepted an unsafe executable path")
			}
		})
	}
}

// TestWriteOutputBrokerLaunchConfigPublishesOwnerPrivateCanonicalFile proves raw tickets never enter a mutable manifest shape.
func TestWriteOutputBrokerLaunchConfigPublishesOwnerPrivateCanonicalFile(t *testing.T) {
	directory := t.TempDir()
	config := OutputBrokerLaunchConfig{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         "project-launcher-config",
		SessionID:         "session-launcher-config",
		OutputDirectory:   filepath.Join(directory, "output"),
		EndpointReference: filepath.Join(directory, "broker.sock"),
		AttachmentTicket:  "launcher-ticket-1",
	}
	if err := os.MkdirAll(config.OutputDirectory, 0o700); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	path, err := writeOutputBrokerLaunchConfig(directory, config)
	if err != nil {
		t.Fatalf("writeOutputBrokerLaunchConfig() error = %v", err)
	}
	defer os.Remove(path)
	loaded, err := ReadOutputBrokerLaunchConfig(path)
	if err != nil {
		t.Fatalf("ReadOutputBrokerLaunchConfig() error = %v", err)
	}
	if loaded != config {
		t.Fatalf("loaded launch config = %#v, want %#v", loaded, config)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat launch config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("launch config mode = %o, want 600", info.Mode().Perm())
	}
}

// TestOutputBrokerProcessLauncherTransfersRealInheritedPipes proves the macOS/Linux production path across the actual broker executable.
func TestOutputBrokerProcessLauncherTransfersRealInheritedPipes(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native inherited descriptor proof is currently Unix-only")
	}
	executable := buildOutputBrokerTestBinary(t)
	launcher, err := NewOutputBrokerProcessLauncher(executable)
	if err != nil {
		t.Fatalf("NewOutputBrokerProcessLauncher() error = %v", err)
	}
	outputRoot := t.TempDir()
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		t.Fatalf("create stderr pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
	})
	client, err := launcher.Launch(t.Context(), OutputBrokerLaunchSpec{
		ProjectID:       "project-real-launcher",
		SessionID:       "session-real-launcher",
		OutputDirectory: outputRoot,
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		t.Fatalf("OutputBrokerProcessLauncher.Launch() error = %v", err)
	}
	if client == nil {
		t.Fatal("OutputBrokerProcessLauncher.Launch() returned nil attachment")
	}
	_ = stdout.Close()
	_ = stderr.Close()
	if _, err := stdoutWriter.Write([]byte("real broker output\n")); err != nil {
		t.Fatalf("write inherited stdout: %v", err)
	}
	clientContext, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	record, err := client.Receive(clientContext)
	if err != nil {
		t.Fatalf("client.Receive() error = %v", err)
	}
	if record.Frame == nil || record.Frame.Text != "real broker output\n" {
		t.Fatalf("client.Receive() = %#v, want real broker output", record)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close() error = %v", err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot, available, readErr := readOutputSpool(outputRoot, "project-real-launcher", "session-real-launcher")
		if readErr != nil {
			t.Fatalf("read broker spool: %v", readErr)
		}
		if available {
			chunk := snapshot.transcript.read(0)
			if chunk.Text == "real broker output\n" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("broker spool did not settle: available=%t snapshot=%#v", available, snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// buildOutputBrokerTestBinary builds the exact standalone command in an isolated temporary destination.
func buildOutputBrokerTestBinary(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	name := "outputbroker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", path, "./cmd/outputbroker")
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE=/tmp/gocache", "GOMODCACHE=/tmp/gomodcache")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build output broker: %v\n%s", err, strings.TrimSpace(string(output)))
	}
	return path
}
