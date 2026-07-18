package dnsserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

const (
	maxUDPRequestBytes     = 1232
	maxUDPQueries          = 64
	maxTCPRequestBytes     = 4096
	maxTCPQueries          = 16
	maxTCPConnections      = 64
	dnsHeaderBytes         = 12
	maxServerTimeout       = 30 * time.Second
	defaultReadTimeout     = 2 * time.Second
	defaultWriteTimeout    = 2 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	testZoneName           = "test."
	soaPrimaryName         = "harbor.invalid."
	soaMailboxName         = "hostmaster.harbor.invalid."
)

var (
	// ErrRunning reports an attempt to start a server that already owns its listeners.
	ErrRunning = errors.New("dns server is already running")
)

// Config defines the exact loopback socket and bounded server timeouts.
type Config struct {
	// Address is the exact IPv4 loopback identity that owns both transports.
	Address netip.Addr
	// Port is the shared UDP and TCP port; zero selects an ephemeral test port.
	Port uint16
	// ReadTimeout bounds retained TCP clients.
	ReadTimeout time.Duration
	// WriteTimeout bounds response writes on both transports.
	WriteTimeout time.Duration
	// ShutdownTimeout bounds graceful joining before callers regain control.
	ShutdownTimeout time.Duration
}

// DefaultConfig returns production timeout bounds for one IPv4 loopback listener pair.
func DefaultConfig(address netip.Addr, port uint16) Config {
	return Config{
		Address:         address,
		Port:            port,
		ReadTimeout:     defaultReadTimeout,
		WriteTimeout:    defaultWriteTimeout,
		ShutdownTimeout: defaultShutdownTimeout,
	}
}

// Validate rejects sockets and timeouts that could widen or indefinitely stall the local server.
func (c Config) Validate() error {
	_, err := c.normalized()
	return err
}

