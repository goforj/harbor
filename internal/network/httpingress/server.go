package httpingress

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	maximumReadHeaderTimeout = time.Minute
	defaultServerIdleTimeout = 2 * time.Minute
	maximumServerIdleTimeout = 30 * time.Minute
	defaultShutdownTimeout   = 10 * time.Second
	maximumShutdownTimeout   = 5 * time.Minute
	defaultMaxHeaderBytes    = 1 << 20
	maximumMaxHeaderBytes    = 8 << 20
	defaultClientConnections = 512
	hardClientConnections    = 8192
)

// ServerConfig defines client-facing resource and shutdown bounds for one ingress lifecycle.
type ServerConfig struct {
	// ReadHeaderTimeout bounds clients that connect without completing HTTP headers.
	// Zero selects Harbor's conservative default.
	ReadHeaderTimeout time.Duration
	// IdleTimeout bounds inactive reusable client connections.
	// Zero selects Harbor's conservative default.
	IdleTimeout time.Duration
	// ShutdownTimeout lets in-flight requests drain before Harbor closes them.
	// Zero selects Harbor's conservative default.
	ShutdownTimeout time.Duration
	// MaxHeaderBytes bounds one request header block.
	// Zero selects Harbor's conservative default.
	MaxHeaderBytes int
	// MaxClientConnections bounds simultaneous served HTTP and HTTPS connections together.
	// Zero selects Harbor's conservative default.
	MaxClientConnections int
}

// ServerSnapshot reports the exact sockets and lifecycle state without exposing route internals.
type ServerSnapshot struct {
	// HTTPAddress is the redirect listener claimed by Serve.
	HTTPAddress netip.AddrPort
	// HTTPSAddress is the TLS listener claimed by Serve.
	HTTPSAddress netip.AddrPort
	// Running reports whether Serve currently owns both listeners.
	Running bool
}

// serverState prevents concurrent or repeated ownership of the same Server instance.
type serverState uint8

const (
	serverStateNew serverState = iota
	serverStateRunning
	serverStateStopped
)

// Server owns one paired HTTP redirect and HTTPS reverse-proxy lifecycle.
type Server struct {
	config      ServerConfig
	router      *Router
	certificate CertificateProvider

	mutex        sync.RWMutex
	state        serverState
	httpAddress  netip.AddrPort
	httpsAddress netip.AddrPort
}

// serveResult identifies which paired listener left its serving loop.
type serveResult struct {
	name string
	err  error
}

// connectionLimiter bounds served sockets across the paired listeners.
type connectionLimiter struct {
	slots       chan struct{}
	mutex       sync.Mutex
	connections map[*boundedConnection]struct{}
}

// boundedListener shares one admission limit while retaining independent listener closure.
type boundedListener struct {
	net.Listener
	limiter *connectionLimiter
	closed  chan struct{}
	once    sync.Once
}

// boundedConnection returns one shared admission slot exactly once.
type boundedConnection struct {
	net.Conn
	limiter *connectionLimiter
	once    sync.Once
}

// observerLogWriter sends protocol-server diagnostics through Router's bounded observer path.
type observerLogWriter struct {
	router *Router
}

// NewServer validates lifecycle policy and retains the required routing dependencies.
func NewServer(config ServerConfig, router *Router, certificate CertificateProvider) (*Server, error) {
	if router == nil {
		return nil, fmt.Errorf("ingress router is required")
	}
	if certificate == nil {
		return nil, fmt.Errorf("ingress certificate provider is required")
	}
	config = normalizeServerConfig(config)
	if err := validateServerConfig(config); err != nil {
		return nil, err
	}
	return &Server{config: config, router: router, certificate: certificate}, nil
}

