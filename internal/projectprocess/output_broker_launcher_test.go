package projectprocess

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNewOutputBrokerEndpointReferenceUsesCompactEndpointToken keeps the path-safe endpoint token independent from attachment credentials.
func TestNewOutputBrokerEndpointReferenceUsesCompactEndpointToken(t *testing.T) {
	endpoint, err := newOutputBrokerEndpointReference()
	if err != nil {
		t.Fatalf("newOutputBrokerEndpointReference() error = %v", err)
	}
	prefix := "output-"
	suffix := ".sock"
	name := filepath.Base(endpoint)
	if runtime.GOOS == "windows" {
		prefix = `\\.\pipe\goforj-harbor-output-`
		suffix = ""
		name = endpoint
	}
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		t.Fatalf("endpoint name = %q, want %q token %q", name, prefix, suffix)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(token) != hex.EncodedLen(outputBrokerEndpointTokenBytes) {
		t.Fatalf("endpoint token length = %d, want %d", len(token), hex.EncodedLen(outputBrokerEndpointTokenBytes))
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("decode endpoint token: %v", err)
	}
}

// TestOutputBrokerLaunchConfigAcceptsLegacyEndpointTokenLength keeps restart adoption compatible with persisted endpoints from earlier brokers.
func TestOutputBrokerLaunchConfigAcceptsLegacyEndpointTokenLength(t *testing.T) {
	directory := t.TempDir()
	endpoint := filepath.Join(directory, "output-"+strings.Repeat("a", hex.EncodedLen(outputBrokerChallengeBytes))+".sock")
	config := OutputBrokerLaunchConfig{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         "project-legacy-endpoint",
		SessionID:         "session-legacy-endpoint",
		OutputDirectory:   directory,
		EndpointReference: endpoint,
		AttachmentTicket:  "legacy-endpoint-ticket",
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("OutputBrokerLaunchConfig.Validate() error = %v", err)
	}
}

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
	peer := client.Peer()
	if peer.ManifestPath == "" || peer.TicketDigest == "" {
		t.Fatalf("broker peer omitted durable reattach metadata: %#v", peer)
	}
	if _, err := os.Stat(peer.ManifestPath); err != nil {
		t.Fatalf("durable broker manifest is unavailable after attach: %v", err)
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
	if _, err := os.Stat(peer.ManifestPath); !os.IsNotExist(err) {
		t.Fatalf("broker manifest after attachment close error = %v, want removed", err)
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

// TestOutputBrokerProcessLauncherAdoptsSurvivingBroker proves a daemon replacement can reconnect without owning broker termination.
func TestOutputBrokerProcessLauncherAdoptsSurvivingBroker(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native broker adoption proof is currently Unix-only")
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
	attachment, err := launcher.Launch(t.Context(), OutputBrokerLaunchSpec{
		ProjectID:       "project-real-adopt",
		SessionID:       "session-real-adopt",
		OutputDirectory: outputRoot,
		Stdout:          stdout,
		Stderr:          stderr,
	})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	launched, ok := attachment.(*outputBrokerProcessAttachment)
	if !ok {
		t.Fatalf("Launch() attachment type = %T, want *outputBrokerProcessAttachment", attachment)
	}
	peer := launched.Peer()
	if err := launched.client.Close(); err != nil {
		t.Fatalf("detach original broker transport: %v", err)
	}
	if _, err := os.Stat(peer.ManifestPath); err != nil {
		t.Fatalf("manifest disappeared when only transport detached: %v", err)
	}
	adopted, err := launcher.Adopt(t.Context(), OutputBrokerAdoptionSpec{
		ProjectID:       peer.ProjectID,
		SessionID:       peer.SessionID,
		OutputDirectory: outputRoot,
		Peer:            peer,
	})
	if err != nil {
		_ = attachment.Close()
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted == nil || adopted.Peer() != peer {
		if adopted != nil {
			_ = adopted.Close()
		}
		_ = attachment.Close()
		t.Fatalf("Adopt() peer = %#v, want %#v", adopted.Peer(), peer)
	}
	if err := stdout.Close(); err != nil {
		t.Fatalf("close parent stdout reader: %v", err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatalf("close parent stderr reader: %v", err)
	}
	if _, err := stdoutWriter.Write([]byte("adopted broker output\n")); err != nil {
		_ = adopted.Close()
		_ = attachment.Close()
		t.Fatalf("write adopted broker output: %v", err)
	}
	readContext, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	record, err := adopted.Receive(readContext)
	if err != nil {
		_ = adopted.Close()
		_ = attachment.Close()
		t.Fatalf("adopted.Receive() error = %v", err)
	}
	if record.Frame == nil || record.Frame.Text != "adopted broker output\n" {
		t.Fatalf("adopted.Receive() = %#v, want adopted output", record)
	}
	if err := adopted.Close(); err != nil {
		t.Fatalf("adopted.Close() error = %v", err)
	}
	if err := attachment.Close(); err != nil {
		t.Fatalf("original attachment cleanup error = %v", err)
	}
	if _, err := os.Stat(peer.ManifestPath); !os.IsNotExist(err) {
		t.Fatalf("manifest after broker cleanup error = %v, want removed", err)
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