// normalized applies safe timeout defaults while retaining port zero for ephemeral test listeners.
func (c Config) normalized() (Config, error) {
	address := c.Address.Unmap()
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() {
		return Config{}, fmt.Errorf("dns server: address %s is not IPv4 loopback", c.Address)
	}
	c.Address = address

	var err error
	if c.ReadTimeout, err = normalizeTimeout("read", c.ReadTimeout, defaultReadTimeout); err != nil {
		return Config{}, err
	}
	if c.WriteTimeout, err = normalizeTimeout("write", c.WriteTimeout, defaultWriteTimeout); err != nil {
		return Config{}, err
	}
	if c.ShutdownTimeout, err = normalizeTimeout("shutdown", c.ShutdownTimeout, defaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	return c, nil
}

// normalizeTimeout supplies a bounded default without accepting negative or excessive durations.
func normalizeTimeout(label string, value time.Duration, fallback time.Duration) (time.Duration, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 0 || value > maxServerTimeout {
		return 0, fmt.Errorf("dns server: %s timeout must be between 1ns and %s", label, maxServerTimeout)
	}
	return value, nil
}

// Server publishes one immutable record snapshot through matching UDP and TCP listeners.
type Server struct {
	config Config

	records atomic.Pointer[recordSnapshot]

	mu      sync.Mutex
	run     *serverRun
	lastErr error
}

// recordSnapshot is never mutated after its pointer becomes visible to query goroutines.
type recordSnapshot struct {
	ttl      uint32
	records  map[string]netip.Addr
	existing map[string]struct{}
}

// protocolServer captures the shared lifecycle surface of Harbor's bounded UDP loop and miekg TCP server.
type protocolServer interface {
	ActivateAndServe() error
	ShutdownContext(context.Context) error
}

// serverRun owns one generation of UDP and TCP listener state.
type serverRun struct {
	address     netip.AddrPort
	udp         protocolServer
	tcp         protocolServer
	udpConn     *net.UDPConn
	tcpListener net.Listener
	started     chan string
	results     chan protocolResult
	stop        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	err         error
}

// protocolResult identifies a listener that returned from its serving loop.
type protocolResult struct {
	protocol string
	err      error
}

// udpProtocol bounds datagram ownership before a worker goroutine exists.
type udpProtocol struct {
	connection   *net.UDPConn
	writeTimeout time.Duration
	notify       func()
	respond      func(*dns.Msg) *dns.Msg
	permits      chan struct{}
	buffers      sync.Pool

	mutex     sync.Mutex
	activated bool
	started   bool
	stopping  bool
	done      chan struct{}
	workers   sync.WaitGroup
}

// tcpProtocol exposes shutdown state to the decorated reader before miekg changes socket deadlines.
type tcpProtocol struct {
	server   *dns.Server
	stopping atomic.Bool
}

// newUDPProtocol creates a fixed-capacity datagram loop whose buffers remain locally owned.
func newUDPProtocol(connection *net.UDPConn, writeTimeout time.Duration, notify func(), respond func(*dns.Msg) *dns.Msg) *udpProtocol {
	protocol := &udpProtocol{
		connection:   connection,
		writeTimeout: writeTimeout,
		notify:       notify,
		respond:      respond,
		permits:      make(chan struct{}, maxUDPQueries),
		done:         make(chan struct{}),
	}
	protocol.buffers.New = func() any {
		return make([]byte, maxUDPRequestBytes+1)
	}
	return protocol
}

// ActivateAndServe owns the UDP socket until shutdown joins every admitted datagram.
func (protocol *udpProtocol) ActivateAndServe() error {
	protocol.mutex.Lock()
	if protocol.activated {
		protocol.mutex.Unlock()
		return errors.New("DNS UDP server lifecycle has already started")
	}
	protocol.activated = true
	protocol.started = true
	protocol.mutex.Unlock()

	protocol.notify()
	err := protocol.serve()
	protocol.workers.Wait()
	protocol.mutex.Lock()
	stopping := protocol.stopping
	protocol.started = false
	protocol.mutex.Unlock()
	close(protocol.done)
	if stopping && errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// ShutdownContext closes admission immediately and bounds the wait for admitted datagrams.
func (protocol *udpProtocol) ShutdownContext(ctx context.Context) error {
	protocol.mutex.Lock()
	if !protocol.started {
		protocol.mutex.Unlock()
		return errors.New("DNS UDP server is not started")
	}
	protocol.stopping = true
	done := protocol.done
	closeErr := protocol.connection.Close()
	protocol.mutex.Unlock()

	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(closeErr, waitErr)
}

// serve reads or drops each datagram before allocating one of the bounded worker slots.
func (protocol *udpProtocol) serve() error {
	for {
		buffer := protocol.buffers.Get().([]byte)
		count, remote, err := protocol.connection.ReadFromUDPAddrPort(buffer)
		if err != nil {
			protocol.buffers.Put(buffer)
			return err
		}
		if count > maxUDPRequestBytes || !remote.Addr().IsLoopback() {
			protocol.buffers.Put(buffer)
			continue
		}
		select {
		case protocol.permits <- struct{}{}:
			protocol.workers.Add(1)
			go protocol.handle(buffer[:count], remote)
		default:
			protocol.buffers.Put(buffer)
		}
	}
}

// handle parses and answers one admitted datagram while retaining its buffer only for that operation.
func (protocol *udpProtocol) handle(packet []byte, remote netip.AddrPort) {
	defer protocol.workers.Done()
	defer func() { <-protocol.permits }()
	defer protocol.buffers.Put(packet[:cap(packet)])

	if !admissibleWireHeader(packet) {
		return
	}
	request := new(dns.Msg)
	if err := request.Unpack(packet); err != nil {
		return
	}
	response := protocol.respond(request)
	if response == nil {
		return
	}
	response.Truncate(udpPayloadSize(request))
	packed, err := response.Pack()
	if err != nil {
		return
	}
	if err := protocol.connection.SetWriteDeadline(time.Now().Add(protocol.writeTimeout)); err != nil {
		return
	}
	_, _ = protocol.connection.WriteToUDPAddrPort(packed, remote)
}

// admissibleWireHeader bounds parser-owned section allocations before decoding attacker-controlled data.
func admissibleWireHeader(packet []byte) bool {
	if len(packet) < dnsHeaderBytes {
		return false
	}
	flags := binary.BigEndian.Uint16(packet[2:4])
	if flags&0x8000 != 0 {
		return false
	}
	questions := binary.BigEndian.Uint16(packet[4:6])
	answers := binary.BigEndian.Uint16(packet[6:8])
	authorities := binary.BigEndian.Uint16(packet[8:10])
	extras := binary.BigEndian.Uint16(packet[10:12])
	return questions <= 2 && answers <= 1 && authorities <= 1 && extras <= 2
}

// ActivateAndServe delegates TCP ownership after exposing shutdown state to the reader.
func (protocol *tcpProtocol) ActivateAndServe() error {
	return protocol.server.ActivateAndServe()
}

// ShutdownContext marks reads before miekg installs its past-due socket deadline.
func (protocol *tcpProtocol) ShutdownContext(ctx context.Context) error {
	protocol.stopping.Store(true)
	return protocol.server.ShutdownContext(ctx)
}

// NewServer validates configuration and installs the initial immutable record snapshot.
func NewServer(config Config, snapshot Snapshot) (*Server, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if err := snapshot.validate(); err != nil {
		return nil, err
	}

	server := &Server{config: normalized}
	server.records.Store(copySnapshot(snapshot))
	return server, nil
}

// Replace atomically publishes a complete validated record set to both protocols.
func (s *Server) Replace(snapshot Snapshot) error {
	if err := snapshot.validate(); err != nil {
		return err
	}
	s.records.Store(copySnapshot(snapshot))
	return nil
}

// copySnapshot prevents later package-local changes from violating reader immutability.
func copySnapshot(snapshot Snapshot) *recordSnapshot {
	records := make(map[string]netip.Addr, len(snapshot.records))
	existing := map[string]struct{}{"test": {}}
	for name, address := range snapshot.records {
		records[name] = address
		// An ancestor remains a real DNS name even when Harbor publishes no A record at that owner.
		labels := strings.Split(name, ".")
		for index := range len(labels) - 1 {
			existing[strings.Join(labels[index:], ".")] = struct{}{}
		}
	}
	return &recordSnapshot{ttl: snapshot.ttl, records: records, existing: existing}
}

// Start binds one loopback port for UDP and TCP and begins serving in the background.
// Start also couples the listener generation to ctx so daemon cancellation cannot orphan it.
func (s *Server) Start(ctx context.Context) (netip.AddrPort, error) {
	if ctx == nil {
		return netip.AddrPort{}, fmt.Errorf("dns server: context is required")
	}
	if err := ctx.Err(); err != nil {
		return netip.AddrPort{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		return netip.AddrPort{}, ErrRunning
	}

	run, err := s.bind()
	if err != nil {
		return netip.AddrPort{}, err
	}
	s.run = run
	s.lastErr = nil

	go serveProtocol("udp", run.udp, run.results)
	go serveProtocol("tcp", run.tcp, run.results)

	if err := awaitStartup(ctx, run); err != nil {
		s.run = nil
		s.lastErr = err
		return netip.AddrPort{}, err
	}

	go s.monitor(ctx, run)
	return run.address, nil
}

// bind reserves one exact port for both DNS transports before either starts accepting traffic.
func (s *Server) bind() (*serverRun, error) {
	ip := net.IP(s.config.Address.AsSlice())
	tcpListener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ip, Port: int(s.config.Port)})
	if err != nil {
		return nil, fmt.Errorf("dns server: bind TCP: %w", err)
	}

	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("dns server: bind UDP: %w", err)
	}

	started := make(chan string, 2)
	results := make(chan protocolResult, 2)
	tcpServer := new(tcpProtocol)
	decorator := func(reader dns.Reader) dns.Reader {
		return &boundedReader{Reader: reader, stopping: &tcpServer.stopping}
	}
	handler := dns.HandlerFunc(s.serveDNS)
	limitedTCP := &limitedListener{
		Listener: tcpListener,
		permits:  make(chan struct{}, maxTCPConnections),
	}
	udpServer := newUDPProtocol(udpConn, s.config.WriteTimeout, func() { started <- "udp" }, s.responseFor)
	tcpServer.server = &dns.Server{
		Listener:          limitedTCP,
		Handler:           handler,
		ReadTimeout:       s.config.ReadTimeout,
		WriteTimeout:      s.config.WriteTimeout,
		DecorateReader:    decorator,
		MaxTCPQueries:     maxTCPQueries,
		NotifyStartedFunc: func() { started <- "tcp" },
	}

	return &serverRun{
		address:     netip.AddrPortFrom(s.config.Address, uint16(port)),
		udp:         udpServer,
		tcp:         tcpServer,
		udpConn:     udpConn,
		tcpListener: limitedTCP,
		started:     started,
		results:     results,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}, nil
}

