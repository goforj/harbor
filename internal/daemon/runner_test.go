package daemon

import (
	"context"
	"errors"
	"fmt"
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
	events        *testEventLog
	releaseErr    error
	releaseSignal chan struct{}
	releaseOnce   sync.Once
	mutex         sync.Mutex
	releases      int
}

// Release records the point at which singleton daemon authority is relinquished.
func (lock *testAuthorityLock) Release() error {
	lock.mutex.Lock()
	defer lock.mutex.Unlock()

	lock.releases++
	lock.events.add("lock.release")
	if lock.releaseSignal != nil {
		lock.releaseOnce.Do(func() { close(lock.releaseSignal) })
	}
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
	acceptOnce sync.Once
	entered    chan struct{}
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
	if listener.entered != nil {
		listener.acceptOnce.Do(func() { close(listener.entered) })
	}
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

// gatedCloseListener pauses endpoint closure so competing terminal signals can be ordered exactly.
type gatedCloseListener struct {
	*testListener
	closeEntered chan struct{}
	releaseClose <-chan struct{}
	closeGate    sync.Once
}

// Close exposes the frozen-admission boundary before allowing endpoint teardown to continue.
func (listener *gatedCloseListener) Close() error {
	listener.closeGate.Do(func() { close(listener.closeEntered) })
	<-listener.releaseClose
	return listener.testListener.Close()
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

// testRuntime exposes daemon-owned infrastructure lifecycle boundaries and injected failures.
type testRuntime struct {
	events       *testEventLog
	done         chan struct{}
	closeOnce    sync.Once
	mutex        sync.Mutex
	terminalErr  error
	startErr     error
	closeErr     error
	startFunc    func(context.Context) error
	closeFunc    func(context.Context) error
	doneFunc     func() <-chan struct{}
	startContext chan context.Context
	starts       atomic.Int64
	closes       atomic.Int64
}

// newTestRuntime creates a healthy runtime that remains active until Close.
func newTestRuntime(events *testEventLog) *testRuntime {
	return &testRuntime{
		events:       events,
		done:         make(chan struct{}),
		startContext: make(chan context.Context, 1),
	}
}

// Start records its private daemon lifecycle context before applying an injected result.
func (runtime *testRuntime) Start(ctx context.Context) error {
	runtime.starts.Add(1)
	runtime.events.add("runtime.start")
	runtime.startContext <- ctx
	if runtime.startFunc != nil {
		return runtime.startFunc(ctx)
	}
	return runtime.startErr
}

// Done closes when Close or fail makes the test runtime terminal.
func (runtime *testRuntime) Done() <-chan struct{} {
	if runtime.doneFunc != nil {
		return runtime.doneFunc()
	}
	return runtime.done
}

// Err returns the injected terminal runtime failure.
func (runtime *testRuntime) Err() error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.terminalErr
}

// Close records cleanup, applies injected behavior, and publishes terminal completion once.
func (runtime *testRuntime) Close(ctx context.Context) error {
	runtime.closes.Add(1)
	runtime.events.add("runtime.close")
	var err error
	if runtime.closeFunc != nil {
		err = runtime.closeFunc(ctx)
	} else {
		err = runtime.closeErr
	}
	if runtime.done != nil {
		runtime.closeOnce.Do(func() { close(runtime.done) })
	}
	return err
}

// fail publishes one unexpected runtime exit with its retained terminal failure.
func (runtime *testRuntime) fail(err error) {
	runtime.mutex.Lock()
	runtime.terminalErr = err
	runtime.mutex.Unlock()
	runtime.events.add("runtime.exit")
	if runtime.done != nil {
		runtime.closeOnce.Do(func() { close(runtime.done) })
	}
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
	runtime := newTestRuntime(events)
	runtime.closeFunc = func(ctx context.Context) error {
		lifecycle := <-runtime.startContext
		if lifecycle.Err() != nil {
			return errors.New("runtime lifecycle was cancelled before IPC joined")
		}
		deadline, bounded := ctx.Deadline()
		if !bounded {
			return errors.New("runtime cleanup context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 35*time.Second {
			return fmt.Errorf("runtime cleanup budget %s does not exceed the controller budget", remaining)
		}
		return nil
	}
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
		Server:  server,
		Runtime: runtime,
		Readiness: func(context.Context) error {
			events.add("readiness")
			return nil
		},
		Recovery: func(context.Context) error {
			events.add("recovery")
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

	assertEventOrder(t, events, "readiness", "runtime.start")
	assertEventOrder(t, events, "readiness", "recovery")
	assertEventOrder(t, events, "recovery", "runtime.start")
	assertEventOrder(t, events, "runtime.start", "listener.open")
	assertEventOrder(t, events, "listener.open", "listener.close")
	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close", "lock.release")
	if listener.closeCalls.Load() != 1 {
		t.Fatalf("listener close calls = %d, want 1", listener.closeCalls.Load())
	}
	if runtime.starts.Load() != 1 || runtime.closes.Load() != 1 {
		t.Fatalf("runtime calls = start %d close %d, want one each", runtime.starts.Load(), runtime.closes.Load())
	}
}

// TestRunnerRequestedShutdownClosesAndJoinsBeforeAuthorityRelease proves an authenticated request follows the lock-last lifecycle.
func TestRunnerRequestedShutdownClosesAndJoinsBeforeAuthorityRelease(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	listener := newTestListener(events, 1)
	connection := newTestConnection(events, 43)
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
	shutdown := NewShutdown()
	runner := mustTestRunner(t, RunnerConfig{
		Server:            server,
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: shutdown.Requested(),
	}, lock, listener)
	result := make(chan error, 1)
	go func() { result <- runner.Run(context.Background()) }()

	waitSignal(t, started, "connection server startup")
	shutdown.Request()
	waitSignal(t, interrupted, "connection server requested shutdown")
	waitEvent(t, events, "listener.close")
	waitEvent(t, events, "connection.close")
	if lock.releaseCount() != 0 {
		t.Fatal("daemon authority released before the requested connection shutdown joined")
	}
	close(finish)
	if err := waitResult(t, result, "requested daemon shutdown"); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close", "lock.release")
	if listener.closeCalls.Load() != 1 || runtime.closes.Load() != 1 || lock.releaseCount() != 1 {
		t.Fatalf(
			"cleanup calls = listener %d runtime %d lock %d, want one each",
			listener.closeCalls.Load(),
			runtime.closes.Load(),
			lock.releaseCount(),
		)
	}
}

// TestRunnerRequestedShutdownRetainsCleanupFailures verifies an intentional request does not hide failed cleanup.
func TestRunnerRequestedShutdownRetainsCleanupFailures(t *testing.T) {
	listenerFailure := errors.New("listener cleanup failed")
	runtimeFailure := errors.New("runtime cleanup failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	runtime.closeErr = runtimeFailure
	listener := newTestListener(events, 0)
	listener.entered = make(chan struct{})
	listener.closeErr = listenerFailure
	shutdown := NewShutdown()
	runner := mustTestRunner(t, RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: shutdown.Requested(),
	}, lock, listener)
	result := make(chan error, 1)
	go func() { result <- runner.Run(context.Background()) }()
	waitSignal(t, listener.entered, "requested shutdown listener admission")

	shutdown.Request()
	err := waitResult(t, result, "requested shutdown cleanup failure")
	if !errors.Is(err, listenerFailure) || !errors.Is(err, runtimeFailure) {
		t.Fatalf("Run() error = %v, want listener and runtime cleanup failures", err)
	}
	if lock.releaseCount() != 1 {
		t.Fatalf("authority release calls = %d, want 1 after completed cleanup", lock.releaseCount())
	}
}

// TestRunnerRequestedShutdownRacingRuntimeLossRetainsRuntimeFailure verifies intent cannot reclassify failed infrastructure as clean.
func TestRunnerRequestedShutdownRacingRuntimeLossRetainsRuntimeFailure(t *testing.T) {
	runtimeFailure := errors.New("runtime failed during requested shutdown")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	baseListener := newTestListener(events, 0)
	baseListener.entered = make(chan struct{})
	closeEntered := make(chan struct{})
	releaseClose := make(chan struct{})
	listener := &gatedCloseListener{
		testListener: baseListener,
		closeEntered: closeEntered,
		releaseClose: releaseClose,
	}
	shutdown := NewShutdown()
	runner := mustTestRunner(t, RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: shutdown.Requested(),
	}, lock, listener)
	result := make(chan error, 1)
	go func() { result <- runner.Run(context.Background()) }()
	waitSignal(t, baseListener.entered, "runtime-loss race listener admission")

	shutdown.Request()
	waitSignal(t, closeEntered, "requested shutdown listener closure")
	runtime.fail(runtimeFailure)
	close(releaseClose)
	err := waitResult(t, result, "requested shutdown runtime-loss race")
	if !errors.Is(err, runtimeFailure) {
		t.Fatalf("Run() error = %v, want runtime failure", err)
	}
	if lock.releaseCount() != 1 {
		t.Fatalf("authority release calls = %d, want 1", lock.releaseCount())
	}
}

// TestRunnerHoldsAuthorityAcrossNestedRuntimeCleanup proves the outer budget outlives a healthy inner drain.
func TestRunnerHoldsAuthorityAcrossNestedRuntimeCleanup(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	closeEntered := make(chan struct{})
	releaseClose := make(chan struct{})
	runtime.closeFunc = func(ctx context.Context) error {
		close(closeEntered)
		select {
		case <-releaseClose:
			events.add("runtime.close.finished")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	listener := newTestListener(events, 1)
	listener.results <- acceptResult{connection: newTestConnection(events, 42)}
	serverStarted := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		close(serverStarted)
		<-ctx.Done()
		events.add("server.exit")
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:              server,
		Readiness:           func(context.Context) error { return nil },
		Runtime:             runtime,
		RuntimeCloseTimeout: 250 * time.Millisecond,
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(ctx) }()
	waitSignal(t, serverStarted, "connection before nested cleanup")
	cancel()
	waitSignal(t, closeEntered, "nested runtime cleanup")
	if lock.releaseCount() != 0 {
		t.Fatal("daemon authority released while nested runtime cleanup remained active")
	}
	close(releaseClose)
	if err := waitResult(t, result, "nested runtime cleanup"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close.finished", "lock.release")
}

// TestRunnerStartupFailuresReleaseOnlyOwnedResources proves each boundary unwinds what precedes it.
func TestRunnerStartupFailuresReleaseOnlyOwnedResources(t *testing.T) {
	acquireFailure := errors.New("lock unavailable")
	readinessFailure := errors.New("schema pending")
	recoveryFailure := errors.New("state recovery failed")
	runtimeFailure := errors.New("runtime unavailable")
	listenFailure := errors.New("endpoint unavailable")
	runtimeCleanupFailure := errors.New("runtime cleanup unavailable")
	tests := []struct {
		name         string
		acquireErr   error
		readinessErr error
		recoveryErr  error
		runtimeErr   error
		closeErr     error
		listenErr    error
		want         error
		wantRelease  int
		wantListen   int
		wantStart    int64
		wantClose    int64
	}{
		{name: "lock", acquireErr: acquireFailure, want: acquireFailure},
		{name: "readiness", readinessErr: readinessFailure, want: readinessFailure, wantRelease: 1},
		{name: "recovery", recoveryErr: recoveryFailure, want: recoveryFailure, wantRelease: 1},
		{name: "runtime", runtimeErr: runtimeFailure, want: runtimeFailure, wantRelease: 1, wantStart: 1},
		{name: "listener", listenErr: listenFailure, closeErr: runtimeCleanupFailure, want: listenFailure, wantRelease: 1, wantListen: 1, wantStart: 1, wantClose: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &testEventLog{}
			lock := &testAuthorityLock{events: events}
			runtime := newTestRuntime(events)
			runtime.startErr = test.runtimeErr
			runtime.closeErr = test.closeErr
			listener := newTestListener(events, 0)
			listenCalls := 0
			runner, err := newRunner(RunnerConfig{
				Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
				Runtime:           runtime,
				ShutdownRequested: make(chan struct{}),
				Readiness: func(context.Context) error {
					return test.readinessErr
				},
				Recovery: func(context.Context) error {
					return test.recoveryErr
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
			if test.closeErr != nil && !errors.Is(err, test.closeErr) {
				t.Fatalf("Run() error = %v, want cleanup %v", err, test.closeErr)
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
			if runtime.starts.Load() != test.wantStart || runtime.closes.Load() != test.wantClose {
				t.Fatalf("runtime calls = start %d close %d, want start %d close %d", runtime.starts.Load(), runtime.closes.Load(), test.wantStart, test.wantClose)
			}
			if test.name == "listener" {
				assertEventOrder(t, events, "runtime.start", "runtime.close")
				assertEventOrder(t, events, "runtime.close", "lock.release")
			}
		})
	}
}

// TestRunnerCancellationDuringRecoveryReturnsOnlyLockCleanup keeps interrupted recovery from publishing runtime state.
func TestRunnerCancellationDuringRecoveryReturnsOnlyLockCleanup(t *testing.T) {
	events := &testEventLog{}
	lockFailure := errors.New("lock cleanup failed")
	lock := &testAuthorityLock{events: events, releaseErr: lockFailure}
	runtime := newTestRuntime(events)
	ctx, cancel := context.WithCancel(context.Background())
	runner := mustTestRunner(t, RunnerConfig{
		Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness: func(context.Context) error { return nil },
		Recovery: func(context.Context) error {
			cancel()
			return context.Canceled
		},
		Runtime: runtime,
	}, lock, newTestListener(events, 0))

	err := runner.Run(ctx)
	if !errors.Is(err, lockFailure) || errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want only %v", err, lockFailure)
	}
	if runtime.starts.Load() != 0 || runtime.closes.Load() != 0 {
		t.Fatalf("runtime calls = start %d close %d, want 0", runtime.starts.Load(), runtime.closes.Load())
	}
	if events.index("listener.open") >= 0 {
		t.Fatalf("listener opened after recovery cancellation: events %v", events.snapshot())
	}
}

// TestRunnerCancellationInterruptsRuntimeStartup proves private runtime startup remains caller-abortable.
func TestRunnerCancellationInterruptsRuntimeStartup(t *testing.T) {
	type contextMarker string
	const marker contextMarker = "caller"
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	startEntered := make(chan context.Context, 1)
	runtime.startFunc = func(ctx context.Context) error {
		startEntered <- ctx
		<-ctx.Done()
		events.add("runtime.start.abort")
		return ctx.Err()
	}
	var listenCalls atomic.Int64
	runner, err := newRunner(RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: make(chan struct{}),
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			listenCalls.Add(1)
			return newTestListener(events, 0), nil
		},
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	caller, cancel := context.WithCancel(context.WithValue(context.Background(), marker, "present"))
	result := make(chan error, 1)
	go func() { result <- runner.Run(caller) }()
	var lifecycle context.Context
	select {
	case lifecycle = <-startEntered:
	case <-time.After(testWait):
		t.Fatal("runtime startup did not begin")
	}
	if lifecycle.Value(marker) != nil {
		t.Fatal("runtime startup inherited caller values instead of using a private lifecycle")
	}
	if lifecycle.Err() != nil {
		t.Fatalf("runtime lifecycle began cancelled: %v", lifecycle.Err())
	}
	cancel()
	if err := waitResult(t, result, "cancelled runtime startup"); err != nil {
		t.Fatalf("Run() error = %v, want intentional cancellation", err)
	}
	if listenCalls.Load() != 0 {
		t.Fatalf("listener calls = %d, want 0", listenCalls.Load())
	}
	if runtime.starts.Load() != 1 || runtime.closes.Load() != 0 {
		t.Fatalf("runtime calls = start %d close %d, want start 1 close 0", runtime.starts.Load(), runtime.closes.Load())
	}
	assertEventOrder(t, events, "runtime.start.abort", "lock.release")
}

// TestRunnerCancellationDuringRuntimeStartupRetainsRollbackFailure proves abort cleanup survives end-to-end classification.
func TestRunnerCancellationDuringRuntimeStartupRetainsRollbackFailure(t *testing.T) {
	rollbackFailure := errors.New("runtime startup rollback failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	runtime.startFunc = func(ctx context.Context) error {
		<-ctx.Done()
		events.add("runtime.start.rollback")
		return errors.Join(ctx.Err(), rollbackFailure)
	}
	var listenCalls atomic.Int64
	runner, err := newRunner(RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: make(chan struct{}),
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			listenCalls.Add(1)
			return newTestListener(events, 0), nil
		},
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	caller, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(caller) }()
	<-runtime.startContext
	cancel()

	err = waitResult(t, result, "cancelled runtime startup rollback")
	if !errors.Is(err, rollbackFailure) {
		t.Fatalf("Run() error = %v, want rollback failure %v", err, rollbackFailure)
	}
	if listenCalls.Load() != 0 {
		t.Fatalf("listener calls = %d, want zero", listenCalls.Load())
	}
	if runtime.starts.Load() != 1 || runtime.closes.Load() != 0 {
		t.Fatalf("runtime calls = start %d close %d, want start 1 close 0", runtime.starts.Load(), runtime.closes.Load())
	}
	if lock.releaseCount() != 1 {
		t.Fatalf("lock releases = %d, want one", lock.releaseCount())
	}
	assertEventOrder(t, events, "runtime.start.rollback", "lock.release")
}

// TestRunnerCancellationDuringReadinessReturnsOnlyLockCleanup keeps caller shutdown intentional before startup.
func TestRunnerCancellationDuringReadinessReturnsOnlyLockCleanup(t *testing.T) {
	readinessFailure := errors.New("readiness interrupted")
	lockFailure := errors.New("lock cleanup failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events, releaseErr: lockFailure}
	runtime := newTestRuntime(events)
	caller, cancel := context.WithCancel(context.Background())
	runner, err := newRunner(RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		ShutdownRequested: make(chan struct{}),
		Readiness: func(context.Context) error {
			cancel()
			return readinessFailure
		},
		Runtime: runtime,
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen:      func() (local.Listener, error) { return newTestListener(events, 0), nil },
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	err = runner.Run(caller)
	if !errors.Is(err, lockFailure) || errors.Is(err, readinessFailure) {
		t.Fatalf("Run() error = %v, want only lock cleanup %v", err, lockFailure)
	}
	if runtime.starts.Load() != 0 || runtime.closes.Load() != 0 {
		t.Fatalf("runtime calls = start %d close %d, want 0", runtime.starts.Load(), runtime.closes.Load())
	}
}

// TestRunnerRejectsMissingRuntimeCompletionSignal closes started infrastructure without opening IPC.
func TestRunnerRejectsMissingRuntimeCompletionSignal(t *testing.T) {
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	runtime.done = nil
	var listenCalls atomic.Int64
	runner, err := newRunner(RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: make(chan struct{}),
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			listenCalls.Add(1)
			return newTestListener(events, 0), nil
		},
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}
	err = runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no completion signal") {
		t.Fatalf("Run() error = %v, want missing runtime completion signal", err)
	}
	if listenCalls.Load() != 0 {
		t.Fatalf("listener calls = %d, want 0", listenCalls.Load())
	}
	if runtime.starts.Load() != 1 || runtime.closes.Load() != 1 {
		t.Fatalf("runtime calls = start %d close %d, want one each", runtime.starts.Load(), runtime.closes.Load())
	}
	assertEventOrder(t, events, "runtime.close", "lock.release")
}

// TestRunnerRetainsAuthorityWhenMissingCompletionCloseFails prevents failed cleanup from transferring unverifiable ownership.
func TestRunnerRetainsAuthorityWhenMissingCompletionCloseFails(t *testing.T) {
	closeFailure := errors.New("runtime cleanup failed")
	events := &testEventLog{}
	releaseSignal := make(chan struct{})
	lock := &testAuthorityLock{events: events, releaseSignal: releaseSignal}
	runtime := newTestRuntime(events)
	runtime.done = nil
	runtime.closeErr = closeFailure
	var listenCalls atomic.Int64
	runner, err := newRunner(RunnerConfig{
		Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:         func(context.Context) error { return nil },
		Runtime:           runtime,
		ShutdownRequested: make(chan struct{}),
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			listenCalls.Add(1)
			return newTestListener(events, 0), nil
		},
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}

	err = runner.Run(context.Background())
	for _, want := range []error{closeFailure, ErrRuntimeCleanupIncomplete} {
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want %v", err, want)
		}
	}
	if !strings.Contains(err.Error(), "no completion signal") {
		t.Fatalf("Run() error = %v, want missing completion signal", err)
	}
	if listenCalls.Load() != 0 || runtime.closes.Load() != 1 {
		t.Fatalf("calls = listener %d runtime Close %d, want 0 and 1", listenCalls.Load(), runtime.closes.Load())
	}
	if lock.releaseCount() != 0 {
		t.Fatalf("lock releases after failed unverifiable cleanup = %d, want zero", lock.releaseCount())
	}
	select {
	case <-releaseSignal:
		t.Fatal("authority released after failed unverifiable cleanup")
	default:
	}
}

// TestRunnerRetainsAuthorityWhenMissingCompletionCannotBeClosed keeps unverifiable ownership fail-closed for process lifetime.
func TestRunnerRetainsAuthorityWhenMissingCompletionCannotBeClosed(t *testing.T) {
	events := &testEventLog{}
	releaseSignal := make(chan struct{})
	lock := &testAuthorityLock{events: events, releaseSignal: releaseSignal}
	runtime := newTestRuntime(events)
	runtime.done = nil
	closeRelease := make(chan struct{})
	closeReturned := make(chan struct{})
	runtime.closeFunc = func(context.Context) error {
		<-closeRelease
		close(closeReturned)
		return nil
	}
	var listenCalls atomic.Int64
	runner, err := newRunner(RunnerConfig{
		Server:              connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness:           func(context.Context) error { return nil },
		Runtime:             runtime,
		ShutdownRequested:   make(chan struct{}),
		RuntimeCloseTimeout: 20 * time.Millisecond,
	}, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			listenCalls.Add(1)
			return newTestListener(events, 0), nil
		},
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatalf("newRunner() error = %v", err)
	}

	startedCleanup := time.Now()
	err = runner.Run(context.Background())
	if !errors.Is(err, ErrRuntimeCleanupIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want incomplete outer timeout", err)
	}
	if !strings.Contains(err.Error(), "no completion signal") {
		t.Fatalf("Run() error = %v, want missing completion signal", err)
	}
	if elapsed := time.Since(startedCleanup); elapsed >= time.Second {
		t.Fatalf("Run() returned after %s, want bounded cleanup", elapsed)
	}
	if listenCalls.Load() != 0 || runtime.closes.Load() != 1 {
		t.Fatalf("calls = listener %d runtime Close %d, want 0 and 1", listenCalls.Load(), runtime.closes.Load())
	}
	if lock.releaseCount() != 0 {
		t.Fatalf("lock releases without runtime completion proof = %d, want zero", lock.releaseCount())
	}

	close(closeRelease)
	waitSignal(t, closeReturned, "broken runtime Close return")
	select {
	case <-releaseSignal:
		t.Fatal("authority released after unverifiable cleanup")
	default:
	}
}

// TestRunnerUnexpectedRuntimeExitDrainsIPCBeforeCleanup makes runtime loss terminal without duplicate causes.
func TestRunnerUnexpectedRuntimeExitDrainsIPCBeforeCleanup(t *testing.T) {
	runtimeFailure := errors.New("runtime listener ownership lost")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	runtime.closeErr = runtimeFailure
	listener := newTestListener(events, 1)
	listener.results <- acceptResult{connection: newTestConnection(events, 301)}
	serverStarted := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		close(serverStarted)
		<-ctx.Done()
		events.add("server.exit")
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:    server,
		Readiness: func(context.Context) error { return nil },
		Runtime:   runtime,
	}, lock, listener)
	result := make(chan error, 1)
	go func() { result <- runner.Run(context.Background()) }()
	waitSignal(t, serverStarted, "connection before unexpected runtime exit")
	runtime.fail(runtimeFailure)
	err := waitResult(t, result, "unexpected runtime exit")
	if !errors.Is(err, runtimeFailure) {
		t.Fatalf("Run() error = %v, want %v", err, runtimeFailure)
	}
	if count := strings.Count(err.Error(), runtimeFailure.Error()); count != 1 {
		t.Fatalf("runtime failure occurrences = %d in %q, want 1", count, err)
	}
	if listener.closeCalls.Load() != 1 || runtime.closes.Load() != 1 || lock.releaseCount() != 1 {
		t.Fatalf("cleanup calls = listener %d runtime %d lock %d, want one each", listener.closeCalls.Load(), runtime.closes.Load(), lock.releaseCount())
	}
	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close", "lock.release")
}

// TestRunnerRuntimeShutdownDoesNotManufactureEndpointFailure verifies listener closure is classified through admission cancellation.
func TestRunnerRuntimeShutdownDoesNotManufactureEndpointFailure(t *testing.T) {
	runtimeFailure := errors.New("runtime generation failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	listener := newTestListener(events, 0)
	listener.entered = make(chan struct{})
	runner := mustTestRunner(t, RunnerConfig{
		Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
		Readiness: func(context.Context) error { return nil },
		Runtime:   runtime,
	}, lock, listener)
	result := make(chan error, 1)
	go func() { result <- runner.Run(context.Background()) }()
	waitSignal(t, listener.entered, "runtime-owned endpoint admission")

	runtime.fail(runtimeFailure)
	err := waitResult(t, result, "runtime-triggered endpoint shutdown")
	if !errors.Is(err, runtimeFailure) {
		t.Fatalf("Run() error = %v, want %v", err, runtimeFailure)
	}
	if strings.Contains(err.Error(), "accept local daemon connection") {
		t.Fatalf("Run() error = %v, want no manufactured endpoint failure", err)
	}
	if listener.closeCalls.Load() != 1 || lock.releaseCount() != 1 {
		t.Fatalf("cleanup calls = listener %d lock %d, want one each", listener.closeCalls.Load(), lock.releaseCount())
	}
}

// TestRunnerRuntimeTerminationClassifiesCleanup preserves distinct failures without repeating terminal causes.
func TestRunnerRuntimeTerminationClassifiesCleanup(t *testing.T) {
	terminalFailure := errors.New("runtime terminal")
	cleanupFailure := errors.New("runtime cleanup")
	tests := []struct {
		name       string
		terminal   error
		closeErr   error
		want       []error
		occurrence string
	}{
		{name: "clean unexpected exit", want: []error{errRuntimeStopped}},
		{name: "cleanup after clean exit", closeErr: cleanupFailure, want: []error{errRuntimeStopped, cleanupFailure}},
		{name: "same retained failure", terminal: terminalFailure, closeErr: terminalFailure, want: []error{terminalFailure}, occurrence: terminalFailure.Error()},
		{name: "cleanup contains retained failure", terminal: terminalFailure, closeErr: fmt.Errorf("cleanup retained: %w", terminalFailure), want: []error{terminalFailure}, occurrence: terminalFailure.Error()},
		{name: "distinct cleanup", terminal: terminalFailure, closeErr: cleanupFailure, want: []error{terminalFailure, cleanupFailure}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestRuntime(&testEventLog{})
			runtime.terminalErr = test.terminal
			runtime.closeErr = test.closeErr
			runner := &Runner{config: RunnerConfig{Runtime: runtime, RuntimeCloseTimeout: defaultRuntimeCloseTimeout}}
			err := runner.runtimeTermination(runtime.Done())
			for _, want := range test.want {
				if !errors.Is(err, want) {
					t.Fatalf("runtimeTermination() error = %v, want %v", err, want)
				}
			}
			if test.occurrence != "" && strings.Count(err.Error(), test.occurrence) != 1 {
				t.Fatalf("runtimeTermination() error = %q, want one %q", err, test.occurrence)
			}
			if runtime.closes.Load() != 1 {
				t.Fatalf("runtime Close() calls = %d, want 1", runtime.closes.Load())
			}
		})
	}
}

// TestRunnerReprobesRuntimeAfterWatcherJoin preserves a child failure that races an endpoint terminal result.
func TestRunnerReprobesRuntimeAfterWatcherJoin(t *testing.T) {
	endpointFailure := errors.New("daemon endpoint failed")
	runtimeFailure := errors.New("runtime failed during endpoint shutdown")
	runtime := newTestRuntime(&testEventLog{})
	runtime.fail(runtimeFailure)
	runner := &Runner{config: RunnerConfig{Runtime: runtime, RuntimeCloseTimeout: defaultRuntimeCloseTimeout}}

	err := runner.finishServe(context.Background(), serveResult{terminalErr: endpointFailure}, runtime.Done())
	if !errors.Is(err, endpointFailure) || !errors.Is(err, runtimeFailure) {
		t.Fatalf("finishServe() error = %v, want endpoint %v and runtime %v", err, endpointFailure, runtimeFailure)
	}
	if runtime.closes.Load() != 1 {
		t.Fatalf("runtime Close() calls = %d, want one", runtime.closes.Load())
	}
}

// TestRunnerClassifiesPreIPCTerminalSignals covers cancellation and runtime exit at each startup boundary.
func TestRunnerClassifiesPreIPCTerminalSignals(t *testing.T) {
	t.Run("cancel after readiness", func(t *testing.T) {
		events := &testEventLog{}
		lock := &testAuthorityLock{events: events}
		runtime := newTestRuntime(events)
		caller, cancel := context.WithCancel(context.Background())
		runner := mustTestRunner(t, RunnerConfig{
			Server: connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error {
				cancel()
				return nil
			},
			Runtime: runtime,
		}, lock, newTestListener(events, 0))
		if err := runner.Run(caller); err != nil {
			t.Fatalf("Run() error = %v, want intentional cancellation", err)
		}
		if runtime.starts.Load() != 0 || events.index("listener.open") >= 0 {
			t.Fatalf("startup continued after readiness cancellation: events %v", events.snapshot())
		}
	})

	t.Run("successful start observes cancellation", func(t *testing.T) {
		events := &testEventLog{}
		lock := &testAuthorityLock{events: events}
		runtime := newTestRuntime(events)
		caller, cancel := context.WithCancel(context.Background())
		runtime.startFunc = func(context.Context) error {
			cancel()
			return nil
		}
		runner := mustTestRunner(t, RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
			Runtime:   runtime,
		}, lock, newTestListener(events, 0))
		if err := runner.Run(caller); err != nil {
			t.Fatalf("Run() error = %v, want intentional cancellation", err)
		}
		if runtime.closes.Load() != 1 || events.index("listener.open") >= 0 {
			t.Fatalf("post-start cancellation cleanup = closes %d events %v", runtime.closes.Load(), events.snapshot())
		}
	})

	t.Run("cancel while completion signal is acquired", func(t *testing.T) {
		events := &testEventLog{}
		lock := &testAuthorityLock{events: events}
		runtime := newTestRuntime(events)
		caller, cancel := context.WithCancel(context.Background())
		runtime.doneFunc = func() <-chan struct{} {
			cancel()
			return runtime.done
		}
		runner := mustTestRunner(t, RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
			Runtime:   runtime,
		}, lock, newTestListener(events, 0))
		if err := runner.Run(caller); err != nil {
			t.Fatalf("Run() error = %v, want intentional cancellation", err)
		}
		if runtime.closes.Load() != 1 || events.index("listener.open") >= 0 {
			t.Fatalf("completion-boundary cancellation cleanup = closes %d events %v", runtime.closes.Load(), events.snapshot())
		}
	})

	t.Run("runtime exits before listener", func(t *testing.T) {
		runtimeFailure := errors.New("runtime exited before IPC")
		events := &testEventLog{}
		lock := &testAuthorityLock{events: events}
		runtime := newTestRuntime(events)
		runtime.startFunc = func(context.Context) error {
			runtime.fail(runtimeFailure)
			return nil
		}
		runner := mustTestRunner(t, RunnerConfig{
			Server:    connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness: func(context.Context) error { return nil },
			Runtime:   runtime,
		}, lock, newTestListener(events, 0))
		err := runner.Run(context.Background())
		if !errors.Is(err, runtimeFailure) {
			t.Fatalf("Run() error = %v, want %v", err, runtimeFailure)
		}
		if runtime.closes.Load() != 1 || events.index("listener.open") >= 0 {
			t.Fatalf("pre-IPC runtime exit cleanup = closes %d events %v", runtime.closes.Load(), events.snapshot())
		}
	})
}

