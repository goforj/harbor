package projectprocess

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

const (
	// OutputBrokerProtocolVersion identifies the first framed broker handshake.
	OutputBrokerProtocolVersion    uint16 = 1
	maximumOutputBrokerTokenBytes         = 512
	maximumOutputBrokerErrorBytes         = 512
	maximumOutputBrokerFrameBytes         = 1 << 20
	outputBrokerChallengeBytes            = 32
	outputBrokerEndpointTokenBytes        = 16
)

// OutputBrokerEnvelopeKind identifies the one message carried by a framed broker envelope.
type OutputBrokerEnvelopeKind string

const (
	// OutputBrokerEnvelopeHello starts one broker attachment.
	OutputBrokerEnvelopeHello OutputBrokerEnvelopeKind = "hello"
	// OutputBrokerEnvelopeChallenge binds a fresh broker challenge to one hello.
	OutputBrokerEnvelopeChallenge OutputBrokerEnvelopeKind = "challenge"
	// OutputBrokerEnvelopeConfirm proves possession of the challenge.
	OutputBrokerEnvelopeConfirm OutputBrokerEnvelopeKind = "confirm"
	// OutputBrokerEnvelopeReady marks the point at which live records may follow.
	OutputBrokerEnvelopeReady OutputBrokerEnvelopeKind = "ready"
	// OutputBrokerEnvelopeCommand carries an acknowledgement or close request.
	OutputBrokerEnvelopeCommand OutputBrokerEnvelopeKind = "command"
	// OutputBrokerEnvelopeRecord carries one replay or live output record.
	OutputBrokerEnvelopeRecord OutputBrokerEnvelopeKind = "record"
	// OutputBrokerEnvelopeError carries one bounded terminal protocol error.
	OutputBrokerEnvelopeError OutputBrokerEnvelopeKind = "error"
)

// OutputBrokerCommandKind identifies a client command after attachment.
type OutputBrokerCommandKind string

const (
	// OutputBrokerCommandAck advances the client's durable replay acknowledgement.
	OutputBrokerCommandAck OutputBrokerCommandKind = "ack"
	// OutputBrokerCommandClose retires the connection without affecting the child process.
	OutputBrokerCommandClose OutputBrokerCommandKind = "close"
)

// OutputBrokerHello requests one exact project/session replay and live attachment.
type OutputBrokerHello struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// ProjectID identifies the project whose output is requested.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID identifies the exact lifecycle whose output is requested.
	SessionID domain.SessionID `json:"session_id"`
	// EndpointReference binds the request to the owner-private endpoint selected by Harbor.
	EndpointReference string `json:"endpoint_reference"`
	// Cursor requests replay beginning at one exact absolute output cursor.
	Cursor uint64 `json:"cursor"`
	// Ticket is the one-use opaque attachment credential issued for this broker.
	Ticket string `json:"ticket"`
	// ClientNonce correlates the challenge and confirmation without becoming authority.
	ClientNonce string `json:"client_nonce"`
}

// Validate reports whether a hello is bounded and bound to one exact lifecycle.
func (hello OutputBrokerHello) Validate() error {
	if hello.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker hello protocol version %d is unsupported", hello.Version)
	}
	if err := hello.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker hello project ID: %w", err)
	}
	if err := hello.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker hello session ID: %w", err)
	}
	if err := validateOutputBrokerEndpointReference(hello.EndpointReference); err != nil {
		return err
	}
	if hello.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("output broker hello cursor exceeds %d", domain.MaximumSequence)
	}
	if err := validateOutputBrokerToken("output broker hello ticket", hello.Ticket); err != nil {
		return err
	}
	return validateOutputBrokerToken("output broker hello client nonce", hello.ClientNonce)
}

// OutputBrokerChallenge returns exact replay and the broker process proof before live output is exposed.
type OutputBrokerChallenge struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// ProjectID identifies the project whose output is retained.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID identifies the lifecycle whose output is retained.
	SessionID domain.SessionID `json:"session_id"`
	// EndpointReference binds the response to the selected owner-private endpoint.
	EndpointReference string `json:"endpoint_reference"`
	// ClientNonce correlates the response to one hello.
	ClientNonce string `json:"client_nonce"`
	// Challenge is a fresh ephemeral value that must be confirmed before live output.
	Challenge string `json:"challenge"`
	// Peer identifies the broker process independently from the child GoForj process.
	Peer OutputBrokerPeer `json:"peer"`
	// Replay contains retained output at the requested cursor.
	Replay OutputBrokerReplay `json:"replay"`
}

