package daemon

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc/local"
)

const testWait = 3 * time.Second

// testEventLog records lifecycle boundaries without imposing goroutine scheduling order.
type testEventLog struct {
	mutex  sync.Mutex
	events []string
}

// add appends one lifecycle event.
func (log *testEventLog) add(event string) {
	log.mutex.Lock()
	log.events = append(log.events, event)
	log.mutex.Unlock()
}

// snapshot returns an isolated event sequence for assertions.
func (log *testEventLog) snapshot() []string {
	log.mutex.Lock()
	defer log.mutex.Unlock()

	return append([]string(nil), log.events...)
}

// index returns an event's first position or -1 when it has not happened.
func (log *testEventLog) index(event string) int {
	for index, candidate := range log.snapshot() {
		if candidate == event {
			return index
		}
	}
	return -1
}

// testAuthorityLock exposes release ordering and failures to runner tests.
type testAuthorityLock struct {
	events     *testEventLog
	releaseErr error
	mutex      sync.Mutex
	releases   int
}

// Release records the point at which singleton daemon authority is relinquished.
func (lock *testAuthorityLock) Release() error {
	lock.mutex.Lock()
	defer lock.mutex.Unlock()

	lock.releases++
	lock.events.add("lock.release")
	return lock.releaseErr
}

// releaseCount returns the number of release attempts.
func (lock *testAuthorityLock) releaseCount() int {
	lock.mutex.Lock()
	defer lock.mutex.Unlock()

	return lock.releases
}

// acceptResult scripts one listener admission outcome.
type acceptResult struct {
	connection local.Conn
	err        error
}

// testListener provides deterministic accepts while retaining blocking listener semantics.
type testListener struct {
	events     *testEventLog
	results    chan acceptResult
	closed     chan struct{}
	closeOnce  sync.Once
	closeErr   error
	accepts    atomic.Int64
	closeCalls atomic.Int64
}

// newTestListener creates a listener whose unfilled script blocks until shutdown.
func newTestListener(events *testEventLog, capacity int) *testListener {
	return &testListener{
		events:  events,
		results: make(chan acceptResult, capacity),
		closed:  make(chan struct{}),
	}
}

