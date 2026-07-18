// Package tcprelay forwards one loopback TCP socket to one private loopback upstream.
package tcprelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaximumConnections = 128
	hardMaximumConnections    = 4096
	defaultConnectTimeout     = 5 * time.Second
	maximumConnectTimeout     = time.Minute
	defaultShutdownTimeout    = 10 * time.Second
	maximumShutdownTimeout    = 5 * time.Minute
	defaultKeepAlive          = 30 * time.Second
	maximumKeepAlive          = 10 * time.Minute
	initialAcceptBackoff      = 10 * time.Millisecond
	maximumAcceptBackoff      = time.Second
	maximumPendingDiagnostics = 64
)

// ErrorObserver receives connection-local failures that do not relinquish the listener.
//
// Delivery is asynchronous and bounded. Observers should return promptly because Harbor drops
// later diagnostics when the single observer worker and its queue are occupied.
type ErrorObserver func(error)

// Config defines the fixed upstream and resource limits for one native TCP relay.
type Config struct {
	// Upstream is the private loopback listener that receives new connections.
	Upstream netip.AddrPort
	// MaxConnections bounds simultaneously admitted client connections.
	// Zero selects Harbor's conservative default.
	MaxConnections int
	// ConnectTimeout bounds each private-upstream dial.
	// Zero selects Harbor's conservative default.
	ConnectTimeout time.Duration
	// ShutdownTimeout allows established connections to drain before forced closure.
	// Zero selects Harbor's conservative default.
	ShutdownTimeout time.Duration
	// KeepAlive enables TCP keepalive with this idle period on both sides of the relay.
	// Zero selects Harbor's conservative default.
	KeepAlive time.Duration
	// ObserveError optionally records connection-local failures.
	ObserveError ErrorObserver
}

// Snapshot is a concurrency-safe observation of one relay's lifetime counters.
type Snapshot struct {
	// ListenAddress is the exact socket owned by Serve after it starts.
	ListenAddress netip.AddrPort
	// Upstream is the destination selected for the next admitted connection.
	Upstream netip.AddrPort
	// Running reports whether Serve currently owns its listener.
	Running bool
	// ActiveConnections is the number of admitted connections not yet closed.
	ActiveConnections uint64
	// AcceptedConnections is the lifetime number of admitted client connections.
	AcceptedConnections uint64
	// CompletedConnections is the lifetime number of closed client connections.
	CompletedConnections uint64
	// DialFailures is the lifetime number of upstream connection failures.
	DialFailures uint64
	// ClientBytes is the lifetime number of bytes copied from clients to upstreams.
	ClientBytes uint64
	// UpstreamBytes is the lifetime number of bytes copied from upstreams to clients.
	UpstreamBytes uint64
	// DroppedDiagnostics is the lifetime number of errors discarded because the observer was busy.
	DroppedDiagnostics uint64
}

// relayState prevents two Serve calls from sharing lifecycle ownership.
type relayState uint8

const (
	relayStateNew relayState = iota
	relayStateRunning
	relayStateStopped
)

// dialContext opens one TCP connection while preserving a deterministic test boundary.
type dialContext func(context.Context, string, string) (net.Conn, error)

// Relay owns one bounded listener lifecycle and an atomically replaceable upstream.
type Relay struct {
	config Config
	dial   dialContext

	upstream atomic.Pointer[netip.AddrPort]
	active   atomic.Uint64
	accepted atomic.Uint64
	complete atomic.Uint64
	dialFail atomic.Uint64
	toServer atomic.Uint64
	toClient atomic.Uint64
	dropped  atomic.Uint64

	stateMu     sync.RWMutex
	state       relayState
	listen      netip.AddrPort
	diagnostics chan error
}

// connectionPair holds both halves so forced shutdown cannot strand a dial or copy loop.
type connectionPair struct {
	mutex    sync.Mutex
	client   net.Conn
	upstream net.Conn
	cancel   context.CancelFunc
	closed   bool
	close    sync.Once
}

