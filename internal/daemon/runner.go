package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/rpc/local"
)

const (
	defaultMaxConnections      = 64
	maximumConnections         = 1024
	initialAcceptBackoff       = 10 * time.Millisecond
	maximumAcceptBackoff       = time.Second
	defaultRuntimeCloseTimeout = time.Minute
)

var (
	// ErrRuntimeCleanupIncomplete reports that Runtime.Close returned or reached its bound before complete resource release.
	ErrRuntimeCleanupIncomplete = errors.New("daemon runtime cleanup is incomplete")
	errRuntimeStopped           = errors.New("daemon runtime stopped unexpectedly")
)

// retainedAuthorities keeps fail-closed process locks reachable when a broken runtime provides no terminal signal.
var retainedAuthorities struct {
	sync.Mutex
	locks []authorityLock
}

// ConnectionServer serves one authenticated local connection until the peer or daemon closes it.
type ConnectionServer interface {
	Serve(ctx context.Context, connection local.Conn) error
}

// Runtime owns the daemon's long-lived DNS, ingress, and native relay generation.
type Runtime interface {
	// Start acquires the complete runtime generation and returns after it is ready.
	Start(ctx context.Context) error
	// Done closes after the runtime relinquishes every owned resource.
	Done() <-chan struct{}
	// Err returns the retained terminal runtime failure when one exists.
	Err() error
	// Close requests idempotent shutdown and waits for complete resource release.
	Close(ctx context.Context) error
}

// ReadinessCheck verifies that durable daemon state is safe to open without mutating it.
type ReadinessCheck func(ctx context.Context) error

// StartupRecovery reconciles durable work while singleton daemon authority is held and before runtime publication.
type StartupRecovery func(ctx context.Context) error

// ErrorObserver receives operational failures that cannot be returned synchronously by Run.
//
// Observers may run concurrently or after Run returns and must return promptly.
type ErrorObserver func(err error)

// RunnerConfig describes the application services owned by a foreground Harbor daemon.
type RunnerConfig struct {
	// Server handles connections after the local transport authenticates their operating-system identity.
	Server ConnectionServer
	// Readiness verifies that the daemon's durable schema is already ready to use.
	Readiness ReadinessCheck
	// Recovery optionally reconciles interrupted durable operations before the network runtime reads state.
	Recovery StartupRecovery
	// Runtime owns network infrastructure for the complete authenticated endpoint lifetime.
	Runtime Runtime
	// ShutdownRequested lets an authenticated control request initiate the same joined shutdown as process cancellation.
	ShutdownRequested <-chan struct{}
	// RuntimeCloseTimeout is the outer cleanup budget after IPC fully joins.
	// Zero uses a default that exceeds Harbor's nested controller and data-plane cleanup budgets.
	RuntimeCloseTimeout time.Duration
	// MaxConnections bounds accepted connections, including peers still negotiating a session.
	// A zero value uses Harbor's conservative per-user default; values above Harbor's hard safety
	// limit are rejected before allocating connection accounting.
	MaxConnections int
	// ObserveError optionally records rejected peers, session failures, and asynchronous authority-release failures.
	ObserveError ErrorObserver
}

// Runner owns singleton daemon authority, its authenticated endpoint, and all accepted connections.
type Runner struct {
	config      RunnerConfig
	acquireLock processLockFactory
	listen      listenerFactory
	retryAccept acceptRetryPolicy
	acceptDelay acceptDelay
}

// authorityLock is the smallest process-lock surface needed by the lifecycle runner.
type authorityLock interface {
	Release() error
}

// processLockFactory acquires exclusive authority for the current user's daemon.
type processLockFactory func() (authorityLock, error)

// listenerFactory opens the authenticated endpoint after daemon authority is established.
type listenerFactory func() (local.Listener, error)

// acceptRetryPolicy distinguishes a rejected or transient peer from a failed endpoint.
type acceptRetryPolicy func(err error) bool

// acceptDelay prevents repeated transient endpoint failures from spinning the daemon.
type acceptDelay func(ctx context.Context, consecutiveFailures int) error

// runnerDependencies keeps operating-system boundaries replaceable in deterministic lifecycle tests.
type runnerDependencies struct {
	acquireLock processLockFactory
	listen      listenerFactory
	retryAccept acceptRetryPolicy
	acceptDelay acceptDelay
}

// managedConnection makes runner and session teardown share one idempotent connection close.
type managedConnection struct {
	local.Conn
	closeOnce sync.Once
	closeErr  error
}

