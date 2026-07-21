package projectprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc/local"
)

const maximumOutputBrokerLaunchConfigBytes = 16 * 1024

// OutputBrokerLaunchConfig is the owner-private, non-process portion of one broker launch manifest.
type OutputBrokerLaunchConfig struct {
	// Version identifies the launch manifest schema.
	Version uint16 `json:"version"`
	// ProjectID identifies the registered project whose output is retained.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID identifies the exact lifecycle whose output is retained.
	SessionID domain.SessionID `json:"session_id"`
	// OutputDirectory is the owner-private root used for the checksummed journal.
	OutputDirectory string `json:"output_directory"`
	// EndpointReference is the owner-private Unix socket or Windows named pipe.
	EndpointReference string `json:"endpoint_reference"`
	// AttachmentTicket is the opaque credential required for Harbor attachment.
	AttachmentTicket string `json:"attachment_ticket"`
}

// Validate reports whether a launch manifest is complete without containing process authority.
func (config OutputBrokerLaunchConfig) Validate() error {
	if config.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker launch config version %d is unsupported", config.Version)
	}
	if err := config.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker launch config project ID: %w", err)
	}
	if err := config.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker launch config session ID: %w", err)
	}
	if config.OutputDirectory == "" || !filepath.IsAbs(config.OutputDirectory) || filepath.Clean(config.OutputDirectory) != config.OutputDirectory {
		return errors.New("output broker launch config output directory must be a canonical absolute path")
	}
	if err := validateOutputBrokerEndpointReference(config.EndpointReference); err != nil {
		return err
	}
	return validateOutputBrokerToken("output broker launch config attachment ticket", config.AttachmentTicket)
}

