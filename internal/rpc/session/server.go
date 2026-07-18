package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"sync"

	"github.com/goforj/harbor/internal/rpc"
)

// Server negotiates and dispatches one or more authenticated stream connections.
type Server struct {
	config ServerConfig
}

// NewServer validates and freezes daemon-side session policy.
func NewServer(config ServerConfig) (*Server, error) {
	normalized, err := normalizedServerConfig(config)
	if err != nil {
		return nil, err
	}

	return &Server{config: normalized}, nil
}

// Serve owns one already-authenticated transport connection until it closes or
// violates the negotiated protocol.
func (s *Server) Serve(ctx context.Context, connection net.Conn) error {
	if connection == nil {
		return errors.New("RPC server connection is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reader := rpc.NewDefaultFrameReader(connection)
	writer := rpc.NewDefaultFrameWriter(connection)
	hello, welcome, err := s.negotiate(ctx, connection, reader, writer)
	if err != nil {
		_ = connection.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	peer := Peer{
		Role:         hello.Role,
		BuildVersion: hello.ClientVersion,
		Protocol:     welcome.Protocol,
		Capabilities: append([]rpc.Capability(nil), welcome.Capabilities...),
	}
	active := newServerConnection(s, connection, reader, writer, peer)

	return active.run(ctx)
}

// negotiate bounds the untrusted first exchange and rejects roles that need
// application authorization before any request can be dispatched.
func (s *Server) negotiate(
	ctx context.Context,
	connection net.Conn,
	reader *rpc.FrameReader,
	writer *rpc.FrameWriter,
) (rpc.Hello, rpc.Welcome, error) {
	deadline := handshakeDeadline(ctx, s.config.HandshakeTimeout)
	handshakeContext, cancelHandshake := context.WithDeadline(ctx, deadline)
	defer cancelHandshake()
	if err := connection.SetDeadline(deadline); err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf("set server handshake deadline: %w", err)
	}
	stopContextClose := closeConnectionWhenDone(handshakeContext, connection)
	defer stopContextClose()

	envelope, err := reader.ReadEnvelope()
	if err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf(
			"read client handshake: %w",
			classifyHandshakeError(handshakeContext, deadline, err),
		)
	}
	if envelope.Kind != rpc.KindHello {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf("%w: first envelope must be hello", ErrProtocolViolation)
	}
	hello, err := rpc.DecodePayload[rpc.Hello](envelope)
	if err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf("decode client hello: %w", err)
	}

	welcome, rejection := rpc.NegotiateHello(
		hello,
		s.config.DaemonVersion,
		s.config.ProtocolRanges,
		s.config.Capabilities,
	)
	if rejection != nil {
		if err := writeRejection(writer, *rejection); err != nil {
			return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf(
				"write handshake rejection: %w",
				classifyHandshakeError(handshakeContext, deadline, err),
			)
		}
		return rpc.Hello{}, rpc.Welcome{}, &HandshakeError{
			Failure:        rejection.Error,
			ProtocolRanges: append([]rpc.VersionRange(nil), rejection.ProtocolRanges...),
		}
	}
	if hello.Role == rpc.RoleGoForjSession && s.config.Authorize == nil {
		rejection = authorizationRejection(s.config)
		if err := writeRejection(writer, *rejection); err != nil {
			return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf(
				"write role rejection: %w",
				classifyHandshakeError(handshakeContext, deadline, err),
			)
		}
		return rpc.Hello{}, rpc.Welcome{}, &HandshakeError{
			Failure:        rejection.Error,
			ProtocolRanges: append([]rpc.VersionRange(nil), rejection.ProtocolRanges...),
		}
	}
	if s.config.Authorize != nil {
		if err := s.config.Authorize(handshakeContext, hello); err != nil {
			if handshakeContext.Err() != nil {
				return rpc.Hello{}, rpc.Welcome{}, handshakeContext.Err()
			}
			rejection = authorizationRejection(s.config)
			if writeErr := writeRejection(writer, *rejection); writeErr != nil {
				return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf(
					"write authorization rejection: %w",
					classifyHandshakeError(handshakeContext, deadline, writeErr),
				)
			}
			return rpc.Hello{}, rpc.Welcome{}, &HandshakeError{
				Failure:        rejection.Error,
				ProtocolRanges: append([]rpc.VersionRange(nil), rejection.ProtocolRanges...),
			}
		}
	}

	welcomeEnvelope, err := rpc.NewWelcomeEnvelope(welcome)
	if err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf("create welcome: %w", err)
	}
	if err := writer.WriteEnvelope(welcomeEnvelope); err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf(
			"write welcome: %w",
			classifyHandshakeError(handshakeContext, deadline, err),
		)
	}
	if err := connection.SetDeadline(noDeadline); err != nil {
		return rpc.Hello{}, rpc.Welcome{}, fmt.Errorf("clear server handshake deadline: %w", err)
	}

	return hello, welcome, nil
}