// Serve owns two already-bound IPv4 loopback listeners until cancellation or a serving failure.
//
// The caller pre-binds both listeners so host reconciliation can prove low-port forwarding before
// ingress starts. Serve takes ownership of closing both listeners, including on validation failure.
func (server *Server) Serve(ctx context.Context, httpListener, httpsListener net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if httpListener == nil || httpsListener == nil {
		closeListeners(httpListener, httpsListener)
		return fmt.Errorf("serve HTTP ingress: HTTP and HTTPS listeners are required")
	}
	httpAddress, err := ingressListenerAddress("HTTP", httpListener)
	if err != nil {
		closeListeners(httpListener, httpsListener)
		return fmt.Errorf("serve HTTP ingress: %w", err)
	}
	httpsAddress, err := ingressListenerAddress("HTTPS", httpsListener)
	if err != nil {
		closeListeners(httpListener, httpsListener)
		return fmt.Errorf("serve HTTP ingress: %w", err)
	}
	if httpAddress == httpsAddress {
		closeListeners(httpListener, httpsListener)
		return fmt.Errorf("serve HTTP ingress: HTTP and HTTPS listeners must be distinct")
	}
	if err := server.begin(httpAddress, httpsAddress); err != nil {
		closeListeners(httpListener, httpsListener)
		return err
	}
	defer server.finish()

	tlsConfig, err := server.router.TLSConfig(server.certificate)
	if err != nil {
		closeListeners(httpListener, httpsListener)
		return fmt.Errorf("serve HTTP ingress: %w", err)
	}
	limiter := newConnectionLimiter(server.config.MaxClientConnections)
	boundedHTTP := newBoundedListener(httpListener, limiter)
	boundedHTTPS := newBoundedListener(httpsListener, limiter)
	httpServer := server.httpServer(server.router.HTTPHandler())
	httpsServer := server.httpServer(server.router.HTTPSHandler())
	httpsServer.TLSConfig = tlsConfig
	results := make(chan serveResult, 2)
	go func() {
		results <- serveResult{name: "HTTP", err: httpServer.Serve(boundedHTTP)}
	}()
	go func() {
		results <- serveResult{name: "HTTPS", err: httpsServer.ServeTLS(boundedHTTPS, "", "")}
	}()

	var first serveResult
	select {
	case <-ctx.Done():
	case first = <-results:
	}
	shutdownErr := server.shutdown(httpServer, httpsServer)
	limiter.closeConnections()
	server.router.CloseIdleConnections()

	serveErrs := make([]error, 0, 2)
	if first.name != "" {
		if err := unexpectedServeError(first); err != nil {
			serveErrs = append(serveErrs, err)
		}
	}
	remaining := 2
	if first.name != "" {
		remaining--
	}
	for remaining > 0 {
		result := <-results
		remaining--
		if err := unexpectedServeError(result); err != nil {
			serveErrs = append(serveErrs, err)
		}
	}
	if shutdownErr != nil {
		serveErrs = append(serveErrs, shutdownErr)
	}
	return errors.Join(serveErrs...)
}

// Snapshot returns paired listener ownership without blocking serving goroutines.
func (server *Server) Snapshot() ServerSnapshot {
	server.mutex.RLock()
	defer server.mutex.RUnlock()
	return ServerSnapshot{
		HTTPAddress:  server.httpAddress,
		HTTPSAddress: server.httpsAddress,
		Running:      server.state == serverStateRunning,
	}
}

// begin claims lifecycle ownership before either serving goroutine starts.
func (server *Server) begin(httpAddress, httpsAddress netip.AddrPort) error {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if server.state != serverStateNew {
		return fmt.Errorf("serve HTTP ingress: server lifecycle has already started")
	}
	server.state = serverStateRunning
	server.httpAddress = httpAddress
	server.httpsAddress = httpsAddress
	return nil
}

// finish publishes terminal state only after both serving goroutines have returned.
func (server *Server) finish() {
	server.mutex.Lock()
	server.state = serverStateStopped
	server.mutex.Unlock()
}

// httpServer gives the redirect and proxy listeners identical defensive protocol limits.
func (server *Server) httpServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:                      handler,
		ErrorLog:                     log.New(observerLogWriter{router: server.router}, "", 0),
		ReadHeaderTimeout:            server.config.ReadHeaderTimeout,
		IdleTimeout:                  server.config.IdleTimeout,
		MaxHeaderBytes:               server.config.MaxHeaderBytes,
		DisableGeneralOptionsHandler: true,
	}
}

// Write accepts http.Server log records without allowing its goroutine to block on diagnostics.
func (writer observerLogWriter) Write(message []byte) (int, error) {
	writer.router.observe(fmt.Errorf("HTTP ingress: %s", message))
	return len(message), nil
}

// shutdown drains both protocol listeners concurrently under one shared deadline.
func (server *Server) shutdown(httpServer, httpsServer *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), server.config.ShutdownTimeout)
	defer cancel()
	errorsChannel := make(chan error, 2)
	for _, candidate := range []*http.Server{httpServer, httpsServer} {
		candidate := candidate
		go func() {
			errorsChannel <- candidate.Shutdown(ctx)
		}()
	}
	var result []error
	for count := 0; count < 2; count++ {
		if err := <-errorsChannel; err != nil {
			result = append(result, err)
		}
	}
	if len(result) != 0 {
		_ = httpServer.Close()
		_ = httpsServer.Close()
		return fmt.Errorf("shut down HTTP ingress: %w", errors.Join(result...))
	}
	return nil
}

