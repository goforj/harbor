package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
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
	// ErrNotReady reports that a route replacement reached a runtime outside its ready lifecycle.
	ErrNotReady = errors.New("data plane runtime is not ready")
)

// Config defines one initial generation and its bounded lifecycle policy.
type Config struct {
	// Desired is the initial complete validated route and listener generation.
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
	config                             Config
	listen                             listenTCP
	beforeReadyPublication             func()
	afterStopPublication               func()
	beforeIngressActivationPublication func(*httpingress.Server)

	dns            *dnsserver.Server
	ingress        *httpingress.Server
	replaceDNS     func(dnsserver.Snapshot) error
	replaceIngress func(*httpingress.Snapshot) error
	relays         []managedRelay

	mutex              sync.RWMutex
	state              State
	desired            DesiredState
	run                *runtimeRun
	terminalErr        error
	stop               chan struct{}
	done               chan struct{}
	stopOnce           sync.Once
	doneOnce           sync.Once
	nativeReplaceMutex sync.Mutex
	ingressActivation  chan struct{}
	dynamicMutex       sync.Mutex
	dynamicRelays      map[string]*dynamicNativeRelay
}

// managedRelay couples stable desired identity with its one-shot relay instance.
type managedRelay struct {
	route NativeRoute
	relay *tcprelay.Relay
}