// TestRunnerLifecycleErrorHelpers cover duplicate-aware joining and typed-nil reflection boundaries.
func TestRunnerLifecycleErrorHelpers(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	wrappedFirst := fmt.Errorf("wrapped first: %w", first)
	wrappedSecond := fmt.Errorf("wrapped second: %w", second)
	tests := []struct {
		name  string
		left  error
		right error
		want  error
	}{
		{name: "left nil", right: first, want: first},
		{name: "right nil", left: first, want: first},
		{name: "left contains right", left: wrappedFirst, right: first, want: wrappedFirst},
		{name: "right contains left", left: second, right: wrappedSecond, want: wrappedSecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if result := joinDistinct(test.left, test.right); result != test.want {
				t.Fatalf("joinDistinct() = %v, want exact %v", result, test.want)
			}
		})
	}
	joined := joinDistinct(first, second)
	if !errors.Is(joined, first) || !errors.Is(joined, second) {
		t.Fatalf("joinDistinct() = %v, want both distinct errors", joined)
	}
	if requiredInterfaceIsNil(struct{}{}) {
		t.Fatal("requiredInterfaceIsNil() rejected a non-nil value type")
	}
}

// TestClassifyRuntimeStartResult suppresses cancellation-only trees without hiding rollback failures.
func TestClassifyRuntimeStartResult(t *testing.T) {
	startFailure := errors.New("start failed")
	cleanupFailure := errors.New("rollback failed")
	nestedCancellation := fmt.Errorf(
		"runtime start: %w",
		errors.Join(
			fmt.Errorf("listener: %w", context.Canceled),
			fmt.Errorf("relay: %w", context.DeadlineExceeded),
		),
	)
	joinedCleanup := errors.Join(context.Canceled, fmt.Errorf("cleanup runtime: %w", cleanupFailure))

	tests := []struct {
		name        string
		callerErr   error
		startErr    error
		wantStarted bool
		wantErr     error
	}{
		{name: "successful start", wantStarted: true},
		{name: "failure without cancellation", startErr: startFailure, wantErr: startFailure},
		{name: "cancellation after successful start", callerErr: context.Canceled, wantStarted: true},
		{name: "plain cancellation", callerErr: context.Canceled, startErr: context.Canceled},
		{name: "plain deadline", callerErr: context.DeadlineExceeded, startErr: context.DeadlineExceeded},
		{name: "nested wrapped cancellation", callerErr: context.Canceled, startErr: nestedCancellation},
		{name: "joined cleanup failure", callerErr: context.Canceled, startErr: joinedCleanup, wantErr: cleanupFailure},
		{name: "failure racing cancellation", callerErr: context.Canceled, startErr: startFailure, wantErr: startFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started, err := classifyRuntimeStartResult(test.callerErr, test.startErr)
			if started != test.wantStarted {
				t.Fatalf("classifyRuntimeStartResult() started = %t, want %t", started, test.wantStarted)
			}
			if test.wantErr == nil && err != nil {
				t.Fatalf("classifyRuntimeStartResult() error = %v, want nil", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("classifyRuntimeStartResult() error = %v, want %v", err, test.wantErr)
			}
		})
	}
	if isCancellationOnly(nil) {
		t.Fatal("isCancellationOnly(nil) = true")
	}
}

