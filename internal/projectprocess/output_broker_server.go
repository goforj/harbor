package projectprocess

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
)

const (
	outputBrokerHandshakeTimeout    = 5 * time.Second
	outputBrokerSubscriptionBuffer  = 256
	outputBrokerCommandBuffer       = 4
	outputBrokerProtocolErrorCode   = "protocol_error"
	outputBrokerAuthenticationCode  = "authentication_failed"
	outputBrokerJournalErrorCode    = "journal_unavailable"
	outputBrokerAcknowledgementCode = "invalid_acknowledgement"
)

// OutputBrokerServerConfig supplies the process-local authority for one broker endpoint.
type OutputBrokerServerConfig struct {
	// Listener is the owner-authenticated endpoint retained by the broker process.
	Listener local.Listener
	// Journal is the append-before-notify output journal owned by this broker.
	Journal *OutputBrokerJournal
	// Peer is the broker process proof advertised to Harbor clients.
	Peer OutputBrokerPeer
	// AttachmentTicket is the opaque credential required before replay or live output is exposed.
	AttachmentTicket string
}

// OutputBrokerServer accepts authenticated replay/live attachments without lifecycle authority.
type OutputBrokerServer struct {
	listener local.Listener
	journal  *OutputBrokerJournal
	peer     OutputBrokerPeer
	ticket   string
}

// NewOutputBrokerServer validates one process-local broker authority before accepting connections.
func NewOutputBrokerServer(config OutputBrokerServerConfig) (*OutputBrokerServer, error) {
	if config.Listener == nil {
		return nil, errors.New("output broker server listener is required")
	}
	if config.Journal == nil {
		return nil, errors.New("output broker server journal is required")
	}
	if err := config.Peer.Validate(); err != nil {
		return nil, fmt.Errorf("validate output broker server peer: %w", err)
	}
	if err := validateOutputBrokerToken("output broker server attachment ticket", config.AttachmentTicket); err != nil {
		return nil, err
	}
	return &OutputBrokerServer{
		listener: config.Listener,
		journal:  config.Journal,
		peer:     config.Peer,
		ticket:   config.AttachmentTicket,
	}, nil
}

// Serve accepts multiple independent clients until the listener or context becomes terminal.
func (server *OutputBrokerServer) Serve(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if server == nil || server.listener == nil {
		return errors.New("output broker server is not initialized")
	}
	stopContext := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.listener.Close()
		case <-stopContext:
		}
	}()
	defer close(stopContext)

	var workers sync.WaitGroup
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				workers.Wait()
				return nil
			}
			workers.Wait()
			return fmt.Errorf("accept output broker client: %w", err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			_ = server.ServeConnection(ctx, connection)
		}()
	}
}

// ServeConnection performs one challenge-confirmed replay/live attachment.
func (server *OutputBrokerServer) ServeConnection(ctx context.Context, connection local.Conn) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if server == nil || server.journal == nil {
		return errors.New("output broker server is not initialized")
	}
	if connection == nil {
		return errors.New("output broker connection is required")
	}
	defer connection.Close()
	if err := validateOutputBrokerTransportPeer(connection.Peer()); err != nil {
		return err
	}
	stopConnection := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-stopConnection:
		}
	}()
	defer close(stopConnection)

	if err := connection.SetDeadline(time.Now().Add(outputBrokerHandshakeTimeout)); err != nil {
		return fmt.Errorf("set output broker handshake deadline: %w", err)
	}
	reader := rpc.NewDefaultFrameReader(connection)
	writer := rpc.NewDefaultFrameWriter(connection)
	helloEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		return fmt.Errorf("read output broker hello: %w", err)
	}
	if helloEnvelope.Kind != OutputBrokerEnvelopeHello || helloEnvelope.Hello == nil {
		return server.writeProtocolError(writer, outputBrokerProtocolErrorCode, "output broker expected a hello")
	}
	hello := *helloEnvelope.Hello
	if err := server.validateHello(hello, connection.Peer()); err != nil {
		return server.writeProtocolError(writer, outputBrokerAuthenticationCode, "output broker hello was rejected")
	}
	replay, subscription, err := server.journal.Subscribe(hello.Cursor, outputBrokerSubscriptionBuffer)
	if err != nil {
		return server.writeProtocolError(writer, outputBrokerJournalErrorCode, "output broker replay is unavailable")
	}
	defer subscription.Close()
	challengeToken, err := newOutputBrokerChallenge()
	if err != nil {
		return fmt.Errorf("create output broker challenge: %w", err)
	}
	challenge := OutputBrokerChallenge{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         hello.ProjectID,
		SessionID:         hello.SessionID,
		EndpointReference: hello.EndpointReference,
		ClientNonce:       hello.ClientNonce,
		Challenge:         challengeToken,
		Peer:              server.peer,
		Replay:            replay,
	}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeChallenge, Challenge: &challenge}); err != nil {
		return fmt.Errorf("write output broker challenge: %w", err)
	}
	confirmEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		return fmt.Errorf("read output broker confirmation: %w", err)
	}
	if confirmEnvelope.Kind != OutputBrokerEnvelopeConfirm || confirmEnvelope.Confirm == nil {
		return server.writeProtocolError(writer, outputBrokerProtocolErrorCode, "output broker expected confirmation")
	}
	if err := ValidateOutputBrokerConfirmCorrelation(challenge, *confirmEnvelope.Confirm); err != nil {
		return server.writeProtocolError(writer, outputBrokerAuthenticationCode, "output broker confirmation was rejected")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear output broker handshake deadline: %w", err)
	}
	ready := OutputBrokerReady{Version: OutputBrokerProtocolVersion, NextCursor: replay.NextCursor}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeReady, Ready: &ready}); err != nil {
		return fmt.Errorf("write output broker ready message: %w", err)
	}
	return server.serveAttachedConnection(ctx, connection, reader, writer, subscription)
}