// dynamicNativeRelay owns one managed publication that was admitted after the shared runtime started.
type dynamicNativeRelay struct {
	route    NativeRoute
	relay    *tcprelay.Relay
	cancel   context.CancelFunc
	done     chan struct{}
	mutex    sync.Mutex
	err      error
	stopping bool
	admitted bool
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
	listen                             listenTCP
	beforeReadyPublication             func()
	afterStopPublication               func()
	beforeIngressActivationPublication func(*httpingress.Server)
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
		config:                             config,
		listen:                             dependencies.listen,
		beforeReadyPublication:             dependencies.beforeReadyPublication,
		afterStopPublication:               dependencies.afterStopPublication,
		beforeIngressActivationPublication: dependencies.beforeIngressActivationPublication,
		state:                              StateNew,
		desired:                            config.Desired,
		stop:                               make(chan struct{}),
		done:                               make(chan struct{}),
		relays:                             make([]managedRelay, 0, len(config.Desired.nativeRoutes)),
		dynamicRelays:                      make(map[string]*dynamicNativeRelay),
	}
	if config.Desired.listeners.DNS != (netip.AddrPort{}) {
		dnsConfig := dnsserver.DefaultConfig(config.Desired.listeners.DNS.Addr(), config.Desired.listeners.DNS.Port())
		dnsConfig.ShutdownTimeout = config.ShutdownTimeout
		server, err := dnsserver.NewServer(dnsConfig, config.Desired.dnsSnapshot)
		if err != nil {
			return nil, fmt.Errorf("create data plane DNS server: %w", err)
		}
		runtime.dns = server
		runtime.replaceDNS = server.Replace
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
		runtime.replaceIngress = router.Replace
	}
	for _, route := range config.Desired.nativeRoutes {
		if route.Direct {
			continue
		}
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

// ActivateHTTPIngress adds the paired HTTP and HTTPS listeners to one ready DNS-only generation.
//
// The resolver listener and every managed native relay remain live throughout the transition. The
// supplied state describes only the committed shared-listener foundation; routes are reconciled
// separately after the listener pair is ready.
func (runtime *Runtime) ActivateHTTPIngress(ctx context.Context, next DesiredState) error {
	if runtime == nil {
		return ErrNotReady
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return fmt.Errorf("activate data plane HTTP ingress: %w", err)
	}

	runtime.nativeReplaceMutex.Lock()
	defer runtime.nativeReplaceMutex.Unlock()

	current, run, activation, err := runtime.beginHTTPIngressActivation()
	if err != nil {
		return err
	}
	defer runtime.completeHTTPIngressActivation(activation)

	promoted, err := prepareHTTPIngressActivation(current, next)
	if err != nil {
		return err
	}
	if runtime.config.CertificateProvider == nil {
		return errors.New("activate data plane HTTP ingress: certificate provider is required")
	}
	if runtime.dns == nil || runtime.replaceDNS == nil {
		return fmt.Errorf("activate data plane HTTP ingress: %w: DNS is not configured", ErrNotReady)
	}
	if _, running := runtime.dns.Address(); !running {
		return fmt.Errorf("activate data plane HTTP ingress: %w: DNS is not running", ErrNotReady)
	}

	router, err := httpingress.NewRouter(httpingress.Config{}, promoted.ingressSnapshot)
	if err != nil {
		return fmt.Errorf("activate data plane HTTP ingress: construct router: %w", err)
	}
	server, err := httpingress.NewServer(
		httpingress.ServerConfig{ShutdownTimeout: runtime.config.ShutdownTimeout},
		router,
		runtime.config.CertificateProvider,
	)
	if err != nil {
		return fmt.Errorf("activate data plane HTTP ingress: construct server: %w", err)
	}

	httpListener, httpsListener, err := runtime.bindHTTPIngressActivationListeners(ctx, run, promoted.listeners)
	if err != nil {
		return err
	}
	ingressContext, cancelIngress := context.WithCancel(run.context)
	result := make(chan childResult, 1)
	go func() {
		result <- childResult{name: "HTTP ingress", err: server.Serve(ingressContext, httpListener, httpsListener)}
	}()
	if err := runtime.awaitHTTPIngressActivation(ctx, run, server, cancelIngress, result); err != nil {
		return err
	}
	if runtime.beforeIngressActivationPublication != nil {
		runtime.beforeIngressActivationPublication(server)
	}
	if err := runtime.publishHTTPIngressActivation(ctx, run, server, router.Replace, promoted, result); err != nil {
		return errors.Join(err, stopHTTPIngressActivation(cancelIngress, result))
	}
	return nil
}

// beginHTTPIngressActivation claims one transition so terminal publication cannot outrun candidate cleanup.
func (runtime *Runtime) beginHTTPIngressActivation() (DesiredState, *runtimeRun, chan struct{}, error) {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.state != StateReady {
		return DesiredState{}, nil, nil, fmt.Errorf("activate data plane HTTP ingress: %w: lifecycle state is %q", ErrNotReady, runtime.state)
	}
	if runtime.run == nil || runtime.run.context == nil {
		return DesiredState{}, nil, nil, fmt.Errorf("activate data plane HTTP ingress: %w: runtime context is unavailable", ErrNotReady)
	}
	if runtime.ingress != nil || runtime.replaceIngress != nil {
		return DesiredState{}, nil, nil, errors.New("activate data plane HTTP ingress: HTTP ingress is already configured")
	}
	if runtime.ingressActivation != nil {
		return DesiredState{}, nil, nil, errors.New("activate data plane HTTP ingress: another activation is already running")
	}
	activation := make(chan struct{})
	runtime.ingressActivation = activation
	return runtime.desired, runtime.run, activation, nil
}

// completeHTTPIngressActivation releases terminal publication after every candidate socket is published or retired.
func (runtime *Runtime) completeHTTPIngressActivation(activation chan struct{}) {
	runtime.mutex.Lock()
	if runtime.ingressActivation == activation {
		runtime.ingressActivation = nil
		close(activation)
	}
	runtime.mutex.Unlock()
}

// prepareHTTPIngressActivation validates the only mutable listener transition and preserves managed native routes.
func prepareHTTPIngressActivation(current DesiredState, next DesiredState) (DesiredState, error) {
	currentListeners := current.ListenerPlan()
	nextListeners := next.ListenerPlan()
	if currentListeners.DNS == (netip.AddrPort{}) || currentListeners.HTTP != (netip.AddrPort{}) || currentListeners.HTTPS != (netip.AddrPort{}) {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: current generation must own only DNS")
	}
	if nextListeners.DNS != currentListeners.DNS {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: DNS listener must remain unchanged")
	}
	if nextListeners.HTTP == (netip.AddrPort{}) || nextListeners.HTTPS == (netip.AddrPort{}) {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: committed HTTP and HTTPS listeners are required")
	}
	if current.TTL() != next.TTL() {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: DNS TTL must remain unchanged")
	}
	if len(current.HTTPRoutes()) != 0 || len(next.HTTPRoutes()) != 0 {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: routes must remain empty until reconciliation")
	}
	if routes := next.NativeRoutes(); len(routes) != 0 && !slices.Equal(routes, current.NativeRoutes()) {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: committed state cannot replace managed native routes")
	}
	promoted, err := NewDesiredState(nextListeners, nil, current.NativeRoutes(), current.TTL())
	if err != nil {
		return DesiredState{}, fmt.Errorf("activate data plane HTTP ingress: construct promoted generation: %w", err)
	}
	if !sameDNSRecords(current.dnsSnapshot.Records(), promoted.dnsSnapshot.Records()) {
		return DesiredState{}, errors.New("activate data plane HTTP ingress: promotion changed authoritative DNS records")
	}
	return promoted, nil
}

// bindHTTPIngressActivationListeners acquires the exact pair without disturbing the running resolver generation.
func (runtime *Runtime) bindHTTPIngressActivationListeners(
	ctx context.Context,
	run *runtimeRun,
	listeners ListenerPlan,
) (net.Listener, net.Listener, error) {
	startupContext, cancelStartup := context.WithCancel(run.context)
	stopCallerCancellation := context.AfterFunc(ctx, cancelStartup)
	defer cancelStartup()

	bound := make([]net.Listener, 0, 2)
	for _, candidate := range []struct {
		name     string
		endpoint netip.AddrPort
	}{
		{name: "HTTP", endpoint: listeners.HTTP},
		{name: "HTTPS", endpoint: listeners.HTTPS},
	} {
		listener, err := runtime.listen(startupContext, candidate.endpoint)
		if err != nil {
			stopCallerCancellation()
			closeListeners(bound)
			if interruption := httpIngressActivationInterruption(ctx, run.context, runtime.stop); interruption != nil {
				return nil, nil, interruption
			}
			return nil, nil, fmt.Errorf("activate data plane HTTP ingress: bind %s listener %s: %w", candidate.name, candidate.endpoint, err)
		}
		if listener == nil {
			stopCallerCancellation()
			closeListeners(bound)
			return nil, nil, fmt.Errorf("activate data plane HTTP ingress: bind %s listener %s: listener factory returned nil", candidate.name, candidate.endpoint)
		}
		actual, err := exactListenerAddress(listener)
		if err != nil || actual != candidate.endpoint {
			_ = listener.Close()
			stopCallerCancellation()
			closeListeners(bound)
			if err != nil {
				return nil, nil, fmt.Errorf("activate data plane HTTP ingress: verify %s listener %s: %w", candidate.name, candidate.endpoint, err)
			}
			return nil, nil, fmt.Errorf("activate data plane HTTP ingress: %s listener acquired unexpected socket %s", candidate.name, actual)
		}
		bound = append(bound, listener)
	}
	callerStillActive := stopCallerCancellation()
	if !callerStillActive {
		closeListeners(bound)
		return nil, nil, ctx.Err()
	}
	if interruption := httpIngressActivationInterruption(ctx, run.context, runtime.stop); interruption != nil {
		closeListeners(bound)
		return nil, nil, interruption
	}
	return bound[0], bound[1], nil
}

// httpIngressActivationInterruption gives caller cancellation precedence while recognizing runtime teardown.
func httpIngressActivationInterruption(ctx context.Context, runContext context.Context, stop <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runContext.Err(); err != nil {
		return fmt.Errorf("activate data plane HTTP ingress: %w: runtime generation stopped", ErrNotReady)
	}
	select {
	case <-stop:
		return ErrClosed
	default:
		return nil
	}
}

// awaitHTTPIngressActivation keeps candidate failures local until both listeners report ownership.
func (runtime *Runtime) awaitHTTPIngressActivation(
	ctx context.Context,
	run *runtimeRun,
	server *httpingress.Server,
	cancel context.CancelFunc,
	result <-chan childResult,
) error {
	deadline := time.NewTimer(runtime.config.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupPollInterval)
	defer ticker.Stop()
	for {
		if server.Snapshot().Running {
			select {
			case terminal := <-result:
				cancel()
				return unexpectedChildError(terminal)
			default:
				return nil
			}
		}
		select {
		case terminal := <-result:
			cancel()
			return unexpectedChildError(terminal)
		case <-ctx.Done():
			return errors.Join(ctx.Err(), stopHTTPIngressActivation(cancel, result))
		case <-run.context.Done():
			return errors.Join(
				fmt.Errorf("activate data plane HTTP ingress: %w: runtime generation stopped", ErrNotReady),
				stopHTTPIngressActivation(cancel, result),
			)
		case <-runtime.stop:
			return errors.Join(ErrClosed, stopHTTPIngressActivation(cancel, result))
		case <-deadline.C:
			return errors.Join(
				fmt.Errorf("activate data plane HTTP ingress: listeners did not become ready within %s", runtime.config.StartupTimeout),
				stopHTTPIngressActivation(cancel, result),
			)
		case <-ticker.C:
		}
	}
}

// publishHTTPIngressActivation admits one ready pair into the existing runtime monitor and desired state.
func (runtime *Runtime) publishHTTPIngressActivation(
	ctx context.Context,
	run *runtimeRun,
	server *httpingress.Server,
	replace func(*httpingress.Snapshot) error,
	promoted DesiredState,
	result <-chan childResult,
) error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.state != StateReady || runtime.run != run || run.context.Err() != nil || runtime.stopRequested() {
		return fmt.Errorf("activate data plane HTTP ingress: %w: runtime lifecycle changed during activation", ErrNotReady)
	}
	if runtime.ingress != nil || runtime.replaceIngress != nil {
		return errors.New("activate data plane HTTP ingress: HTTP ingress was concurrently configured")
	}
	if !server.Snapshot().Running {
		return errors.New("activate data plane HTTP ingress: candidate stopped before publication")
	}
	run.children++
	runtime.ingress = server
	runtime.replaceIngress = replace
	runtime.desired = promoted
	go func() {
		run.results <- <-result
	}()
	return nil
}