// serverConnection owns request accounting and terminal state for one peer.
type serverConnection struct {
	server     *Server
	connection net.Conn
	reader     *rpc.FrameReader
	writer     *rpc.FrameWriter
	peer       Peer
	context    context.Context
	cancel     context.CancelFunc
	workers    chan struct{}
	slots      chan struct{}
	requestsMu sync.Mutex
	requests   map[string]context.CancelFunc
	work       sync.WaitGroup
	terminal   chan struct{}
	termOnce   sync.Once
	termMu     sync.Mutex
	termErr    error
}

// newServerConnection initializes fixed-capacity accounting before peer input is read.
func newServerConnection(
	server *Server,
	connection net.Conn,
	reader *rpc.FrameReader,
	writer *rpc.FrameWriter,
	peer Peer,
) *serverConnection {
	ctx, cancel := context.WithCancel(context.Background())

	return &serverConnection{
		server:     server,
		connection: connection,
		reader:     reader,
		writer:     writer,
		peer:       peer,
		context:    ctx,
		cancel:     cancel,
		workers:    make(chan struct{}, server.config.MaxConcurrentRequests),
		slots:      make(chan struct{}, server.config.MaxConcurrentRequests+server.config.MaxQueuedRequests),
		requests:   make(map[string]context.CancelFunc),
		terminal:   make(chan struct{}),
	}
}

// run reads until either side closes, then cancels and joins every accepted request.
func (s *serverConnection) run(ctx context.Context) error {
	watchDone := make(chan struct{})
	go s.watchContext(ctx, watchDone)

	readErr := s.readLoop()
	if errors.Is(readErr, io.EOF) {
		s.terminate(nil)
	} else if readErr != nil {
		s.terminate(readErr)
	}
	close(watchDone)
	s.cancelRequests()
	s.work.Wait()

	return s.terminalError()
}

// watchContext turns parent cancellation into a terminal transport close so a blocked read wakes.
func (s *serverConnection) watchContext(ctx context.Context, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		s.terminate(ctx.Err())
	case <-done:
	case <-s.terminal:
	}
}

// readLoop accepts only request and cancel envelopes after negotiation.
func (s *serverConnection) readLoop() error {
	for {
		envelope, err := s.reader.ReadEnvelope()
		if err != nil {
			return err
		}
		if envelope.Protocol == nil || envelope.Protocol.Compare(s.peer.Protocol) != 0 {
			return fmt.Errorf("%w: envelope protocol does not match negotiation", ErrProtocolViolation)
		}

		switch envelope.Kind {
		case rpc.KindRequest:
			if err := s.acceptRequest(envelope); err != nil {
				return err
			}
		case rpc.KindCancel:
			s.cancelRequest(envelope.RequestID)
		default:
			return fmt.Errorf("%w: unexpected %s envelope from client", ErrProtocolViolation, envelope.Kind)
		}
	}
}

// acceptRequest reserves bounded capacity before starting request-owned work.
func (s *serverConnection) acceptRequest(envelope rpc.Envelope) error {
	if s.hasRequest(envelope.RequestID) {
		return fmt.Errorf("%w: duplicate in-flight request ID", ErrProtocolViolation)
	}
	select {
	case s.slots <- struct{}{}:
	default:
		response, err := rpc.NewErrorResponseEnvelope(
			s.peer.Protocol,
			envelope.RequestID,
			rpc.ErrorCodeUnavailable,
			ErrBusy,
		)
		if err != nil {
			return err
		}

		return s.writer.WriteEnvelope(response)
	}

	requestContext, cancel, err := envelope.RequestContext(s.context)
	if err != nil {
		<-s.slots
		code := rpc.ErrorCodeInvalidRequest
		if errors.Is(err, context.DeadlineExceeded) {
			code = rpc.ErrorCodeDeadlineExceeded
		}
		response, responseErr := rpc.NewErrorResponseEnvelope(s.peer.Protocol, envelope.RequestID, code, err)
		if responseErr != nil {
			return responseErr
		}

		return s.writer.WriteEnvelope(response)
	}

	s.requestsMu.Lock()
	if _, exists := s.requests[envelope.RequestID]; exists {
		s.requestsMu.Unlock()
		cancel()
		<-s.slots
		return fmt.Errorf("%w: duplicate in-flight request ID", ErrProtocolViolation)
	}
	s.requests[envelope.RequestID] = cancel
	s.requestsMu.Unlock()

	s.work.Add(1)
	go s.serveRequest(requestContext, envelope)

	return nil
}

