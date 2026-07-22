package projectprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const outputBrokerExecutableBaseName = "outputbroker"

// OutputBrokerProcessLauncher starts the standalone broker beside the Harbor daemon and attaches its stream.
//
// It owns only the broker process it starts. The managed GoForj process remains under Supervisor's platform
// process boundary, and the returned attachment's Close method retires only this broker transport/process.
type OutputBrokerProcessLauncher struct {
	executable string
}

// NewOutputBrokerProcessLauncher admits one canonical, regular broker executable.
func NewOutputBrokerProcessLauncher(executable string) (*OutputBrokerProcessLauncher, error) {
	canonical, err := validateOutputBrokerLauncherPath(executable)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("inspect output broker executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("output broker executable must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("output broker executable must be executable")
	}
	return &OutputBrokerProcessLauncher{executable: canonical}, nil
}

// NewSiblingOutputBrokerProcessLauncher resolves the broker beside the running Harbor daemon.
//
// Missing packaged or development artifacts return an error so callers can retain the direct-pipe fallback
// until the broker binary is installed; the error does not itself make Supervisor startup fail.
func NewSiblingOutputBrokerProcessLauncher() (*OutputBrokerProcessLauncher, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve Harbor executable for output broker: %w", err)
	}
	canonical, err := canonicalExecutable(executable)
	if err != nil {
		return nil, fmt.Errorf("canonicalize Harbor executable for output broker: %w", err)
	}
	name := outputBrokerExecutableBaseName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return NewOutputBrokerProcessLauncher(filepath.Join(filepath.Dir(canonical), name))
}

// Launch starts one broker, transfers the inherited pipe ends, and completes its authenticated attachment.
func (launcher *OutputBrokerProcessLauncher) Launch(ctx context.Context, spec OutputBrokerLaunchSpec) (OutputBrokerAttachment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if launcher == nil || launcher.executable == "" {
		return nil, errors.New("output broker process launcher is not initialized")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outputDirectory, err := prepareOutputSpoolDirectory(spec.OutputDirectory)
	if err != nil {
		return nil, fmt.Errorf("prepare output broker directory: %w", err)
	}
	endpoint, err := newOutputBrokerEndpointReference()
	if err != nil {
		return nil, err
	}
	ticket, err := newOutputBrokerChallenge()
	if err != nil {
		return nil, fmt.Errorf("create output broker attachment ticket: %w", err)
	}
	configPath, err := writeOutputBrokerLaunchConfig(outputDirectory, OutputBrokerLaunchConfig{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         spec.ProjectID,
		SessionID:         spec.SessionID,
		OutputDirectory:   spec.OutputDirectory,
		EndpointReference: endpoint,
		AttachmentTicket:  ticket,
	})
	if err != nil {
		return nil, err
	}
	removeConfig := true
	defer func() {
		if removeConfig {
			_ = os.Remove(configPath)
		}
	}()
	command, arguments, err := prepareOutputBrokerProcess(launcher.executable, configPath, spec.Stdout, spec.Stderr)
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start output broker: %w", err)
	}
	terminate := true
	defer func() {
		if terminate {
			_ = terminateOutputBrokerProcess(command)
		}
	}()
	process, err := ObserveOutputBrokerProcessEvidence(int64(command.PID()), launcher.executable, arguments)
	if err != nil {
		return nil, err
	}
	peer := OutputBrokerPeer{
		ProjectID:         spec.ProjectID,
		SessionID:         spec.SessionID,
		EndpointReference: endpoint,
		Process:           process,
		ManifestPath:      configPath,
		TicketDigest:      DigestOutputBrokerTicket(ticket),
	}
	attachContext, cancel := context.WithTimeout(ctx, outputBrokerHandshakeTimeout)
	client, err := attachOutputBrokerWithRetry(attachContext, OutputBrokerAttachmentConfig{
		ProjectID:         spec.ProjectID,
		SessionID:         spec.SessionID,
		EndpointReference: endpoint,
		Ticket:            ticket,
		Peer:              peer,
		ObservedProcess:   process,
	})
	cancel()
	if err != nil {
		return nil, err
	}
	removeConfig = false
	terminate = false
	go func() { _ = command.Wait() }()
	return &outputBrokerProcessAttachment{client: client, process: command, manifestPath: configPath}, nil
}

