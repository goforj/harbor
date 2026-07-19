package ingressrelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/network/tcprelay"
)

const firstUnprivilegedPort = 1024

var localhost = netip.MustParseAddr("127.0.0.1")

// Config contains the immutable private destinations for one paired relay generation.
type Config struct {
	// HTTPUpstream is harbord's private high-port HTTP listener.
	HTTPUpstream netip.AddrPort
	// HTTPSUpstream is harbord's private high-port HTTPS listener.
	HTTPSUpstream netip.AddrPort
	// ShutdownTimeout bounds how long established streams may drain after either relay stops.
	ShutdownTimeout time.Duration
}

// Listeners contains the two low-port sockets acquired together by the platform adapter.
type Listeners struct {
	// HTTP is the exact 127.0.0.1:80 listener.
	HTTP net.Listener
	// HTTPS is the exact 127.0.0.1:443 listener.
	HTTPS net.Listener
}

// Snapshot is a payload-free observation of the paired relay lifetime.
type Snapshot struct {
	// Running is true only while both protocol relays are running.
	Running bool
	// HTTP contains the raw HTTP relay counters and fixed route.
	HTTP tcprelay.Snapshot
	// HTTPS contains the raw HTTPS relay counters and fixed route.
	HTTPS tcprelay.Snapshot
}

// runtimeState prevents listener authority from being reused across Serve calls.
type runtimeState uint8

const (
	runtimeStateNew runtimeState = iota
	runtimeStateRunning
	runtimeStateStopped
)

// Runtime owns one indivisible HTTP and HTTPS relay lifecycle.
type Runtime struct {
	http  *tcprelay.Relay
	https *tcprelay.Relay

	mutex sync.RWMutex
	state runtimeState
}

// relayResult identifies which sibling ended and preserves its terminal error.
type relayResult struct {
	name string
	err  error
}

// relayFactory keeps the paired construction rollback-free and deterministic in tests.
type relayFactory func(tcprelay.Config) (*tcprelay.Relay, error)

// gatedListener prevents either protocol from accepting before its sibling owns a listener.
type gatedListener struct {
	net.Listener
	gate <-chan struct{}
}

// New validates the paired route and constructs two bounded raw TCP relays.
func New(config Config) (*Runtime, error) {
	return newRuntime(config, tcprelay.New)
}

// newRuntime isolates raw relay construction failures without broadening the production API.
func newRuntime(config Config, create relayFactory) (*Runtime, error) {
	if err := validateUpstream("HTTP", config.HTTPUpstream); err != nil {
		return nil, err
	}
	if err := validateUpstream("HTTPS", config.HTTPSUpstream); err != nil {
		return nil, err
	}
	if config.HTTPUpstream == config.HTTPSUpstream {
		return nil, errors.New("ingress relay HTTP and HTTPS upstreams must be distinct")
	}

	httpRelay, err := create(tcprelay.Config{
		Upstream:        config.HTTPUpstream,
		ShutdownTimeout: config.ShutdownTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create ingress HTTP relay: %w", err)
	}
	httpsRelay, err := create(tcprelay.Config{
		Upstream:        config.HTTPSUpstream,
		ShutdownTimeout: config.ShutdownTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create ingress HTTPS relay: %w", err)
	}
	return &Runtime{http: httpRelay, https: httpsRelay}, nil
}

// Serve takes ownership of both listeners and runs them as one failure domain.
func (runtime *Runtime) Serve(ctx context.Context, listeners Listeners) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := runtime.begin(); err != nil {
		return errors.Join(err, closeListeners(listeners))
	}
	defer runtime.finish()

	if err := validateListeners(listeners); err != nil {
		return errors.Join(err, closeListeners(listeners))
	}

	serveContext, cancel := context.WithCancel(ctx)
	defer cancel()
	gate := make(chan struct{})
	openGate := sync.OnceFunc(func() { close(gate) })
	results := make(chan relayResult, 2)
	go serveRelay(serveContext, runtime.http, "HTTP", &gatedListener{Listener: listeners.HTTP, gate: gate}, results)
	go serveRelay(serveContext, runtime.https, "HTTPS", &gatedListener{Listener: listeners.HTTPS, gate: gate}, results)

	first, bothRunning, receivedFirst := runtime.waitForPair(ctx, results)
	if bothRunning {
		openGate()
		first = <-results
		receivedFirst = true
	}
	parentStopped := ctx.Err() != nil
	cancel()
	openGate()
	if !receivedFirst {
		first = <-results
	}
	second := <-results

	return pairedResult(parentStopped, first, second)
}

// Snapshot reports both relay observations without exposing application traffic.
func (runtime *Runtime) Snapshot() Snapshot {
	httpSnapshot := runtime.http.Snapshot()
	httpsSnapshot := runtime.https.Snapshot()
	runtime.mutex.RLock()
	running := runtime.state == runtimeStateRunning && httpSnapshot.Running && httpsSnapshot.Running
	runtime.mutex.RUnlock()
	return Snapshot{Running: running, HTTP: httpSnapshot, HTTPS: httpsSnapshot}
}

// begin claims this runtime before either listener can be inspected or closed.
func (runtime *Runtime) begin() error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.state != runtimeStateNew {
		return errors.New("serve ingress relay: runtime lifecycle has already started")
	}
	runtime.state = runtimeStateRunning
	return nil
}

