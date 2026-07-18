package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

// scriptedWriteConnection exposes deterministic deadline and write failures to the session writer.
type scriptedWriteConnection struct {
	net.Conn
	mutex        sync.Mutex
	deadlines    []time.Time
	writes       int
	setDeadline  func(int, time.Time) error
	writePayload func(int, []byte) (int, error)
}

// SetWriteDeadline records the serialized deadline operation before applying its test script.
func (c *scriptedWriteConnection) SetWriteDeadline(deadline time.Time) error {
	c.mutex.Lock()
	c.deadlines = append(c.deadlines, deadline)
	ordinal := len(c.deadlines)
	c.mutex.Unlock()

	return c.setDeadline(ordinal, deadline)
}

// Write records the serialized frame operation before applying its test script.
func (c *scriptedWriteConnection) Write(payload []byte) (int, error) {
	c.mutex.Lock()
	c.writes++
	ordinal := c.writes
	c.mutex.Unlock()

	return c.writePayload(ordinal, payload)
}

// snapshot returns immutable deadline and write observations.
func (c *scriptedWriteConnection) snapshot() ([]time.Time, int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return append([]time.Time(nil), c.deadlines...), c.writes
}

// terminalRecorder captures the first failure callback without introducing another transport close.
type terminalRecorder struct {
	mutex sync.Mutex
	calls int
	cause error
}

// terminate records a writer's connection-terminal cause.
func (r *terminalRecorder) terminate(err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.calls++
	if r.cause == nil {
		r.cause = err
	}
}

// snapshot returns immutable terminal callback observations.
func (r *terminalRecorder) snapshot() (int, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.calls, r.cause
}

// timeoutNetworkError supplies a platform-independent net.Error for classification tests.
type timeoutNetworkError struct{}

// Error describes the synthetic transport timeout.
func (*timeoutNetworkError) Error() string {
	return "synthetic transport timeout"
}

// Timeout marks the synthetic failure as a network timeout.
func (*timeoutNetworkError) Timeout() bool {
	return true
}

// Temporary retains the legacy net.Error contract used by older Go networking code.
func (*timeoutNetworkError) Temporary() bool {
	return true
}

// ordinalWriteConnection reports which complete frame write reached the transport.
type ordinalWriteConnection struct {
	net.Conn
	ordinal atomic.Int64
	entered chan int64
}

// Write reports the ordinal before forwarding so tests can close a deliberately stalled write.
func (c *ordinalWriteConnection) Write(payload []byte) (int, error) {
	ordinal := c.ordinal.Add(1)
	c.entered <- ordinal

	return c.Conn.Write(payload)
}

// testSessionEnvelope creates one valid post-handshake envelope for writer unit tests.
func testSessionEnvelope(t *testing.T) rpc.Envelope {
	t.Helper()

	envelope, err := rpc.NewCancelEnvelope(rpc.Version{Major: 1}, "request-1")
	if err != nil {
		t.Fatalf("create test envelope: %v", err)
	}

	return envelope
}

// TestSessionWriterClearsSuccessfulWriteDeadline verifies a later frame never
// inherits the bounded deadline from a completed frame.
func TestSessionWriterClearsSuccessfulWriteDeadline(t *testing.T) {
	var encoded bytes.Buffer
	connection := &scriptedWriteConnection{
		setDeadline: func(_ int, _ time.Time) error {
			return nil
		},
		writePayload: func(_ int, payload []byte) (int, error) {
			return encoded.Write(payload)
		},
	}
	recorder := &terminalRecorder{}
	writer := newSessionWriter(
		connection,
		rpc.NewDefaultFrameWriter(connection),
		3*time.Second,
		recorder.terminate,
	)
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	writer.now = func() time.Time { return now }

	if err := writer.writeEnvelope(testSessionEnvelope(t)); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	deadlines, writes := connection.snapshot()
	if writes != 1 {
		t.Fatalf("frame writes = %d, want 1", writes)
	}
	if len(deadlines) != 2 {
		t.Fatalf("deadline operations = %d, want 2", len(deadlines))
	}
	if want := now.Add(3 * time.Second); !deadlines[0].Equal(want) {
		t.Fatalf("write deadline = %s, want %s", deadlines[0], want)
	}
	if !deadlines[1].IsZero() {
		t.Fatalf("cleared deadline = %s, want zero", deadlines[1])
	}
	reader := rpc.NewDefaultFrameReader(bytes.NewReader(encoded.Bytes()))
	decoded, err := reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read encoded envelope: %v", err)
	}
	if decoded.Kind != rpc.KindCancel || decoded.RequestID != "request-1" {
		t.Fatalf("encoded envelope = %#v, want cancellation", decoded)
	}
	if calls, cause := recorder.snapshot(); calls != 0 || cause != nil {
		t.Fatalf("terminal callback = (%d, %v), want no callback", calls, cause)
	}
}

