package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/network/dnsserver"
	"github.com/goforj/harbor/internal/network/httpingress"
	"github.com/goforj/harbor/internal/network/tcprelay"
)

const (
	defaultStartupTimeout  = 5 * time.Second
	maximumStartupTimeout  = time.Minute
	defaultShutdownTimeout = 10 * time.Second
	maximumShutdownTimeout = 30 * time.Second
	startupPollInterval    = time.Millisecond
)

var (
	// ErrAlreadyStarted reports a second attempt to use one one-shot Runtime.
	ErrAlreadyStarted = errors.New("data plane runtime lifecycle has already started")
	// ErrClosed reports that shutdown interrupted startup or preceded it.
	ErrClosed = errors.New("data plane runtime is closed")
)

// Config defines one immutable generation and its bounded lifecycle policy.
type Config struct {
	// Desired is the complete validated route and listener generation.
	Desired DesiredState
	// CertificateProvider supplies certificates only for already-authorized exact HTTP hosts.
	// It is required whenever Desired owns the paired HTTP and HTTPS listeners.
	CertificateProvider httpingress.CertificateProvider
	// StartupTimeout bounds the interval between listener acquisition and child readiness.
	// Zero selects Harbor's conservative default.
	StartupTimeout time.Duration
	// ShutdownTimeout bounds each child server's graceful drain before it forces closure.
	// Zero selects Harbor's conservative default.
	ShutdownTimeout time.Duration
}

// Runtime owns one process-local generation of Harbor DNS, HTTP ingress, and native relays.
type Runtime struct {
	config                 Config
	listen                 listenTCP
	beforeReadyPublication func()
	afterStopPublication   func()

	dns     *dnsserver.Server
	ingress *httpingress.Server
	relays  []managedRelay

	mutex       sync.RWMutex
	state       State
	run         *runtimeRun
	terminalErr error
	stop        chan struct{}
	done        chan struct{}
	stopOnce    sync.Once
	doneOnce    sync.Once
}

// managedRelay couples stable desired identity with its one-shot relay instance.
type managedRelay struct {
	route NativeRoute
	relay *tcprelay.Relay
}

// runtimeRun contains every resource that must be joined before terminal state is published.
type runtimeRun struct {
	context   context.Context
	cancel    context.CancelFunc
	results   chan childResult
	children  int
	listeners []net.Listener
}

// childResult identifies the exact server that relinquished listener ownership.
type childResult struct {
	name string
	err  error
}

// listenTCP acquires one exact IPv4 loopback TCP listener.
type listenTCP func(context.Context, netip.AddrPort) (net.Listener, error)

// runtimeDependencies keeps socket acquisition and lifecycle publication deterministic in tests.
type runtimeDependencies struct {
	listen                 listenTCP
	beforeReadyPublication func()
	afterStopPublication   func()
}

// NewRuntime validates all dependencies and constructs a one-shot data-plane generation without binding sockets.
func NewRuntime(config Config) (*Runtime, error) {
	return newRuntime(config, runtimeDependencies{listen: listenExactTCP})
}

