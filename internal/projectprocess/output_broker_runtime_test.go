package projectprocess

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
)

// TestCaptureCurrentProcessEvidenceBindsTheRunningExecutable proves broker evidence is derived from the process, not a request payload.
func TestCaptureCurrentProcessEvidenceBindsTheRunningExecutable(t *testing.T) {
	evidence, err := CaptureCurrentProcessEvidence()
	if err != nil {
		t.Fatalf("CaptureCurrentProcessEvidence() error = %v", err)
	}
	if evidence.PID <= 0 || evidence.BirthToken == "" || evidence.ExecutableIdentity == "" || len(evidence.ArgumentDigest) != 64 {
		t.Fatalf("current process evidence = %#v, want complete identity", evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("current process evidence validation: %v", err)
	}
}

// TestRunOutputBrokerOwnsInheritedPipesAndServesLiveOutput proves the standalone runtime can outlive Harbor's in-process supervisor boundary.
func TestRunOutputBrokerOwnsInheritedPipesAndServesLiveOutput(t *testing.T) {
	evidence, err := CaptureCurrentProcessEvidence()
	if err != nil {
		t.Fatalf("CaptureCurrentProcessEvidence() error = %v", err)
	}
	outputRoot := t.TempDir()
	endpoint := filepath.Join(t.TempDir(), "output-broker.sock")
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunOutputBroker(ctx, OutputBrokerRuntimeConfig{
			ProjectID:         "project-runtime-broker",
			SessionID:         "session-runtime-broker",
			OutputDirectory:   outputRoot,
			EndpointReference: endpoint,
			AttachmentTicket:  "runtime-ticket-1",
			Process:           evidence,
			Stdout:            stdoutReader,
			Stderr:            stderrReader,
		})
	}()

	var connection local.Conn
	var dialErr error
	for attempt := 0; attempt < 100; attempt++ {
		connection, dialErr = local.DialAt(ctx, endpoint)
		if dialErr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if dialErr != nil {
		t.Fatalf("dial output broker endpoint: %v", dialErr)
	}
	t.Cleanup(func() { _ = connection.Close() })
	peer := OutputBrokerPeer{ProjectID: "project-runtime-broker", SessionID: "session-runtime-broker", EndpointReference: endpoint, Process: evidence}
	reader := rpc.NewDefaultFrameReader(connection)
	writer := rpc.NewDefaultFrameWriter(connection)
	hello := OutputBrokerHello{Version: OutputBrokerProtocolVersion, ProjectID: peer.ProjectID, SessionID: peer.SessionID, EndpointReference: endpoint, Ticket: "runtime-ticket-1", ClientNonce: "runtime-client-1"}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeHello, Hello: &hello}); err != nil {
		t.Fatalf("write runtime broker hello: %v", err)
	}
	challengeEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		t.Fatalf("read runtime broker challenge: %v", err)
	}
	if challengeEnvelope.Challenge == nil {
		t.Fatalf("runtime broker challenge = %#v", challengeEnvelope)
	}
	if err := AuthenticateOutputBrokerPeer(connection, peer, evidence); err != nil {
		t.Fatalf("authenticate runtime broker peer: %v", err)
	}
	challenge := challengeEnvelope.Challenge
	confirm := OutputBrokerConfirm{Version: OutputBrokerProtocolVersion, ProjectID: peer.ProjectID, SessionID: peer.SessionID, EndpointReference: endpoint, ClientNonce: hello.ClientNonce, Challenge: challenge.Challenge}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeConfirm, Confirm: &confirm}); err != nil {
		t.Fatalf("write runtime broker confirm: %v", err)
	}
	if ready, err := ReadOutputBrokerEnvelope(reader); err != nil || ready.Kind != OutputBrokerEnvelopeReady {
		t.Fatalf("read runtime broker ready = %#v, %v", ready, err)
	}
	if _, err := stdoutWriter.Write([]byte("runtime output\n")); err != nil {
		t.Fatalf("write inherited stdout: %v", err)
	}
	record, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		t.Fatalf("read runtime broker record: %v", err)
	}
	if record.Record == nil || record.Record.Frame == nil || record.Record.Frame.Text != "runtime output\n" {
		t.Fatalf("runtime broker record = %#v, want output", record)
	}
	if err := stdoutWriter.Close(); err != nil {
		t.Fatalf("close inherited stdout: %v", err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatalf("close inherited stderr: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunOutputBroker() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunOutputBroker() timed out: %v", ctx.Err())
	}
	if strings.Contains(record.Record.Frame.Text, "\x00") {
		t.Fatal("runtime broker record retained NUL output")
	}
}

// TestRunOutputBrokerRejectsIncompleteConfiguration proves no endpoint or pipe is created from partial authority.
func TestRunOutputBrokerRejectsIncompleteConfiguration(t *testing.T) {
	if err := validateOutputBrokerRuntimeConfig(OutputBrokerRuntimeConfig{}); err == nil {
		t.Fatal("validateOutputBrokerRuntimeConfig() accepted empty configuration")
	}
}

// TestReadOutputBrokerLaunchConfigRequiresOwnerPrivateCanonicalContent keeps the raw ticket outside argv and rejects mutable file aliases.
func TestReadOutputBrokerLaunchConfigRequiresOwnerPrivateCanonicalContent(t *testing.T) {
	config := OutputBrokerLaunchConfig{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         "project-launch-config",
		SessionID:         "session-launch-config",
		OutputDirectory:   filepath.Join(t.TempDir(), "output"),
		EndpointReference: filepath.Join(t.TempDir(), "broker.sock"),
		AttachmentTicket:  "launch-ticket-1",
	}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal launch config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "broker.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write launch config: %v", err)
	}
	loaded, err := ReadOutputBrokerLaunchConfig(path)
	if err != nil {
		t.Fatalf("ReadOutputBrokerLaunchConfig() error = %v", err)
	}
	if loaded != config {
		t.Fatalf("loaded launch config = %#v, want %#v", loaded, config)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("relax launch config mode: %v", err)
	}
	if _, err := ReadOutputBrokerLaunchConfig(path); err == nil {
		t.Fatal("ReadOutputBrokerLaunchConfig() accepted group-readable config")
	}
}