// Close releases the connection once even when both session and daemon shutdown own teardown paths.
func (connection *managedConnection) Close() error {
	connection.closeOnce.Do(func() {
		connection.closeErr = connection.Conn.Close()
	})

	return connection.closeErr
}

// connectionRegistry prevents a connection accepted concurrently with shutdown from escaping teardown.
type connectionRegistry struct {
	mutex       sync.Mutex
	connections map[*managedConnection]struct{}
	closing     bool
}

// errorCollector joins cleanup failures produced by connection goroutines during shutdown.
type errorCollector struct {
	mutex sync.Mutex
	err   error
}

// serveResult keeps endpoint termination distinct from cleanup that intentional cancellation must retain.
type serveResult struct {
	terminalErr    error
	cleanupErr     error
	runtimeStopped bool
}

// add preserves one cleanup failure for the foreground runner's eventual result.
func (collector *errorCollector) add(err error) {
	if err == nil {
		return
	}
	collector.mutex.Lock()
	collector.err = errors.Join(collector.err, err)
	collector.mutex.Unlock()
}

// result returns the joined cleanup result after all connection goroutines have exited.
func (collector *errorCollector) result() error {
	collector.mutex.Lock()
	defer collector.mutex.Unlock()

	return collector.err
}

// newConnectionRegistry creates the active-set used for bounded, joined shutdown.
func newConnectionRegistry() *connectionRegistry {
	return &connectionRegistry{connections: make(map[*managedConnection]struct{})}
}

// add registers a connection only while the endpoint still owns daemon authority.
func (registry *connectionRegistry) add(connection *managedConnection) bool {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	if registry.closing {
		return false
	}
	registry.connections[connection] = struct{}{}
	return true
}

// remove forgets a connection after its serving goroutine has completed teardown.
func (registry *connectionRegistry) remove(connection *managedConnection) {
	registry.mutex.Lock()
	delete(registry.connections, connection)
	registry.mutex.Unlock()
}

// beginShutdown freezes admission and returns the connections that must be interrupted.
func (registry *connectionRegistry) beginShutdown() []*managedConnection {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	registry.closing = true
	connections := make([]*managedConnection, 0, len(registry.connections))
	for connection := range registry.connections {
		connections = append(connections, connection)
	}
	return connections
}

// NewRunner creates a foreground daemon runner backed by Harbor's production process lock and local transport.
func NewRunner(config RunnerConfig) (*Runner, error) {
	return newRunner(config, runnerDependencies{
		acquireLock: func() (authorityLock, error) {
			return AcquireProcessLock()
		},
		listen:      local.Listen,
		retryAccept: retryableAcceptError,
		acceptDelay: waitForAcceptRetry,
	})
}

// newRunner validates all required wiring before the daemon acquires process authority.
func newRunner(config RunnerConfig, dependencies runnerDependencies) (*Runner, error) {
	if requiredInterfaceIsNil(config.Server) {
		return nil, errors.New("create daemon runner: connection server is required")
	}
	if config.Readiness == nil {
		return nil, errors.New("create daemon runner: readiness check is required")
	}
	if requiredInterfaceIsNil(config.Runtime) {
		return nil, errors.New("create daemon runner: runtime is required")
	}
	if config.ShutdownRequested == nil {
		return nil, errors.New("create daemon runner: shutdown request signal is required")
	}
	if config.RuntimeCloseTimeout < 0 {
		return nil, errors.New("create daemon runner: runtime close timeout cannot be negative")
	}
	if config.MaxConnections < 0 {
		return nil, errors.New("create daemon runner: maximum connections cannot be negative")
	}
	if config.MaxConnections > maximumConnections {
		return nil, fmt.Errorf(
			"create daemon runner: maximum connections %d exceeds limit %d",
			config.MaxConnections,
			maximumConnections,
		)
	}
	if dependencies.acquireLock == nil {
		return nil, errors.New("create daemon runner: process lock factory is required")
	}
	if dependencies.listen == nil {
		return nil, errors.New("create daemon runner: listener factory is required")
	}
	if dependencies.retryAccept == nil {
		return nil, errors.New("create daemon runner: accept retry policy is required")
	}
	if dependencies.acceptDelay == nil {
		return nil, errors.New("create daemon runner: accept retry delay is required")
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = defaultMaxConnections
	}
	if config.RuntimeCloseTimeout == 0 {
		config.RuntimeCloseTimeout = defaultRuntimeCloseTimeout
	}

	return &Runner{
		config:      config,
		acquireLock: dependencies.acquireLock,
		listen:      dependencies.listen,
		retryAccept: dependencies.retryAccept,
		acceptDelay: dependencies.acceptDelay,
	}, nil
}

