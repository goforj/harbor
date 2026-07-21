package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
)

// outputBrokerServerTestListener keeps server construction independent from endpoint lifecycle tests.
type outputBrokerServerTestListener struct{}

// Accept is unused by ServeConnection tests.
func (outputBrokerServerTestListener) Accept() (local.Conn, error) {
	return nil, errors.New("test listener does not accept")
}

// Close is a no-op for the in-memory server listener.
func (outputBrokerServerTestListener) Close() error {
	return nil
}

// Addr returns the exact endpoint advertised by the broker test proof.
func (outputBrokerServerTestListener) Addr() net.Addr {
	return outputBrokerTestAddress("broker-test")
}

// outputBrokerTestAddress is a deterministic endpoint for the listener contract.
type outputBrokerTestAddress string

// Network identifies the test endpoint transport.
func (outputBrokerTestAddress) Network() string {
	return "broker-test"
}

// String returns the deterministic test endpoint name.
func (address outputBrokerTestAddress) String() string {
	return string(address)
}

// outputBrokerServerTestConnection pairs a net.Pipe with authenticated local identity metadata.
type outputBrokerServerTestConnection struct {
	net.Conn
	peer     local.PeerIdentity
	endpoint string
}

// Peer returns the controlled operating-system identity for the test connection.
func (connection outputBrokerServerTestConnection) Peer() local.PeerIdentity {
	return connection.peer
}

// EndpointReference returns the controlled endpoint captured by the test transport.
func (connection outputBrokerServerTestConnection) EndpointReference() string {
	return connection.endpoint
}

// outputBrokerServerTestProof returns one complete server peer proof.
func outputBrokerServerTestProof(t *testing.T) OutputBrokerPeer {
	t.Helper()
	return OutputBrokerPeer{
		ProjectID:         "project-broker-server",
		SessionID:         "session-broker-server",
		EndpointReference: filepath.Join(t.TempDir(), "broker.sock"),
		Process: domain.ProcessEvidence{
			PID:                8124,
			BirthToken:         "broker-server-birth",
			ExecutableIdentity: filepath.Join(t.TempDir(), "harbor-output-broker"),
			ArgumentDigest:     strings.Repeat("a", 64),
		},
	}
}

// newOutputBrokerServerTest constructs one journal-backed server with a controlled endpoint.
func newOutputBrokerServerTest(t *testing.T) (*OutputBrokerServer, OutputBrokerPeer) {
	t.Helper()
	proof := outputBrokerServerTestProof(t)
	journal, err := OpenOutputBrokerJournal(t.TempDir(), proof.ProjectID, proof.SessionID)
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	server, err := NewOutputBrokerServer(OutputBrokerServerConfig{
		Listener:         outputBrokerServerTestListener{},
		Journal:          journal,
		Peer:             proof,
		AttachmentTicket: "broker-ticket-1",
	})
	if err != nil {
		t.Fatalf("NewOutputBrokerServer() error = %v", err)
	}
	return server, proof
}

// TestOutputBrokerEnvelopeRoundTripKeepsCanonicalUnionBoundaries proves future transports cannot accept ambiguous payload shapes.
func TestOutputBrokerEnvelopeRoundTripKeepsCanonicalUnionBoundaries(t *testing.T) {
	proof := outputBrokerServerTestProof(t)
	frame := OutputBrokerFrame{Cursor: 0, NextCursor: 5, Stream: OutputBrokerStreamStdout, Text: "hello"}
	envelope := OutputBrokerEnvelope{
		Version: OutputBrokerProtocolVersion,
		Kind:    OutputBrokerEnvelopeRecord,
		Record:  &OutputBrokerRecord{Frame: &frame},
	}
	payload, err := MarshalOutputBrokerEnvelope(envelope)
	if err != nil {
		t.Fatalf("MarshalOutputBrokerEnvelope() error = %v", err)
	}
	decoded, err := DecodeOutputBrokerEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeOutputBrokerEnvelope() error = %v", err)
	}
	if decoded.Kind != envelope.Kind || decoded.Record == nil || decoded.Record.Frame == nil || decoded.Record.Frame.Text != "hello" {
		t.Fatalf("decoded envelope = %#v, want record hello", decoded)
	}
	for index, candidate := range []string{
		string(payload) + "\n",
		strings.Replace(string(payload), `"kind":"record"`, `"kind":"record","unknown":true`, 1),
	} {
		t.Run(fmt.Sprintf("noncanonical-%d", index), func(t *testing.T) {
			if _, err := DecodeOutputBrokerEnvelope([]byte(candidate)); err == nil {
				t.Fatal("non-canonical broker envelope decoded")
			}
		})
	}
	if err := proof.Validate(); err != nil {
		t.Fatalf("test broker proof: %v", err)
	}
}