// stopHTTPIngressActivation cancels and joins one unadmitted candidate before returning ownership.
func stopHTTPIngressActivation(cancel context.CancelFunc, result <-chan childResult) error {
	cancel()
	terminal := <-result
	if terminal.err == nil {
		return nil
	}
	return fmt.Errorf("stop HTTP ingress activation candidate: %w", terminal.err)
}

// ReplaceHTTPRoutes publishes one validated HTTP route generation without changing owned listeners or native relays.
func (runtime *Runtime) ReplaceHTTPRoutes(next DesiredState) error {
	if runtime == nil {
		return ErrNotReady
	}

	runtime.nativeReplaceMutex.Lock()
	defer runtime.nativeReplaceMutex.Unlock()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.state != StateReady {
		return fmt.Errorf("%w: lifecycle state is %q", ErrNotReady, runtime.state)
	}
	current := runtime.desired
	if !next.valid {
		if err := next.validate(); err != nil {
			return fmt.Errorf("replace data plane HTTP routes: %w", err)
		}
	}
	if len(next.nativeRoutes) == 0 && len(current.nativeRoutes) != 0 {
		preserved, err := NewDesiredState(next.listeners, next.httpRoutes, current.nativeRoutes, next.ttl)
		if err != nil {
			return fmt.Errorf("replace data plane HTTP routes: preserve managed native routes: %w", err)
		}
		next = preserved
	}
	if err := next.validate(); err != nil {
		return fmt.Errorf("replace data plane HTTP routes: %w", err)
	}
	if current.listeners != next.listeners {
		return fmt.Errorf("replace data plane HTTP routes: listener topology must remain unchanged")
	}
	if !slices.Equal(current.nativeRoutes, next.nativeRoutes) {
		return fmt.Errorf("replace data plane HTTP routes: native route topology must remain unchanged")
	}
	if current.ttl != next.ttl {
		return fmt.Errorf("replace data plane HTTP routes: DNS TTL must remain unchanged")
	}
	if slices.Equal(current.httpRoutes, next.httpRoutes) {
		return nil
	}
	if runtime.ingress == nil || runtime.replaceIngress == nil || !runtime.ingress.Snapshot().Running {
		return fmt.Errorf("%w: HTTP ingress is not running", ErrNotReady)
	}
	if runtime.dns == nil || runtime.replaceDNS == nil {
		return fmt.Errorf("%w: DNS is not configured", ErrNotReady)
	}
	if _, running := runtime.dns.Address(); !running {
		return fmt.Errorf("%w: DNS is not running", ErrNotReady)
	}

	intermediate, err := retainedHTTPDesiredState(current, next)
	if err != nil {
		return fmt.Errorf("replace data plane HTTP routes: construct withdrawal generation: %w", err)
	}
	currentRecords := current.dnsSnapshot.Records()
	intermediateRecords := intermediate.dnsSnapshot.Records()
	nextRecords := next.dnsSnapshot.Records()
	hasRemovals := !sameDNSRecords(currentRecords, intermediateRecords)
	hasAdditions := !sameDNSRecords(intermediateRecords, nextRecords)

	if hasRemovals {
		if err := runtime.replaceDNS(intermediate.dnsSnapshot); err != nil {
			return fmt.Errorf("replace data plane HTTP routes: withdraw DNS records: %w", err)
		}
	}
	if err := runtime.replaceIngress(next.ingressSnapshot); err != nil {
		publicationErr := fmt.Errorf("replace data plane HTTP routes: publish ingress routes: %w", err)
		if !hasRemovals {
			return publicationErr
		}
		if rollbackErr := runtime.replaceDNS(current.dnsSnapshot); rollbackErr != nil {
			return runtime.failHTTPRouteReplacementLocked(errors.Join(
				publicationErr,
				fmt.Errorf("restore DNS after ingress publication failure: %w", rollbackErr),
			))
		}
		return publicationErr
	}
	if hasAdditions {
		if err := runtime.replaceDNS(next.dnsSnapshot); err != nil {
			publicationErr := fmt.Errorf("replace data plane HTTP routes: publish DNS additions: %w", err)
			ingressRollbackErr := runtime.replaceIngress(current.ingressSnapshot)
			if ingressRollbackErr != nil {
				return runtime.failHTTPRouteReplacementLocked(errors.Join(
					publicationErr,
					fmt.Errorf("restore ingress after DNS publication failure: %w", ingressRollbackErr),
				))
			}
			if hasRemovals {
				if dnsRollbackErr := runtime.replaceDNS(current.dnsSnapshot); dnsRollbackErr != nil {
					return runtime.failHTTPRouteReplacementLocked(errors.Join(
						publicationErr,
						fmt.Errorf("restore DNS after DNS publication failure: %w", dnsRollbackErr),
					))
				}
			}
			return publicationErr
		}
	}

	runtime.desired = next
	return nil
}