// serveRequest waits for a worker, invokes one handler, and writes exactly one response.
func (s *serverConnection) serveRequest(ctx context.Context, envelope rpc.Envelope) {
	defer s.work.Done()
	defer func() { <-s.slots }()
	defer s.finishRequest(envelope.RequestID)

	select {
	case s.workers <- struct{}{}:
		defer func() { <-s.workers }()
	case <-ctx.Done():
		s.writeRequestError(envelope.RequestID, ctx.Err())
		return
	}

	handler, exists := s.server.config.Handlers[envelope.Method]
	if !exists {
		s.writeRequestError(
			envelope.RequestID,
			NewHandlerError(rpc.ErrorCodeNotFound, errors.New("RPC method is not registered")),
		)
		return
	}

	request := Request{
		ID:      envelope.RequestID,
		Method:  envelope.Method,
		Payload: append([]byte(nil), envelope.Payload...),
		Peer:    copyPeer(s.peer),
	}
	payload, err := invokeHandler(handler, ctx, request)
	if err != nil {
		s.observeError(request, err)
		s.writeRequestError(envelope.RequestID, err)
		return
	}
	if ctx.Err() != nil {
		s.writeRequestError(envelope.RequestID, ctx.Err())
		return
	}

	response, err := rpc.NewResponseEnvelope(s.peer.Protocol, envelope.RequestID, payload)
	if err != nil {
		s.observeError(request, err)
		s.writeRequestError(envelope.RequestID, err)
		return
	}
	if err := s.writeResponse(response); err != nil {
		s.terminate(fmt.Errorf("write response: %w", err))
	}
}

// hasRequest prevents capacity errors from masquerading as a duplicate request's response.
func (s *serverConnection) hasRequest(requestID string) bool {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()

	_, exists := s.requests[requestID]

	return exists
}

// observeError keeps handler diagnostics daemon-local and contains an optional observer panic.
func (s *serverConnection) observeError(request Request, err error) {
	if s.server.config.ObserveError == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	s.server.config.ObserveError(request, err)
}

// writeRequestError maps local causes to reviewed wire categories before writing.
func (s *serverConnection) writeRequestError(requestID string, cause error) {
	code := handlerErrorCode(cause)
	response, err := rpc.NewErrorResponseEnvelope(s.peer.Protocol, requestID, code, cause)
	if err != nil {
		s.terminate(fmt.Errorf("create error response: %w", err))
		return
	}
	if err := s.writeResponse(response); err != nil {
		s.terminate(fmt.Errorf("write error response: %w", err))
	}
}

// writeResponse skips writes only after the connection itself is terminal; a
// cancelled request still receives its final reviewed cancellation response.
func (s *serverConnection) writeResponse(response rpc.Envelope) error {
	select {
	case <-s.terminal:
		return ErrClosed
	default:
		return s.writer.WriteEnvelope(response)
	}
}

// cancelRequest delivers peer cancellation only to a currently active request ID.
func (s *serverConnection) cancelRequest(requestID string) {
	s.requestsMu.Lock()
	cancel := s.requests[requestID]
	s.requestsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// finishRequest removes correlation state and releases its deadline timer.
func (s *serverConnection) finishRequest(requestID string) {
	s.requestsMu.Lock()
	cancel := s.requests[requestID]
	delete(s.requests, requestID)
	s.requestsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelRequests makes connection teardown visible to every accepted handler.
func (s *serverConnection) cancelRequests() {
	s.cancel()
	s.requestsMu.Lock()
	for _, cancel := range s.requests {
		cancel()
	}
	s.requestsMu.Unlock()
}

// terminate records only the first terminal cause and wakes blocked readers and writers.
func (s *serverConnection) terminate(err error) {
	s.termOnce.Do(func() {
		s.termMu.Lock()
		s.termErr = err
		s.termMu.Unlock()
		close(s.terminal)
		s.cancel()
		_ = s.connection.Close()
	})
}

// terminalError returns the immutable first terminal cause.
func (s *serverConnection) terminalError() error {
	s.termMu.Lock()
	defer s.termMu.Unlock()

	return s.termErr
}

// invokeHandler contains a handler panic so one faulty method cannot terminate harbord.
func invokeHandler(handler Handler, ctx context.Context, request Request) (payload any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &HandlerPanicError{value: recovered, stack: debug.Stack()}
		}
	}()

	return handler(ctx, request)
}

// handlerErrorCode classifies cancellation first, then intentional handler categories.
func handlerErrorCode(err error) rpc.ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return rpc.ErrorCodeDeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return rpc.ErrorCodeCancelled
	}
	var handlerError *HandlerError
	if errors.As(err, &handlerError) {
		return handlerError.Code()
	}

	return rpc.ErrorCodeInternal
}

// writeRejection serializes one terminal negotiation response.
func writeRejection(writer *rpc.FrameWriter, rejection rpc.Reject) error {
	envelope, err := rpc.NewRejectEnvelope(rejection)
	if err != nil {
		return err
	}

	return writer.WriteEnvelope(envelope)
}

// authorizationRejection exposes no authorization cause or transport identity details.
func authorizationRejection(config ServerConfig) *rpc.Reject {
	return &rpc.Reject{
		ProtocolRanges: append([]rpc.VersionRange(nil), config.ProtocolRanges...),
		Role:           rpc.RoleDaemon,
		DaemonVersion:  config.DaemonVersion,
		Error:          rpc.NewWireError(rpc.ErrorCodePermissionDenied),
	}
}