// finish publishes terminal state only after both relay goroutines have joined.
func (runtime *Runtime) finish() {
	runtime.mutex.Lock()
	runtime.state = runtimeStateStopped
	runtime.mutex.Unlock()
}

// waitForPair holds both Accept calls behind the gate until both raw relays report running.
func (runtime *Runtime) waitForPair(ctx context.Context, results <-chan relayResult) (relayResult, bool, bool) {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if snapshot := runtime.Snapshot(); snapshot.HTTP.Running && snapshot.HTTPS.Running {
			return relayResult{}, true, false
		}
		select {
		case result := <-results:
			return result, false, true
		case <-ctx.Done():
			return relayResult{}, false, false
		case <-ticker.C:
		}
	}
}

// serveRelay preserves the protocol label while handing listener ownership to the raw relay.
func serveRelay(
	ctx context.Context,
	relay *tcprelay.Relay,
	name string,
	listener net.Listener,
	results chan<- relayResult,
) {
	results <- relayResult{name: name, err: relay.Serve(ctx, listener)}
}

// pairedResult treats a one-sided terminal result as fatal while keeping caller shutdown quiet.
func pairedResult(parentStopped bool, first relayResult, second relayResult) error {
	var result error
	for _, terminal := range []relayResult{first, second} {
		if terminal.err != nil {
			result = errors.Join(result, fmt.Errorf("serve ingress %s relay: %w", terminal.name, terminal.err))
		}
	}
	if result != nil {
		return result
	}
	if parentStopped {
		return nil
	}
	name := first.name
	if name == "" {
		name = second.name
	}
	return fmt.Errorf("serve ingress relay: %s relay stopped unexpectedly", name)
}

// validateListeners requires the exact launchd-owned public sockets before either relay starts.
func validateListeners(listeners Listeners) error {
	if listeners.HTTP == nil {
		return errors.New("ingress HTTP listener is required")
	}
	if listeners.HTTPS == nil {
		return errors.New("ingress HTTPS listener is required")
	}
	checks := []struct {
		name     string
		listener net.Listener
		want     netip.AddrPort
	}{
		{name: "HTTP", listener: listeners.HTTP, want: netip.AddrPortFrom(localhost, 80)},
		{name: "HTTPS", listener: listeners.HTTPS, want: netip.AddrPortFrom(localhost, 443)},
	}
	for _, check := range checks {
		address, err := listenerAddress(check.listener)
		if err != nil {
			return fmt.Errorf("validate ingress %s listener: %w", check.name, err)
		}
		if address != check.want {
			return fmt.Errorf("ingress %s listener is %s, want exactly %s", check.name, address, check.want)
		}
	}
	return nil
}

// listenerAddress parses one listener without accepting hostnames or mapped address aliases.
func listenerAddress(listener net.Listener) (netip.AddrPort, error) {
	native := listener.Addr()
	if native == nil {
		return netip.AddrPort{}, errors.New("listener has no address")
	}
	address, err := netip.ParseAddrPort(native.String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("listener address %q is not an IP socket", native)
	}
	if !address.IsValid() || !address.Addr().Is4() || !address.Addr().IsLoopback() ||
		address.Addr() != address.Addr().Unmap() || address.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("listener address %s is not canonical IPv4 loopback", address)
	}
	return address, nil
}

// validateUpstream confines the root-controlled relay destination to one private high socket.
func validateUpstream(name string, upstream netip.AddrPort) error {
	if !upstream.IsValid() || upstream.Addr() != upstream.Addr().Unmap() || upstream.Addr() != localhost {
		return fmt.Errorf("ingress %s upstream must use 127.0.0.1", name)
	}
	if upstream.Port() < firstUnprivilegedPort {
		return fmt.Errorf("ingress %s upstream port must be at least %d", name, firstUnprivilegedPort)
	}
	return nil
}

// closeListeners releases every supplied capability after pre-start rejection.
func closeListeners(listeners Listeners) error {
	var result error
	if listeners.HTTP != nil {
		result = errors.Join(result, listeners.HTTP.Close())
	}
	if listeners.HTTPS != nil {
		result = errors.Join(result, listeners.HTTPS.Close())
	}
	return result
}

// Accept waits for paired startup before exposing the underlying socket.
func (listener *gatedListener) Accept() (net.Conn, error) {
	<-listener.gate
	return listener.Listener.Accept()
}