// ReplaceNativeRoutes replaces managed TCP relay publications while preserving shared DNS and HTTP listeners.
//
// Native listeners are deliberately admitted after the shared generation is ready. Harbor withdraws their DNS
// records before stopping an old relay, then binds and proves every replacement before publishing the new records.
// A failed replacement leaves the data plane failed and route-free rather than claiming a partially known generation.
func (runtime *Runtime) ReplaceNativeRoutes(ctx context.Context, routes []NativeRoute) error {
	if runtime == nil {
		return ErrNotReady
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	runtime.nativeReplaceMutex.Lock()
	defer runtime.nativeReplaceMutex.Unlock()

	runtime.mutex.RLock()
	if runtime.state != StateReady {
		runtime.mutex.RUnlock()
		return fmt.Errorf("%w: lifecycle state is %q", ErrNotReady, runtime.state)
	}
	current := runtime.desired
	run := runtime.run
	staticRelayCount := len(runtime.relays)
	runtime.mutex.RUnlock()
	if staticRelayCount != 0 {
		return fmt.Errorf("replace data plane native routes: initial native topology is immutable")
	}
	if run == nil || run.context == nil {
		return fmt.Errorf("%w: runtime startup context is unavailable", ErrNotReady)
	}
	if err := runtime.validateDynamicRouteOwnership(current.nativeRoutes); err != nil {
		return err
	}
	next, err := NewDesiredState(current.listeners, current.httpRoutes, routes, current.ttl)
	if err != nil {
		return fmt.Errorf("replace data plane native routes: construct desired state: %w", err)
	}
	if slices.Equal(current.nativeRoutes, next.nativeRoutes) {
		return nil
	}
	if runtime.dns == nil || runtime.replaceDNS == nil {
		return fmt.Errorf("%w: DNS is not configured", ErrNotReady)
	}
	if _, running := runtime.dns.Address(); !running {
		return fmt.Errorf("%w: DNS is not running", ErrNotReady)
	}
	intermediate, err := NewDesiredState(current.listeners, current.httpRoutes, nil, current.ttl)
	if err != nil {
		return fmt.Errorf("replace data plane native routes: construct withdrawal generation: %w", err)
	}
	if err := runtime.replaceDNS(intermediate.dnsSnapshot); err != nil {
		return fmt.Errorf("replace data plane native routes: withdraw DNS records: %w", err)
	}
	if err := runtime.stopDynamicRoutes(ctx); err != nil {
		cause := fmt.Errorf("replace data plane native routes: stop prior relays: %w", err)
		runtime.failNativeRouteReplacement(cause)
		return cause
	}
	runtime.mutex.Lock()
	runtime.desired = intermediate
	runtime.mutex.Unlock()

	for _, route := range next.nativeRoutes {
		if route.Direct {
			continue
		}
		entry, startErr := runtime.startDynamicNativeRelay(run.context, route)
		if startErr != nil {
			cleanupErr := runtime.stopDynamicRoutes(context.Background())
			cause := fmt.Errorf("replace data plane native routes: start relay %q: %w", route.ID, startErr)
			runtime.failNativeRouteReplacement(errors.Join(cause, cleanupErr))
			return errors.Join(cause, cleanupErr)
		}
		runtime.admitDynamicNativeRelay(entry)
	}
	if err := runtime.replaceDNS(next.dnsSnapshot); err != nil {
		cleanupErr := runtime.stopDynamicRoutes(context.Background())
		cause := fmt.Errorf("replace data plane native routes: publish DNS records: %w", err)
		runtime.failNativeRouteReplacement(errors.Join(cause, cleanupErr))
		return errors.Join(cause, cleanupErr)
	}
	runtime.mutex.Lock()
	runtime.desired = next
	runtime.mutex.Unlock()
	return nil
}

// validateDynamicRouteOwnership proves the current native routes are all managed entries rather than immutable startup relays.
func (runtime *Runtime) validateDynamicRouteOwnership(routes []NativeRoute) error {
	runtime.dynamicMutex.Lock()
	defer runtime.dynamicMutex.Unlock()
	relayCount := 0
	for _, route := range routes {
		if !route.Direct {
			relayCount++
		}
	}
	if relayCount != len(runtime.dynamicRelays) {
		return fmt.Errorf("replace data plane native routes: dynamic relay ownership does not match the current topology")
	}
	for _, route := range routes {
		if route.Direct {
			continue
		}
		entry, found := runtime.dynamicRelays[route.ID]
		if !found || entry.route != route {
			return fmt.Errorf("replace data plane native routes: relay %q ownership is not provable", route.ID)
		}
	}
	return nil
}

// startDynamicNativeRelay binds and proves one managed relay before it can enter the replacement set.
func (runtime *Runtime) startDynamicNativeRelay(ctx context.Context, route NativeRoute) (*dynamicNativeRelay, error) {
	relay, err := tcprelay.New(tcprelay.Config{Upstream: route.Upstream, ShutdownTimeout: runtime.config.ShutdownTimeout})
	if err != nil {
		return nil, fmt.Errorf("construct relay: %w", err)
	}
	listener, err := runtime.listen(ctx, route.Listen)
	if err != nil {
		return nil, fmt.Errorf("bind listener %s: %w", route.Listen, err)
	}
	actual, err := exactListenerAddress(listener)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("verify listener %s: %w", route.Listen, err)
	}
	if actual != route.Listen {
		_ = listener.Close()
		return nil, fmt.Errorf("bind listener %s: acquired unexpected socket %s", route.Listen, actual)
	}
	serveContext, cancel := context.WithCancel(ctx)
	entry := &dynamicNativeRelay{route: route, relay: relay, cancel: cancel, done: make(chan struct{})}
	go func() {
		err := relay.Serve(serveContext, listener)
		entry.mutex.Lock()
		entry.err = err
		close(entry.done)
		entry.mutex.Unlock()
		runtime.handleDynamicRelayExit(entry)
	}()
	deadline := time.NewTimer(runtime.config.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupPollInterval)
	defer ticker.Stop()
	for {
		if relay.Snapshot().Running {
			return entry, nil
		}
		select {
		case <-entry.done:
			return nil, fmt.Errorf("relay exited before readiness: %w", dynamicNativeRelayError(entry))
		case <-ctx.Done():
			entry.mutex.Lock()
			entry.stopping = true
			entry.mutex.Unlock()
			cancel()
			<-entry.done
			return nil, ctx.Err()
		case <-deadline.C:
			entry.mutex.Lock()
			entry.stopping = true
			entry.mutex.Unlock()
			cancel()
			<-entry.done
			return nil, fmt.Errorf("relay did not become ready within %s", runtime.config.StartupTimeout)
		case <-ticker.C:
		}
	}
}

