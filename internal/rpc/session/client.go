package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

var noDeadline time.Time

// Client multiplexes bounded requests over one negotiated daemon connection.
type Client struct {
	config     ClientConfig
	connection net.Conn
	reader     *rpc.FrameReader
	writer     *rpc.FrameWriter
	peer       Peer
	nextID     atomic.Uint64
	pendingMu  sync.Mutex
	pending    map[string]*pendingCall
	terminal   chan struct{}
	termOnce   sync.Once
	termMu     sync.Mutex
	termErr    error
}

// pendingCall keeps cancelled calls correlated until their terminal response arrives.
type pendingCall struct {
	result    chan callResult
	abandoned bool
}

// callResult carries exactly one successful payload or terminal failure.
type callResult struct {
	payload json.RawMessage
	err     error
}

// NewClient negotiates one already-authenticated transport and starts response dispatch.
func NewClient(ctx context.Context, connection net.Conn, config ClientConfig) (*Client, error) {
	if connection == nil {
		return nil, errors.New("RPC client connection is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizedClientConfig(config)
	if err != nil {
		return nil, err
	}

	client := &Client{
		config:     normalized,
		connection: connection,
		reader:     rpc.NewDefaultFrameReader(connection),
		writer:     rpc.NewDefaultFrameWriter(connection),
		pending:    make(map[string]*pendingCall),
		terminal:   make(chan struct{}),
	}
	if err := client.negotiate(ctx); err != nil {
		_ = connection.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	go client.readLoop()

	return client, nil
}

// Peer returns an immutable copy of the negotiated daemon identity.
func (c *Client) Peer() Peer {
	return copyPeer(c.peer)
}

// Call sends one method request and waits for its correlated response.
func (c *Client) Call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.RequestTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	deadline, _ := ctx.Deadline()
	requestID := "request-" + strconv.FormatUint(c.nextID.Add(1), 10)
	envelope, err := rpc.NewRequestEnvelope(c.peer.Protocol, requestID, method, deadline.UTC(), payload)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	call := &pendingCall{result: make(chan callResult, 1)}
	if err := c.addPending(requestID, call); err != nil {
		return nil, err
	}
	if err := c.writer.WriteEnvelope(envelope); err != nil {
		c.removePending(requestID, call)
		c.terminate(fmt.Errorf("write request: %w", err))
		return nil, c.closedError()
	}

	select {
	case result := <-call.result:
		return result.payload, result.err
	case <-ctx.Done():
		// Prefer a response already accepted by the reader over a simultaneous
		// local cancellation so callers do not discard completed work.
		select {
		case result := <-call.result:
			return result.payload, result.err
		default:
		}
		if !c.abandonPending(requestID, call) {
			result := <-call.result
			return result.payload, result.err
		}
		// The daemon owns the same absolute deadline, so an extra cancel at that
		// boundary would race DeadlineExceeded into the less precise Cancelled.
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancelEnvelope, createErr := rpc.NewCancelEnvelope(c.peer.Protocol, requestID)
			if createErr == nil {
				if writeErr := c.writer.WriteEnvelope(cancelEnvelope); writeErr != nil {
					c.terminate(fmt.Errorf("write cancellation: %w", writeErr))
				}
			}
		}

		return nil, ctx.Err()
	}
}

// Close terminates the connection and wakes every pending caller.
func (c *Client) Close() error {
	c.terminate(ErrClosed)

	return nil
}

// Done closes when the connection becomes terminal.
func (c *Client) Done() <-chan struct{} {
	return c.terminal
}

// Err returns nil while connected and the first terminal cause after Done closes.
func (c *Client) Err() error {
	select {
	case <-c.terminal:
		return c.closedError()
	default:
		return nil
	}
}

// negotiate verifies that the daemon selected from the client's advertised ranges and features.
func (c *Client) negotiate(ctx context.Context) error {
	deadline := handshakeDeadline(ctx, c.config.HandshakeTimeout)
	if err := c.connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set client handshake deadline: %w", err)
	}
	stopContextClose := closeConnectionWhenDone(ctx, c.connection)
	defer stopContextClose()

	hello := rpc.Hello{
		ProtocolRanges: c.config.ProtocolRanges,
		Role:           c.config.Role,
		ClientVersion:  c.config.ClientVersion,
		Capabilities:   c.config.Capabilities,
	}
	envelope, err := rpc.NewHelloEnvelope(hello)
	if err != nil {
		return fmt.Errorf("create client hello: %w", err)
	}
	if err := c.writer.WriteEnvelope(envelope); err != nil {
		return fmt.Errorf("write client hello: %w", classifyHandshakeError(ctx, deadline, err))
	}

	response, err := c.reader.ReadEnvelope()
	if err != nil {
		return fmt.Errorf("read daemon handshake: %w", classifyHandshakeError(ctx, deadline, err))
	}
	switch response.Kind {
	case rpc.KindReject:
		rejection, decodeErr := rpc.DecodePayload[rpc.Reject](response)
		if decodeErr != nil {
			return fmt.Errorf("decode daemon rejection: %w", decodeErr)
		}

		return &HandshakeError{
			Failure:        rejection.Error,
			ProtocolRanges: append([]rpc.VersionRange(nil), rejection.ProtocolRanges...),
		}
	case rpc.KindWelcome:
	default:
		return fmt.Errorf("%w: daemon handshake must welcome or reject", ErrProtocolViolation)
	}

	welcome, err := rpc.DecodePayload[rpc.Welcome](response)
	if err != nil {
		return fmt.Errorf("decode daemon welcome: %w", err)
	}
	selected, err := rpc.NegotiateVersion(c.config.ProtocolRanges, welcome.ProtocolRanges)
	if err != nil || selected.Compare(welcome.Protocol) != 0 {
		return fmt.Errorf("%w: daemon selected an unadvertised protocol", ErrProtocolViolation)
	}
	if !capabilitiesSubset(welcome.Capabilities, c.config.Capabilities) {
		return fmt.Errorf("%w: daemon selected an unadvertised capability", ErrProtocolViolation)
	}
	if err := c.connection.SetDeadline(noDeadline); err != nil {
		return fmt.Errorf("clear client handshake deadline: %w", err)
	}
	c.peer = Peer{
		Role:         welcome.Role,
		BuildVersion: welcome.DaemonVersion,
		Protocol:     welcome.Protocol,
		Capabilities: append([]rpc.Capability(nil), welcome.Capabilities...),
	}

	return nil
}