// TestOutputBrokerServerChallengeReplayAndLiveStream proves durable replay precedes live records and acknowledgements remain monotonic.
func TestOutputBrokerServerChallengeReplayAndLiveStream(t *testing.T) {
	server, proof := newOutputBrokerServerTest(t)
	first, err := server.journal.Append(OutputBrokerStreamStdout, 0, []byte("before"))
	if err != nil {
		t.Fatalf("append replay frame: %v", err)
	}
	serverSide, clientSide := net.Pipe()
	serverConnection := outputBrokerServerTestConnection{
		Conn:     serverSide,
		peer:     local.PeerIdentity{UserID: "501", ProcessID: 9001},
		endpoint: proof.EndpointReference,
	}
	clientConnection := outputBrokerServerTestConnection{
		Conn:     clientSide,
		peer:     local.PeerIdentity{UserID: "501", ProcessID: uint32(proof.Process.PID)},
		endpoint: proof.EndpointReference,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeConnection(t.Context(), serverConnection) }()
	t.Cleanup(func() { _ = clientConnection.Close() })

	reader := rpc.NewDefaultFrameReader(clientConnection)
	writer := rpc.NewDefaultFrameWriter(clientConnection)
	hello := OutputBrokerHello{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         proof.ProjectID,
		SessionID:         proof.SessionID,
		EndpointReference: proof.EndpointReference,
		Cursor:            0,
		Ticket:            "broker-ticket-1",
		ClientNonce:       "client-nonce-1",
	}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeHello, Hello: &hello}); err != nil {
		t.Fatalf("write broker hello: %v", err)
	}
	challengeEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		t.Fatalf("read broker challenge: %v", err)
	}
	if challengeEnvelope.Kind != OutputBrokerEnvelopeChallenge || challengeEnvelope.Challenge == nil {
		t.Fatalf("challenge envelope = %#v, want challenge", challengeEnvelope)
	}
	challenge := *challengeEnvelope.Challenge
	if err := ValidateOutputBrokerChallengeCorrelation(hello, challenge); err != nil {
		t.Fatalf("challenge correlation: %v", err)
	}
	if len(challenge.Replay.Frames) != 1 || challenge.Replay.Frames[0].Text != "before" || challenge.Replay.NextCursor != first.NextCursor {
		t.Fatalf("challenge replay = %#v, want before at cursor %d", challenge.Replay, first.NextCursor)
	}
	confirm := OutputBrokerConfirm{
		Version:           OutputBrokerProtocolVersion,
		ProjectID:         hello.ProjectID,
		SessionID:         hello.SessionID,
		EndpointReference: hello.EndpointReference,
		ClientNonce:       hello.ClientNonce,
		Challenge:         challenge.Challenge,
	}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeConfirm, Confirm: &confirm}); err != nil {
		t.Fatalf("write broker confirmation: %v", err)
	}
	readyEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		t.Fatalf("read broker ready: %v", err)
	}
	if readyEnvelope.Kind != OutputBrokerEnvelopeReady || readyEnvelope.Ready == nil || readyEnvelope.Ready.NextCursor != first.NextCursor {
		t.Fatalf("ready envelope = %#v, want cursor %d", readyEnvelope, first.NextCursor)
	}
	second, err := server.journal.Append(OutputBrokerStreamStderr, first.NextCursor, []byte("after"))
	if err != nil {
		t.Fatalf("append live frame: %v", err)
	}
	recordEnvelope, err := ReadOutputBrokerEnvelope(reader)
	if err != nil {
		t.Fatalf("read live broker record: %v", err)
	}
	if recordEnvelope.Kind != OutputBrokerEnvelopeRecord || recordEnvelope.Record == nil || recordEnvelope.Record.Frame == nil || recordEnvelope.Record.Frame.Text != "after" || recordEnvelope.Record.Frame.Stream != OutputBrokerStreamStderr {
		t.Fatalf("live record = %#v, want stderr after", recordEnvelope)
	}
	ack := OutputBrokerCommand{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerCommandAck, Cursor: second.NextCursor}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeCommand, Command: &ack}); err != nil {
		t.Fatalf("write broker acknowledgement: %v", err)
	}
	closeCommand := OutputBrokerCommand{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerCommandClose}
	if err := WriteOutputBrokerEnvelope(writer, OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeCommand, Command: &closeCommand}); err != nil {
		t.Fatalf("write broker close command: %v", err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("ServeConnection() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeConnection() did not finish after close command")
	}
}