// admitDynamicNativeRelay publishes one ready relay into the process-local replacement set.
func (runtime *Runtime) admitDynamicNativeRelay(entry *dynamicNativeRelay) {
	entry.mutex.Lock()
	entry.admitted = true
	entry.mutex.Unlock()
	runtime.dynamicMutex.Lock()
	runtime.dynamicRelays[entry.route.ID] = entry
	runtime.dynamicMutex.Unlock()
}

// stopDynamicRoutes cancels every dynamic relay and waits for listener and connection ownership to settle.
func (runtime *Runtime) stopDynamicRoutes(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	runtime.dynamicMutex.Lock()
	entries := make([]*dynamicNativeRelay, 0, len(runtime.dynamicRelays))
	for _, entry := range runtime.dynamicRelays {
		entry.mutex.Lock()
		entry.stopping = true
		entry.mutex.Unlock()
		entries = append(entries, entry)
	}
	runtime.dynamicMutex.Unlock()
	for _, entry := range entries {
		entry.cancel()
	}
	var result error
	for _, entry := range entries {
		select {
		case <-entry.done:
			runtime.dynamicMutex.Lock()
			if current := runtime.dynamicRelays[entry.route.ID]; current == entry {
				delete(runtime.dynamicRelays, entry.route.ID)
			}
			runtime.dynamicMutex.Unlock()
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
		}
	}
	return result
}