// Adopt reattaches Harbor to a broker that survived the daemon which launched it.
//
// The manifest is read only after its canonical owner-private path is checked against the configured
// runtime root. The raw ticket stays in that manifest, while the persisted process evidence is reread
// from the host before the authenticated endpoint handshake. An adopted attachment never owns broker
// termination; the broker exits naturally when its inherited child pipes reach EOF.
func (launcher *OutputBrokerProcessLauncher) Adopt(ctx context.Context, spec OutputBrokerAdoptionSpec) (OutputBrokerAttachment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if launcher == nil || launcher.executable == "" {
		return nil, errors.New("output broker process launcher is not initialized")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifestDirectory := filepath.Join(spec.OutputDirectory, outputSpoolDirectoryName)
	if filepath.Dir(spec.Peer.ManifestPath) != manifestDirectory {
		return nil, errors.New("output broker manifest is outside the configured owner-private runtime directory")
	}
	config, err := ReadOutputBrokerLaunchConfig(spec.Peer.ManifestPath)
	if err != nil {
		return nil, err
	}
	if config.ProjectID != spec.ProjectID || config.SessionID != spec.SessionID {
		return nil, errors.New("output broker manifest lifecycle does not match durable session")
	}
	if config.OutputDirectory != spec.OutputDirectory || config.EndpointReference != spec.Peer.EndpointReference {
		return nil, errors.New("output broker manifest runtime identity does not match durable session")
	}
	if DigestOutputBrokerTicket(config.AttachmentTicket) != spec.Peer.TicketDigest {
		return nil, errors.New("output broker manifest ticket digest does not match durable session")
	}
	observed, err := ObservePersistedOutputBrokerProcessEvidence(spec.Peer.Process)
	if err != nil {
		return nil, err
	}
	attachContext, cancel := context.WithTimeout(ctx, outputBrokerHandshakeTimeout)
	client, err := attachOutputBrokerWithRetry(attachContext, OutputBrokerAttachmentConfig{
		ProjectID:         spec.ProjectID,
		SessionID:         spec.SessionID,
		EndpointReference: spec.Peer.EndpointReference,
		Ticket:            config.AttachmentTicket,
		Peer:              spec.Peer,
		ObservedProcess:   observed,
		RevalidateProcess: ObservePersistedOutputBrokerProcessEvidence,
	})
	cancel()
	if err != nil {
		return nil, err
	}
	return &outputBrokerAdoptedAttachment{client: client}, nil
}

// writeOutputBrokerLaunchConfig publishes one complete owner-private manifest before the broker is started.
func writeOutputBrokerLaunchConfig(directory string, config OutputBrokerLaunchConfig) (string, error) {
	if err := config.Validate(); err != nil {
		return "", fmt.Errorf("validate output broker launch config: %w", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode output broker launch config: %w", err)
	}
	token, err := newOutputBrokerChallenge()
	if err != nil {
		return "", fmt.Errorf("create output broker launch config name: %w", err)
	}
	path := filepath.Join(directory, "output-broker-"+token+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create output broker launch config: %w", err)
	}
	var writeErr error
	if _, writeErr = file.Write(encoded); writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		var encodedErr error
		if writeErr != nil {
			encodedErr = fmt.Errorf("write output broker launch config: %w", writeErr)
		}
		return "", errors.Join(encodedErr, closeErr)
	}
	return path, nil
}

// newOutputBrokerEndpointReference creates a short per-user local endpoint without using a checkout path.
func newOutputBrokerEndpointReference() (string, error) {
	token, err := newOutputBrokerEndpointToken()
	if err != nil {
		return "", fmt.Errorf("create output broker endpoint name: %w", err)
	}
	if runtime.GOOS == "windows" {
		return `\\.\pipe\goforj-harbor-output-` + token, nil
	}
	directory, err := runtimepath.OutputBrokerDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve output broker runtime directory: %w", err)
	}
	return filepath.Join(directory, "output-"+token+".sock"), nil
}