// TestOutputBrokerServerRejectsWrongTicketBeforeReplay proves bearer failure cannot reveal retained bytes.
func TestOutputBrokerServerRejectsWrongTicketBeforeReplay(t *testing.T) {
	server, proof := newOutputBrokerServerTest(t)
	if _, err := server.journal.Append(OutputBrokerStreamStdout, 0, []byte("secret")); err != nil {
		t.Fatalf("append protected frame: %v", err)
	}
	serverSide, clientSide := net.Pipe()
	serverConnection := outputBrokerServerTestConnection{Conn: serverSide, peer: local.PeerIdentity{UserID: "501", ProcessID: 9002}, endpoint: proof.EndpointReference}
	clientConnection := outputBrokerServerTestConnection{Conn: clientSide, peer: local.PeerIdentity{UserID: "501", ProcessID: uint32(proof.Process.PID)}, endpoint: proof.EndpointReference}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeConnection(t.Context(), serverConnection) }()
	t.Cleanup(func() { _ = clientConnection.Close() })
	hello := OutputBrokerHello{Version: OutputBrokerProtocolVersion, ProjectID: proof.ProjectID, SessionID: proof.SessionID, EndpointReference: proof.EndpointReference, Ticket: "wrong-ticket", ClientNonce: "client-nonce-2"}
	if err := WriteOutputBrokerEnvelope(rpc.NewDefaultFrameWriter(clientConnection), OutputBrokerEnvelope{Version: OutputBrokerProtocolVersion, Kind: OutputBrokerEnvelopeHello, Hello: &hello}); err != nil {
		t.Fatalf("write invalid broker hello: %v", err)
	}
	errorEnvelope, err := ReadOutputBrokerEnvelope(rpc.NewDefaultFrameReader(clientConnection))
	if err != nil {
		t.Fatalf("read broker rejection: %v", err)
	}
	if errorEnvelope.Kind != OutputBrokerEnvelopeError || errorEnvelope.Error == nil || errorEnvelope.Error.Code != outputBrokerAuthenticationCode {
		t.Fatalf("broker rejection = %#v, want authentication error", errorEnvelope)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("ServeConnection() did not finish after rejected hello")
	}
}

// TestNewOutputBrokerServerRejectsMissingAuthority prevents an unbound listener from becoming a broker.
func TestNewOutputBrokerServerRejectsMissingAuthority(t *testing.T) {
	proof := outputBrokerServerTestProof(t)
	journal, err := OpenOutputBrokerJournal(t.TempDir(), proof.ProjectID, proof.SessionID)
	if err != nil {
		t.Fatalf("OpenOutputBrokerJournal() error = %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if _, err := NewOutputBrokerServer(OutputBrokerServerConfig{Journal: journal, Peer: proof, AttachmentTicket: "ticket"}); err == nil {
		t.Fatal("NewOutputBrokerServer() accepted missing listener")
	}
	if _, err := NewOutputBrokerServer(OutputBrokerServerConfig{Listener: outputBrokerServerTestListener{}, Peer: proof, AttachmentTicket: "ticket"}); err == nil {
		t.Fatal("NewOutputBrokerServer() accepted missing journal")
	}
	if _, err := NewOutputBrokerServer(OutputBrokerServerConfig{Listener: outputBrokerServerTestListener{}, Journal: journal, Peer: proof}); err == nil {
		t.Fatal("NewOutputBrokerServer() accepted missing attachment ticket")
	}
}

// TestOutputBrokerServerContextCancellationClosesConnection proves shutdown cannot leave a client blocked in a pipe read.
func TestOutputBrokerServerContextCancellationClosesConnection(t *testing.T) {
	server, proof := newOutputBrokerServerTest(t)
	serverSide, clientSide := net.Pipe()
	serverConnection := outputBrokerServerTestConnection{Conn: serverSide, peer: local.PeerIdentity{UserID: "501", ProcessID: 9003}, endpoint: proof.EndpointReference}
	ctx, cancel := context.WithCancel(t.Context())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeConnection(ctx, serverConnection) }()
	_ = clientSide.Close()
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("ServeConnection() did not stop after context cancellation")
	}
}