// serveProtocol reports every serving-loop exit so one failed transport cannot leave a partial server.
func serveProtocol(protocol string, server protocolServer, results chan<- protocolResult) {
	results <- protocolResult{protocol: protocol, err: server.ActivateAndServe()}
}

// awaitStartup requires both pre-bound transports to confirm readiness before Start succeeds.
func awaitStartup(ctx context.Context, run *serverRun) error {
	started := 0
	results := 0
	for started < 2 {
		select {
		case <-run.started:
			started++
		case result := <-run.results:
			results++
			closeRunSockets(run)
			drainProtocolResults(run, results)
			close(run.done)
			if result.err == nil {
				return fmt.Errorf("dns server: %s listener stopped during startup", result.protocol)
			}
			return fmt.Errorf("dns server: start %s listener: %w", result.protocol, result.err)
		case <-ctx.Done():
			closeRunSockets(run)
			drainProtocolResults(run, results)
			close(run.done)
			return ctx.Err()
		}
	}
	return nil
}

// closeRunSockets unblocks transports that have not completed listener startup yet.
func closeRunSockets(run *serverRun) {
	_ = run.udpConn.Close()
	_ = run.tcpListener.Close()
}

// drainProtocolResults joins startup goroutines before their run state is discarded.
func drainProtocolResults(run *serverRun, received int) {
	for received < 2 {
		<-run.results
		received++
	}
}