// newRuntime retains a replaceable listener boundary while keeping production constructors exact.
func newRuntime(config Config, dependencies runtimeDependencies) (*Runtime, error) {
	if err := config.Desired.validate(); err != nil {
		return nil, err
	}
	config = normalizeRuntimeConfig(config)
	if err := validateRuntimeConfig(config); err != nil {
		return nil, err
	}
	if dependencies.listen == nil {
		return nil, fmt.Errorf("create data plane runtime: TCP listener factory is required")
	}

	runtime := &Runtime{
		config:                 config,
		listen:                 dependencies.listen,
		beforeReadyPublication: dependencies.beforeReadyPublication,
		afterStopPublication:   dependencies.afterStopPublication,
		state:                  StateNew,
		stop:                   make(chan struct{}),
		done:                   make(chan struct{}),
		relays:                 make([]managedRelay, 0, len(config.Desired.nativeRoutes)),
	}
	if config.Desired.listeners.DNS != (netip.AddrPort{}) {
		dnsConfig := dnsserver.DefaultConfig(config.Desired.listeners.DNS.Addr(), config.Desired.listeners.DNS.Port())
		dnsConfig.ShutdownTimeout = config.ShutdownTimeout
		server, err := dnsserver.NewServer(dnsConfig, config.Desired.dnsSnapshot)
		if err != nil {
			return nil, fmt.Errorf("create data plane DNS server: %w", err)
		}
		runtime.dns = server
	}
	if config.Desired.listeners.HTTP != (netip.AddrPort{}) {
		router, err := httpingress.NewRouter(httpingress.Config{}, config.Desired.ingressSnapshot)
		if err != nil {
			return nil, fmt.Errorf("create data plane HTTP router: %w", err)
		}
		server, err := httpingress.NewServer(
			httpingress.ServerConfig{ShutdownTimeout: config.ShutdownTimeout},
			router,
			config.CertificateProvider,
		)
		if err != nil {
			return nil, fmt.Errorf("create data plane HTTP ingress: %w", err)
		}
		runtime.ingress = server
	}
	for _, route := range config.Desired.nativeRoutes {
		relay, err := tcprelay.New(tcprelay.Config{Upstream: route.Upstream, ShutdownTimeout: config.ShutdownTimeout})
		if err != nil {
			return nil, fmt.Errorf("create data plane native route %q: %w", route.ID, err)
		}
		runtime.relays = append(runtime.relays, managedRelay{route: route, relay: relay})
	}
	return runtime, nil
}

// Start acquires every exact socket and returns only after every configured child reports listener ownership.
func (runtime *Runtime) Start(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	if err := runtime.beginStart(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		runtime.finishExpectedStart()
		return err
	}
	if runtime.stopRequested() {
		runtime.finishExpectedStart()
		return ErrClosed
	}

	// The Start context owns the complete generation, including cancellation that arrives
	// while a child is still moving from listener acquisition to reported readiness.
	runContext, cancelRun := context.WithCancel(ctx)
	run := &runtimeRun{
		context: runContext,
		cancel:  cancelRun,
		results: make(chan childResult, runtime.childCount()),
	}
	runtime.setRun(run)

	if err := runtime.bindListeners(ctx, run); err != nil {
		return runtime.failBeforeChildren(run, err)
	}
	runtime.startRoutedChildren(run)
	first, consumed, err := runtime.awaitRoutedReady(ctx, run)
	if err != nil {
		return runtime.rollbackStarted(run, first, consumed, err)
	}
	if err := startupInterruption(ctx, runtime.stop); err != nil {
		return runtime.rollbackStarted(run, childResult{}, false, err)
	}
	if err := runtime.startDNS(run); err != nil {
		return runtime.rollbackStarted(run, childResult{}, false, err)
	}
	first, consumed, err = runtime.awaitReady(ctx, run)
	if err != nil {
		return runtime.rollbackStarted(run, first, consumed, err)
	}
	if err := startupInterruption(ctx, runtime.stop); err != nil {
		return runtime.rollbackStarted(run, childResult{}, false, err)
	}
	if runtime.beforeReadyPublication != nil {
		runtime.beforeReadyPublication()
	}
	if err := runtime.claimReady(ctx); err != nil {
		return runtime.rollbackStarted(run, childResult{}, false, err)
	}
	go runtime.monitor(ctx, run)
	return nil
}