// Run holds daemon authority through joined shutdown and transfers it to a terminal waiter when cleanup remains incomplete.
func (runner *Runner) Run(ctx context.Context) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}

	lock, err := runner.acquireLock()
	if err != nil {
		return fmt.Errorf("acquire daemon authority: %w", err)
	}
	if lock == nil {
		return errors.New("acquire daemon authority: process lock factory returned no lock")
	}
	var runtimeDone <-chan struct{}
	defer func() {
		if err := runner.releaseAuthority(lock, runtimeDone, runErr); err != nil {
			runErr = joinDistinct(runErr, fmt.Errorf("release daemon authority: %w", err))
		}
	}()

	if err := runner.config.Readiness(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("verify daemon state readiness: %w", err)
	}
	if ctx.Err() != nil {
		return nil
	}
	if runner.config.Recovery != nil {
		if err := runner.config.Recovery(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recover daemon state: %w", err)
		}
		if ctx.Err() != nil {
			return nil
		}
	}

	runtimeContext, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	started, err := runner.startRuntime(ctx, runtimeContext, cancelRuntime)
	if err != nil {
		return fmt.Errorf("start daemon runtime: %w", err)
	}
	if !started {
		return nil
	}
	runtimeDone = runner.config.Runtime.Done()
	if ctx.Err() != nil {
		return runner.closeRuntime(runtimeDone)
	}
	if runtimeDone == nil {
		return joinDistinct(
			errors.New("start daemon runtime: runtime returned no completion signal"),
			runner.closeRuntime(runtimeDone),
		)
	}
	select {
	case <-ctx.Done():
		return runner.closeRuntime(runtimeDone)
	case <-runtimeDone:
		if ctx.Err() != nil {
			return runner.closeRuntime(runtimeDone)
		}
		return runner.runtimeTermination(runtimeDone)
	default:
	}

	listener, err := runner.listen()
	if err != nil {
		var terminal error
		if ctx.Err() == nil {
			terminal = fmt.Errorf("open authenticated daemon endpoint: %w", err)
		}
		return joinDistinct(terminal, runner.closeRuntime(runtimeDone))
	}
	if listener == nil {
		var terminal error
		if ctx.Err() == nil {
			terminal = errors.New("open authenticated daemon endpoint: listener factory returned no listener")
		}
		return joinDistinct(terminal, runner.closeRuntime(runtimeDone))
	}

	served := runner.serve(ctx, listener, runtimeDone)
	return runner.finishServe(ctx, served, runtimeDone)
}

// finishServe rechecks child termination after the watcher joins before classifying endpoint cleanup.
func (runner *Runner) finishServe(ctx context.Context, served serveResult, runtimeDone <-chan struct{}) error {
	if !served.runtimeStopped && signalClosed(runtimeDone) {
		served.runtimeStopped = true
	}
	result := served.cleanupErr
	if ctx.Err() == nil {
		result = joinDistinct(result, served.terminalErr)
		if served.runtimeStopped {
			result = joinDistinct(result, runner.runtimeTermination(runtimeDone))
		} else {
			result = joinDistinct(result, runner.closeRuntime(runtimeDone))
		}
		return result
	}
	return joinDistinct(result, runner.closeRuntime(runtimeDone))
}

// startRuntime lets caller cancellation interrupt startup without owning the runtime's post-start lifetime.
func (runner *Runner) startRuntime(
	caller context.Context,
	runtimeContext context.Context,
	cancelRuntime context.CancelFunc,
) (bool, error) {
	result := make(chan error, 1)
	go func() {
		result <- runner.config.Runtime.Start(runtimeContext)
	}()

	select {
	case err := <-result:
		return classifyRuntimeStartResult(caller.Err(), err)
	case <-caller.Done():
		cancelRuntime()
		return classifyRuntimeStartResult(caller.Err(), <-result)
	}
}

// classifyRuntimeStartResult suppresses only cancellation-exclusive startup results after caller cancellation.
func classifyRuntimeStartResult(callerErr error, startErr error) (bool, error) {
	if callerErr == nil {
		return startErr == nil, startErr
	}
	if startErr == nil {
		return true, nil
	}
	if isCancellationOnly(startErr) {
		return false, nil
	}
	return false, startErr
}