// connectionRegistry tracks exactly the connections admitted by one Serve invocation.
type connectionRegistry struct {
	mutex       sync.Mutex
	connections map[*connectionPair]struct{}
}

// copyResult records one directional transfer without retaining application payloads.
type copyResult struct {
	err error
}

// New validates relay policy and installs the standard TCP dialer.
func New(config Config) (*Relay, error) {
	dialer := &net.Dialer{KeepAlive: normalizedKeepAlive(config.KeepAlive)}
	return newRelay(config, dialer.DialContext)
}

// newRelay keeps upstream failures and cancellation deterministic in package tests.
func newRelay(config Config, dial dialContext) (*Relay, error) {
	config = normalizeConfig(config)
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	relay := &Relay{config: config, dial: dial}
	upstream := config.Upstream
	relay.upstream.Store(&upstream)
	return relay, nil
}

// Serve forwards connections from an already-bound loopback listener until shutdown completes.
//
// The caller binds the listener because Harbor's host reconciler must prove and claim the exact
// project address before a relay starts. Serve takes ownership of closing the listener.
func (relay *Relay) Serve(ctx context.Context, listener net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if listener == nil {
		return errors.New("serve TCP relay: listener is required")
	}
	address, err := listenerAddress(listener)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("serve TCP relay: %w", err)
	}
	if err := relay.begin(address); err != nil {
		_ = listener.Close()
		return err
	}
	defer relay.finish()

	serveContext, stopAccepting := context.WithCancel(context.Background())
	registry := newConnectionRegistry()
	slots := make(chan struct{}, relay.config.MaxConnections)
	var workers sync.WaitGroup

	closeListener := sync.OnceFunc(func() {
		stopAccepting()
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			relay.observe(fmt.Errorf("close TCP relay listener: %w", closeErr))
		}
	})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			closeListener()
		case <-serveContext.Done():
		}
	}()

	acceptErr := relay.accept(serveContext, listener, registry, slots, &workers)
	closeListener()
	<-watcherDone
	relay.drain(registry, &workers)
	stopAccepting()

	return acceptErr
}

// UpdateUpstream atomically moves new connections while established connections drain naturally.
func (relay *Relay) UpdateUpstream(upstream netip.AddrPort) error {
	upstream = canonicalEndpoint(upstream)
	if err := validateEndpoint("TCP relay upstream", upstream); err != nil {
		return err
	}
	relay.stateMu.Lock()
	defer relay.stateMu.Unlock()
	if relay.state == relayStateRunning && relay.listen == upstream {
		return fmt.Errorf("TCP relay upstream %s cannot equal the active listener", upstream)
	}
	relay.upstream.Store(&upstream)
	return nil
}

// Snapshot returns counters and routing state without blocking active copies.
func (relay *Relay) Snapshot() Snapshot {
	relay.stateMu.RLock()
	state := relay.state
	listen := relay.listen
	relay.stateMu.RUnlock()
	upstream := relay.upstream.Load()

	return Snapshot{
		ListenAddress:        listen,
		Upstream:             *upstream,
		Running:              state == relayStateRunning,
		ActiveConnections:    relay.active.Load(),
		AcceptedConnections:  relay.accepted.Load(),
		CompletedConnections: relay.complete.Load(),
		DialFailures:         relay.dialFail.Load(),
		ClientBytes:          relay.toServer.Load(),
		UpstreamBytes:        relay.toClient.Load(),
		DroppedDiagnostics:   relay.dropped.Load(),
	}
}

// begin claims the relay lifecycle before any connection can be admitted.
func (relay *Relay) begin(address netip.AddrPort) error {
	relay.stateMu.Lock()
	defer relay.stateMu.Unlock()
	if relay.state != relayStateNew {
		return errors.New("serve TCP relay: relay lifecycle has already started")
	}
	if upstream := *relay.upstream.Load(); upstream == address {
		return fmt.Errorf("serve TCP relay: upstream %s cannot equal the listener", upstream)
	}
	relay.state = relayStateRunning
	relay.listen = address
	if relay.config.ObserveError != nil {
		relay.diagnostics = make(chan error, maximumPendingDiagnostics)
		go relay.runObserver(relay.diagnostics)
	}
	return nil
}