// monitor couples transport lifetimes and records one bounded terminal result for Close and Wait.
func (s *Server) monitor(parent context.Context, run *serverRun) {
	expectedStop := false
	results := make([]protocolResult, 0, 2)
	select {
	case <-parent.Done():
		expectedStop = true
	case <-run.stop:
		expectedStop = true
	case result := <-run.results:
		results = append(results, result)
	}

	shutdownErrors := shutdownProtocols(run, s.config.ShutdownTimeout)
	for len(results) < 2 {
		results = append(results, <-run.results)
	}

	run.err = terminalError(expectedStop, results, shutdownErrors)
	s.mu.Lock()
	if s.run == run {
		s.run = nil
		s.lastErr = run.err
	}
	s.mu.Unlock()
	close(run.done)
}

// shutdownProtocols stops UDP and TCP concurrently so neither consumes the other's deadline budget.
func shutdownProtocols(run *serverRun, timeout time.Duration) []error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	errorsByProtocol := make(chan error, 2)
	go func() {
		errorsByProtocol <- run.udp.ShutdownContext(ctx)
	}()
	go func() {
		errorsByProtocol <- run.tcp.ShutdownContext(ctx)
	}()

	shutdownErrors := make([]error, 0, 2)
	for range 2 {
		if err := <-errorsByProtocol; err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return shutdownErrors
}

// terminalError distinguishes requested shutdown from a transport disappearing unexpectedly.
func terminalError(expectedStop bool, results []protocolResult, shutdownErrors []error) error {
	errs := make([]error, 0, len(results)+len(shutdownErrors))
	for _, result := range results {
		if result.err != nil && !errors.Is(result.err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("dns %s listener: %w", result.protocol, result.err))
			continue
		}
		if !expectedStop && result.err == nil {
			errs = append(errs, fmt.Errorf("dns %s listener stopped unexpectedly", result.protocol))
		}
	}
	for _, err := range shutdownErrors {
		if !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("dns shutdown: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Close requests an idempotent graceful stop and waits for both transports within the caller's deadline.
func (s *Server) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("dns server: context is required")
	}
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run == nil {
		return nil
	}

	run.requestStop()
	select {
	case <-run.done:
		return run.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the current listener generation stops or the caller cancels the wait.
func (s *Server) Wait(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("dns server: context is required")
	}
	s.mu.Lock()
	run := s.run
	lastErr := s.lastErr
	s.mu.Unlock()
	if run == nil {
		return lastErr
	}
	select {
	case <-run.done:
		return run.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Address returns the active listener generation's shared UDP and TCP endpoint.
func (s *Server) Address() (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return netip.AddrPort{}, false
	}
	return s.run.address, true
}

// requestStop makes concurrent Close calls collapse into one monitor notification.
func (r *serverRun) requestStop() {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
}

// serveDNS applies exact-name authority without forwarding or synthesizing wildcard records.
func (s *Server) serveDNS(writer dns.ResponseWriter, request *dns.Msg) {
	if !isLoopbackClient(writer.RemoteAddr()) {
		return
	}
	response := s.responseFor(request)
	if response != nil {
		_ = writer.WriteMsg(response)
	}
}

// responseFor builds one transport-independent authoritative response from the current snapshot.
func (s *Server) responseFor(request *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Compress = true
	response.RecursionAvailable = false
	requestEDNS := request.IsEdns0()
	if requestEDNS != nil {
		response.SetEdns0(maxUDPRequestBytes, false)
	}
	if !validQuery(request) {
		response.Rcode = dns.RcodeFormatError
		return response
	}
	if requestEDNS != nil && requestEDNS.Version() != 0 {
		response.Rcode = dns.RcodeBadVers
		return response
	}

	question := request.Question[0]
	name := canonicalQueryName(question.Name)
	if !insideTestZone(name) || question.Qclass != dns.ClassINET {
		response.Rcode = dns.RcodeRefused
		return response
	}

	response.Authoritative = true
	snapshot := s.records.Load()
	address, found := snapshot.records[name]
	_, exists := snapshot.existing[name]
	if !exists {
		response.Rcode = dns.RcodeNameError
		response.Ns = []dns.RR{soaRecord(snapshot.ttl)}
		return response
	}
	if name == "test" && (question.Qtype == dns.TypeSOA || question.Qtype == dns.TypeANY) {
		response.Answer = []dns.RR{soaRecord(snapshot.ttl)}
		return response
	}
	if found && (question.Qtype == dns.TypeA || question.Qtype == dns.TypeANY) {
		response.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn(name),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    snapshot.ttl,
			},
			A: net.IP(address.AsSlice()),
		}}
		return response
	}
	response.Ns = []dns.RR{soaRecord(snapshot.ttl)}
	return response
}