// isCancellationOnly reports whether every leaf in one error tree is cancellation or deadline expiration.
func isCancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		found := false
		for _, cause := range joined.Unwrap() {
			if cause == nil {
				continue
			}
			found = true
			if !isCancellationOnly(cause) {
				return false
			}
		}
		return found
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if cause := wrapped.Unwrap(); cause != nil {
			return isCancellationOnly(cause)
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// closeRuntime applies an outer cleanup bound after IPC no longer admits or serves clients.
func (runner *Runner) closeRuntime(runtimeDone <-chan struct{}) error {
	if err := runner.closeRuntimeCause(runtimeDone); err != nil {
		return fmt.Errorf("close daemon runtime: %w", err)
	}
	return nil
}

// closeRuntimeCause bounds a possibly broken Close and preserves its native result when available.
func (runner *Runner) closeRuntimeCause(runtimeDone <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), runner.config.RuntimeCloseTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- runner.config.Runtime.Close(ctx)
	}()

	var closeErr error
	timedOut := false
	select {
	case closeErr = <-result:
	case <-ctx.Done():
		closeErr = ctx.Err()
		timedOut = true
	}
	if runtimeDone == nil {
		if timedOut || closeErr != nil {
			return joinDistinct(closeErr, ErrRuntimeCleanupIncomplete)
		}
		return closeErr
	}
	if !signalClosed(runtimeDone) {
		return joinDistinct(closeErr, ErrRuntimeCleanupIncomplete)
	}
	return closeErr
}

// runtimeTermination reports unexpected infrastructure loss and closes an already-terminal runtime idempotently.
func (runner *Runner) runtimeTermination(runtimeDone <-chan struct{}) error {
	terminal := runner.config.Runtime.Err()
	closeErr := runner.closeRuntimeCause(runtimeDone)
	if terminal == nil {
		return joinDistinct(errRuntimeStopped, wrapRuntimeClose(closeErr))
	}
	if closeErr == nil || errors.Is(terminal, closeErr) {
		return fmt.Errorf("daemon runtime stopped unexpectedly: %w", terminal)
	}
	if errors.Is(closeErr, terminal) {
		return fmt.Errorf("daemon runtime stopped unexpectedly: %w", closeErr)
	}
	return errors.Join(
		fmt.Errorf("daemon runtime stopped unexpectedly: %w", terminal),
		wrapRuntimeClose(closeErr),
	)
}

// releaseAuthority releases proven terminal ownership or retains it until safety can be established.
func (runner *Runner) releaseAuthority(lock authorityLock, runtimeDone <-chan struct{}, runErr error) error {
	if runtimeDone == nil {
		if errors.Is(runErr, ErrRuntimeCleanupIncomplete) {
			retainAuthority(lock)
			return nil
		}
		return lock.Release()
	}
	if signalClosed(runtimeDone) {
		return lock.Release()
	}
	go func() {
		<-runtimeDone
		if err := lock.Release(); err != nil {
			runner.observe(fmt.Errorf("release daemon authority after runtime cleanup: %w", err))
		}
	}()
	return nil
}

// retainAuthority keeps an unverifiable runtime's process lock alive until process termination.
func retainAuthority(lock authorityLock) {
	retainedAuthorities.Lock()
	retainedAuthorities.locks = append(retainedAuthorities.locks, lock)
	retainedAuthorities.Unlock()
}

// signalClosed reports completion without blocking lifecycle classification.
func signalClosed(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// wrapRuntimeClose labels runtime cleanup without manufacturing an error for a clean close.
func wrapRuntimeClose(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close daemon runtime: %w", err)
}

// joinDistinct preserves the broader error when one result already contains the other.
func joinDistinct(left error, right error) error {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if errors.Is(left, right) {
		return left
	}
	if errors.Is(right, left) {
		return right
	}
	return errors.Join(left, right)
}

// requiredInterfaceIsNil rejects typed-nil required collaborators before daemon authority is acquired.
func requiredInterfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// serve accepts bounded connections and centralizes endpoint teardown before Run releases authority.
func (runner *Runner) serve(ctx context.Context, listener local.Listener, runtimeDone <-chan struct{}) serveResult {
	// Admission stops before listener closure, while sessions retain the lock-last listener-before-session order.
	admissionContext, cancelAdmission := context.WithCancel(context.Background())
	sessionContext, cancelSessions := context.WithCancel(context.Background())
	registry := newConnectionRegistry()
	slots := make(chan struct{}, runner.config.MaxConnections)
	connectionErrors := &errorCollector{}
	var workers sync.WaitGroup
	var shutdownOnce sync.Once
	var shutdownErr error
	runtimeStopped := false

	shutdown := func() {
		shutdownOnce.Do(func() {
			connections := registry.beginShutdown()
			cancelAdmission()
			if err := listener.Close(); err != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close daemon listener: %w", err))
			}
			cancelSessions()
			for _, connection := range connections {
				_ = connection.Close()
			}
		})
	}

	watcherDone := make(chan struct{})
	stopWatcher := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			shutdown()
		case <-runner.config.ShutdownRequested:
			shutdown()
		case <-runtimeDone:
			runtimeStopped = true
			shutdown()
		case <-stopWatcher:
		}
	}()

	acceptErr := runner.accept(admissionContext, sessionContext, listener, registry, slots, &workers, connectionErrors)
	shutdown()
	close(stopWatcher)
	<-watcherDone
	workers.Wait()
	cancelAdmission()
	cancelSessions()

	return serveResult{
		terminalErr:    acceptErr,
		cleanupErr:     errors.Join(shutdownErr, connectionErrors.result()),
		runtimeStopped: runtimeStopped,
	}
}