// readLoop dispatches responses by request ID and makes malformed correlation terminal.
func (c *Client) readLoop() {
	for {
		envelope, err := c.reader.ReadEnvelope()
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.terminate(ErrClosed)
			} else {
				c.terminate(fmt.Errorf("read response: %w", err))
			}
			return
		}
		if envelope.Protocol == nil || envelope.Protocol.Compare(c.peer.Protocol) != 0 {
			c.terminate(fmt.Errorf("%w: response protocol does not match negotiation", ErrProtocolViolation))
			return
		}
		if envelope.Kind != rpc.KindResponse {
			c.terminate(fmt.Errorf("%w: unexpected %s envelope from daemon", ErrProtocolViolation, envelope.Kind))
			return
		}

		call, abandoned := c.takePending(envelope.RequestID)
		if call == nil {
			c.terminate(fmt.Errorf("%w: response has no pending request", ErrProtocolViolation))
			return
		}
		if abandoned {
			continue
		}
		if envelope.Error != nil {
			call.result <- callResult{err: *envelope.Error}
			continue
		}
		call.result <- callResult{payload: append([]byte(nil), envelope.Payload...)}
	}
}

// addPending enforces the client-side bound before a request is written.
func (c *Client) addPending(requestID string, call *pendingCall) error {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	select {
	case <-c.terminal:
		return c.closedError()
	default:
	}
	if len(c.pending) >= c.config.MaxPendingRequests {
		return ErrBusy
	}
	c.pending[requestID] = call

	return nil
}

// removePending rolls back correlation state when its request could not be written.
func (c *Client) removePending(requestID string, expected *pendingCall) {
	c.pendingMu.Lock()
	if c.pending[requestID] == expected {
		delete(c.pending, requestID)
	}
	c.pendingMu.Unlock()
}

// abandonPending keeps correlation bounded until the daemon acknowledges cancellation.
func (c *Client) abandonPending(requestID string, expected *pendingCall) bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.pending[requestID] == expected {
		expected.abandoned = true
		return true
	}

	return false
}

// takePending atomically completes one correlation and reports whether its caller left.
func (c *Client) takePending(requestID string) (*pendingCall, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	call := c.pending[requestID]
	if call == nil {
		return nil, false
	}
	delete(c.pending, requestID)

	return call, call.abandoned
}

// terminate closes once and wakes only callers still waiting for a response.
func (c *Client) terminate(err error) {
	c.termOnce.Do(func() {
		if err == nil {
			err = ErrClosed
		}
		c.termMu.Lock()
		c.termErr = err
		c.termMu.Unlock()
		close(c.terminal)
		_ = c.connection.Close()

		c.pendingMu.Lock()
		for requestID, call := range c.pending {
			delete(c.pending, requestID)
			if !call.abandoned {
				call.result <- callResult{err: c.closedError()}
			}
		}
		c.pendingMu.Unlock()
	})
}

// closedError wraps the terminal cause with a stable errors.Is target.
func (c *Client) closedError() error {
	c.termMu.Lock()
	defer c.termMu.Unlock()
	if c.termErr == nil || errors.Is(c.termErr, ErrClosed) {
		return ErrClosed
	}

	return fmt.Errorf("%w: %w", ErrClosed, c.termErr)
}

// handshakeDeadline applies the shorter of the configured timeout and caller deadline.
func handshakeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}

	return deadline
}

// classifyHandshakeError gives configured and caller deadlines one stable error
// category even when the stream reports its platform-specific timeout first.
func classifyHandshakeError(ctx context.Context, deadline time.Time, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}

	return err
}

// closeConnectionWhenDone interrupts a handshake read immediately when its context is cancelled.
func closeConnectionWhenDone(ctx context.Context, connection net.Conn) func() {
	done := make(chan struct{})
	var mutex sync.Mutex
	finished := false
	go func() {
		select {
		case <-ctx.Done():
			mutex.Lock()
			if !finished {
				_ = connection.Close()
			}
			mutex.Unlock()
		case <-done:
		}
	}()

	return func() {
		mutex.Lock()
		finished = true
		mutex.Unlock()
		close(done)
	}
}

// capabilitiesSubset verifies that negotiation never grants a feature the client omitted.
func capabilitiesSubset(selected []rpc.Capability, advertised []rpc.Capability) bool {
	available := make(map[rpc.Capability]struct{}, len(advertised))
	for _, capability := range advertised {
		available[capability] = struct{}{}
	}
	for _, capability := range selected {
		if _, ok := available[capability]; !ok {
			return false
		}
	}

	return true
}

// copyPeer prevents handler or caller mutations from changing negotiated connection state.
func copyPeer(peer Peer) Peer {
	peer.Capabilities = append([]rpc.Capability(nil), peer.Capabilities...)

	return peer
}