// Snapshot returns one defensive, payload-free observation without blocking active traffic.
func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mutex.RLock()
	state := runtime.state
	run := runtime.run
	runtime.mutex.RUnlock()

	snapshot := Snapshot{
		State:  state,
		Relays: make([]RelayStatus, 0, len(runtime.relays)),
	}
	if runtime.dns != nil {
		address := runtime.config.Desired.listeners.DNS
		runningAddress, running := runtime.dns.Address()
		if running {
			address = runningAddress
		}
		snapshot.DNS = DNSStatus{
			Configured: true,
			Address:    address,
			Running:    running,
			Records:    len(runtime.config.Desired.dnsSnapshot.Records()),
		}
	}
	if runtime.ingress != nil {
		server := runtime.ingress.Snapshot()
		httpAddress := runtime.config.Desired.listeners.HTTP
		httpsAddress := runtime.config.Desired.listeners.HTTPS
		if server.HTTPAddress.IsValid() {
			httpAddress = server.HTTPAddress
		}
		if server.HTTPSAddress.IsValid() {
			httpsAddress = server.HTTPSAddress
		}
		snapshot.Ingress = IngressStatus{
			Configured:   true,
			HTTPAddress:  httpAddress,
			HTTPSAddress: httpsAddress,
			Running:      server.Running,
			Routes:       len(runtime.config.Desired.httpRoutes),
		}
	}
	for _, managed := range runtime.relays {
		relay := managed.relay.Snapshot()
		listenAddress := managed.route.Listen
		if relay.ListenAddress.IsValid() {
			listenAddress = relay.ListenAddress
		}
		snapshot.Relays = append(snapshot.Relays, RelayStatus{
			ID:                   managed.route.ID,
			Host:                 managed.route.Host,
			ListenAddress:        listenAddress,
			Upstream:             relay.Upstream,
			Running:              relay.Running,
			ActiveConnections:    relay.ActiveConnections,
			AcceptedConnections:  relay.AcceptedConnections,
			CompletedConnections: relay.CompletedConnections,
			DialFailures:         relay.DialFailures,
			ClientBytes:          relay.ClientBytes,
			UpstreamBytes:        relay.UpstreamBytes,
			DroppedDiagnostics:   relay.DroppedDiagnostics,
		})
	}
	if snapshot.State == StateReady && !snapshot.configuredChildrenRunning() {
		if runtime.stopRequested() || run != nil && run.context.Err() != nil {
			snapshot.State = StateStopping
		} else {
			snapshot.State = StateFailed
		}
	}
	return snapshot
}

// Done closes after every configured child relinquishes ownership and terminal state is published.
func (runtime *Runtime) Done() <-chan struct{} {
	return runtime.done
}

// Err returns the terminal startup, child, or shutdown failure when one exists.
func (runtime *Runtime) Err() error {
	runtime.mutex.RLock()
	defer runtime.mutex.RUnlock()
	return runtime.terminalErr
}

// Close requests idempotent shutdown and waits until all children join or the caller cancels.
func (runtime *Runtime) Close(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	completed, run := runtime.claimClose()
	if run != nil {
		run.cancel()
	}
	if completed {
		runtime.closeDone()
		return nil
	}

	select {
	case <-runtime.done:
		return runtime.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// claimClose publishes shutdown state and intent as one lifecycle boundary before cancellation can block.
func (runtime *Runtime) claimClose() (bool, *runtimeRun) {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()

	completed := runtime.state == StateNew
	if completed {
		runtime.state = StateStopped
	} else if runtime.state == StateStarting || runtime.state == StateReady {
		runtime.state = StateStopping
	}
	published := false
	runtime.stopOnce.Do(func() {
		close(runtime.stop)
		published = true
	})
	if published && runtime.afterStopPublication != nil {
		runtime.afterStopPublication()
	}
	return completed, runtime.run
}

// beginStart claims the one-shot lifecycle before any socket acquisition begins.
func (runtime *Runtime) beginStart() error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.state != StateNew {
		if runtime.state == StateStopped && runtime.stopRequested() {
			return ErrClosed
		}
		return ErrAlreadyStarted
	}
	runtime.state = StateStarting
	return nil
}

// setRun publishes cancellation ownership so concurrent diagnostics can identify active startup.
func (runtime *Runtime) setRun(run *runtimeRun) {
	runtime.mutex.Lock()
	runtime.run = run
	runtime.mutex.Unlock()
}

// claimReady linearizes successful startup against shutdown before publishing availability.
func (runtime *Runtime) claimReady(ctx context.Context) error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.state != StateStarting || runtime.stopRequested() {
		return ErrClosed
	}
	runtime.state = StateReady
	return nil
}