// validateHello binds a request to this exact broker configuration and peer transport.
func (server *OutputBrokerServer) validateHello(hello OutputBrokerHello, identity local.PeerIdentity) error {
	if err := hello.Validate(); err != nil {
		return err
	}
	if hello.ProjectID != server.peer.ProjectID || hello.SessionID != server.peer.SessionID || hello.EndpointReference != server.peer.EndpointReference {
		return errors.New("output broker hello lifecycle does not match server")
	}
	if !sameOutputBrokerToken(hello.Ticket, server.ticket) {
		return errors.New("output broker attachment ticket is invalid")
	}
	return validateOutputBrokerTransportPeer(identity)
}

// serveAttachedConnection forwards live records while a command reader advances acknowledgements.
func (server *OutputBrokerServer) serveAttachedConnection(
	ctx context.Context,
	connection local.Conn,
	reader *rpc.FrameReader,
	writer *rpc.FrameWriter,
	subscription *OutputBrokerSubscription,
) error {
	commands := make(chan OutputBrokerCommand, outputBrokerCommandBuffer)
	readErrors := make(chan error, 1)
	serveDone := make(chan struct{})
	defer close(serveDone)
	go func() {
		for {
			envelope, err := ReadOutputBrokerEnvelope(reader)
			if err != nil {
				readErrors <- err
				return
			}
			if envelope.Kind != OutputBrokerEnvelopeCommand || envelope.Command == nil {
				readErrors <- errors.New("output broker received a non-command after attachment")
				return
			}
			select {
			case commands <- *envelope.Command:
			case <-ctx.Done():
				return
			case <-serveDone:
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read output broker command: %w", err)
		case command := <-commands:
			switch command.Kind {
			case OutputBrokerCommandClose:
				return nil
			case OutputBrokerCommandAck:
				if err := subscription.Ack(command.Cursor); err != nil {
					_ = server.writeProtocolError(writer, outputBrokerAcknowledgementCode, "output broker acknowledgement was rejected")
					return err
				}
			}
		case record, open := <-subscription.Records():
			if !open {
				return nil
			}
			recordEnvelope := OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeRecord, Record: &record}
			if err := WriteOutputBrokerEnvelope(writer, recordEnvelope); err != nil {
				return fmt.Errorf("write output broker record: %w", err)
			}
		}
	}
}

// writeProtocolError returns the bounded error after attempting to tell a connected client why the stream ended.
func (server *OutputBrokerServer) writeProtocolError(writer *rpc.FrameWriter, code, message string) error {
	response := OutputBrokerError{Version: OutputBrokerProtocolVersion, Code: code, Message: message}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeError, Error: &response}); err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}

// validateOutputBrokerTransportPeer keeps server-side transport identities bounded before protocol use.
func validateOutputBrokerTransportPeer(identity local.PeerIdentity) error {
	if identity.UserID == "" || strings.TrimSpace(identity.UserID) != identity.UserID || !utf8.ValidString(identity.UserID) {
		return errors.New("output broker transport peer user identity is invalid")
	}
	for _, character := range identity.UserID {
		if unicode.IsControl(character) {
			return errors.New("output broker transport peer user identity contains a control character")
		}
	}
	if identity.ProcessID == 0 {
		return errors.New("output broker transport peer process identity is invalid")
	}
	return nil
}

// sameOutputBrokerToken compares bearer values without exposing length or content through the equality operation.
func sameOutputBrokerToken(left, right string) bool {
	leftDigest := sha256.Sum256([]byte(left))
	rightDigest := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}