// dynamicNativeRelayError returns the terminal relay result without exposing its mutable lifecycle state.
func dynamicNativeRelayError(entry *dynamicNativeRelay) error {
	entry.mutex.Lock()
	defer entry.mutex.Unlock()
	if entry.err == nil {
		return errors.New("relay stopped unexpectedly")
	}
	return entry.err
}

// handleDynamicRelayExit fails the shared runtime when an admitted relay loses listener ownership unexpectedly.
func (runtime *Runtime) handleDynamicRelayExit(entry *dynamicNativeRelay) {
	entry.mutex.Lock()
	stopping := entry.stopping
	admitted := entry.admitted
	entry.mutex.Unlock()
	if stopping || !admitted || runtime.stopRequested() {
		return
	}
	runtime.mutex.Lock()
	if runtime.state != StateReady {
		runtime.mutex.Unlock()
		return
	}
	if runtime.terminalErr == nil {
		runtime.terminalErr = fmt.Errorf("data plane native route %s failed: %w", entry.route.ID, dynamicNativeRelayError(entry))
	}
	run := runtime.run
	runtime.state = StateFailed
	runtime.mutex.Unlock()
	if run != nil {
		run.cancel()
	}
}

// failNativeRouteReplacement fails closed after a route update has withdrawn the previous DNS generation.
func (runtime *Runtime) failNativeRouteReplacement(cause error) {
	runtime.mutex.Lock()
	if runtime.terminalErr == nil {
		runtime.terminalErr = cause
	} else if cause != nil {
		runtime.terminalErr = errors.Join(runtime.terminalErr, cause)
	}
	runtime.state = StateFailed
	run := runtime.run
	runtime.mutex.Unlock()
	if run != nil {
		run.cancel()
	}
}