// finish publishes terminal lifecycle state after every admitted worker has joined.
func (relay *Relay) finish() {
	relay.stateMu.Lock()
	relay.state = relayStateStopped
	if relay.diagnostics != nil {
		close(relay.diagnostics)
		relay.diagnostics = nil
	}
	relay.stateMu.Unlock()
}

// accept bounds admission before calling Accept so user space never owns more than its limit.
func (relay *Relay) accept(
	ctx context.Context,
	listener net.Listener,
	registry *connectionRegistry,
	slots chan struct{},
	workers *sync.WaitGroup,
) error {
	consecutiveFailures := 0
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}

		client, err := listener.Accept()
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return nil
			}
			if !retryableAcceptError(err) {
				return fmt.Errorf("accept TCP relay connection: %w", err)
			}
			consecutiveFailures++
			relay.observe(fmt.Errorf("retry TCP relay accept: %w", err))
			if err := waitForAcceptRetry(ctx, consecutiveFailures); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("back off TCP relay accept: %w", err)
			}
			continue
		}
		if client == nil {
			<-slots
			return errors.New("accept TCP relay connection: listener returned no connection")
		}

		consecutiveFailures = 0
		pair := newConnectionPair(client)
		registry.add(pair)
		relay.active.Add(1)
		relay.accepted.Add(1)
		workers.Add(1)
		go relay.serveConnection(ctx, pair, registry, slots, workers)
	}
}

// serveConnection contains each upstream failure and guarantees registry and slot release.
func (relay *Relay) serveConnection(
	serveContext context.Context,
	pair *connectionPair,
	registry *connectionRegistry,
	slots chan struct{},
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	defer func() { <-slots }()
	defer registry.remove(pair)
	defer relay.active.Add(^uint64(0))
	defer relay.complete.Add(1)
	defer pair.Close()

	upstream := *relay.upstream.Load()
	dialContext, cancelDial := context.WithTimeout(serveContext, relay.config.ConnectTimeout)
	pair.setCancel(cancelDial)
	connection, err := relay.dial(dialContext, "tcp4", upstream.String())
	cancelDial()
	pair.setCancel(nil)
	if err != nil {
		if connection != nil {
			_ = connection.Close()
		}
		relay.dialFail.Add(1)
		if serveContext.Err() == nil {
			relay.observe(fmt.Errorf("connect TCP relay upstream %s: %w", upstream, err))
		}
		return
	}
	if connection == nil {
		relay.dialFail.Add(1)
		relay.observe(fmt.Errorf("connect TCP relay upstream %s: dialer returned no connection", upstream))
		return
	}
	if !pair.setUpstream(connection) {
		return
	}

	if err := configureKeepAlive(pair.client, relay.config.KeepAlive); err != nil {
		relay.observe(fmt.Errorf("configure TCP relay client keepalive: %w", err))
		return
	}
	if err := configureKeepAlive(connection, relay.config.KeepAlive); err != nil {
		relay.observe(fmt.Errorf("configure TCP relay upstream keepalive: %w", err))
		return
	}

	clientResult := make(chan copyResult, 1)
	upstreamResult := make(chan copyResult, 1)
	go copyAndHalfClose(connection, pair.client, &relay.toServer, clientResult)
	go copyAndHalfClose(pair.client, connection, &relay.toClient, upstreamResult)

	toServer := <-clientResult
	toClient := <-upstreamResult
	if err := expectedCopyError(toServer.err); err != nil {
		relay.observe(fmt.Errorf("copy TCP relay client to upstream: %w", err))
	}
	if err := expectedCopyError(toClient.err); err != nil {
		relay.observe(fmt.Errorf("copy TCP relay upstream to client: %w", err))
	}
}

