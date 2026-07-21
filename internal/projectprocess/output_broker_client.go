package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
)

var errOutputBrokerEndpointNotReady = errors.New("output broker endpoint is not ready")

// OutputBrokerAttachmentConfig identifies one authenticated broker attachment.
//
// ObservedProcess must be collected immediately before this call from the broker PID named by the local
// transport. The full evidence comparison prevents a recycled PID or a look-alike endpoint from becoming
// output authority after a Harbor restart.
type OutputBrokerAttachmentConfig struct {
	// ProjectID identifies the registered project whose output is requested.
	ProjectID domain.ProjectID
	// SessionID identifies the exact lifecycle whose output is requested.
	SessionID domain.SessionID
	// EndpointReference identifies the owner-private broker endpoint.
	EndpointReference string
	// Cursor is the absolute output cursor from which replay should begin.
	Cursor uint64
	// Ticket proves the caller was issued this exact attachment credential.
	Ticket string
	// Peer is the expected broker process and endpoint evidence.
	Peer OutputBrokerPeer
	// ObservedProcess is a fresh observation of Peer.Process.
	ObservedProcess domain.ProcessEvidence
	// RevalidateProcess optionally rereads Peer.Process after the endpoint is connected and before authentication.
	//
	// Restart adoption supplies this callback because a persisted PID can be recycled between the initial
	// process census and the local endpoint dial. Existing in-process callers may omit it when their process
	// evidence was captured as part of the same launch boundary.
	RevalidateProcess func(domain.ProcessEvidence) (domain.ProcessEvidence, error)
}

// Validate reports whether an attachment request contains one exact broker identity.
func (config OutputBrokerAttachmentConfig) Validate() error {
	if err := config.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker attachment project ID: %w", err)
	}
	if err := config.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker attachment session ID: %w", err)
	}
	if err := validateOutputBrokerEndpointReference(config.EndpointReference); err != nil {
		return err
	}
	if config.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("output broker attachment cursor exceeds %d", domain.MaximumSequence)
	}
	if err := validateOutputBrokerToken("output broker attachment ticket", config.Ticket); err != nil {
		return err
	}
	if err := config.Peer.Validate(); err != nil {
		return fmt.Errorf("output broker attachment peer: %w", err)
	}
	if config.Peer.ProjectID != config.ProjectID || config.Peer.SessionID != config.SessionID || config.Peer.EndpointReference != config.EndpointReference {
		return errors.New("output broker attachment peer does not match lifecycle")
	}
	if err := config.ObservedProcess.Validate(); err != nil {
		return fmt.Errorf("output broker attachment observed process: %w", err)
	}
	if config.ObservedProcess != config.Peer.Process {
		return errors.New("output broker attachment observed process does not match peer")
	}
	return nil
}

// OutputBrokerClient is one authenticated replay/live stream and transport-only attachment.
type OutputBrokerClient struct {
	connection local.Conn
	reader     *rpc.FrameReader
	writer     *rpc.FrameWriter
	peer       OutputBrokerPeer
	replay     []OutputBrokerRecord
	replayAt   int
	readMutex  sync.Mutex
	closeOnce  sync.Once
	closeMutex sync.Mutex
	closeErr   error
}