// Validate reports whether a challenge is complete and internally correlated.
func (challenge OutputBrokerChallenge) Validate() error {
	if challenge.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker challenge protocol version %d is unsupported", challenge.Version)
	}
	if err := challenge.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker challenge project ID: %w", err)
	}
	if err := challenge.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker challenge session ID: %w", err)
	}
	if err := validateOutputBrokerEndpointReference(challenge.EndpointReference); err != nil {
		return err
	}
	if err := validateOutputBrokerToken("output broker challenge client nonce", challenge.ClientNonce); err != nil {
		return err
	}
	if err := validateOutputBrokerToken("output broker challenge", challenge.Challenge); err != nil {
		return err
	}
	if err := challenge.Peer.Validate(); err != nil {
		return fmt.Errorf("output broker challenge peer: %w", err)
	}
	if err := challenge.Replay.Validate(); err != nil {
		return fmt.Errorf("output broker challenge replay: %w", err)
	}
	return nil
}

// ValidateOutputBrokerChallengeCorrelation binds a challenge to one hello.
func ValidateOutputBrokerChallengeCorrelation(hello OutputBrokerHello, challenge OutputBrokerChallenge) error {
	if err := hello.Validate(); err != nil {
		return fmt.Errorf("validate output broker hello: %w", err)
	}
	if err := challenge.Validate(); err != nil {
		return fmt.Errorf("validate output broker challenge: %w", err)
	}
	if challenge.ProjectID != hello.ProjectID || challenge.SessionID != hello.SessionID ||
		challenge.EndpointReference != hello.EndpointReference || challenge.ClientNonce != hello.ClientNonce {
		return errors.New("output broker challenge does not match hello lifecycle")
	}
	return nil
}

// OutputBrokerConfirm proves possession of one challenge before live records are sent.
type OutputBrokerConfirm struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// ProjectID identifies the project being attached.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID identifies the lifecycle being attached.
	SessionID domain.SessionID `json:"session_id"`
	// EndpointReference binds confirmation to the owner-private endpoint.
	EndpointReference string `json:"endpoint_reference"`
	// ClientNonce correlates confirmation to one hello.
	ClientNonce string `json:"client_nonce"`
	// Challenge proves the client received the fresh server challenge.
	Challenge string `json:"challenge"`
}

// Validate reports whether a confirmation is bounded and complete.
func (confirm OutputBrokerConfirm) Validate() error {
	if confirm.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker confirm protocol version %d is unsupported", confirm.Version)
	}
	if err := confirm.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker confirm project ID: %w", err)
	}
	if err := confirm.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker confirm session ID: %w", err)
	}
	if err := validateOutputBrokerEndpointReference(confirm.EndpointReference); err != nil {
		return err
	}
	if err := validateOutputBrokerToken("output broker confirm client nonce", confirm.ClientNonce); err != nil {
		return err
	}
	return validateOutputBrokerToken("output broker confirm challenge", confirm.Challenge)
}

// ValidateOutputBrokerConfirmCorrelation binds a confirmation to one exact challenge.
func ValidateOutputBrokerConfirmCorrelation(challenge OutputBrokerChallenge, confirm OutputBrokerConfirm) error {
	if err := challenge.Validate(); err != nil {
		return fmt.Errorf("validate output broker challenge: %w", err)
	}
	if err := confirm.Validate(); err != nil {
		return fmt.Errorf("validate output broker confirm: %w", err)
	}
	if confirm.ProjectID != challenge.ProjectID || confirm.SessionID != challenge.SessionID ||
		confirm.EndpointReference != challenge.EndpointReference || confirm.ClientNonce != challenge.ClientNonce ||
		confirm.Challenge != challenge.Challenge {
		return errors.New("output broker confirmation does not match challenge")
	}
	return nil
}

// OutputBrokerReady marks the point where the broker may emit live records.
type OutputBrokerReady struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// NextCursor is the first byte after the replay retained by the broker.
	NextCursor uint64 `json:"next_cursor"`
}

// Validate reports whether a ready message has a bounded cursor.
func (ready OutputBrokerReady) Validate() error {
	if ready.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker ready protocol version %d is unsupported", ready.Version)
	}
	if ready.NextCursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("output broker ready cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}

// OutputBrokerCommand asks the broker to advance an acknowledgement or close the attachment.
type OutputBrokerCommand struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// Kind identifies the command operation.
	Kind OutputBrokerCommandKind `json:"kind"`
	// Cursor is required for acknowledgements and ignored for close.
	Cursor uint64 `json:"cursor,omitempty"`
}