// accept retries connection-local admission failures but returns endpoint failures to the daemon owner.
func (runner *Runner) accept(
	admissionContext context.Context,
	sessionContext context.Context,
	listener local.Listener,
	registry *connectionRegistry,
	slots chan struct{},
	workers *sync.WaitGroup,
	connectionErrors *errorCollector,
) error {
	consecutiveFailures := 0
	for {
		select {
		case slots <- struct{}{}:
		case <-admissionContext.Done():
			return nil
		}

		connection, err := listener.Accept()
		if err != nil {
			<-slots
			if connection != nil {
				if closeErr := connection.Close(); closeErr != nil {
					err = errors.Join(err, fmt.Errorf("close connection returned by failed accept: %w", closeErr))
				}
			}
			if admissionContext.Err() != nil {
				return nil
			}
			if !runner.retryAccept(err) {
				return fmt.Errorf("accept local daemon connection: %w", err)
			}

			consecutiveFailures++
			runner.observe(fmt.Errorf("retry local daemon accept: %w", err))
			if err := runner.acceptDelay(admissionContext, consecutiveFailures); err != nil {
				if admissionContext.Err() != nil {
					return nil
				}
				return fmt.Errorf("back off local daemon accept: %w", err)
			}
			continue
		}
		if connection == nil {
			<-slots
			return errors.New("accept local daemon connection: listener returned no connection")
		}

		consecutiveFailures = 0
		managed := &managedConnection{Conn: connection}
		if !registry.add(managed) {
			<-slots
			if err := managed.Close(); err != nil {
				connectionErrors.add(fmt.Errorf("close connection accepted during daemon shutdown: %w", err))
			}
			return nil
		}

		workers.Add(1)
		go runner.serveConnection(sessionContext, managed, registry, slots, workers, connectionErrors)
	}
}

// serveConnection contains every peer failure within its connection while preserving joined teardown.
func (runner *Runner) serveConnection(
	ctx context.Context,
	connection *managedConnection,
	registry *connectionRegistry,
	slots chan struct{},
	workers *sync.WaitGroup,
	connectionErrors *errorCollector,
) {
	defer workers.Done()
	defer func() { <-slots }()
	defer registry.remove(connection)

	serveErr := runner.config.Server.Serve(ctx, connection)
	closeErr := connection.Close()
	if ctx.Err() != nil {
		if closeErr != nil {
			connectionErrors.add(fmt.Errorf("close daemon connection during shutdown: %w", closeErr))
		}
		return
	}
	if serveErr != nil {
		serveErr = fmt.Errorf(
			"serve local daemon peer user %q process %d: %w",
			connection.Peer().UserID,
			connection.Peer().ProcessID,
			serveErr,
		)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close completed daemon connection: %w", closeErr)
	}
	runner.observe(errors.Join(serveErr, closeErr))
}

// observe reports an operational failure only when the application configured an observer.
func (runner *Runner) observe(err error) {
	if err == nil || runner.config.ObserveError == nil {
		return
	}
	// Diagnostics are optional; a broken observer cannot be allowed to relinquish daemon authority.
	defer func() {
		_ = recover()
	}()
	runner.config.ObserveError(err)
}

// retryableAcceptError recognizes failures that affect one peer or a temporarily unavailable endpoint.
func retryableAcceptError(err error) bool {
	if errors.Is(err, local.ErrPeerUnauthorized) {
		return true
	}

	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// waitForAcceptRetry applies capped exponential delay so a damaged or attacked endpoint cannot spin.
func waitForAcceptRetry(ctx context.Context, consecutiveFailures int) error {
	delay := initialAcceptBackoff
	for attempt := 1; attempt < consecutiveFailures && delay < maximumAcceptBackoff; attempt++ {
		delay *= 2
		if delay > maximumAcceptBackoff {
			delay = maximumAcceptBackoff
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