// childCount returns the exact number of terminal results the runtime must join.
func (runtime *Runtime) childCount() int {
	count := len(runtime.relays)
	if runtime.dns != nil {
		count++
	}
	if runtime.ingress != nil {
		count++
	}
	return count
}

// bindListeners reserves every HTTP and native TCP socket before DNS publishes any route.
func (runtime *Runtime) bindListeners(ctx context.Context, run *runtimeRun) error {
	bindings := make([]netip.AddrPort, 0, 2+len(runtime.relays))
	if runtime.ingress != nil {
		bindings = append(bindings, runtime.config.Desired.listeners.HTTP, runtime.config.Desired.listeners.HTTPS)
	}
	for _, managed := range runtime.relays {
		bindings = append(bindings, managed.route.Listen)
	}
	for _, endpoint := range bindings {
		if err := startupInterruption(ctx, runtime.stop); err != nil {
			return err
		}
		listener, err := runtime.listen(run.context, endpoint)
		if err != nil {
			if interruption := startupInterruption(ctx, runtime.stop); interruption != nil {
				return interruption
			}
			return fmt.Errorf("bind data plane listener %s: %w", endpoint, err)
		}
		if listener == nil {
			return fmt.Errorf("bind data plane listener %s: listener factory returned nil", endpoint)
		}
		actual, err := exactListenerAddress(listener)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("bind data plane listener %s: %w", endpoint, err)
		}
		if actual != endpoint {
			_ = listener.Close()
			return fmt.Errorf("bind data plane listener %s: acquired unexpected socket %s", endpoint, actual)
		}
		run.listeners = append(run.listeners, listener)
	}
	return nil
}

// startRoutedChildren transfers every pre-bound listener before any public name is published.
func (runtime *Runtime) startRoutedChildren(run *runtimeRun) {
	listenerIndex := 0
	if runtime.ingress != nil {
		httpListener := run.listeners[listenerIndex]
		httpsListener := run.listeners[listenerIndex+1]
		listenerIndex += 2
		run.children++
		go func() {
			run.results <- childResult{name: "HTTP ingress", err: runtime.ingress.Serve(run.context, httpListener, httpsListener)}
		}()
	}
	for _, managed := range runtime.relays {
		managed := managed
		listener := run.listeners[listenerIndex]
		listenerIndex++
		run.children++
		go func() {
			run.results <- childResult{
				name: "native route " + managed.route.ID,
				err:  managed.relay.Serve(run.context, listener),
			}
		}()
	}
}

// startDNS publishes names only after every route destination has reported listener ownership.
func (runtime *Runtime) startDNS(run *runtimeRun) error {
	if runtime.dns == nil {
		return nil
	}
	if _, err := runtime.dns.Start(run.context); err != nil {
		return fmt.Errorf("start data plane DNS server: %w", err)
	}
	run.children++
	go func() {
		run.results <- childResult{name: "DNS", err: runtime.dns.Wait(context.Background())}
	}()
	return nil
}

// awaitRoutedReady proves every published HTTP or native destination is already serving.
func (runtime *Runtime) awaitRoutedReady(ctx context.Context, run *runtimeRun) (childResult, bool, error) {
	return runtime.awaitReadiness(ctx, run, runtime.routedChildrenRunning)
}

// awaitReady requires all configured children to publish listener ownership before Start succeeds.
func (runtime *Runtime) awaitReady(ctx context.Context, run *runtimeRun) (childResult, bool, error) {
	return runtime.awaitReadiness(ctx, run, runtime.childrenRunning)
}