// TestRunnerCallerCancellationWinsSimultaneousRuntimeExit returns only failures from intentional cleanup.
func TestRunnerCallerCancellationWinsSimultaneousRuntimeExit(t *testing.T) {
	runtimeFailure := errors.New("runtime exited")
	cleanupFailure := errors.New("runtime cleanup failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events}
	runtime := newTestRuntime(events)
	runtime.closeErr = cleanupFailure
	baseListener := newTestListener(events, 1)
	baseListener.results <- acceptResult{connection: newTestConnection(events, 302)}
	closeEntered := make(chan struct{})
	releaseClose := make(chan struct{})
	listener := &gatedCloseListener{
		testListener: baseListener,
		closeEntered: closeEntered,
		releaseClose: releaseClose,
	}
	serverStarted := make(chan struct{})
	server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
		close(serverStarted)
		<-ctx.Done()
		events.add("server.exit")
		return ctx.Err()
	})
	runner := mustTestRunner(t, RunnerConfig{
		Server:    server,
		Readiness: func(context.Context) error { return nil },
		Runtime:   runtime,
	}, lock, listener)
	caller, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(caller) }()
	waitSignal(t, serverStarted, "connection before competing terminal signals")
	runtime.fail(runtimeFailure)
	waitSignal(t, closeEntered, "IPC admission freeze after runtime exit")
	cancel()
	close(releaseClose)
	err := waitResult(t, result, "caller cancellation racing runtime exit")
	if !errors.Is(err, cleanupFailure) || errors.Is(err, runtimeFailure) {
		t.Fatalf("Run() error = %v, want only cleanup failure %v", err, cleanupFailure)
	}
	if listener.closeCalls.Load() != 1 || runtime.closes.Load() != 1 || lock.releaseCount() != 1 {
		t.Fatalf("cleanup calls = listener %d runtime %d lock %d, want one each", listener.closeCalls.Load(), runtime.closes.Load(), lock.releaseCount())
	}
	assertEventOrder(t, events, "listener.close", "server.exit")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close", "lock.release")
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
				Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
				Readiness:         func(context.Context) error { return nil },
				Runtime:           newTestRuntime(&testEventLog{}),
				ShutdownRequested: make(chan struct{}),
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
		Server:            server,
		Readiness:         func(context.Context) error { return nil },
		Runtime:           newTestRuntime(events),
		ShutdownRequested: make(chan struct{}),
		ObserveError:      func(err error) { observations <- err },
		MaxConnections:    1,
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
			Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness:         func(context.Context) error { return nil },
			Runtime:           newTestRuntime(events),
			ShutdownRequested: make(chan struct{}),
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
			Server:            connectionServerFunc(func(context.Context, local.Conn) error { return nil }),
			Readiness:         func(context.Context) error { return nil },
			Runtime:           newTestRuntime(events),
			ShutdownRequested: make(chan struct{}),
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