// Accept returns the next scripted authenticated connection or wakes when the endpoint closes.
func (listener *testListener) Accept() (local.Conn, error) {
	listener.accepts.Add(1)
	select {
	case result := <-listener.results:
		return result.connection, result.err
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

// Close releases the test endpoint once and wakes a blocked accept.
func (listener *testListener) Close() error {
	listener.closeCalls.Add(1)
	listener.closeOnce.Do(func() {
		listener.events.add("listener.close")
		close(listener.closed)
	})
	return listener.closeErr
}

// Addr supplies the net.Listener diagnostic contract without allocating an endpoint.
func (listener *testListener) Addr() net.Addr {
	return testAddress("harbor-test")
}

// lateAcceptListener returns one authenticated peer only after shutdown has closed its endpoint.
type lateAcceptListener struct {
	events        *testEventLog
	connection    local.Conn
	acceptStarted chan struct{}
	closed        chan struct{}
	acceptOnce    sync.Once
	closeOnce     sync.Once
}

// newLateAcceptListener creates the race boundary where admission completes after shutdown begins.
func newLateAcceptListener(events *testEventLog, connection local.Conn) *lateAcceptListener {
	return &lateAcceptListener{
		events:        events,
		connection:    connection,
		acceptStarted: make(chan struct{}),
		closed:        make(chan struct{}),
	}
}

// Accept waits for endpoint closure before returning the peer already admitted by the operating system.
func (listener *lateAcceptListener) Accept() (local.Conn, error) {
	listener.acceptOnce.Do(func() {
		close(listener.acceptStarted)
	})
	<-listener.closed
	return listener.connection, nil
}

// Close starts the deliberately late accept after the registry has frozen new admission.
func (listener *lateAcceptListener) Close() error {
	listener.closeOnce.Do(func() {
		listener.events.add("listener.close")
		close(listener.closed)
	})
	return nil
}

// Addr supplies the endpoint diagnostic contract for the late-accept race.
func (listener *lateAcceptListener) Addr() net.Addr {
	return testAddress("harbor-late-accept")
}

// testAddress is an inert local address for listener and connection fakes.
type testAddress string

// Network identifies the fake endpoint as an in-process test transport.
func (address testAddress) Network() string {
	return "test"
}

// String returns the stable endpoint label used in diagnostics.
func (address testAddress) String() string {
	return string(address)
}

// testConnection blocks reads until runner shutdown closes it.
type testConnection struct {
	identity  local.PeerIdentity
	events    *testEventLog
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// newTestConnection creates one authenticated fake connection.
func newTestConnection(events *testEventLog, processID uint32) *testConnection {
	return &testConnection{
		identity: local.PeerIdentity{UserID: "test-user", ProcessID: processID},
		events:   events,
		closed:   make(chan struct{}),
	}
}

// Read waits for shutdown so connection-serving tests model an idle peer.
func (connection *testConnection) Read(_ []byte) (int, error) {
	<-connection.closed
	return 0, net.ErrClosed
}

// Write accepts bytes while the fake connection is open.
func (connection *testConnection) Write(payload []byte) (int, error) {
	select {
	case <-connection.closed:
		return 0, net.ErrClosed
	default:
		return len(payload), nil
	}
}

// Close records endpoint interruption exactly once.
func (connection *testConnection) Close() error {
	connection.closeOnce.Do(func() {
		connection.events.add("connection.close")
		close(connection.closed)
	})
	return connection.closeErr
}

// LocalAddr returns an inert client-side endpoint.
func (connection *testConnection) LocalAddr() net.Addr {
	return testAddress("local")
}

// RemoteAddr returns an inert daemon-side endpoint.
func (connection *testConnection) RemoteAddr() net.Addr {
	return testAddress("remote")
}

// SetDeadline is a no-op because tests coordinate interruption explicitly.
func (connection *testConnection) SetDeadline(_ time.Time) error {
	return nil
}

// SetReadDeadline is a no-op because tests coordinate interruption explicitly.
func (connection *testConnection) SetReadDeadline(_ time.Time) error {
	return nil
}

// SetWriteDeadline is a no-op because tests coordinate interruption explicitly.
func (connection *testConnection) SetWriteDeadline(_ time.Time) error {
	return nil
}

// Peer returns the immutable identity admitted by the fake local listener.
func (connection *testConnection) Peer() local.PeerIdentity {
	return connection.identity
}

// connectionServerFunc adapts a test callback to ConnectionServer.
type connectionServerFunc func(context.Context, local.Conn) error

// Serve delegates one connection to the test callback.
func (serve connectionServerFunc) Serve(ctx context.Context, connection local.Conn) error {
	return serve(ctx, connection)
}

// temporaryAcceptError models a retryable net.Listener failure.
type temporaryAcceptError struct {
	err error
}

// Error returns the underlying test diagnostic.
func (failure temporaryAcceptError) Error() string {
	return failure.err.Error()
}

// Unwrap preserves sentinel classification in assertions.
func (failure temporaryAcceptError) Unwrap() error {
	return failure.err
}

// Timeout reports that this failure is transient rather than deadline-driven.
func (failure temporaryAcceptError) Timeout() bool {
	return false
}

// Temporary allows the production accept policy to retry this failure.
func (failure temporaryAcceptError) Temporary() bool {
	return true
}

// TestRunnerCancellationClosesAndJoinsBeforeAuthorityRelease proves the full shutdown ownership order.
func TestRunnerCancellationClosesAndJoinsBeforeAuthorityRelease(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	listener := newTestListener(events, 1)
	connection := newTestConnection(events, 41)
	listener.results <- acceptResult{connection: connection}
	started := make(chan struct{})
	interrupted := make(chan struct{})
	finish := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		events.add("server.start")
		close(started)
		<-ctx.Done()
		close(interrupted)
		<-finish
		events.add("server.exit")
		return ctx.Err()
	})

	runner := mustTestRunner(t, RunnerConfig{
		Server: server,
		Readiness: func(context.Context) error {
			events.add("readiness")
			return nil
		},
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, started, "connection server startup")
	cancel()
	waitSignal(t, interrupted, "connection server cancellation")
	waitEvent(t, events, "listener.close")
	waitEvent(t, events, "connection.close")
	if lock.releaseCount() != 0 {
		t.Fatal("daemon authority released before the connection server joined")
	}
	close(finish)
	if err := waitResult(t, result, "daemon shutdown"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	assertEventOrder(t, events, "readiness", "listener.close")
	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "lock.release")
	if listener.closeCalls.Load() != 1 {
		t.Fatalf("listener close calls = %d, want 1", listener.closeCalls.Load())
	}
}

// TestRunnerStartupFailuresReleaseOnlyOwnedResources proves each boundary unwinds what precedes it.
func TestRunnerStartupFailuresReleaseOnlyOwnedResources(t *testing.T) {
	acquireFailure := errors.New("lock unavailable")
	readinessFailure := errors.New("schema pending")
	listenFailure := errors.New("endpoint unavailable")
	tests := []struct {
		name         string
		acquireErr   error
		readinessErr error
		listenErr    error
		want         error
		wantRelease  int
		wantListen   int
	}{
		{name: "lock", acquireErr: acquireFailure, want: acquireFailure},
		{name: "readiness", readinessErr: readinessFailure, want: readinessFailure, wantRelease: 1},
		{name: "listener", listenErr: listenFailure, want: listenFailure, wantRelease: 1, wantListen: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &testEventLog{}
			lock := &testAuthorityLock{events: events}
			listener := newTestListener(events, 0)
			listenCalls := 0
			runner, err := newRunner(RunnerConfig{
				Server: connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
				Readiness: func(context.Context) error {
					return test.readinessErr
				},
			}, runnerDependencies{
				acquireLock: func() (authorityLock, error) {
					if test.acquireErr != nil {
						return nil, test.acquireErr
					}
					return lock, nil
				},
				listen: func() (local.Listener, error) {
					listenCalls++
					if test.listenErr != nil {
						return nil, test.listenErr
					}
					return listener, nil
				},
				retryAccept: retryableAcceptError,
				acceptDelay: func(context.Context, int) error { return nil },
			})
			if err != nil {
				t.Fatalf("newRunner() error = %v", err)
			}

			err = runner.Run(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want %v", err, test.want)
			}
			if lock.releaseCount() != test.wantRelease {
				t.Fatalf("lock releases = %d, want %d", lock.releaseCount(), test.wantRelease)
			}
			if listenCalls != test.wantListen {
				t.Fatalf("listener calls = %d, want %d", listenCalls, test.wantListen)
			}
			if listener.closeCalls.Load() != 0 {
				t.Fatalf("unopened listener close calls = %d, want 0", listener.closeCalls.Load())
			}
		})
	}
}

// TestRunnerRejectsMissingFactoryResults proves invalid operating-system adapters fail at their boundary.
func TestRunnerRejectsMissingFactoryResults(t *testing.T) {
	tests := []struct {
		name        string
		acquireLock processLockFactory
		listen      listenerFactory
		want        string
		wantRelease int
	}{
		{
			name:        "missing lock",
			acquireLock: func() (authorityLock, error) { return nil, nil },
			listen:      func() (local.Listener, error) { return newTestListener(&testEventLog{}, 0), nil },
			want:        "no lock",
		},
		{
			name: "missing listener",
			acquireLock: func() (authorityLock, error) {
				return &testAuthorityLock{events: &testEventLog{}}, nil
			},
			listen:      func() (local.Listener, error) { return nil, nil },
			want:        "no listener",
			wantRelease: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var acquired *testAuthorityLock
			acquire := test.acquireLock
			if test.wantRelease > 0 {
				acquire = func() (authorityLock, error) {
					lock, err := test.acquireLock()
					if lock != nil {
						acquired = lock.(*testAuthorityLock)
					}
					return lock, err
				}
			}
			runner, err := newRunner(RunnerConfig{
				Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
				Readiness: func(context.Context) error { return nil },
			}, runnerDependencies{
				acquireLock: acquire,
				listen:      test.listen,
				retryAccept: retryableAcceptError,
				acceptDelay: func(context.Context, int) error { return nil },
			})
			if err != nil {
				t.Fatalf("newRunner() error = %v", err)
			}
			err = runner.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want %q", err, test.want)
			}
			if acquired != nil && acquired.releaseCount() != test.wantRelease {
				t.Fatalf("lock releases = %d, want %d", acquired.releaseCount(), test.wantRelease)
			}
		})
	}
}