// unexpectedServeError treats only shutdown's documented terminal error as normal.
func unexpectedServeError(result serveResult) error {
	if result.err == nil || errors.Is(result.err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve %s ingress: %w", result.name, result.err)
}

// normalizeServerConfig gives zero-value server settings conservative production bounds.
func normalizeServerConfig(config ServerConfig) ServerConfig {
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultServerIdleTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.MaxHeaderBytes == 0 {
		config.MaxHeaderBytes = defaultMaxHeaderBytes
	}
	if config.MaxClientConnections == 0 {
		config.MaxClientConnections = defaultClientConnections
	}
	return config
}

// validateServerConfig rejects bounds that would disable protection or stall daemon shutdown.
func validateServerConfig(config ServerConfig) error {
	if config.ReadHeaderTimeout < 0 || config.ReadHeaderTimeout > maximumReadHeaderTimeout {
		return fmt.Errorf("ingress read header timeout must be between zero and %s", maximumReadHeaderTimeout)
	}
	if config.IdleTimeout < 0 || config.IdleTimeout > maximumServerIdleTimeout {
		return fmt.Errorf("ingress client idle timeout must be between zero and %s", maximumServerIdleTimeout)
	}
	if config.ShutdownTimeout < 0 || config.ShutdownTimeout > maximumShutdownTimeout {
		return fmt.Errorf("ingress shutdown timeout must be between zero and %s", maximumShutdownTimeout)
	}
	if config.MaxHeaderBytes < 1 || config.MaxHeaderBytes > maximumMaxHeaderBytes {
		return fmt.Errorf("ingress maximum header bytes must be between 1 and %d", maximumMaxHeaderBytes)
	}
	if config.MaxClientConnections < 1 || config.MaxClientConnections > hardClientConnections {
		return fmt.Errorf("ingress maximum client connections must be between 1 and %d", hardClientConnections)
	}
	return nil
}

// ingressListenerAddress proves the caller supplied a concrete IPv4 loopback TCP socket.
func ingressListenerAddress(name string, listener net.Listener) (netip.AddrPort, error) {
	if listener.Addr() == nil || listener.Addr().Network() != "tcp" {
		return netip.AddrPort{}, fmt.Errorf("%s listener must be TCP", name)
	}
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("%s listener address %q is invalid: %w", name, listener.Addr(), err)
	}
	address = netip.AddrPortFrom(address.Addr().Unmap(), address.Port())
	if !address.Addr().Is4() || !address.Addr().IsLoopback() {
		return netip.AddrPort{}, fmt.Errorf("%s listener %q must use IPv4 loopback", name, address)
	}
	if address.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("%s listener port must not be zero", name)
	}
	return address, nil
}

// closeListeners releases caller-owned sockets after Serve accepts lifecycle ownership.
func closeListeners(listeners ...net.Listener) {
	for _, listener := range listeners {
		if listener != nil {
			_ = listener.Close()
		}
	}
}

// newBoundedListener attaches one protocol listener to the shared admission pool.
func newBoundedListener(listener net.Listener, limiter *connectionLimiter) *boundedListener {
	return &boundedListener{
		Listener: listener,
		limiter:  limiter,
		closed:   make(chan struct{}),
	}
}

// newConnectionLimiter keeps the exact served-connection limit shared across both protocol listeners.
func newConnectionLimiter(maximum int) *connectionLimiter {
	return &connectionLimiter{
		slots:       make(chan struct{}, maximum),
		connections: make(map[*boundedConnection]struct{}, maximum),
	}
}

// Accept rejects overload immediately so saturation cannot hide listener failure or retain extra sockets.
func (listener *boundedListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case <-listener.closed:
			_ = connection.Close()
			return nil, net.ErrClosed
		default:
		}
		select {
		case listener.limiter.slots <- struct{}{}:
			select {
			case <-listener.closed:
				<-listener.limiter.slots
				_ = connection.Close()
				return nil, net.ErrClosed
			default:
			}
			return listener.limiter.track(connection), nil
		default:
			_ = connection.Close()
		}
	}
}

// Close unblocks the underlying accept loop and prevents a raced socket from being admitted.
func (listener *boundedListener) Close() error {
	listener.once.Do(func() {
		close(listener.closed)
	})
	return listener.Listener.Close()
}

// Close releases one shared admission slot after closing the client connection.
func (connection *boundedConnection) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(func() {
		connection.limiter.release(connection)
	})
	return err
}

// track retains accepted sockets so hijacked upgrades cannot outlive the ingress lifecycle.
func (limiter *connectionLimiter) track(connection net.Conn) *boundedConnection {
	bounded := &boundedConnection{Conn: connection, limiter: limiter}
	limiter.mutex.Lock()
	limiter.connections[bounded] = struct{}{}
	limiter.mutex.Unlock()
	return bounded
}

// release returns admission exactly once and removes the socket from lifecycle ownership.
func (limiter *connectionLimiter) release(connection *boundedConnection) {
	limiter.mutex.Lock()
	delete(limiter.connections, connection)
	limiter.mutex.Unlock()
	<-limiter.slots
}

// closeConnections terminates upgrades that net/http deliberately excludes from Shutdown.
func (limiter *connectionLimiter) closeConnections() {
	limiter.mutex.Lock()
	connections := make([]*boundedConnection, 0, len(limiter.connections))
	for connection := range limiter.connections {
		connections = append(connections, connection)
	}
	limiter.mutex.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}