// drain gives healthy connections one bounded chance to finish before closing every owned socket.
func (relay *Relay) drain(registry *connectionRegistry, workers *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()

	timer := time.NewTimer(relay.config.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}

	registry.closeAll()
	<-done
}

// observe drops diagnostics under pressure so optional reporting can never stop the data plane.
func (relay *Relay) observe(err error) {
	if err == nil || relay.config.ObserveError == nil {
		return
	}
	relay.stateMu.RLock()
	defer relay.stateMu.RUnlock()
	if relay.diagnostics == nil {
		relay.dropped.Add(1)
		return
	}
	select {
	case relay.diagnostics <- err:
	default:
		relay.dropped.Add(1)
	}
}

// runObserver confines a permanently blocked callback to one disposable diagnostic goroutine.
func (relay *Relay) runObserver(diagnostics <-chan error) {
	for err := range diagnostics {
		relay.invokeObserver(err)
	}
}

// invokeObserver recovers one callback panic without terminating later diagnostic delivery.
func (relay *Relay) invokeObserver(err error) {
	defer func() {
		_ = recover()
	}()
	relay.config.ObserveError(err)
}

// newConnectionPair captures the accepted client before upstream dialing begins.
func newConnectionPair(client net.Conn) *connectionPair {
	return &connectionPair{client: client}
}

// setUpstream publishes the second socket unless forced shutdown already owns cleanup.
func (pair *connectionPair) setUpstream(upstream net.Conn) bool {
	pair.mutex.Lock()
	if pair.closed {
		pair.mutex.Unlock()
		_ = upstream.Close()
		return false
	}
	pair.upstream = upstream
	pair.mutex.Unlock()
	return true
}