// attachOutputBrokerWithRetry tolerates only the bounded startup race while the broker binds its endpoint.
func attachOutputBrokerWithRetry(ctx context.Context, config OutputBrokerAttachmentConfig) (*OutputBrokerClient, error) {
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, errors.Join(lastErr, err)
			}
			return nil, err
		}
		client, err := AttachOutputBroker(ctx, config)
		if err == nil {
			return client, nil
		}
		if !errors.Is(err, errOutputBrokerEndpointNotReady) {
			return nil, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// terminateOutputBrokerProcess retires a broker that failed before its attachment became usable.
func terminateOutputBrokerProcess(command outputBrokerCommand) error {
	if command == nil {
		return nil
	}
	return command.Terminate()
}

// outputBrokerCommand keeps process setup testable without exposing an operating-system process handle.
type outputBrokerCommand interface {
	Start() error
	Wait() error
	Terminate() error
	PID() int
}

// outputBrokerProcess adapts exec.Cmd to the launcher cleanup boundary.
type outputBrokerProcess struct {
	command  *os.Process
	start    func() error
	wait     func() error
	waitOnce sync.Once
	waitDone chan struct{}
	waitErr  error
	kill     sync.Once
	killErr  error
}

// Start starts the broker process after its owner-private manifest and inherited pipes are ready.
func (process *outputBrokerProcess) Start() error {
	if err := process.start(); err != nil {
		return err
	}
	process.waitDone = make(chan struct{})
	return nil
}

// Wait reaps the launched broker process.
func (process *outputBrokerProcess) Wait() error {
	if process == nil || process.command == nil || process.waitDone == nil {
		return errors.New("output broker process is not started")
	}
	process.waitOnce.Do(func() {
		go func() {
			process.waitErr = process.wait()
			close(process.waitDone)
		}()
	})
	<-process.waitDone
	return process.waitErr
}

// Terminate kills and reaps the launched broker process exactly once.
func (process *outputBrokerProcess) Terminate() error {
	process.kill.Do(func() {
		if process.command != nil {
			process.killErr = process.command.Kill()
			if errors.Is(process.killErr, os.ErrProcessDone) {
				process.killErr = nil
			}
		}
	})
	return errors.Join(process.killErr, process.Wait())
}

// PID returns the operating-system process identity captured after Start.
func (process *outputBrokerProcess) PID() int {
	if process.command == nil {
		return 0
	}
	return process.command.Pid
}

// outputBrokerProcessAttachment couples transport cleanup to the broker process it launched without acquiring child authority.
type outputBrokerProcessAttachment struct {
	client       *OutputBrokerClient
	process      outputBrokerCommand
	manifestPath string
}

// outputBrokerAdoptedAttachment owns only a fresh reader transport; the surviving broker remains independent.
type outputBrokerAdoptedAttachment struct {
	client *OutputBrokerClient
}

// Peer returns the authenticated broker evidence carried by this adopted transport.
func (attachment *outputBrokerAdoptedAttachment) Peer() OutputBrokerPeer {
	if attachment == nil || attachment.client == nil {
		return OutputBrokerPeer{}
	}
	return attachment.client.Peer()
}

// Receive returns one replay or live record from the adopted broker.
func (attachment *outputBrokerAdoptedAttachment) Receive(ctx context.Context) (OutputBrokerRecord, error) {
	if attachment == nil || attachment.client == nil {
		return OutputBrokerRecord{}, errors.New("adopted output broker attachment is not initialized")
	}
	return attachment.client.Receive(ctx)
}

// Close retires only the adopted transport and never signals the surviving broker process.
func (attachment *outputBrokerAdoptedAttachment) Close() error {
	if attachment == nil || attachment.client == nil {
		return nil
	}
	return attachment.client.Close()
}

// Peer returns the authenticated broker evidence carried by the transport client.
func (attachment *outputBrokerProcessAttachment) Peer() OutputBrokerPeer {
	if attachment == nil || attachment.client == nil {
		return OutputBrokerPeer{}
	}
	return attachment.client.Peer()
}

// Receive returns one replay or live record from the broker transport.
func (attachment *outputBrokerProcessAttachment) Receive(ctx context.Context) (OutputBrokerRecord, error) {
	if attachment == nil || attachment.client == nil {
		return OutputBrokerRecord{}, errors.New("output broker process attachment is not initialized")
	}
	return attachment.client.Receive(ctx)
}

// Close retires the broker transport and only the broker process launched for that transport.
func (attachment *outputBrokerProcessAttachment) Close() error {
	if attachment == nil {
		return nil
	}
	var closeErr error
	if attachment.client != nil {
		closeErr = attachment.client.Close()
	}
	if attachment.manifestPath != "" {
		if err := os.Remove(attachment.manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			closeErr = errors.Join(closeErr, fmt.Errorf("retire output broker manifest: %w", err))
		}
	}
	if attachment.process != nil {
		_ = attachment.process.Terminate()
	}
	return closeErr
}
