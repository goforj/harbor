package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

// completionTestPeer drives one already-negotiated server connection without
// hiding response-write ordering behind the high-level client reader.
type completionTestPeer struct {
	connection net.Conn
	reader     *rpc.FrameReader
	writer     *rpc.FrameWriter
	protocol   rpc.Version
	cancel     context.CancelFunc
	done       chan error
	stopped    chan struct{}
}

// completionBoundaryConnection reports a complete response-frame write after
// the underlying stream accepts every byte.
type completionBoundaryConnection struct {
	net.Conn
	written chan struct{}
	once    sync.Once
}

// Write forwards one full frame and publishes only a successful complete write.
func (c *completionBoundaryConnection) Write(payload []byte) (int, error) {
	written, err := c.Conn.Write(payload)
	if err == nil && written == len(payload) {
		c.once.Do(func() { close(c.written) })
	}

	return written, err
}

// failedResponseWriteConnection rejects the first server response write.
type failedResponseWriteConnection struct {
	net.Conn
	attempted chan struct{}
	cause     error
	once      sync.Once
}

// Write reports the attempted frame and rejects it without forwarding bytes.
func (c *failedResponseWriteConnection) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.attempted) })

	return 0, c.cause
}

// newCompletionTestPeer starts dispatch after the handshake boundary with a
// caller-selected server-side transport wrapper.
func newCompletionTestPeer(
	t *testing.T,
	config ServerConfig,
	wrap func(net.Conn) net.Conn,
) *completionTestPeer {
	t.Helper()

	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("new completion server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	if wrap != nil {
		serverConnection = wrap(serverConnection)
	}
	protocol := rpc.Version{Major: 1}
	active := newServerConnection(
		server,
		serverConnection,
		rpc.NewDefaultFrameReader(serverConnection),
		rpc.NewDefaultFrameWriter(serverConnection),
		Peer{
			Role:         rpc.RoleCLI,
			BuildVersion: "completion-test",
			Protocol:     protocol,
			Capabilities: []rpc.Capability{"control.v1"},
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- active.run(ctx)
		close(stopped)
	}()
	peer := &completionTestPeer{
		connection: clientConnection,
		reader:     rpc.NewDefaultFrameReader(clientConnection),
		writer:     rpc.NewDefaultFrameWriter(clientConnection),
		protocol:   protocol,
		cancel:     cancel,
		done:       done,
		stopped:    stopped,
	}
	t.Cleanup(func() {
		_ = peer.connection.Close()
		peer.cancel()
		select {
		case <-peer.stopped:
		case <-time.After(time.Second):
			t.Error("completion test server did not stop")
		}
	})

	return peer
}

// call writes one request and reads its complete response frame.
func (p *completionTestPeer) call(t *testing.T, method string) rpc.Envelope {
	t.Helper()

	request, err := rpc.NewRequestEnvelope(
		p.protocol,
		"request-"+method,
		method,
		time.Now().UTC().Add(time.Minute),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create %s request: %v", method, err)
	}
	if err := p.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write %s request: %v", method, err)
	}
	response, err := p.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}

	return response
}

// waitCompletionSignal waits on an explicit test barrier with a deadlock guard.
func waitCompletionSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

// assertCompletionNotCalled verifies a completed request did not run its opt-in callback.
func assertCompletionNotCalled(t *testing.T, called <-chan struct{}) {
	t.Helper()

	select {
	case <-called:
		t.Fatal("after-write callback ran for an unsuccessful response")
	default:
	}
}

// TestRespondAfterWriteRunsAfterCompleteFrame verifies the wrapper is removed
// before encoding and its callback runs once beyond the complete-write boundary.
func TestRespondAfterWriteRunsAfterCompleteFrame(t *testing.T) {
	written := make(chan struct{})
	callbackDone := make(chan struct{})
	order := make(chan error, 1)
	var calls atomic.Int64
	config := testServerConfig(map[string]Handler{
		"complete": func(context.Context, Request) (any, error) {
			payload := struct {
				State string `json:"state"`
			}{State: "stopping"}

			return RespondAfterWrite(payload, func() {
				calls.Add(1)
				select {
				case <-written:
					order <- nil
				default:
					order <- errors.New("callback ran before the complete frame write")
				}
				close(callbackDone)
			}), nil
		},
		"barrier": func(context.Context, Request) (any, error) {
			return struct{}{}, nil
		},
	})
	config.MaxConcurrentRequests = 1
	peer := newCompletionTestPeer(t, config, func(connection net.Conn) net.Conn {
		return &completionBoundaryConnection{Conn: connection, written: written}
	})

	response := peer.call(t, "complete")
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		t.Fatalf("decode completion response: %v", err)
	}
	if payload.State != "stopping" {
		t.Fatalf("completion response state = %q, want stopping", payload.State)
	}
	waitCompletionSignal(t, callbackDone, "after-write callback")
	if err := <-order; err != nil {
		t.Fatal(err)
	}
	peer.call(t, "barrier")
	if got := calls.Load(); got != 1 {
		t.Fatalf("after-write callback calls = %d, want 1", got)
	}
}