// setCancel lets forced shutdown interrupt an outstanding upstream dial.
func (pair *connectionPair) setCancel(cancel context.CancelFunc) {
	pair.mutex.Lock()
	if pair.closed {
		pair.mutex.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	pair.cancel = cancel
	pair.mutex.Unlock()
}

// Close interrupts both copy directions and any dial exactly once.
func (pair *connectionPair) Close() {
	pair.close.Do(func() {
		pair.mutex.Lock()
		pair.closed = true
		client := pair.client
		upstream := pair.upstream
		cancel := pair.cancel
		pair.mutex.Unlock()
		if cancel != nil {
			cancel()
		}
		if client != nil {
			_ = client.Close()
		}
		if upstream != nil {
			_ = upstream.Close()
		}
	})
}

// newConnectionRegistry creates the active set for one listener lifetime.
func newConnectionRegistry() *connectionRegistry {
	return &connectionRegistry{connections: make(map[*connectionPair]struct{})}
}

// add records a connection before its worker starts.
func (registry *connectionRegistry) add(pair *connectionPair) {
	registry.mutex.Lock()
	registry.connections[pair] = struct{}{}
	registry.mutex.Unlock()
}

// remove forgets a connection only after both socket directions have stopped.
func (registry *connectionRegistry) remove(pair *connectionPair) {
	registry.mutex.Lock()
	delete(registry.connections, pair)
	registry.mutex.Unlock()
}

// closeAll snapshots the set so socket closure never holds registry authority.
func (registry *connectionRegistry) closeAll() {
	registry.mutex.Lock()
	pairs := make([]*connectionPair, 0, len(registry.connections))
	for pair := range registry.connections {
		pairs = append(pairs, pair)
	}
	registry.mutex.Unlock()
	for _, pair := range pairs {
		pair.Close()
	}
}

// countingWriter publishes successful writes while never retaining application payloads.
type countingWriter struct {
	writer  io.Writer
	counter *atomic.Uint64
}

// Write accounts only bytes accepted by the destination, including partial writes before errors.
func (writer *countingWriter) Write(payload []byte) (int, error) {
	written, err := writer.writer.Write(payload)
	if written > 0 {
		writer.counter.Add(uint64(written))
	}
	return written, err
}

// copyAndHalfClose preserves TCP half-close semantics for request/response protocols that wait for EOF.
func copyAndHalfClose(destination net.Conn, source net.Conn, counter *atomic.Uint64, result chan<- copyResult) {
	_, err := io.Copy(&countingWriter{writer: destination, counter: counter}, source)
	if closeWriter, ok := destination.(interface{ CloseWrite() error }); ok {
		if closeErr := closeWriter.CloseWrite(); err == nil {
			err = closeErr
		}
	}
	result <- copyResult{err: err}
}

// configureKeepAlive applies the same liveness policy to client and upstream TCP sockets.
func configureKeepAlive(connection net.Conn, period time.Duration) error {
	tcpConnection, ok := connection.(*net.TCPConn)
	if !ok {
		return nil
	}
	if err := tcpConnection.SetKeepAlive(true); err != nil {
		return err
	}
	return tcpConnection.SetKeepAlivePeriod(period)
}

// expectedCopyError filters closure errors produced by orderly or forced relay teardown.
func expectedCopyError(err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// retryableAcceptError recognizes temporary resource pressure without hiding a failed listener.
func retryableAcceptError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// waitForAcceptRetry applies capped exponential delay so endpoint pressure cannot spin the daemon.
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

// listenerAddress converts an owned listener to the exact IPv4 loopback socket identity.
func listenerAddress(listener net.Listener) (netip.AddrPort, error) {
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("listener address %q is not an IP socket", listener.Addr())
	}
	address = canonicalEndpoint(address)
	if err := validateEndpoint("TCP relay listener", address); err != nil {
		return netip.AddrPort{}, err
	}
	return address, nil
}

// normalizeConfig supplies bounded production defaults before validation.
func normalizeConfig(config Config) Config {
	config.Upstream = canonicalEndpoint(config.Upstream)
	if config.MaxConnections == 0 {
		config.MaxConnections = defaultMaximumConnections
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = defaultConnectTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.KeepAlive == 0 {
		config.KeepAlive = defaultKeepAlive
	}
	return config
}

// normalizedKeepAlive gives the standard dialer its final default without duplicating Config mutation.
func normalizedKeepAlive(period time.Duration) time.Duration {
	if period == 0 {
		return defaultKeepAlive
	}
	return period
}

// validateConfig rejects resource bounds that could make one project exhaust the daemon.
func validateConfig(config Config) error {
	if err := validateEndpoint("TCP relay upstream", config.Upstream); err != nil {
		return err
	}
	if config.MaxConnections < 1 || config.MaxConnections > hardMaximumConnections {
		return fmt.Errorf("TCP relay maximum connections must be between 1 and %d", hardMaximumConnections)
	}
	if config.ConnectTimeout < time.Millisecond || config.ConnectTimeout > maximumConnectTimeout {
		return fmt.Errorf("TCP relay connect timeout must be between 1ms and %s", maximumConnectTimeout)
	}
	if config.ShutdownTimeout < time.Millisecond || config.ShutdownTimeout > maximumShutdownTimeout {
		return fmt.Errorf("TCP relay shutdown timeout must be between 1ms and %s", maximumShutdownTimeout)
	}
	if config.KeepAlive < time.Second || config.KeepAlive > maximumKeepAlive {
		return fmt.Errorf("TCP relay keepalive must be between 1s and %s", maximumKeepAlive)
	}
	return nil
}

// validateEndpoint confines both sides of a relay to an exact IPv4 loopback socket.
func validateEndpoint(label string, endpoint netip.AddrPort) error {
	address := endpoint.Addr()
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("%s address %s must be IPv4 loopback", label, address)
	}
	if endpoint.Port() == 0 {
		return fmt.Errorf("%s port must be greater than zero", label)
	}
	return nil
}

// canonicalEndpoint removes IPv4-in-IPv6 representation differences before comparison or storage.
func canonicalEndpoint(endpoint netip.AddrPort) netip.AddrPort {
	if !endpoint.IsValid() {
		return endpoint
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
}