// Validate reports whether a command is one supported bounded operation.
func (command OutputBrokerCommand) Validate() error {
	if command.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker command protocol version %d is unsupported", command.Version)
	}
	switch command.Kind {
	case OutputBrokerCommandAck:
		if command.Cursor > uint64(domain.MaximumSequence) {
			return fmt.Errorf("output broker acknowledgement cursor exceeds %d", domain.MaximumSequence)
		}
	case OutputBrokerCommandClose:
		if command.Cursor != 0 {
			return errors.New("output broker close command must not carry a cursor")
		}
	default:
		return fmt.Errorf("output broker command kind %q is unsupported", command.Kind)
	}
	return nil
}

// OutputBrokerError reports one bounded terminal broker protocol failure.
type OutputBrokerError struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// Code is a stable ASCII classification for the caller.
	Code string `json:"code"`
	// Message is a bounded single-line diagnostic.
	Message string `json:"message"`
}

// Validate reports whether an error can safely cross the broker transport.
func (brokerError OutputBrokerError) Validate() error {
	if brokerError.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker error protocol version %d is unsupported", brokerError.Version)
	}
	if err := validateOutputBrokerToken("output broker error code", brokerError.Code); err != nil {
		return err
	}
	if !utf8.ValidString(brokerError.Message) || len([]byte(brokerError.Message)) == 0 || len([]byte(brokerError.Message)) > maximumOutputBrokerErrorBytes {
		return fmt.Errorf("output broker error message must be valid UTF-8 of 1..%d bytes", maximumOutputBrokerErrorBytes)
	}
	for _, character := range brokerError.Message {
		if character == '\n' || character == '\r' || unicode.IsControl(character) {
			return errors.New("output broker error message must be one line without control characters")
		}
	}
	return nil
}

// OutputBrokerEnvelope carries exactly one framed broker message.
type OutputBrokerEnvelope struct {
	// Version identifies the framed broker protocol revision.
	Version uint16 `json:"version"`
	// Kind identifies which payload is present.
	Kind OutputBrokerEnvelopeKind `json:"kind"`
	// Hello starts an attachment.
	Hello *OutputBrokerHello `json:"hello,omitempty"`
	// Challenge returns replay and a fresh challenge.
	Challenge *OutputBrokerChallenge `json:"challenge,omitempty"`
	// Confirm proves challenge possession.
	Confirm *OutputBrokerConfirm `json:"confirm,omitempty"`
	// Ready marks live stream readiness.
	Ready *OutputBrokerReady `json:"ready,omitempty"`
	// Command advances an acknowledgement or closes the stream.
	Command *OutputBrokerCommand `json:"command,omitempty"`
	// Record carries one replay or live output record.
	Record *OutputBrokerRecord `json:"record,omitempty"`
	// Error carries one bounded terminal failure.
	Error *OutputBrokerError `json:"error,omitempty"`
}

// Validate reports whether exactly one envelope payload matches its kind.
func (envelope OutputBrokerEnvelope) Validate() error {
	if envelope.Version != OutputBrokerProtocolVersion {
		return fmt.Errorf("output broker envelope protocol version %d is unsupported", envelope.Version)
	}
	if envelope.Kind == "" {
		return errors.New("output broker envelope kind is required")
	}
	count := 0
	if envelope.Hello != nil {
		count++
	}
	if envelope.Challenge != nil {
		count++
	}
	if envelope.Confirm != nil {
		count++
	}
	if envelope.Ready != nil {
		count++
	}
	if envelope.Command != nil {
		count++
	}
	if envelope.Record != nil {
		count++
	}
	if envelope.Error != nil {
		count++
	}
	if count != 1 {
		return errors.New("output broker envelope must contain exactly one payload")
	}
	switch envelope.Kind {
	case OutputBrokerEnvelopeHello:
		if envelope.Hello == nil {
			return errors.New("output broker hello envelope payload is missing")
		}
		return envelope.Hello.Validate()
	case OutputBrokerEnvelopeChallenge:
		if envelope.Challenge == nil {
			return errors.New("output broker challenge envelope payload is missing")
		}
		return envelope.Challenge.Validate()
	case OutputBrokerEnvelopeConfirm:
		if envelope.Confirm == nil {
			return errors.New("output broker confirm envelope payload is missing")
		}
		return envelope.Confirm.Validate()
	case OutputBrokerEnvelopeReady:
		if envelope.Ready == nil {
			return errors.New("output broker ready envelope payload is missing")
		}
		return envelope.Ready.Validate()
	case OutputBrokerEnvelopeCommand:
		if envelope.Command == nil {
			return errors.New("output broker command envelope payload is missing")
		}
		return envelope.Command.Validate()
	case OutputBrokerEnvelopeRecord:
		if envelope.Record == nil {
			return errors.New("output broker record envelope payload is missing")
		}
		return envelope.Record.Validate()
	case OutputBrokerEnvelopeError:
		if envelope.Error == nil {
			return errors.New("output broker error envelope payload is missing")
		}
		return envelope.Error.Validate()
	default:
		return fmt.Errorf("output broker envelope kind %q is unsupported", envelope.Kind)
	}
}