// retainedHTTPDesiredState keeps only existing HTTP hosts that remain authorized by the next generation.
func retainedHTTPDesiredState(current DesiredState, next DesiredState) (DesiredState, error) {
	nextHosts := make(map[string]struct{}, len(next.httpRoutes))
	for _, route := range next.httpRoutes {
		nextHosts[route.Host] = struct{}{}
	}
	retained := make([]HTTPRoute, 0, len(current.httpRoutes))
	for _, route := range current.httpRoutes {
		if _, found := nextHosts[route.Host]; found {
			retained = append(retained, route)
		}
	}
	return NewDesiredState(current.listeners, retained, current.nativeRoutes, current.ttl)
}

// failHTTPRouteReplacementLocked stops serving when an unexpected rollback failure leaves publication state unprovable.
func (runtime *Runtime) failHTTPRouteReplacementLocked(cause error) error {
	if runtime.terminalErr == nil {
		runtime.terminalErr = cause
	} else {
		runtime.terminalErr = errors.Join(runtime.terminalErr, cause)
	}
	runtime.state = StateFailed
	if runtime.run != nil {
		runtime.run.cancel()
	}
	return cause
}

// Snapshot returns one defensive, payload-free observation without blocking active traffic.
func (runtime *Runtime) Snapshot() Snapshot {
	runtime.mutex.RLock()
	state := runtime.state
	desired := runtime.desired
	run := runtime.run
	ingress := runtime.ingress
	runtime.mutex.RUnlock()

	snapshot := Snapshot{
		State:   state,
		Relays:  make([]RelayStatus, 0, len(runtime.relays)),
		Directs: make([]DirectStatus, 0, len(desired.nativeRoutes)),
	}
	if runtime.dns != nil {
		address := desired.listeners.DNS
		runningAddress, running := runtime.dns.Address()
		if running {
			address = runningAddress
		}
		snapshot.DNS = DNSStatus{
			Configured: true,
			Address:    address,
			Running:    running,
			Records:    len(desired.dnsSnapshot.Records()),
		}
	}
	if ingress != nil {
		server := ingress.Snapshot()
		httpAddress := desired.listeners.HTTP
		httpsAddress := desired.listeners.HTTPS
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
			Routes:       len(desired.httpRoutes),
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
	runtime.dynamicMutex.Lock()
	dynamic := make([]*dynamicNativeRelay, 0, len(runtime.dynamicRelays))
	for _, entry := range runtime.dynamicRelays {
		dynamic = append(dynamic, entry)
	}
	runtime.dynamicMutex.Unlock()
	for _, entry := range dynamic {
		relay := entry.relay.Snapshot()
		listenAddress := entry.route.Listen
		if relay.ListenAddress.IsValid() {
			listenAddress = relay.ListenAddress
		}
		snapshot.Relays = append(snapshot.Relays, RelayStatus{
			ID:                   entry.route.ID,
			Host:                 entry.route.Host,
			ListenAddress:        listenAddress,
			Upstream:             relay.Upstream,
			Running:              relay.Running,
			ActiveConnections:    relay.ActiveConnections,
			CompletedConnections: relay.CompletedConnections,
			DialFailures:         relay.DialFailures,
			ClientBytes:          relay.ClientBytes,
			UpstreamBytes:        relay.UpstreamBytes,
			DroppedDiagnostics:   relay.DroppedDiagnostics,
		})
	}
	for _, route := range desired.nativeRoutes {
		if route.Direct {
			snapshot.Directs = append(snapshot.Directs, DirectStatus{
				ID:            route.ID,
				Host:          route.Host,
				ListenAddress: route.Listen,
			})
		}
	}
	slices.SortFunc(snapshot.Relays, compareRelayStatuses)
	slices.SortFunc(snapshot.Directs, func(left, right DirectStatus) int {
		if directStatusLess(left, right) {
			return -1
		}
		if directStatusLess(right, left) {
			return 1
		}
		return 0
	})
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
	runtime.dynamicMutex.Lock()
	defer runtime.dynamicMutex.Unlock()
	for _, entry := range runtime.dynamicRelays {
		if !entry.relay.Snapshot().Running {
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

	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), runtime.config.ShutdownTimeout+time.Second)
	dynamicCleanupErr := runtime.stopDynamicRoutes(cleanupContext)
	cancelCleanup()
	terminal := errors.Join(dynamicCleanupErr, collectChildResults(run, first, consumed, intentional))
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
	runtime.mutex.RLock()
	ingressActivation := runtime.ingressActivation
	runtime.mutex.RUnlock()
	if ingressActivation != nil {
		<-ingressActivation
	}

	runtime.mutex.Lock()
	if runtime.terminalErr == nil {
		runtime.terminalErr = terminal
	} else if terminal != nil {
		runtime.terminalErr = errors.Join(runtime.terminalErr, terminal)
	}
	if runtime.terminalErr != nil || !intentional {
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

// compareRelayStatuses keeps static and dynamically admitted native routes in one canonical snapshot order.
func compareRelayStatuses(left, right RelayStatus) int {
	if left.Host < right.Host {
		return -1
	}
	if left.Host > right.Host {
		return 1
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
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