// TestSessionWriterDeadlineFailuresAreTerminal verifies both deadline setup and
// cleanup failures stop future frames before stream timing becomes unbounded.
func TestSessionWriterDeadlineFailuresAreTerminal(t *testing.T) {
	setCause := errors.New("set deadline failed")
	clearCause := errors.New("clear deadline failed")
	tests := []struct {
		name             string
		failureOrdinal   int
		cause            error
		expectedWrites   int
		expectedDeadline int
	}{
		{
			name:             "set",
			failureOrdinal:   1,
			cause:            setCause,
			expectedWrites:   0,
			expectedDeadline: 1,
		},
		{
			name:             "clear",
			failureOrdinal:   2,
			cause:            clearCause,
			expectedWrites:   1,
			expectedDeadline: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &scriptedWriteConnection{
				setDeadline: func(ordinal int, _ time.Time) error {
					if ordinal == test.failureOrdinal {
						return test.cause
					}

					return nil
				},
				writePayload: func(_ int, payload []byte) (int, error) {
					return len(payload), nil
				},
			}
			recorder := &terminalRecorder{}
			writer := newSessionWriter(
				connection,
				rpc.NewDefaultFrameWriter(connection),
				time.Second,
				recorder.terminate,
			)

			first := writer.writeEnvelope(testSessionEnvelope(t))
			if !errors.Is(first, test.cause) {
				t.Fatalf("first write error = %v, want %v", first, test.cause)
			}
			second := writer.writeEnvelope(testSessionEnvelope(t))
			if second != first {
				t.Fatalf("second write error = %v, want preserved %v", second, first)
			}
			deadlines, writes := connection.snapshot()
			if writes != test.expectedWrites {
				t.Fatalf("frame writes = %d, want %d", writes, test.expectedWrites)
			}
			if len(deadlines) != test.expectedDeadline {
				t.Fatalf("deadline operations = %d, want %d", len(deadlines), test.expectedDeadline)
			}
			calls, cause := recorder.snapshot()
			if calls != 1 || cause != first {
				t.Fatalf("terminal callback = (%d, %v), want (1, %v)", calls, cause, first)
			}
		})
	}
}

// TestSessionWriterDoesNotReplacePeerCloseWithCleanupFailure verifies normal
// peer shutdown remains owned by the reader after a complete frame was delivered.
func TestSessionWriterDoesNotReplacePeerCloseWithCleanupFailure(t *testing.T) {
	connection := &scriptedWriteConnection{
		setDeadline: func(ordinal int, _ time.Time) error {
			if ordinal == 2 {
				return io.ErrClosedPipe
			}

			return nil
		},
		writePayload: func(_ int, payload []byte) (int, error) {
			return len(payload), nil
		},
	}
	recorder := &terminalRecorder{}
	writer := newSessionWriter(
		connection,
		rpc.NewDefaultFrameWriter(connection),
		time.Second,
		recorder.terminate,
	)

	if err := writer.writeEnvelope(testSessionEnvelope(t)); err != nil {
		t.Fatalf("write completed frame: %v", err)
	}
	if calls, cause := recorder.snapshot(); calls != 0 || cause != nil {
		t.Fatalf("terminal callback = (%d, %v), want reader-owned peer close", calls, cause)
	}
}