// MarshalOutputBrokerEnvelope validates and encodes one canonical broker envelope.
func MarshalOutputBrokerEnvelope(envelope OutputBrokerEnvelope) ([]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode output broker envelope: %w", err)
	}
	if len(encoded) > maximumOutputBrokerFrameBytes {
		return nil, fmt.Errorf("output broker envelope exceeds %d bytes", maximumOutputBrokerFrameBytes)
	}
	return encoded, nil
}

// DecodeOutputBrokerEnvelope strictly decodes one canonical broker envelope.
func DecodeOutputBrokerEnvelope(payload []byte) (OutputBrokerEnvelope, error) {
	if len(payload) == 0 || len(payload) > maximumOutputBrokerFrameBytes {
		return OutputBrokerEnvelope{}, fmt.Errorf("output broker envelope must contain 1..%d bytes", maximumOutputBrokerFrameBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope OutputBrokerEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return OutputBrokerEnvelope{}, fmt.Errorf("decode output broker envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OutputBrokerEnvelope{}, errors.New("decode output broker envelope: content must contain exactly one JSON value")
	}
	canonical, err := MarshalOutputBrokerEnvelope(envelope)
	if err != nil {
		return OutputBrokerEnvelope{}, fmt.Errorf("decode output broker envelope: %w", err)
	}
	if !bytes.Equal(payload, canonical) {
		return OutputBrokerEnvelope{}, errors.New("decode output broker envelope: content is not canonical")
	}
	return envelope, nil
}

// WriteOutputBrokerEnvelope frames one validated broker envelope on a stream.
func WriteOutputBrokerEnvelope(writer *rpc.FrameWriter, envelope OutputBrokerEnvelope) error {
	if writer == nil {
		return errors.New("output broker frame writer is required")
	}
	payload, err := MarshalOutputBrokerEnvelope(envelope)
	if err != nil {
		return err
	}
	return writer.WriteFrame(payload)
}

// ReadOutputBrokerEnvelope reads one bounded framed broker envelope from a stream.
func ReadOutputBrokerEnvelope(reader *rpc.FrameReader) (OutputBrokerEnvelope, error) {
	if reader == nil {
		return OutputBrokerEnvelope{}, errors.New("output broker frame reader is required")
	}
	payload, err := reader.ReadFrame()
	if err != nil {
		return OutputBrokerEnvelope{}, err
	}
	return DecodeOutputBrokerEnvelope(payload)
}

// newOutputBrokerChallenge returns one fresh canonical challenge token.
func newOutputBrokerChallenge() (string, error) {
	return newOutputBrokerToken(outputBrokerChallengeBytes)
}

// newOutputBrokerEndpointToken returns one fresh compact endpoint-name token.
func newOutputBrokerEndpointToken() (string, error) {
	return newOutputBrokerToken(outputBrokerEndpointTokenBytes)
}

// newOutputBrokerToken returns one fresh canonical hexadecimal token with the requested entropy length.
func newOutputBrokerToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate output broker token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// validateOutputBrokerToken keeps broker tickets and nonces on a portable opaque-token vocabulary.
func validateOutputBrokerToken(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) || len([]byte(value)) > maximumOutputBrokerTokenBytes {
		return fmt.Errorf("%s must be valid UTF-8 of at most %d bytes", name, maximumOutputBrokerTokenBytes)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !isOutputBrokerTokenCharacter(byte(character)) {
			return fmt.Errorf("%s contains an unsupported character", name)
		}
	}
	return nil
}

// isOutputBrokerTokenCharacter defines the token characters shared by all broker platforms.
func isOutputBrokerTokenCharacter(character byte) bool {
	if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
		return true
	}
	switch character {
	case '.', '_', '-', ':', '+':
		return true
	default:
		return false
	}
}