// ReadOutputBrokerLaunchConfig reads one owner-private canonical launch manifest without following a symlink.
func ReadOutputBrokerLaunchConfig(path string) (OutputBrokerLaunchConfig, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return OutputBrokerLaunchConfig{}, errors.New("output broker launch config path must be a canonical absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return OutputBrokerLaunchConfig{}, fmt.Errorf("inspect output broker launch config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return OutputBrokerLaunchConfig{}, errors.New("output broker launch config must be a direct regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return OutputBrokerLaunchConfig{}, errors.New("output broker launch config must not be readable by group or other users")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return OutputBrokerLaunchConfig{}, fmt.Errorf("read output broker launch config: %w", err)
	}
	if len(body) == 0 || len(body) > maximumOutputBrokerLaunchConfigBytes {
		return OutputBrokerLaunchConfig{}, fmt.Errorf("output broker launch config must contain 1..%d bytes", maximumOutputBrokerLaunchConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config OutputBrokerLaunchConfig
	if err := decoder.Decode(&config); err != nil {
		return OutputBrokerLaunchConfig{}, fmt.Errorf("decode output broker launch config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OutputBrokerLaunchConfig{}, errors.New("decode output broker launch config: content must contain exactly one JSON value")
	}
	canonical, err := json.Marshal(config)
	if err != nil {
		return OutputBrokerLaunchConfig{}, fmt.Errorf("encode output broker launch config: %w", err)
	}
	if !bytes.Equal(body, canonical) {
		return OutputBrokerLaunchConfig{}, errors.New("decode output broker launch config: content is not canonical")
	}
	if err := config.Validate(); err != nil {
		return OutputBrokerLaunchConfig{}, err
	}
	return config, nil
}

// OutputBrokerRuntimeConfig describes one standalone broker process and its inherited output pipes.
type OutputBrokerRuntimeConfig struct {
	// ProjectID identifies the registered project whose output is retained.
	ProjectID domain.ProjectID
	// SessionID identifies the exact lifecycle whose output is retained.
	SessionID domain.SessionID
	// OutputDirectory is the owner-private root used for the checksummed journal.
	OutputDirectory string
	// EndpointReference is the owner-private Unix socket or Windows named pipe.
	EndpointReference string
	// AttachmentTicket is the opaque credential required for Harbor attachment.
	AttachmentTicket string
	// Process is the exact evidence for this broker process.
	Process domain.ProcessEvidence
	// Stdout is the inherited child standard-output pipe.
	Stdout io.Reader
	// Stderr is the inherited child standard-error pipe.
	Stderr io.Reader
}

// RunOutputBroker owns inherited output pipes, appends them durably, and serves replay/live clients until both pipes end.
func RunOutputBroker(ctx context.Context, config OutputBrokerRuntimeConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOutputBrokerRuntimeConfig(config); err != nil {
		return err
	}
	currentProcess, err := CaptureCurrentProcessEvidence()
	if err != nil {
		return fmt.Errorf("capture output broker process evidence: %w", err)
	}
	if currentProcess != config.Process {
		return errors.New("output broker launch process evidence does not match the running process")
	}
	journal, err := OpenOutputBrokerJournal(config.OutputDirectory, config.ProjectID, config.SessionID)
	if err != nil {
		return fmt.Errorf("open output broker journal: %w", err)
	}
	defer journal.Close()
	listener, err := local.ListenAt(config.EndpointReference)
	if err != nil {
		return fmt.Errorf("listen on output broker endpoint: %w", err)
	}
	defer listener.Close()
	server, err := NewOutputBrokerServer(OutputBrokerServerConfig{
		Listener:         listener,
		Journal:          journal,
		Peer:             OutputBrokerPeer{ProjectID: config.ProjectID, SessionID: config.SessionID, EndpointReference: config.EndpointReference, Process: config.Process},
		AttachmentTicket: config.AttachmentTicket,
	})
	if err != nil {
		return fmt.Errorf("construct output broker server: %w", err)
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(runContext) }()

	inputs := make(chan outputBrokerInput, 128)
	readErrors := make(chan error, 2)
	var readers sync.WaitGroup
	readers.Add(2)
	go readOutputBrokerInput(runContext, &readers, inputs, readErrors, OutputBrokerStreamStdout, config.Stdout)
	go readOutputBrokerInput(runContext, &readers, inputs, readErrors, OutputBrokerStreamStderr, config.Stderr)
	go func() {
		readers.Wait()
		close(inputs)
	}()

	cursor := journal.NextCursor()
	var result error
	for inputs != nil {
		select {
		case <-runContext.Done():
			result = runContext.Err()
			inputs = nil
		case readErr := <-readErrors:
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				result = fmt.Errorf("read output broker pipe: %w", readErr)
				cancel()
				inputs = nil
			}
		case input, open := <-inputs:
			if !open {
				inputs = nil
				cancel()
				continue
			}
			frame, appendErr := journal.Append(input.stream, cursor, input.bytes)
			if appendErr != nil {
				result = fmt.Errorf("append output broker pipe: %w", appendErr)
				cancel()
				inputs = nil
				continue
			}
			cursor = frame.NextCursor
		}
	}
	if err := journal.Close(); err != nil && result == nil {
		result = fmt.Errorf("close output broker journal: %w", err)
	}
	cancel()
	if serverErr := <-serverDone; serverErr != nil && result == nil && !errors.Is(serverErr, context.Canceled) {
		result = serverErr
	}
	return result
}

// outputBrokerInput serializes one inherited pipe fragment before the journal assigns its global cursor.
type outputBrokerInput struct {
	stream OutputBrokerStream
	bytes  []byte
}

// readOutputBrokerInput drains one inherited pipe without allowing stdout and stderr to race journal cursors.
func readOutputBrokerInput(ctx context.Context, readers *sync.WaitGroup, inputs chan<- outputBrokerInput, readErrors chan<- error, stream OutputBrokerStream, reader io.Reader) {
	defer readers.Done()
	buffer := make([]byte, outputReadBufferBytes)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			body := append([]byte(nil), buffer[:count]...)
			select {
			case inputs <- outputBrokerInput{stream: stream, bytes: body}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErrors <- err
			}
			return
		}
	}
}

// validateOutputBrokerRuntimeConfig rejects incomplete process and pipe ownership before any endpoint is created.
func validateOutputBrokerRuntimeConfig(config OutputBrokerRuntimeConfig) error {
	if err := config.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker project ID: %w", err)
	}
	if err := config.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker session ID: %w", err)
	}
	if config.OutputDirectory == "" || !filepath.IsAbs(config.OutputDirectory) || filepath.Clean(config.OutputDirectory) != config.OutputDirectory {
		return errors.New("output broker output directory must be a canonical absolute path")
	}
	if err := validateOutputBrokerEndpointReference(config.EndpointReference); err != nil {
		return err
	}
	if err := validateOutputBrokerToken("output broker attachment ticket", config.AttachmentTicket); err != nil {
		return err
	}
	if err := config.Process.Validate(); err != nil {
		return fmt.Errorf("output broker process evidence: %w", err)
	}
	if config.Stdout == nil || config.Stderr == nil {
		return errors.New("output broker stdout and stderr readers are required")
	}
	return nil
}

// CaptureCurrentProcessEvidence returns immutable evidence for the process that will own a broker endpoint.
func CaptureCurrentProcessEvidence() (domain.ProcessEvidence, error) {
	executable, err := os.Executable()
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("resolve current output broker executable: %w", err)
	}
	executable, err = canonicalExecutable(executable)
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("canonicalize current output broker executable: %w", err)
	}
	birthToken, err := processBirthToken(os.Getpid())
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("read current output broker birth: %w", err)
	}
	evidence := domain.ProcessEvidence{
		PID:                int64(os.Getpid()),
		BirthToken:         birthToken,
		ExecutableIdentity: executable,
		ArgumentDigest:     digestArguments(os.Args),
	}
	if err := evidence.Validate(); err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("validate current output broker evidence: %w", err)
	}
	return evidence, nil
}