// soaRecord supplies the negative-cache boundary required by authoritative `.test` responses.
func soaRecord(ttl uint32) *dns.SOA {
	return &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   testZoneName,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ns:      soaPrimaryName,
		Mbox:    soaMailboxName,
		Serial:  1,
		Refresh: 60,
		Retry:   60,
		Expire:  uint32(MaxTTL / time.Second),
		Minttl:  ttl,
	}
}

// udpPayloadSize honors the client limit without allowing one response to exceed Harbor's budget.
func udpPayloadSize(request *dns.Msg) int {
	requestEDNS := request.IsEdns0()
	if requestEDNS == nil {
		return dns.MinMsgSize
	}
	size := int(requestEDNS.UDPSize())
	if size < dns.MinMsgSize {
		return dns.MinMsgSize
	}
	if size > maxUDPRequestBytes {
		return maxUDPRequestBytes
	}
	return size
}

// validQuery allows one ordinary question and, at most, one EDNS options record.
func validQuery(request *dns.Msg) bool {
	if request.Response || request.Opcode != dns.OpcodeQuery || request.Rcode != dns.RcodeSuccess || request.Zero || request.Truncated || len(request.Question) != 1 {
		return false
	}
	if len(request.Answer) != 0 || len(request.Ns) != 0 || len(request.Extra) > 1 {
		return false
	}
	return len(request.Extra) == 0 || request.Extra[0].Header().Rrtype == dns.TypeOPT && request.Extra[0].Header().Name == "."
}

// canonicalQueryName maps DNS's case-insensitive wire name to Harbor's stored host form.
func canonicalQueryName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

// insideTestZone distinguishes Harbor authority from unrelated resolver traffic.
func insideTestZone(name string) bool {
	return name == "test" || strings.HasSuffix(name, ".test")
}

// isLoopbackClient prevents a future broader bind adapter from widening query access accidentally.
func isLoopbackClient(address net.Addr) bool {
	switch typed := address.(type) {
	case *net.UDPAddr:
		return typed.IP.IsLoopback()
	case *net.TCPAddr:
		return typed.IP.IsLoopback()
	default:
		return false
	}
}

// boundedReader rejects oversized TCP frames without racing miekg's shutdown deadline.
type boundedReader struct {
	dns.Reader
	stopping *atomic.Bool
}

// ReadTCP checks the length prefix before allocating memory for a client-controlled frame.
func (r *boundedReader) ReadTCP(conn net.Conn, timeout time.Duration) ([]byte, error) {
	if r.stopping.Load() {
		return nil, net.ErrClosed
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if r.stopping.Load() {
		_ = conn.SetReadDeadline(time.Unix(1, 0))
		return nil, net.ErrClosed
	}
	var length uint16
	if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > maxTCPRequestBytes {
		return nil, fmt.Errorf("dns TCP request exceeds %d bytes", maxTCPRequestBytes)
	}
	message := make([]byte, int(length))
	if _, err := io.ReadFull(conn, message); err != nil {
		return nil, err
	}
	return message, nil
}

// limitedListener caps retained TCP clients while leaving UDP answers independent.
type limitedListener struct {
	net.Listener
	permits chan struct{}
}

// Accept closes excess connections before they can consume a DNS server goroutine.
func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.permits <- struct{}{}:
			return &limitedConnection{Conn: connection, release: func() { <-l.permits }}, nil
		default:
			_ = connection.Close()
		}
	}
}

// limitedConnection releases capacity exactly once across competing close paths.
type limitedConnection struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

// Close returns the listener permit even when both shutdown and serving code close the connection.
func (c *limitedConnection) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