// TestRespondAfterWriteSkipsUnsuccessfulResponses verifies handler and payload
// encoding failures cannot commit their deferred lifecycle work.
func TestRespondAfterWriteSkipsUnsuccessfulResponses(t *testing.T) {
	tests := []struct {
		name     string
		handler  func(func()) Handler
		wantCode rpc.ErrorCode
	}{
		{
			name: "handler failure",
			handler: func(afterWrite func()) Handler {
				return func(context.Context, Request) (any, error) {
					return RespondAfterWrite(struct{}{}, afterWrite), NewHandlerError(
						rpc.ErrorCodeConflict,
						errors.New("synthetic handler failure"),
					)
				}
			},
			wantCode: rpc.ErrorCodeConflict,
		},
		{
			name: "payload encoding failure",
			handler: func(afterWrite func()) Handler {
				return func(context.Context, Request) (any, error) {
					return RespondAfterWrite(func() {}, afterWrite), nil
				}
			},
			wantCode: rpc.ErrorCodeInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := make(chan struct{})
			barrier := make(chan struct{})
			config := testServerConfig(map[string]Handler{
				"fail": test.handler(func() { close(called) }),
				"barrier": func(context.Context, Request) (any, error) {
					close(barrier)

					return struct{}{}, nil
				},
			})
			config.MaxConcurrentRequests = 1
			peer := newCompletionTestPeer(t, config, nil)

			response := peer.call(t, "fail")
			if response.Error == nil || response.Error.Code != test.wantCode {
				t.Fatalf("failure response error = %#v, want %q", response.Error, test.wantCode)
			}
			peer.call(t, "barrier")
			waitCompletionSignal(t, barrier, "request completion barrier")
			assertCompletionNotCalled(t, called)
		})
	}
}

// TestRespondAfterWriteSkipsFailedFrameWrites verifies transport rejection
// terminates the session without committing deferred lifecycle work.
func TestRespondAfterWriteSkipsFailedFrameWrites(t *testing.T) {
	called := make(chan struct{})
	attempted := make(chan struct{})
	writeFailure := errors.New("synthetic response write failure")
	config := testServerConfig(map[string]Handler{
		"complete": func(context.Context, Request) (any, error) {
			return RespondAfterWrite(struct{}{}, func() { close(called) }), nil
		},
	})
	peer := newCompletionTestPeer(t, config, func(connection net.Conn) net.Conn {
		return &failedResponseWriteConnection{
			Conn:      connection,
			attempted: attempted,
			cause:     writeFailure,
		}
	})
	request, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"request-complete",
		"complete",
		time.Now().UTC().Add(time.Minute),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitCompletionSignal(t, attempted, "failed response write")
	select {
	case err := <-peer.done:
		if !errors.Is(err, writeFailure) {
			t.Fatalf("server error = %v, want response write failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed response write did not terminate server")
	}
	assertCompletionNotCalled(t, called)
	if _, err := peer.reader.ReadEnvelope(); err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("read after failed response = %v, want EOF", err)
	}
}

// TestRespondAfterWriteContainsAndReportsPanic verifies a callback defect does
// not replace the accepted response or poison later requests on the session.
func TestRespondAfterWriteContainsAndReportsPanic(t *testing.T) {
	observed := make(chan error, 1)
	config := testServerConfig(map[string]Handler{
		"panic": func(context.Context, Request) (any, error) {
			return RespondAfterWrite(struct {
				Accepted bool `json:"accepted"`
			}{Accepted: true}, func() {
				panic("synthetic completion panic")
			}), nil
		},
		"ping": func(context.Context, Request) (any, error) {
			return struct{}{}, nil
		},
	})
	config.ObserveError = func(_ Request, err error) {
		observed <- err
	}
	pair := newTestPair(t, config, testClientConfig())

	payload, err := pair.client.Call(t.Context(), "panic", struct{}{})
	if err != nil {
		t.Fatalf("call callback-panic handler: %v", err)
	}
	var accepted struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.Unmarshal(payload, &accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if !accepted.Accepted {
		t.Fatal("callback panic replaced the accepted response")
	}
	localError := <-observed
	var panicError *AfterWritePanicError
	if !errors.As(localError, &panicError) {
		t.Fatalf("observed error = %T %v, want AfterWritePanicError", localError, localError)
	}
	if len(panicError.Stack()) == 0 {
		t.Fatal("after-write panic has no captured stack")
	}
	if _, err := pair.client.Call(t.Context(), "ping", struct{}{}); err != nil {
		t.Fatalf("ping after callback panic: %v", err)
	}

	var zero *AfterWritePanicError
	if zero.Error() == "" || zero.Stack() != nil {
		t.Fatal("nil AfterWritePanicError is not safe")
	}
}

// TestRespondAfterWriteRejectsNilCallback verifies wiring defects fail at the handler boundary.
func TestRespondAfterWriteRejectsNilCallback(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("RespondAfterWrite accepted a nil callback")
		}
	}()

	RespondAfterWrite(struct{}{}, nil)
}
