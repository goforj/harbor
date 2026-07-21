package projectprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// supervisorOutputBrokerAttachment is a bounded test attachment whose Close method only retires transport.
type supervisorOutputBrokerAttachment struct {
	peer      OutputBrokerPeer
	records   chan OutputBrokerRecord
	closeOnce sync.Once
	closed    chan struct{}
}

// Peer returns the deterministic broker evidence used by the supervisor seam test.
func (attachment *supervisorOutputBrokerAttachment) Peer() OutputBrokerPeer {
	return attachment.peer
}

// Receive returns one queued broker record or the attachment's terminal boundary.
func (attachment *supervisorOutputBrokerAttachment) Receive(ctx context.Context) (OutputBrokerRecord, error) {
	select {
	case record := <-attachment.records:
		return record, nil
	default:
	}
	select {
	case record := <-attachment.records:
		return record, nil
	case <-attachment.closed:
		return OutputBrokerRecord{}, io.EOF
	case <-ctx.Done():
		return OutputBrokerRecord{}, ctx.Err()
	}
}

// Close retires the attachment without touching the managed child process.
func (attachment *supervisorOutputBrokerAttachment) Close() error {
	attachment.closeOnce.Do(func() { close(attachment.closed) })
	return nil
}

// supervisorOutputBrokerLauncher records the launch boundary and returns one supplied attachment.
type supervisorOutputBrokerLauncher struct {
	attachment *supervisorOutputBrokerAttachment
	err        error
	mu         sync.Mutex
	spec       OutputBrokerLaunchSpec
}

// Launch validates and records the exact pipe handoff before returning the configured test attachment.
func (launcher *supervisorOutputBrokerLauncher) Launch(_ context.Context, spec OutputBrokerLaunchSpec) (OutputBrokerAttachment, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	launcher.mu.Lock()
	launcher.spec = spec
	launcher.mu.Unlock()
	if launcher.err != nil {
		return nil, launcher.err
	}
	return launcher.attachment, nil
}

// outputBrokerSupervisorTestPeer supplies valid evidence without granting the broker any child authority.
func outputBrokerSupervisorTestPeer(t *testing.T, projectID domain.ProjectID, sessionID domain.SessionID) OutputBrokerPeer {
	t.Helper()
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("canonicalize test executable: %v", err)
	}
	birthToken, err := processBirthToken(os.Getpid())
	if err != nil {
		t.Fatalf("capture test process birth: %v", err)
	}
	return OutputBrokerPeer{
		ProjectID:         projectID,
		SessionID:         sessionID,
		EndpointReference: filepath.Join(t.TempDir(), "broker.sock"),
		Process: domain.ProcessEvidence{
			PID:                int64(os.Getpid()),
			BirthToken:         birthToken,
			ExecutableIdentity: executable,
			ArgumentDigest:     digestArguments([]string{executable, "output-broker-test"}),
		},
	}
}

// TestOutputBrokerLaunchSpecValidationKeepsPipeHandoffCanonical proves the optional seam rejects incomplete identity.
func TestOutputBrokerLaunchSpecValidationKeepsPipeHandoffCanonical(t *testing.T) {
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
	valid := OutputBrokerLaunchSpec{
		ProjectID:       "project-broker",
		SessionID:       "session-broker",
		OutputDirectory: filepath.Join(t.TempDir(), "output"),
		Stdout:          stdout,
		Stderr:          stderr,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid OutputBrokerLaunchSpec.Validate() error = %v", err)
	}
	for name, mutate := range map[string]func(*OutputBrokerLaunchSpec){
		"relative output directory": func(spec *OutputBrokerLaunchSpec) { spec.OutputDirectory = "relative" },
		"missing stdout":            func(spec *OutputBrokerLaunchSpec) { spec.Stdout = nil },
		"missing stderr":            func(spec *OutputBrokerLaunchSpec) { spec.Stderr = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("OutputBrokerLaunchSpec.Validate() accepted incomplete spec")
			}
		})
	}
}

// TestSupervisorUsesOptionalOutputBrokerAttachment keeps the default direct-pipe path additive while proving broker records reach callers.
func TestSupervisorUsesOptionalOutputBrokerAttachment(t *testing.T) {
	projectID := domain.ProjectID("project-broker")
	sessionID := domain.SessionID("session-broker")
	peer := outputBrokerSupervisorTestPeer(t, projectID, sessionID)
	attachment := &supervisorOutputBrokerAttachment{
		peer:    peer,
		records: make(chan OutputBrokerRecord, 4),
		closed:  make(chan struct{}),
	}
	launcher := &supervisorOutputBrokerLauncher{attachment: attachment}
	installForjHelper(t, "exit")
	stdout := &synchronizedBuffer{}
	supervisor := newTestSupervisor(Options{OutputBrokerLauncher: launcher})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	startContext, cancelStart := context.WithCancel(t.Context())
	handle, err := supervisor.Start(startContext, StartRequest{
		ProjectID:            projectID,
		SessionID:            sessionID,
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               stdout,
		Stderr:               io.Discard,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancelStart()
	if info := handle.Info(); info.OutputBroker == nil || *info.OutputBroker != peer {
		t.Fatalf("Handle.Info().OutputBroker = %#v, want %#v", info.OutputBroker, peer)
	}
	launcher.mu.Lock()
	spec := launcher.spec
	launcher.mu.Unlock()
	if spec.ProjectID != projectID || spec.SessionID != sessionID || spec.Stdout == nil || spec.Stderr == nil {
		t.Fatalf("broker launch spec = %#v", spec)
	}
	attachment.records <- OutputBrokerRecord{Frame: &OutputBrokerFrame{
		Cursor:     0,
		NextCursor: uint64(len([]byte("broker-output\n"))),
		Stream:     OutputBrokerStreamStdout,
		Text:       "broker-output\n",
	}}
	if err := attachment.Close(); err != nil {
		t.Fatalf("attachment.Close() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("handle.Wait() error = %v", err)
	}
	waitForOutput(t, stdout, "broker-output")
}

// TestSupervisorBrokerLaunchFailureFallsBackToDirectPipes proves an optional handoff cannot block a started process.
func TestSupervisorBrokerLaunchFailureFallsBackToDirectPipes(t *testing.T) {
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{OutputBrokerLauncher: &supervisorOutputBrokerLauncher{err: errors.New("broker unavailable")}})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-broker-failure",
		SessionID:            "session-broker-failure",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               io.Discard,
		Stderr:               io.Discard,
	})
	if err != nil {
		t.Fatalf("Start() error = %v, want direct-pipe fallback", err)
	}
	if handle.Info().OutputBroker != nil {
		t.Fatalf("Handle.Info().OutputBroker = %#v, want direct-pipe fallback", handle.Info().OutputBroker)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("handle.Wait() error = %v", err)
	}
}