// awaitReadiness observes child state and terminal results under one bounded startup policy.
func (runtime *Runtime) awaitReadiness(ctx context.Context, run *runtimeRun, ready func() bool) (childResult, bool, error) {
	deadline := time.NewTimer(runtime.config.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupPollInterval)
	defer ticker.Stop()

	for {
		if ready() {
			select {
			case result := <-run.results:
				return result, true, unexpectedChildError(result)
			default:
				return childResult{}, false, nil
			}
		}
		select {
		case result := <-run.results:
			return result, true, unexpectedChildError(result)
		case <-ctx.Done():
			return childResult{}, false, ctx.Err()
		case <-runtime.stop:
			return childResult{}, false, ErrClosed
		case <-deadline.C:
			return childResult{}, false, fmt.Errorf("start data plane: children did not become ready within %s", runtime.config.StartupTimeout)
		case <-ticker.C:
		}
	}
}

// routedChildrenRunning excludes DNS so resolvable names can never lead route readiness.
func (runtime *Runtime) routedChildrenRunning() bool {
	if runtime.ingress != nil && !runtime.ingress.Snapshot().Running {
		return false
	}
	for _, managed := range runtime.relays {
		if !managed.relay.Snapshot().Running {
			return false
		}
	}
	return true
}

// childrenRunning treats an empty generation as immediately ready.
func (runtime *Runtime) childrenRunning() bool {
	if runtime.dns != nil {
		if _, running := runtime.dns.Address(); !running {
			return false
		}
	}
	return runtime.routedChildrenRunning()
}

// monitor turns the first unexpected child exit into one daemon-visible terminal failure.
func (runtime *Runtime) monitor(parent context.Context, run *runtimeRun) {
	var first childResult
	consumed := false
	intentional := false
	select {
	case <-parent.Done():
		intentional = true
	case <-runtime.stop:
		intentional = true
	case first = <-run.results:
		consumed = true
		if parent.Err() != nil || runtime.stopRequested() {
			intentional = true
		}
	}

	runtime.mutex.Lock()
	if intentional {
		runtime.state = StateStopping
	} else {
		runtime.state = StateFailed
	}
	runtime.mutex.Unlock()
	run.cancel()

	terminal := collectChildResults(run, first, consumed, intentional)
	if !intentional && consumed {
		terminal = errors.Join(unexpectedChildError(first), terminal)
	}
	runtime.finish(terminal, intentional)
}

// collectChildResults joins exactly one terminal result from every configured child.
func collectChildResults(run *runtimeRun, first childResult, consumed bool, intentional bool) error {
	received := 0
	var result error
	if consumed {
		received++
		if intentional && first.err != nil {
			result = errors.Join(result, fmt.Errorf("stop %s: %w", first.name, first.err))
		}
	}
	for received < run.children {
		child := <-run.results
		received++
		if child.err != nil {
			result = errors.Join(result, fmt.Errorf("stop %s: %w", child.name, child.err))
		}
	}
	return result
}

// unexpectedChildError gives nil and non-nil serving-loop exits the same fatal ownership meaning.
func unexpectedChildError(result childResult) error {
	if result.err == nil {
		return fmt.Errorf("data plane %s stopped unexpectedly", result.name)
	}
	return fmt.Errorf("data plane %s failed: %w", result.name, result.err)
}

// failBeforeChildren rolls back listeners when no component serving loop has started.
func (runtime *Runtime) failBeforeChildren(run *runtimeRun, cause error) error {
	run.cancel()
	closeListeners(run.listeners)
	if expectedStartupInterruption(cause) {
		runtime.finish(nil, true)
		return cause
	}
	runtime.finish(cause, false)
	return cause
}

// rollbackStarted joins every launched child before reporting a failed or interrupted startup.
func (runtime *Runtime) rollbackStarted(run *runtimeRun, first childResult, consumed bool, cause error) error {
	run.cancel()
	// A consumed startup result is already represented by cause; only later child failures
	// belong to cleanup, otherwise the same operational error appears twice to callers.
	cleanup := collectChildResults(run, first, consumed, false)
	if expectedStartupInterruption(cause) {
		runtime.finish(cleanup, cleanup == nil)
		return errors.Join(cause, cleanup)
	}
	terminal := errors.Join(cause, cleanup)
	runtime.finish(terminal, false)
	return terminal
}