// TestRunnerBoundsAcceptedConnections proves peers remain in the endpoint backlog until capacity returns.
func TestRunnerBoundsAcceptedConnections(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	listener := newTestListener(events, 3)
	for processID := uint32(1); processID <= 3; processID++ {
		listener.results <- acceptResult{connection: newTestConnection(events, processID)}
	}
	started := make(chan uint32, 3)
	release := make(chan struct{}, 1)
	server := connectionServerFunc(func(ctx context.Context, connection local.Conn) error {
		started <- connection.Peer().ProcessID
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:         server,
		Readiness:      func(context.Context) error { return nil },
		MaxConnections: 2,
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitUint32(t, started, "first accepted connection")
	waitUint32(t, started, "second accepted connection")
	assertNoUint32(t, started, "third accepted connection exceeded the configured bound")
	if accepts := listener.accepts.Load(); accepts != 2 {
		t.Fatalf("Accept() calls at capacity = %d, want 2", accepts)
	}

	release <- struct{}{}
	waitUint32(t, started, "third accepted connection after capacity returned")
	cancel()
	if err := waitResult(t, result, "bounded daemon shutdown"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

// TestRunnerReturnsLateAcceptCleanupFailure proves shutdown rejects and accounts for raced admission.
func TestRunnerReturnsLateAcceptCleanupFailure(t *testing.T) {
	closeFailure := errors.New("late connection close failed")
	events := &testEventLog{}
	connection := newTestConnection(events, 61)
	connection.closeErr = closeFailure
	listener := newLateAcceptListener(events, connection)
	lock := &testAuthorityLock{events: events}
	var serverCalls atomic.Int64
	runner := mustTestRunner(t, RunnerConfig{
		Server: connectionServerFunc(func(context.Context, local.Conn) error {
			serverCalls.Add(1)
			return nil
		}),
		Readiness: func(context.Context) error { return nil },
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, listener.acceptStarted, "accept blocked across shutdown")
	cancel()
	err := waitResult(t, result, "late accept cleanup")
	if !errors.Is(err, closeFailure) {
		t.Fatalf("Run() error = %v, want %v", err, closeFailure)
	}
	if serverCalls.Load() != 0 {
		t.Fatalf("connection server calls = %d, want 0", serverCalls.Load())
	}
	assertEventOrder(t, events, "listener.close", "connection.close")
	assertEventOrder(t, events, "connection.close", "lock.release")
}

// TestRunnerRetriesTransientAcceptsWithoutSpinning proves retry delay and observations precede recovery.
func TestRunnerRetriesTransientAcceptsWithoutSpinning(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	listener := newTestListener(events, 3)
	firstFailure := temporaryAcceptError{err: errors.New("temporary one")}
	secondFailure := temporaryAcceptError{err: errors.New("temporary two")}
	listener.results <- acceptResult{err: firstFailure}
	listener.results <- acceptResult{err: secondFailure}
	listener.results <- acceptResult{connection: newTestConnection(events, 72)}
	delays := make(chan int, 2)
	observations := make(chan error, 2)
	started := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	runner, err := newRunner(RunnerConfig{
		Server:         server,
		Readiness:      func(context.Context) error { return nil },
		ObserveError:   func(err error) { observations <- err },
		MaxConnections: 1,
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen:      func() (local.Listener, error) { return listener, nil },
		retryAccept: retryableAcceptError,
		acceptDelay: func(_ context.Context, failureCount int) error {
			delays <- failureCount
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, started, "connection after transient accepts")
	if first, second := <-delays, <-delays; first != 1 || second != 2 {
		t.Fatalf("retry failure counts = [%d %d], want [1 2]", first, second)
	}
	for index := 0; index < 2; index++ {
		if observation := <-observations; !strings.Contains(observation.Error(), "retry local daemon accept") {
			t.Fatalf("observation = %v, want retry context", observation)
		}
	}
	cancel()
	if err := waitResult(t, result, "recovered daemon shutdown"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

// TestRunnerClassifiesAcceptDelayFailures proves cancellation alone converts failed backoff into clean shutdown.
func TestRunnerClassifiesAcceptDelayFailures(t *testing.T) {
	delayFailure := errors.New("retry clock failed")
	t.Run("infrastructure failure", func(t *testing.T) {
		events := &testEventLog{}
		listener := newTestListener(events, 1)
		listener.results <- acceptResult{err: temporaryAcceptError{err: errors.New("temporary")}}
		lock := &testAuthorityLock{events: events}
		runner, err := newRunner(RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
		}, runnerDependencies{
			acquireLock: func() (authorityLock, error) { return lock, nil },
			listen:      func() (local.Listener, error) { return listener, nil },
			retryAccept: retryableAcceptError,
			acceptDelay: func(context.Context, int) error { return delayFailure },
		})
		if err != nil {
			t.Fatalf("newRunner() error = %v", err)
		}

		err = runner.Run(context.Background())
		if !errors.Is(err, delayFailure) {
			t.Fatalf("Run() error = %v, want %v", err, delayFailure)
		}
		assertEventOrder(t, events, "listener.close", "lock.release")
	})

	t.Run("concurrent cancellation", func(t *testing.T) {
		events := &testEventLog{}
		listener := newTestListener(events, 1)
		listener.results <- acceptResult{err: temporaryAcceptError{err: errors.New("temporary")}}
		lock := &testAuthorityLock{events: events}
		ctx, cancel := context.WithCancel(context.Background())
		runner, err := newRunner(RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
		}, runnerDependencies{
			acquireLock: func() (authorityLock, error) { return lock, nil },
			listen:      func() (local.Listener, error) { return listener, nil },
			retryAccept: retryableAcceptError,
			acceptDelay: func(delayContext context.Context, _ int) error {
				cancel()
				<-delayContext.Done()
				return delayFailure
			},
		})
		if err != nil {
			t.Fatalf("newRunner() error = %v", err)
		}

		if err := runner.Run(ctx); err != nil {
			t.Fatalf("Run() error = %v, want clean cancellation", err)
		}
		assertEventOrder(t, events, "listener.close", "lock.release")
	})
}

// TestRunnerReturnsFatalAcceptAndCleanupFailures proves endpoint death is distinct from peer rejection.
func TestRunnerReturnsFatalAcceptAndCleanupFailures(t *testing.T) {
	fatalAccept := errors.New("listener corrupted")
	listenerClose := errors.New("listener cleanup")
	lockRelease := errors.New("lock cleanup")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events, releaseErr: lockRelease}
	listener := newTestListener(events, 1)
	listener.closeErr = listenerClose
	listener.results <- acceptResult{err: fatalAccept}
	runner := mustTestRunner(t, RunnerConfig{
		Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness: func(context.Context) error { return nil },
	}, lock, listener)

	err := runner.Run(nil)
	for _, want := range []error{fatalAccept, listenerClose, lockRelease} {
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want joined %v", err, want)
		}
	}
	assertEventOrder(t, events, "listener.close", "lock.release")
}

// TestRunnerRejectsInvalidAcceptResults proves a broken listener cannot leak or dispatch a connection.
func TestRunnerRejectsInvalidAcceptResults(t *testing.T) {
	t.Run("nil connection", func(t *testing.T) {
		events := &testEventLog{}
		listener := newTestListener(events, 1)
		listener.results <- acceptResult{}
		runner := mustTestRunner(t, RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
		}, &testAuthorityLock{events: events}, listener)

		err := runner.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "listener returned no connection") {
			t.Fatalf("Run() error = %v, want missing connection error", err)
		}
	})

	t.Run("connection with error", func(t *testing.T) {
		acceptFailure := errors.New("accept failed")
		closeFailure := errors.New("rejected connection close failed")
		events := &testEventLog{}
		listener := newTestListener(events, 1)
		connection := newTestConnection(events, 104)
		connection.closeErr = closeFailure
		listener.results <- acceptResult{connection: connection, err: acceptFailure}
		runner := mustTestRunner(t, RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
		}, &testAuthorityLock{events: events}, listener)

		err := runner.Run(context.Background())
		for _, want := range []error{acceptFailure, closeFailure} {
			if !errors.Is(err, want) {
				t.Fatalf("Run() error = %v, want joined %v", err, want)
			}
		}
		if events.index("connection.close") < 0 {
			t.Fatal("connection returned with an accept error was not closed")
		}
	})
}

// TestRunnerContainsConnectionServerFailure proves a failed handshake cannot terminate the daemon endpoint.
func TestRunnerContainsConnectionServerFailure(t *testing.T) {
	sessionFailure := errors.New("handshake rejected")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	listener := newTestListener(events, 2)
	listener.results <- acceptResult{connection: newTestConnection(events, 90)}
	listener.results <- acceptResult{connection: newTestConnection(events, 91)}
	secondStarted := make(chan struct{})
	observed := make(chan error, 1)
	var calls atomic.Int64
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		if calls.Add(1) == 1 {
			return sessionFailure
		}
		close(secondStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:       server,
		Readiness:    func(context.Context) error { return nil },
		ObserveError: func(err error) { observed <- err },
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, secondStarted, "second connection after a session failure")
	if err := waitResult(t, observed, "session failure observation"); !errors.Is(err, sessionFailure) {
		t.Fatalf("observed error = %v, want %v", err, sessionFailure)
	}
	cancel()
	if err := waitResult(t, result, "daemon shutdown after a session failure"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

// TestRunnerObservesCompletedConnectionCloseFailure proves peer cleanup stays local while serving continues.
func TestRunnerObservesCompletedConnectionCloseFailure(t *testing.T) {
	closeFailure := errors.New("peer connection close failed")
	events := &testEventLog{}
	listener := newTestListener(events, 1)
	connection := newTestConnection(events, 92)
	connection.closeErr = closeFailure
	listener.results <- acceptResult{connection: connection}
	observed := make(chan error, 1)
	runner := mustTestRunner(t, RunnerConfig{
		Server:       connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:    func(context.Context) error { return nil },
		ObserveError: func(err error) { observed <- err },
	}, &testAuthorityLock{events: events}, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	if err := waitResult(t, observed, "completed connection close observation"); !errors.Is(err, closeFailure) {
		t.Fatalf("observed error = %v, want %v", err, closeFailure)
	}
	cancel()
	if err := waitResult(t, result, "daemon shutdown after peer cleanup failure"); err != nil {
		t.Fatalf("Run() error = %v, want connection-local observation only", err)
	}
}

// TestNewRunnerRejectsInvalidWiring proves configuration fails before acquiring daemon authority.
func TestNewRunnerRejectsInvalidWiring(t *testing.T) {
	server := connectionServerFunc(func(context.Context, local.Conn) error { return nil })
	readiness := ReadinessCheck(func(context.Context) error { return nil })
	validDependencies := runnerDependencies{
		acquireLock: func() (authorityLock, error) { return &testAuthorityLock{events: &testEventLog{}}, nil },
		listen:      func() (local.Listener, error) { return newTestListener(&testEventLog{}, 0), nil },
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	}
	tests := []struct {
		name         string
		config       RunnerConfig
		dependencies runnerDependencies
	}{
		{name: "server", config: RunnerConfig{Readiness: readiness}, dependencies: validDependencies},
		{name: "readiness", config: RunnerConfig{Server: server}, dependencies: validDependencies},
		{name: "negative bound", config: RunnerConfig{Server: server, Readiness: readiness, MaxConnections: -1}, dependencies: validDependencies},
		{name: "excessive bound", config: RunnerConfig{Server: server, Readiness: readiness, MaxConnections: maximumConnections + 1}, dependencies: validDependencies},
		{name: "lock factory", config: RunnerConfig{Server: server, Readiness: readiness}, dependencies: runnerDependencies{listen: validDependencies.listen, retryAccept: validDependencies.retryAccept, acceptDelay: validDependencies.acceptDelay}},
		{name: "listener factory", config: RunnerConfig{Server: server, Readiness: readiness}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, retryAccept: validDependencies.retryAccept, acceptDelay: validDependencies.acceptDelay}},
		{name: "retry policy", config: RunnerConfig{Server: server, Readiness: readiness}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, listen: validDependencies.listen, acceptDelay: validDependencies.acceptDelay}},
		{name: "retry delay", config: RunnerConfig{Server: server, Readiness: readiness}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, listen: validDependencies.listen, retryAccept: validDependencies.retryAccept}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runner, err := newRunner(test.config, test.dependencies); err == nil || runner != nil {
				t.Fatalf("newRunner() = (%v, %v), want nil runner and wiring error", runner, err)
			}
		})
	}

	runner, err := newRunner(RunnerConfig{Server: server, Readiness: readiness}, validDependencies)
	if err != nil {
		t.Fatalf("newRunner() default bound error = %v", err)
	}
	if runner.config.MaxConnections != defaultMaxConnections {
		t.Fatalf("default maximum connections = %d, want %d", runner.config.MaxConnections, defaultMaxConnections)
	}
	productionRunner, err := NewRunner(RunnerConfig{Server: server, Readiness: readiness})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if productionRunner == nil {
		t.Fatal("NewRunner() returned no production runner")
	}
}

// TestRunnerContainsObserverPanic proves optional diagnostics cannot relinquish daemon authority.
func TestRunnerContainsObserverPanic(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	listener := newTestListener(events, 2)
	listener.results <- acceptResult{connection: newTestConnection(events, 101)}
	listener.results <- acceptResult{connection: newTestConnection(events, 102)}
	secondStarted := make(chan struct{})
	var calls atomic.Int64
	var observerCalls atomic.Int64
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		if calls.Add(1) == 1 {
			return errors.New("peer failure")
		}
		close(secondStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:    server,
		Readiness: func(context.Context) error { return nil },
		ObserveError: func(err error) {
			observerCalls.Add(1)
			if err == nil {
				t.Error("observer received a nil error")
			}
			panic("diagnostic sink failed")
		},
	}, lock, listener)
	runner.observe(nil)
	if observerCalls.Load() != 0 {
		t.Fatal("observer was called for a nil error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, secondStarted, "second connection after observer panic")
	cancel()
	if err := waitResult(t, result, "daemon shutdown after observer panic"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if observerCalls.Load() != 1 {
		t.Fatalf("observer calls = %d, want 1", observerCalls.Load())
	}
}

// TestRunnerCleanupErrorsStillJoinBeforeAuthorityRelease proves failed cleanup preserves lock-last ordering.
func TestRunnerCleanupErrorsStillJoinBeforeAuthorityRelease(t *testing.T) {
	listenerFailure := errors.New("listener close failed")
	connectionFailure := errors.New("connection close failed")
	lockFailure := errors.New("lock release failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events, releaseErr: lockFailure}
	listener := newTestListener(events, 1)
	listener.closeErr = listenerFailure
	connection := newTestConnection(events, 103)
	connection.closeErr = connectionFailure
	listener.results <- acceptResult{connection: connection}
	started := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		close(started)
		<-ctx.Done()
		events.add("server.exit")
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:    server,
		Readiness: func(context.Context) error { return nil },
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, started, "connection before cleanup failure")
	cancel()
	err := waitResult(t, result, "daemon cleanup failure")
	for _, want := range []error{listenerFailure, connectionFailure, lockFailure} {
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want joined %v", err, want)
		}
	}
	assertEventOrder(t, events, "listener.close", "lock.release")
	assertEventOrder(t, events, "connection.close", "lock.release")
	assertEventOrder(t, events, "server.exit", "lock.release")
}

// TestRetryableAcceptErrorClassifiesPeerAndNetworkFailures proves only connection-local errors retry by default.
func TestRetryableAcceptErrorClassifiesPeerAndNetworkFailures(t *testing.T) {
	if !retryableAcceptError(local.ErrPeerUnauthorized) {
		t.Fatal("unauthorized local peer must be connection-local")
	}
	if !retryableAcceptError(temporaryAcceptError{err: errors.New("temporary")}) {
		t.Fatal("temporary network error must be retryable")
	}
	if retryableAcceptError(errors.New("endpoint failed")) {
		t.Fatal("unclassified endpoint failure must terminate daemon authority")
	}
}

// TestWaitForAcceptRetryHonorsCancellation proves backoff cannot delay daemon shutdown.
func TestWaitForAcceptRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForAcceptRetry(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForAcceptRetry() error = %v, want context.Canceled", err)
	}
}

// TestWaitForAcceptRetryCompletes proves the production delay resumes admission without cancellation.
func TestWaitForAcceptRetryCompletes(t *testing.T) {
	if err := waitForAcceptRetry(context.Background(), 1); err != nil {
		t.Fatalf("waitForAcceptRetry() error = %v, want nil", err)
	}
}

// TestErrorCollectorIgnoresNil proves optional cleanup branches cannot manufacture a daemon failure.
func TestErrorCollectorIgnoresNil(t *testing.T) {
	collector := &errorCollector{}
	collector.add(nil)
	if err := collector.result(); err != nil {
		t.Fatalf("collector result = %v, want nil", err)
	}
}

// mustTestRunner builds a runner with production policy and deterministic process boundaries.
func mustTestRunner(
	t *testing.T,
	config RunnerConfig,
	lock authorityLock,
	listener local.Listener,
) *Runner {
	t.Helper()

	runner, err := newRunner(config, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen:      func() (local.Listener, error) { return listener, nil },
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	return runner
}

// waitSignal waits for a deterministic test boundary without allowing a stalled daemon to hang CI.
func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", description)
	}
}

// waitUint32 waits for one accepted connection identity.
func waitUint32(t *testing.T, values <-chan uint32, description string) uint32 {
	t.Helper()

	select {
	case value := <-values:
		return value
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", description)
		return 0
	}
}

// assertNoUint32 ensures a bounded accept cannot start while all slots remain active.
func assertNoUint32(t *testing.T, values <-chan uint32, description string) {
	t.Helper()

	select {
	case value := <-values:
		t.Fatalf("%s: process %d started", description, value)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitResult waits for an asynchronous error result with a bounded test deadline.
func waitResult(t *testing.T, result <-chan error, description string) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

// waitEvent waits until concurrent lifecycle work records the requested event.
func waitEvent(t *testing.T, events *testEventLog, event string) {
	t.Helper()

	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if events.index(event) >= 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for event %q in %v", event, events.snapshot())
}

// assertEventOrder verifies one lifecycle boundary completed before another.
func assertEventOrder(t *testing.T, events *testEventLog, before string, after string) {
	t.Helper()

	beforeIndex := events.index(before)
	afterIndex := events.index(after)
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("event order %q before %q not found in %v", before, after, events.snapshot())
	}
}
