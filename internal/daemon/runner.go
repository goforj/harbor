package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/rpc/local"
)

const (
	defaultMaxConnections = 64
	maximumConnections    = 1024
	initialAcceptBackoff  = 10 * time.Millisecond
	maximumAcceptBackoff  = time.Second
)

// ConnectionServer serves one authenticated local connection until the peer or daemon closes it.
type ConnectionServer interface {
	Serve(ctx context.Context, connection local.Conn) error
}

// ReadinessCheck verifies that durable daemon state is safe to open without mutating it.
type ReadinessCheck func(ctx context.Context) error

// ErrorObserver receives connection-local failures that do not stop daemon authority.
//
// Observers must return promptly because accept retry and connection cleanup call them inline.
type ErrorObserver func(err error)

// RunnerConfig describes the application services owned by a foreground Harbor daemon.
type RunnerConfig struct {
	// Server handles connections after the local transport authenticates their operating-system identity.
	Server ConnectionServer
	// Readiness verifies that the daemon's durable schema is already ready to use.
	Readiness ReadinessCheck
	// MaxConnections bounds accepted connections, including peers still negotiating a session.
	// A zero value uses Harbor's conservative per-user default; values above Harbor's hard safety
	// limit are rejected before allocating connection accounting.
	MaxConnections int
	// ObserveError optionally records rejected peers, retryable accepts, and session failures.
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
	if config.Server == nil {
		return nil, errors.New("create daemon runner: connection server is required")
	}
	if config.Readiness == nil {
		return nil, errors.New("create daemon runner: readiness check is required")
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

	return &Runner{
		config:      config,
		acquireLock: dependencies.acquireLock,
		listen:      dependencies.listen,
		retryAccept: dependencies.retryAccept,
		acceptDelay: dependencies.acceptDelay,
	}, nil
}

// Run holds daemon authority until cancellation or an endpoint-level failure completes joined shutdown.
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
	defer func() {
		if err := lock.Release(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("release daemon authority: %w", err))
		}
	}()

	if err := runner.config.Readiness(ctx); err != nil {
		return fmt.Errorf("verify daemon state readiness: %w", err)
	}

	listener, err := runner.listen()
	if err != nil {
		return fmt.Errorf("open authenticated daemon endpoint: %w", err)
	}
	if listener == nil {
		return errors.New("open authenticated daemon endpoint: listener factory returned no listener")
	}

	return runner.serve(ctx, listener)
}

// serve accepts bounded connections and centralizes endpoint teardown before Run releases authority.
func (runner *Runner) serve(ctx context.Context, listener local.Listener) error {
	serveContext, cancelServe := context.WithCancel(ctx)
	registry := newConnectionRegistry()
	slots := make(chan struct{}, runner.config.MaxConnections)
	connectionErrors := &errorCollector{}
	var workers sync.WaitGroup
	var shutdownOnce sync.Once
	var shutdownErr error

	shutdown := func() {
		shutdownOnce.Do(func() {
			connections := registry.beginShutdown()
			cancelServe()
			if err := listener.Close(); err != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("close daemon listener: %w", err))
			}
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
		case <-stopWatcher:
		}
	}()

	acceptErr := runner.accept(serveContext, listener, registry, slots, &workers, connectionErrors)
	shutdown()
	close(stopWatcher)
	<-watcherDone
	workers.Wait()
	cancelServe()

	return errors.Join(acceptErr, shutdownErr, connectionErrors.result())
}

// accept retries connection-local admission failures but returns endpoint failures to the daemon owner.
func (runner *Runner) accept(
	ctx context.Context,
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
		case <-ctx.Done():
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
			if ctx.Err() != nil {
				return nil
			}
			if !runner.retryAccept(err) {
				return fmt.Errorf("accept local daemon connection: %w", err)
			}

			consecutiveFailures++
			runner.observe(fmt.Errorf("retry local daemon accept: %w", err))
			if err := runner.acceptDelay(ctx, consecutiveFailures); err != nil {
				if ctx.Err() != nil {
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
		go runner.serveConnection(ctx, managed, registry, slots, workers, connectionErrors)
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