// TestRunnerAdmissionCancellationSuppressesListenerCloseError distinguishes intentional endpoint teardown from endpoint failure.
func TestRunnerAdmissionCancellationSuppressesListenerCloseError(t *testing.T) {
	events := &testEventLog{}
	listener := newTestListener(events, 0)
	listener.entered = make(chan struct{})
	admissionContext, cancelAdmission := context.WithCancel(context.Background())
	sessionContext, cancelSessions := context.WithCancel(context.Background())
	defer cancelSessions()
	runner := &Runner{
		retryAccept: retryableAcceptError,
		acceptDelay: func(context.Context, int) error { return nil },
	}
	registry := newConnectionRegistry()
	slots := make(chan struct{}, 1)
	connectionErrors := &errorCollector{}
	var workers sync.WaitGroup
	result := make(chan error, 1)
	go func() {
		result <- runner.accept(
			admissionContext,
			sessionContext,
			listener,
			registry,
			slots,
			&workers,
			connectionErrors,
		)
	}()
	waitSignal(t, listener.entered, "blocked endpoint admission")
	cancelAdmission()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener Close() error = %v", err)
	}
	if err := waitResult(t, result, "listener close after admission cancellation"); err != nil {
		t.Fatalf("accept() error = %v, want intentional shutdown", err)
	}
	if listener.accepts.Load() != 1 {
		t.Fatalf("listener accepts = %d, want one", listener.accepts.Load())
	}
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
	runtime := newTestRuntime(&testEventLog{})
	shutdownRequested := make(chan struct{})
	var typedNilServer connectionServerFunc
	var typedNilRuntime *testRuntime
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
		{name: "server", config: RunnerConfig{Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: validDependencies},
		{name: "typed nil server", config: RunnerConfig{Server: typedNilServer, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: validDependencies},
		{name: "readiness", config: RunnerConfig{Server: server, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: validDependencies},
		{name: "runtime", config: RunnerConfig{Server: server, Readiness: readiness, ShutdownRequested: shutdownRequested}, dependencies: validDependencies},
		{name: "typed nil runtime", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: typedNilRuntime, ShutdownRequested: shutdownRequested}, dependencies: validDependencies},
		{name: "shutdown signal", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime}, dependencies: validDependencies},
		{name: "negative runtime close timeout", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested, RuntimeCloseTimeout: -time.Second}, dependencies: validDependencies},
		{name: "negative bound", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested, MaxConnections: -1}, dependencies: validDependencies},
		{name: "excessive bound", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested, MaxConnections: maximumConnections + 1}, dependencies: validDependencies},
		{name: "lock factory", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: runnerDependencies{listen: validDependencies.listen, retryAccept: validDependencies.retryAccept, acceptDelay: validDependencies.acceptDelay}},
		{name: "listener factory", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, retryAccept: validDependencies.retryAccept, acceptDelay: validDependencies.acceptDelay}},
		{name: "retry policy", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, listen: validDependencies.listen, acceptDelay: validDependencies.acceptDelay}},
		{name: "retry delay", config: RunnerConfig{Server: server, Readiness: readiness, Runtime: runtime, ShutdownRequested: shutdownRequested}, dependencies: runnerDependencies{acquireLock: validDependencies.acquireLock, listen: validDependencies.listen, retryAccept: validDependencies.retryAccept}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if runner, err := newRunner(test.config, test.dependencies); err == nil || runner != nil {
				t.Fatalf("newRunner() = (%v, %v), want nil runner and wiring error", runner, err)
			}
		})
	}

	runner, err := newRunner(RunnerConfig{
		Server:            server,
		Readiness:         readiness,
		Runtime:           runtime,
		ShutdownRequested: shutdownRequested,
	}, validDependencies)
	if err != nil {
		t.Fatalf("newRunner() default bound error = %v", err)
	}
	if runner.config.MaxConnections != defaultMaxConnections {
		t.Fatalf("default maximum connections = %d, want %d", runner.config.MaxConnections, defaultMaxConnections)
	}
	if runner.config.RuntimeCloseTimeout != defaultRuntimeCloseTimeout {
		t.Fatalf("default runtime close timeout = %s, want %s", runner.config.RuntimeCloseTimeout, defaultRuntimeCloseTimeout)
	}
	productionRunner, err := NewRunner(RunnerConfig{
		Server:            server,
		Readiness:         readiness,
		Runtime:           runtime,
		ShutdownRequested: shutdownRequested,
	})
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
	observed := make(chan struct{})
	var observedOnce sync.Once
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
			observedOnce.Do(func() { close(observed) })
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
	waitSignal(t, observed, "contained observer panic")
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
	runtimeFailure := errors.New("runtime close failed")
	lockFailure := errors.New("lock release failed")
	events := &testEventLog{}
	lock := &testAuthorityLock{events: events, releaseErr: lockFailure}
	runtime := newTestRuntime(events)
	runtime.closeErr = runtimeFailure
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
		Runtime:   runtime,
	}, lock, listener)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx)
	}()

	waitSignal(t, started, "connection before cleanup failure")
	cancel()
	err := waitResult(t, result, "daemon cleanup failure")
	for _, want := range []error{listenerFailure, connectionFailure, runtimeFailure, lockFailure} {
		if !errors.Is(err, want) {
			t.Fatalf("Run() error = %v, want joined %v", err, want)
		}
	}
	assertEventOrder(t, events, "listener.close", "lock.release")
	assertEventOrder(t, events, "connection.close", "lock.release")
	assertEventOrder(t, events, "server.exit", "runtime.close")
	assertEventOrder(t, events, "runtime.close", "lock.release")
}