// TestSessionWriterClassifiesOnlyElapsedNetworkTimeouts keeps immediate network
// failures distinct while giving elapsed platform timeouts one stable cause.
func TestSessionWriterClassifiesOnlyElapsedNetworkTimeouts(t *testing.T) {
	base := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	timeoutCause := &timeoutNetworkError{}
	tests := []struct {
		name        string
		afterWrite  time.Time
		wantTimeout bool
	}{
		{name: "before deadline", afterWrite: base.Add(time.Second - time.Nanosecond)},
		{name: "at deadline", afterWrite: base.Add(time.Second), wantTimeout: true},
		{name: "after deadline", afterWrite: base.Add(2 * time.Second), wantTimeout: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &scriptedWriteConnection{
				setDeadline: func(_ int, _ time.Time) error { return nil },
				writePayload: func(_ int, _ []byte) (int, error) {
					return 0, timeoutCause
				},
			}
			recorder := &terminalRecorder{}
			writer := newSessionWriter(
				connection,
				rpc.NewDefaultFrameWriter(connection),
				time.Second,
				recorder.terminate,
			)
			var calls atomic.Int64
			writer.now = func() time.Time {
				if calls.Add(1) == 1 {
					return base
				}

				return test.afterWrite
			}

			err := writer.writeEnvelope(testSessionEnvelope(t))
			if got := errors.Is(err, ErrWriteTimeout); got != test.wantTimeout {
				t.Fatalf("errors.Is(ErrWriteTimeout) = %t, want %t: %v", got, test.wantTimeout, err)
			}
			if !errors.Is(err, timeoutCause) {
				t.Fatalf("write error = %v, want transport cause", err)
			}
		})
	}
}

// TestSessionWriterQueuedCallsShareOneFailure proves a stalled frame consumes
// one timeout while every writer waiting behind it receives the same cause.
func TestSessionWriterQueuedCallsShareOneFailure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	connection := &scriptedWriteConnection{
		setDeadline: func(_ int, _ time.Time) error { return nil },
		writePayload: func(_ int, _ []byte) (int, error) {
			close(entered)
			<-release

			return 0, &timeoutNetworkError{}
		},
	}
	recorder := &terminalRecorder{}
	writer := newSessionWriter(
		connection,
		rpc.NewDefaultFrameWriter(connection),
		time.Second,
		recorder.terminate,
	)
	base := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	var clockCalls atomic.Int64
	writer.now = func() time.Time {
		if clockCalls.Add(1) == 1 {
			return base
		}

		return base.Add(time.Second)
	}
	envelope := testSessionEnvelope(t)

	results := make(chan error, 2)
	go func() {
		results <- writer.writeEnvelope(envelope)
	}()
	<-entered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		results <- writer.writeEnvelope(envelope)
	}()
	<-secondStarted
	close(release)
	first := <-results
	second := <-results
	if !errors.Is(first, ErrWriteTimeout) || second != first {
		t.Fatalf("queued write errors = (%v, %v), want one preserved timeout", first, second)
	}
	deadlines, writes := connection.snapshot()
	if writes != 1 || len(deadlines) != 1 {
		t.Fatalf("transport operations = %d writes, %d deadlines; want 1 and 1", writes, len(deadlines))
	}
	if calls, cause := recorder.snapshot(); calls != 1 || cause != first {
		t.Fatalf("terminal callback = (%d, %v), want (1, %v)", calls, cause, first)
	}
}