// finishExpectedStart publishes a clean terminal state when requested cancellation precedes resources.
func (runtime *Runtime) finishExpectedStart() {
	runtime.finish(nil, true)
}

// finish publishes terminal state only after every owned child and listener has been joined.
func (runtime *Runtime) finish(terminal error, intentional bool) {
	runtime.mutex.Lock()
	runtime.terminalErr = terminal
	if terminal != nil || !intentional {
		runtime.state = StateFailed
	} else {
		runtime.state = StateStopped
	}
	runtime.mutex.Unlock()
	runtime.closeDone()
}

// stopRequested reports shutdown without blocking startup or monitoring paths.
func (runtime *Runtime) stopRequested() bool {
	select {
	case <-runtime.stop:
		return true
	default:
		return false
	}
}

// closeDone publishes terminal completion exactly once across startup and shutdown races.
func (runtime *Runtime) closeDone() {
	runtime.doneOnce.Do(func() {
		close(runtime.done)
	})
}

// startupInterruption gives parent cancellation precedence over a concurrent explicit close.
func startupInterruption(ctx context.Context, stop <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-stop:
		return ErrClosed
	default:
		return nil
	}
}

// expectedStartupInterruption distinguishes requested teardown from operational startup failure.
func expectedStartupInterruption(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClosed)
}

// normalizeRuntimeConfig supplies bounded lifecycle defaults without weakening explicit values.
func normalizeRuntimeConfig(config Config) Config {
	if config.StartupTimeout == 0 {
		config.StartupTimeout = defaultStartupTimeout
	}
	if config.ShutdownTimeout == 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	return config
}

// validateRuntimeConfig rejects policies that could spin startup or indefinitely retain listeners.
func validateRuntimeConfig(config Config) error {
	if config.StartupTimeout < time.Millisecond || config.StartupTimeout > maximumStartupTimeout {
		return fmt.Errorf("data plane startup timeout must be between 1ms and %s", maximumStartupTimeout)
	}
	if config.ShutdownTimeout < time.Millisecond || config.ShutdownTimeout > maximumShutdownTimeout {
		return fmt.Errorf("data plane shutdown timeout must be between 1ms and %s", maximumShutdownTimeout)
	}
	if config.Desired.listeners.HTTP != (netip.AddrPort{}) && config.CertificateProvider == nil {
		return fmt.Errorf("data plane certificate provider is required for HTTP ingress")
	}
	return nil
}

// listenExactTCP binds only the explicit IPv4 loopback socket selected by desired state.
func listenExactTCP(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp4", endpoint.String())
	if err != nil {
		return nil, err
	}
	actual, err := exactListenerAddress(listener)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if actual != endpoint {
		_ = listener.Close()
		return nil, fmt.Errorf("listener acquired %s instead of %s", actual, endpoint)
	}
	return listener, nil
}

// exactListenerAddress proves a listener retained the requested TCP4 loopback identity.
func exactListenerAddress(listener net.Listener) (netip.AddrPort, error) {
	if listener.Addr() == nil || listener.Addr().Network() != "tcp" {
		return netip.AddrPort{}, fmt.Errorf("listener must be TCP")
	}
	endpoint, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("parse listener address %q: %w", listener.Addr(), err)
	}
	endpoint = canonicalAddressPort(endpoint)
	if err := validateLoopbackEndpoint("data plane acquired listener", endpoint); err != nil {
		return netip.AddrPort{}, err
	}
	return endpoint, nil
}

// closeListeners releases every socket still owned by startup or a terminating child.
func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		if listener != nil {
			_ = listener.Close()
		}
	}
}

// normalizeContext keeps public lifecycle calls usable without weakening required dependencies.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