// AttachOutputBroker dials, authenticates, and completes the challenge-confirmed broker handshake.
func AttachOutputBroker(ctx context.Context, config OutputBrokerAttachmentConfig) (*OutputBrokerClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	connection, err := local.DialAt(ctx, config.EndpointReference)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOutputBrokerEndpointNotReady, err)
	}
	closeConnection := true
	defer func() {
		if closeConnection {
			_ = connection.Close()
		}
	}()
	observedProcess := config.ObservedProcess
	if config.RevalidateProcess != nil {
		observedProcess, err = config.RevalidateProcess(config.Peer.Process)
		if err != nil {
			return nil, fmt.Errorf("revalidate output broker process before authentication: %w", err)
		}
	}
	if err := AuthenticateOutputBrokerPeer(connection, config.Peer, observedProcess); err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Now().Add(outputBrokerHandshakeTimeout)); err != nil {
		return nil, fmt.Errorf("set output broker client handshake deadline: %w", err)
	}
	reader := rpc.NewDefaultFrameReader(connection)
	writer := rpc.NewDefaultFrameWriter(connection)
	nonce, err := newOutputBrokerChallenge()
	if err != nil {
		return nil, fmt.Errorf("create output broker client nonce: %w", err)
	}
	hello := OutputBrokerHello{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         config.ProjectID,
		SessionID:         config.SessionID,
		EndpointReference: config.EndpointReference,
		Cursor:            config.Cursor,
		Ticket:            config.Ticket,
		ClientNonce:       nonce,
	}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeHello, Hello: &hello}); err != nil {
		return nil, fmt.Errorf("write output broker hello: %w", err)
	}
	challengeEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		return nil, fmt.Errorf("read output broker challenge: %w", err)
	}
	if challengeEnvelope.Kind == OutputBrokerEnvelopeError && challengeEnvelope.Error != nil {
		return nil, outputBrokerRemoteError(*challengeEnvelope.Error)
	}
	if challengeEnvelope.Kind != OutputBrokerEnvelopeChallenge || challengeEnvelope.Challenge == nil {
		return nil, errors.New("output broker returned no challenge")
	}
	challenge := *challengeEnvelope.Challenge
	if err := ValidateOutputBrokerChallengeCorrelation(hello, challenge); err != nil {
		return nil, err
	}
	if challenge.Peer != config.Peer {
		return nil, errors.New("output broker challenge peer does not match expected evidence")
	}
	confirm := OutputBrokerConfirm{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         challenge.ProjectID,
		SessionID:         challenge.SessionID,
		EndpointReference: challenge.EndpointReference,
		ClientNonce:       challenge.ClientNonce,
		Challenge:         challenge.Challenge,
	}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeConfirm, Confirm: &confirm}); err != nil {
		return nil, fmt.Errorf("write output broker confirmation: %w", err)
	}
	readyEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		return nil, fmt.Errorf("read output broker ready: %w", err)
	}
	if readyEnvelope.Kind == OutputBrokerEnvelopeError && readyEnvelope.Error != nil {
		return nil, outputBrokerRemoteError(*readyEnvelope.Error)
	}
	if readyEnvelope.Kind != OutputBrokerEnvelopeReady || readyEnvelope.Ready == nil {
		return nil, errors.New("output broker returned no ready message")
	}
	if err := readyEnvelope.Ready.Validate(); err != nil {
		return nil, err
	}
	if readyEnvelope.Ready.NextCursor != challenge.Replay.NextCursor {
		return nil, errors.New("output broker ready cursor does not match replay")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear output broker client handshake deadline: %w", err)
	}
	replay := make([]OutputBrokerRecord, 0, len(challenge.Replay.Frames))
	for index := range challenge.Replay.Frames {
		frame := challenge.Replay.Frames[index]
		replay = append(replay, OutputBrokerRecord{Frame: &frame})
	}
	client := &OutputBrokerClient{
		connection: connection,
		reader:     reader,
		writer:     writer,
		peer:       config.Peer,
		replay:     replay,
	}
	closeConnection = false
	return client, nil
}

// Peer returns the authenticated broker process evidence carried by this attachment.
func (client *OutputBrokerClient) Peer() OutputBrokerPeer {
	if client == nil {
		return OutputBrokerPeer{}
	}
	return client.peer
}