// TestClientWriteTimeoutTerminatesStalledDaemon verifies a daemon that stops
// reading cannot retain a client call or its pending correlation indefinitely.
func TestClientWriteTimeoutTerminatesStalledDaemon(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	stopServer := make(chan struct{})
	serverStopped := make(chan struct{})
	go func() {
		defer close(serverStopped)
		defer serverConnection.Close()
		reader := rpc.NewDefaultFrameReader(serverConnection)
		writer := rpc.NewDefaultFrameWriter(serverConnection)
		helloEnvelope, err := reader.ReadEnvelope()
		if err != nil {
			return
		}
		hello, err := rpc.DecodePayload[rpc.Hello](helloEnvelope)
		if err != nil {
			return
		}
		welcome, rejection := rpc.NegotiateHello(
			hello,
			"stalled-daemon",
			testProtocolRanges,
			[]rpc.Capability{"control.v1"},
		)
		if rejection != nil {
			return
		}
		welcomeEnvelope, err := rpc.NewWelcomeEnvelope(welcome)
		if err != nil || writer.WriteEnvelope(welcomeEnvelope) != nil {
			return
		}
		<-stopServer
	}()
	t.Cleanup(func() {
		close(stopServer)
		_ = clientConnection.Close()
		<-serverStopped
	})

	config := testClientConfig()
	config.WriteTimeout = 30 * time.Millisecond
	client, err := NewClient(t.Context(), clientConnection, config)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Call(t.Context(), "status", struct{}{})
	if !errors.Is(err, ErrClosed) || !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("call error = %v, want closed session write timeout", err)
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("write timeout did not terminate client")
	}
	if err := client.Err(); !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("client terminal error = %v, want write timeout", err)
	}
}

// TestServerWriteTimeoutTerminatesStalledClient verifies a client that stops
// reading cannot retain a handler or daemon connection indefinitely.
func TestServerWriteTimeoutTerminatesStalledClient(t *testing.T) {
	config := testServerConfig(map[string]Handler{
		"status": func(context.Context, Request) (any, error) {
			return struct{}{}, nil
		},
	})
	config.WriteTimeout = 30 * time.Millisecond
	peer := newRawServerPeer(t, config)
	request, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"request-stalled-client",
		"status",
		time.Now().Add(time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case err := <-peer.done:
		if !errors.Is(err, ErrWriteTimeout) {
			t.Fatalf("server error = %v, want write timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled client retained server connection")
	}
}

// TestClientCancelWriteAndCloseAreRaceSafe verifies a close wakes a cancellation
// frame stalled behind a daemon that stopped reading after the original request.
func TestClientCancelWriteAndCloseAreRaceSafe(t *testing.T) {
	serverConnection, rawClientConnection := net.Pipe()
	clientConnection := &ordinalWriteConnection{
		Conn:    rawClientConnection,
		entered: make(chan int64, 4),
	}
	requestRead := make(chan struct{})
	stopServer := make(chan struct{})
	serverStopped := make(chan struct{})
	go func() {
		defer close(serverStopped)
		defer serverConnection.Close()
		reader := rpc.NewDefaultFrameReader(serverConnection)
		writer := rpc.NewDefaultFrameWriter(serverConnection)
		helloEnvelope, err := reader.ReadEnvelope()
		if err != nil {
			return
		}
		hello, err := rpc.DecodePayload[rpc.Hello](helloEnvelope)
		if err != nil {
			return
		}
		welcome, rejection := rpc.NegotiateHello(
			hello,
			"cancel-daemon",
			testProtocolRanges,
			[]rpc.Capability{"control.v1"},
		)
		if rejection != nil {
			return
		}
		welcomeEnvelope, err := rpc.NewWelcomeEnvelope(welcome)
		if err != nil || writer.WriteEnvelope(welcomeEnvelope) != nil {
			return
		}
		if _, err := reader.ReadEnvelope(); err != nil {
			return
		}
		close(requestRead)
		<-stopServer
	}()
	t.Cleanup(func() {
		close(stopServer)
		_ = rawClientConnection.Close()
		<-serverStopped
	})

	config := testClientConfig()
	config.WriteTimeout = time.Hour
	client, err := NewClient(t.Context(), clientConnection, config)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	callContext, cancelCall := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, callErr := client.Call(callContext, "status", struct{}{})
		callDone <- callErr
	}()
	<-requestRead
	cancelCall()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case ordinal := <-clientConnection.entered:
			if ordinal < 3 {
				continue
			}
			if err := client.Close(); err != nil {
				t.Fatalf("close client: %v", err)
			}
			goto cancellationStalled
		case <-deadline.C:
			t.Fatal("cancellation frame did not reach stalled transport")
		}
	}

cancellationStalled:
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client close left cancellation write blocked")
	}
	if err := client.Err(); !errors.Is(err, ErrClosed) {
		t.Fatalf("client terminal error = %v, want closed session", err)
	}
}