// TestRunnerRetainsAuthorityUntilIncompleteRuntimeCleanupFinishes proves bounded return never permits overlapping ownership.
func TestRunnerRetainsAuthorityUntilIncompleteRuntimeCleanupFinishes(t *testing.T) {
	for _, test := range []struct {
		name            string
		blockClose      bool
		wantDeadline    bool
		lockReleaseErr  error
		wantObservation bool
	}{
		{name: "Close returns before Done"},
		{name: "Close exceeds outer timeout", blockClose: true, wantDeadline: true},
		{
			name:            "asynchronous release failure is observed",
			lockReleaseErr:  errors.New("deferred authority release failed"),
			wantObservation: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := &testEventLog{}
			ownershipDone := make(chan struct{})
			closeReturned := make(chan struct{})
			closeRelease := make(chan struct{})
			releaseSignal := make(chan struct{})
			observed := make(chan error, 1)
			lock := &testAuthorityLock{
				events:        events,
				releaseErr:    test.lockReleaseErr,
				releaseSignal: releaseSignal,
			}
			runtime := newTestRuntime(events)
			runtime.doneFunc = func() <-chan struct{} { return ownershipDone }
			runtime.closeFunc = func(context.Context) error {
				if test.blockClose {
					<-closeRelease
				}
				close(closeReturned)
				return nil
			}
			listener := newTestListener(events, 1)
			listener.results <- acceptResult{connection: newTestConnection(events, 401)}
			serverStarted := make(chan struct{})
			server := connectionServerFunc(func(ctx context.Context, _ local.Conn) error {
				close(serverStarted)
				<-ctx.Done()
				return ctx.Err()
			})
			runner := mustTestRunner(t, RunnerConfig{
				Server:              server,
				Readiness:           func(context.Context) error { return nil },
				Runtime:             runtime,
				RuntimeCloseTimeout: 20 * time.Millisecond,
				ObserveError: func(err error) {
					observed <- err
				},
			}, lock, listener)
			caller, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- runner.Run(caller) }()
			waitSignal(t, serverStarted, "connection before incomplete runtime cleanup")

			startedCleanup := time.Now()
			cancel()
			err := waitResult(t, result, "bounded incomplete runtime cleanup")
			if !errors.Is(err, ErrRuntimeCleanupIncomplete) {
				t.Fatalf("Run() error = %v, want %v", err, ErrRuntimeCleanupIncomplete)
			}
			if test.wantObservation && errors.Is(err, test.lockReleaseErr) {
				t.Fatalf("Run() error = %v, want asynchronous release failure only through observer", err)
			}
			if test.wantDeadline && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run() error = %v, want outer deadline", err)
			}
			if elapsed := time.Since(startedCleanup); elapsed >= time.Second {
				t.Fatalf("Run() returned after %s, want bounded cleanup", elapsed)
			}
			if lock.releaseCount() != 0 {
				t.Fatalf("lock releases before runtime Done = %d, want zero", lock.releaseCount())
			}
			if runtime.closes.Load() != 1 {
				t.Fatalf("runtime Close() calls = %d, want one", runtime.closes.Load())
			}

			close(ownershipDone)
			waitSignal(t, releaseSignal, "authority release after runtime Done")
			if lock.releaseCount() != 1 {
				t.Fatalf("lock releases after runtime Done = %d, want one", lock.releaseCount())
			}
			if test.wantObservation {
				observation := waitResult(t, observed, "asynchronous authority release failure")
				if !errors.Is(observation, test.lockReleaseErr) {
					t.Fatalf("observed error = %v, want %v", observation, test.lockReleaseErr)
				}
			}
			if test.blockClose {
				close(closeRelease)
			}
			waitSignal(t, closeReturned, "runtime Close return")
			assertEventOrder(t, events, "runtime.close", "lock.release")
		})
	}
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
	events := &testEventLog{}
	if testLock, ok := lock.(*testAuthorityLock); ok {
		events = testLock.events
	}
	if config.Runtime == nil {
		config.Runtime = newTestRuntime(events)
	}
	if config.ShutdownRequested == nil {
		config.ShutdownRequested = make(chan struct{})
	}

	runner, err := newRunner(config, runnerDependencies{
		acquireLock: func() (authorityLock, error) { return lock, nil },
		listen: func() (local.Listener, error) {
			events.add("listener.open")
			return listener, nil
		},
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