// Receive returns one replay or live record and acknowledges its durable cursor.
func (client *OutputBrokerClient) Receive(ctx context.Context) (OutputBrokerRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil || client.connection == nil {
		return OutputBrokerRecord{}, errors.New("output broker client is not initialized")
	}
	client.readMutex.Lock()
	defer client.readMutex.Unlock()
	if err := ctx.Err(); err != nil {
		return OutputBrokerRecord{}, err
	}
	if record, ok := client.nextReplay(); ok {
		if err := client.ack(record); err != nil {
			return OutputBrokerRecord{}, err
		}
		return record, nil
	}
	cancelConnection := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.connection.Close()
		case <-cancelConnection:
		}
	}()
	defer close(cancelConnection)
	envelope, err := ReadOutputBrokerEnvelope(client.reader)
	if err != nil {
		if ctx.Err() != nil {
			return OutputBrokerRecord{}, ctx.Err()
		}
		return OutputBrokerRecord{}, err
	}
	if envelope.Kind == OutputBrokerEnvelopeError && envelope.Error != nil {
		return OutputBrokerRecord{}, outputBrokerRemoteError(*envelope.Error)
	}
	if envelope.Kind != OutputBrokerEnvelopeRecord || envelope.Record == nil {
		return OutputBrokerRecord{}, errors.New("output broker returned an unexpected live message")
	}
	record := *envelope.Record
	if err := record.Validate(); err != nil {
		return OutputBrokerRecord{}, err
	}
	if err := client.ack(record); err != nil {
		return OutputBrokerRecord{}, err
	}
	return record, nil
}

// Close retires the attachment transport without signaling or reaping the managed child process.
func (client *OutputBrokerClient) Close() error {
	if client == nil || client.connection == nil {
		return nil
	}
	client.closeOnce.Do(func() {
		client.closeMutex.Lock()
		defer client.closeMutex.Unlock()
		client.closeErr = WriteOutputBrokerEnvelope(client.writer, OutputBrokerEnvelope{
			Version: OutputBrokerProtocolVersion,
			Kind:    OutputBrokerEnvelopeCommand,
			Command: &OutputBrokerCommand{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerCommandClose},
		})
		client.closeErr = errors.Join(client.closeErr, client.connection.Close())
	})
	client.closeMutex.Lock()
	err := client.closeErr
	client.closeMutex.Unlock()
	return err
}

// nextReplay advances the bounded handshake replay before reading the live connection.
func (client *OutputBrokerClient) nextReplay() (OutputBrokerRecord, bool) {
	if client.replayAt >= len(client.replay) {
		return OutputBrokerRecord{}, false
	}
	record := client.replay[client.replayAt]
	client.replayAt++
	return record, true
}

// ack advances the broker subscription after the caller has received one record.
func (client *OutputBrokerClient) ack(record OutputBrokerRecord) error {
	if record.Frame == nil {
		return nil
	}
	return WriteOutputBrokerEnvelope(client.writer, OutputBrokerEnvelope{
		Version: OutputBrokerProtocolVersion,
		Kind:    OutputBrokerEnvelopeCommand,
		Command: &OutputBrokerCommand{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerCommandAck, Cursor: record.Frame.NextCursor},
	})
}

// outputBrokerRemoteError keeps server diagnostics bounded and distinguishable from transport failures.
func outputBrokerRemoteError(brokerError OutputBrokerError) error {
	if err := brokerError.Validate(); err != nil {
		return err
	}
	return fmt.Errorf("output broker %s: %s", brokerError.Code, brokerError.Message)
}

// ObserveOutputBrokerProcessEvidence binds a broker PID to the expected executable and argument vector.
func ObserveOutputBrokerProcessEvidence(pid int64, executable string, arguments []string) (domain.ProcessEvidence, error) {
	if pid <= 0 {
		return domain.ProcessEvidence{}, errors.New("output broker process PID must be positive")
	}
	if len(arguments) == 0 {
		return domain.ProcessEvidence{}, errors.New("output broker process arguments are required")
	}
	canonical, err := canonicalExecutable(executable)
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("canonicalize output broker executable: %w", err)
	}
	if arguments[0] != canonical {
		return domain.ProcessEvidence{}, errors.New("output broker process arguments must begin with its canonical executable")
	}
	birthToken, err := processBirthToken(int(pid))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("read output broker process birth: %w", err)
	}
	evidence := domain.ProcessEvidence{
		PID:                pid,
		BirthToken:         birthToken,
		ExecutableIdentity: canonical,
		ArgumentDigest:     digestArguments(arguments),
	}
	if err := evidence.Validate(); err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("validate output broker process evidence: %w", err)
	}
	return evidence, nil
}

// validateOutputBrokerLauncherPath keeps executable setup independent of the current working directory.
func validateOutputBrokerLauncherPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("output broker executable must be a canonical absolute path")
	}
	return canonicalExecutable(path)
}
