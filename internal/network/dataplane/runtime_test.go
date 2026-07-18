package dataplane

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/httpingress"
)

// TestRuntimeEmptyGenerationLifecycle verifies harbord can own a ready data plane before routes exist.
func TestRuntimeEmptyGenerationLifecycle(t *testing.T) {
	desired := mustDesiredState(t, ListenerPlan{}, nil, nil)
	runtime, err := NewRuntime(Config{Desired: desired})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateNew || snapshot.Relays == nil {
		t.Fatalf("initial Snapshot() = %#v", snapshot)
	} else if err := snapshot.Validate(); err != nil {
		t.Fatalf("initial Snapshot().Validate() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateReady || snapshot.DNS.Configured || snapshot.Ingress.Configured || len(snapshot.Relays) != 0 {
		t.Fatalf("ready Snapshot() = %#v", snapshot)
	} else if err := snapshot.Validate(); err != nil {
		t.Fatalf("ready Snapshot().Validate() error = %v", err)
	}
	if err := runtime.Start(ctx); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start() error = %v, want %v", err, ErrAlreadyStarted)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped {
		t.Fatalf("stopped Snapshot() = %#v", snapshot)
	} else if err := snapshot.Validate(); err != nil {
		t.Fatalf("stopped Snapshot().Validate() error = %v", err)
	}
	if runtime.Err() != nil {
		t.Fatalf("Err() = %v", runtime.Err())
	}
}

// TestRuntimeInfrastructureOnlyGenerationLifecycle proves shared sockets remain owned before any project route exists.
func TestRuntimeInfrastructureOnlyGenerationLifecycle(t *testing.T) {
	ingress := reserveTCPPorts(t, 2)
	desired := mustDesiredState(t, ListenerPlan{
		DNS:   reserveDNSPort(t),
		HTTP:  ingress[0],
		HTTPS: ingress[1],
	}, nil, nil)
	runtime := mustRuntime(t, Config{Desired: desired, CertificateProvider: inertCertificateProvider()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateReady || !snapshot.DNS.Configured || !snapshot.DNS.Running || snapshot.DNS.Records != 0 {
		t.Fatalf("ready DNS snapshot = %#v", snapshot.DNS)
	}
	if !snapshot.Ingress.Configured || !snapshot.Ingress.Running || snapshot.Ingress.Routes != 0 {
		t.Fatalf("ready ingress snapshot = %#v", snapshot.Ingress)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Snapshot().Validate() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if terminal := runtime.Snapshot(); terminal.State != StateStopped {
		t.Fatalf("terminal Snapshot() = %#v", terminal)
	} else if err := terminal.Validate(); err != nil {
		t.Fatalf("terminal Snapshot().Validate() error = %v", err)
	}
}

// TestRuntimeConcurrentCloseIsIdempotent verifies every caller observes one fully joined shutdown.
func TestRuntimeConcurrentCloseIsIdempotent(t *testing.T) {
	runtime := mustRuntime(t, Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	const callers = 64
	errorsFound := make(chan error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errorsFound <- runtime.Close(ctx)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Errorf("concurrent Close() error = %v", err)
		}
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remains open after concurrent shutdown")
	}
}

// TestRuntimeSnapshotsRemainValidDuringShutdown guards the non-atomic boundary between runtime and child state.
func TestRuntimeSnapshotsRemainValidDuringShutdown(t *testing.T) {
	ports := reserveTCPPorts(t, 2)
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: ports[0]},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: ports[1], Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	runtime := mustRuntime(t, Config{Desired: desired})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	for {
		snapshot := runtime.Snapshot()
		if err := snapshot.Validate(); err != nil {
			t.Fatalf("Snapshot().Validate() during shutdown = %v for %#v", err, snapshot)
		}
		select {
		case err := <-closeResult:
			if err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			return
		default:
		}
	}
}

// TestRuntimeCloseBeforeStartConsumesOneShotLifecycle verifies an unused runtime cannot later acquire sockets unexpectedly.
func TestRuntimeCloseBeforeStartConsumesOneShotLifecycle(t *testing.T) {
	runtime := mustRuntime(t, Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)})
	if err := runtime.Close(nil); err != nil {
		t.Fatalf("Close(nil) error = %v", err)
	}
	if err := runtime.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() after Close() error = %v, want %v", err, ErrClosed)
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}

// TestRuntimeCloseBeforeStartPublishesIntentAtomically gates the lifecycle boundary around a concurrent first Start.
func TestRuntimeCloseBeforeStartPublishesIntentAtomically(t *testing.T) {
	stopPublished := make(chan struct{})
	releaseClose := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseClose) }) })
	runtime, err := newRuntime(
		Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)},
		runtimeDependencies{
			listen: listenExactTCP,
			afterStopPublication: func() {
				close(stopPublished)
				<-releaseClose
			},
		},
	)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	select {
	case <-stopPublished:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not reach its atomic stop boundary")
	}
	if !runtime.stopRequested() {
		t.Fatal("stop intent remains unpublished inside the Close() boundary")
	}

	startAttempted := make(chan struct{})
	startResult := make(chan error, 1)
	go func() {
		close(startAttempted)
		startResult <- runtime.Start(context.Background())
	}()
	<-startAttempted
	releaseOnce.Do(func() { close(releaseClose) })

	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not complete")
	}
	select {
	case err := <-startResult:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() remained blocked after Close()")
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remains open after Close()")
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped {
		t.Fatalf("terminal Snapshot() = %#v", snapshot)
	}
	if runtime.Err() != nil {
		t.Fatalf("Err() = %v", runtime.Err())
	}
}

// TestRuntimeParentCancellationStopsChildren verifies the Start context owns the complete data-plane lifetime.
func TestRuntimeParentCancellationStopsChildren(t *testing.T) {
	runtime := mustRuntime(t, Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)})
	ctx, cancel := context.WithCancel(context.Background())
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancel()
	waitRuntimeDone(t, runtime)
	if runtime.Err() != nil || runtime.Snapshot().State != StateStopped {
		t.Fatalf("terminal runtime = state %s, error %v", runtime.Snapshot().State, runtime.Err())
	}

	cancelled, cancelBeforeStart := context.WithCancel(context.Background())
	cancelBeforeStart()
	second := mustRuntime(t, Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)})
	if err := second.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(cancelled) error = %v", err)
	}
	if second.Err() != nil || second.Snapshot().State != StateStopped {
		t.Fatalf("cancelled startup = state %s, error %v", second.Snapshot().State, second.Err())
	}
}

// TestNewRuntimeRejectsInvalidConfiguration covers constructor boundaries before any listener acquisition.
func TestNewRuntimeRejectsInvalidConfiguration(t *testing.T) {
	httpDesired := mustDesiredState(
		t,
		testListenerPlan(),
		[]HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: testEndpoint("127.0.0.1:41001")}},
		nil,
	)
	infrastructureDesired := mustDesiredState(t, testListenerPlan(), nil, nil)
	empty := mustDesiredState(t, ListenerPlan{}, nil, nil)
	certificate := inertCertificateProvider()
	tests := []struct {
		name         string
		config       Config
		dependencies runtimeDependencies
		want         string
	}{
		{name: "forged desired", config: Config{}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "NewDesiredState"},
		{name: "missing certificate", config: Config{Desired: httpDesired}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "certificate provider"},
		{name: "missing infrastructure certificate", config: Config{Desired: infrastructureDesired}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "certificate provider"},
		{name: "short startup", config: Config{Desired: empty, CertificateProvider: certificate, StartupTimeout: time.Nanosecond}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "startup timeout"},
		{name: "long startup", config: Config{Desired: empty, CertificateProvider: certificate, StartupTimeout: maximumStartupTimeout + time.Nanosecond}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "startup timeout"},
		{name: "short shutdown", config: Config{Desired: empty, CertificateProvider: certificate, ShutdownTimeout: time.Nanosecond}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "shutdown timeout"},
		{name: "long shutdown", config: Config{Desired: empty, CertificateProvider: certificate, ShutdownTimeout: maximumShutdownTimeout + time.Nanosecond}, dependencies: runtimeDependencies{listen: listenExactTCP}, want: "shutdown timeout"},
		{name: "listener factory", config: Config{Desired: empty, CertificateProvider: certificate}, dependencies: runtimeDependencies{}, want: "listener factory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newRuntime(test.config, test.dependencies); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("newRuntime() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimeRollsBackPreboundListeners verifies a later bind failure releases every earlier exact socket.
func TestRuntimeRollsBackPreboundListeners(t *testing.T) {
	ports := reserveTCPPorts(t, 3)
	listeners := ListenerPlan{DNS: ports[0], HTTP: ports[1], HTTPS: ports[2]}
	desired := mustDesiredState(
		t,
		listeners,
		[]HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: testEndpoint("127.0.0.1:41001")}},
		nil,
	)
	want := errors.New("synthetic second bind failure")
	calls := 0
	runtime, err := newRuntime(Config{Desired: desired, CertificateProvider: inertCertificateProvider()}, runtimeDependencies{
		listen: func(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
			calls++
			if calls == 2 {
				return nil, want
			}
			return listenExactTCP(ctx, endpoint)
		},
	})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	if err := runtime.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Start() error = %v, want wrapped %v", err, want)
	}
	if runtime.Snapshot().State != StateFailed || !errors.Is(runtime.Err(), want) {
		t.Fatalf("terminal runtime = state %s, error %v", runtime.Snapshot().State, runtime.Err())
	}
	assertTCPRebindable(t, listeners.HTTP)
}

// TestRuntimeRollsBackTCPBindingsWhenDNSCannotStart verifies DNS publication never leaves dormant public sockets.
func TestRuntimeRollsBackTCPBindingsWhenDNSCannotStart(t *testing.T) {
	dnsEndpoint := reserveTCPPorts(t, 1)[0]
	occupied, err := net.Listen("tcp4", dnsEndpoint.String())
	if err != nil {
		t.Fatalf("occupy DNS endpoint: %v", err)
	}
	defer occupied.Close()
	nativeEndpoint := reserveTCPPorts(t, 1)[0]
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: dnsEndpoint},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: nativeEndpoint, Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	runtime := mustRuntime(t, Config{Desired: desired})
	if err := runtime.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "bind TCP") {
		t.Fatalf("Start() error = %v, want DNS bind failure", err)
	}
	assertTCPRebindable(t, nativeEndpoint)
}

// TestRuntimePublishesDNSAfterRouteReadiness proves names cannot resolve before their public listener starts.
func TestRuntimePublishesDNSAfterRouteReadiness(t *testing.T) {
	dnsEndpoint := reserveDNSPort(t)
	nativeEndpoint := reserveTCPPorts(t, 1)[0]
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: dnsEndpoint},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: nativeEndpoint, Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{
		listen: func(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
			listener, listenErr := listenExactTCP(ctx, endpoint)
			if listenErr != nil || endpoint != nativeEndpoint {
				return listener, listenErr
			}
			return &gatedAddressListener{Listener: listener, entered: entered, release: release}, nil
		},
	})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- runtime.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native route did not enter listener readiness")
	}
	assertDNSRebindable(t, dnsEndpoint)
	close(release)
	released = true
	if err := <-startResult; err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if listener, err := net.Listen("tcp4", dnsEndpoint.String()); err == nil {
		_ = listener.Close()
		t.Fatalf("DNS TCP endpoint %s remained available after readiness", dnsEndpoint)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestRuntimePropagatesFatalChildFailure verifies one lost listener cancels and joins the complete generation.
func TestRuntimePropagatesFatalChildFailure(t *testing.T) {
	ports := reserveTCPPorts(t, 2)
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: ports[0]},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: ports[1], Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	tracked := newTrackingListenerFactory()
	runtime, err := newRuntime(Config{Desired: desired, ShutdownTimeout: 250 * time.Millisecond}, runtimeDependencies{listen: tracked.listen})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := tracked.close(ports[1]); err != nil {
		t.Fatalf("close native listener: %v", err)
	}
	waitRuntimeDone(t, runtime)
	if err := runtime.Err(); err == nil || !strings.Contains(err.Error(), "native route tcp:mysql") {
		t.Fatalf("Err() = %v, want route-labelled failure", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateFailed || snapshot.DNS.Running || snapshot.Relays[0].Running {
		t.Fatalf("failed Snapshot() = %#v", snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("failed Snapshot().Validate() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "native route tcp:mysql") {
		t.Fatalf("Close() error = %v, want retained terminal failure", err)
	}
}

// TestRuntimeCloseRacingStartupRollsBackAcquiredSocket verifies Close cannot strand a listener between bind and Serve.
func TestRuntimeCloseRacingStartupRollsBackAcquiredSocket(t *testing.T) {
	ports := reserveTCPPorts(t, 2)
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: ports[0]},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: ports[1], Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{
		listen: func(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
			listener, listenErr := listenExactTCP(ctx, endpoint)
			close(entered)
			<-release
			return listener, listenErr
		},
	})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- runtime.Start(context.Background()) }()
	<-entered
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	close(release)
	if err := <-startResult; !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertTCPRebindable(t, ports[1])
}

// TestRuntimeReadyPublicationLinearizesWithClose proves only the lifecycle winner publishes its state.
func TestRuntimeReadyPublicationLinearizesWithClose(t *testing.T) {
	t.Run("close wins", func(t *testing.T) {
		dnsEndpoint := reserveDNSPort(t)
		nativeEndpoint := reserveTCPPorts(t, 1)[0]
		desired := mustDesiredState(
			t,
			ListenerPlan{DNS: dnsEndpoint},
			nil,
			[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: nativeEndpoint, Upstream: testEndpoint("127.0.0.1:41006")}},
		)
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{
			listen: listenExactTCP,
			beforeReadyPublication: func() {
				close(entered)
				<-release
			},
		})
		if err != nil {
			t.Fatalf("newRuntime() error = %v", err)
		}
		startResult := make(chan error, 1)
		go func() { startResult <- runtime.Start(context.Background()) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("Start() did not reach ready publication")
		}

		closeResult := make(chan error, 1)
		go func() { closeResult <- runtime.Close(context.Background()) }()
		select {
		case <-runtime.stop:
		case <-time.After(5 * time.Second):
			t.Fatal("Close() did not claim shutdown")
		}
		if snapshot := runtime.Snapshot(); snapshot.State != StateStopping {
			t.Fatalf("Snapshot() while Close owns publication = %#v", snapshot)
		}
		releaseOnce.Do(func() { close(release) })

		select {
		case err := <-startResult:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Start() remained blocked after Close() won")
		}
		select {
		case err := <-closeResult:
			if err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close() remained blocked after startup rollback")
		}
		select {
		case <-runtime.Done():
		default:
			t.Fatal("Done() remains open after startup rollback")
		}
		if snapshot := runtime.Snapshot(); snapshot.State != StateStopped || snapshot.DNS.Running || snapshot.Relays[0].Running {
			t.Fatalf("terminal Snapshot() = %#v", snapshot)
		}
		if runtime.Err() != nil {
			t.Fatalf("Err() = %v", runtime.Err())
		}
		assertDNSRebindable(t, dnsEndpoint)
		assertTCPRebindable(t, nativeEndpoint)
	})

	t.Run("start wins", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		runtime, err := newRuntime(
			Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)},
			runtimeDependencies{
				listen: listenExactTCP,
				beforeReadyPublication: func() {
					close(entered)
					<-release
				},
			},
		)
		if err != nil {
			t.Fatalf("newRuntime() error = %v", err)
		}
		startResult := make(chan error, 1)
		go func() { startResult <- runtime.Start(context.Background()) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("Start() did not reach ready publication")
		}
		close(release)
		select {
		case err := <-startResult:
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Start() did not publish readiness")
		}
		if snapshot := runtime.Snapshot(); snapshot.State != StateReady {
			t.Fatalf("ready Snapshot() = %#v", snapshot)
		}
		if err := runtime.Close(context.Background()); err != nil {
			t.Fatalf("Close() after ready publication error = %v", err)
		}
	})
}

// TestRuntimeCloseCancelsListenerAcquisition proves shutdown reaches a context-aware bind in progress.
func TestRuntimeCloseCancelsListenerAcquisition(t *testing.T) {
	dnsEndpoint := reserveDNSPort(t)
	nativeEndpoint := reserveTCPPorts(t, 1)[0]
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: dnsEndpoint},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: nativeEndpoint, Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	entered := make(chan struct{})
	runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{
		listen: func(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
			listener, listenErr := listenExactTCP(ctx, endpoint)
			if listenErr != nil {
				return nil, listenErr
			}
			close(entered)
			select {
			case <-ctx.Done():
				_ = listener.Close()
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				_ = listener.Close()
				return nil, errors.New("listener context was not cancelled")
			}
		},
	})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- runtime.Start(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("listener factory did not begin acquisition")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	select {
	case err := <-startResult:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() remained blocked in listener acquisition")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() remained blocked behind listener acquisition")
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remains open after cancelled listener acquisition")
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped || snapshot.DNS.Running || snapshot.Relays[0].Running {
		t.Fatalf("terminal Snapshot() = %#v", snapshot)
	}
	if runtime.Err() != nil {
		t.Fatalf("Err() = %v", runtime.Err())
	}
	assertDNSRebindable(t, dnsEndpoint)
	assertTCPRebindable(t, nativeEndpoint)
}

// TestRuntimeCloseClassifiesBindFailureAtStopBoundary proves stop intent cannot lag behind lifecycle state.
func TestRuntimeCloseClassifiesBindFailureAtStopBoundary(t *testing.T) {
	dnsEndpoint := reserveDNSPort(t)
	nativeEndpoint := reserveTCPPorts(t, 1)[0]
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: dnsEndpoint},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: nativeEndpoint, Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	bindEntered := make(chan context.Context, 1)
	releaseBind := make(chan struct{})
	bindReturned := make(chan struct{})
	stopPublished := make(chan struct{})
	releaseClose := make(chan struct{})
	var releaseBindOnce sync.Once
	var releaseCloseOnce sync.Once
	t.Cleanup(func() {
		releaseBindOnce.Do(func() { close(releaseBind) })
		releaseCloseOnce.Do(func() { close(releaseClose) })
	})
	want := errors.New("synthetic bind failure")
	runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{
		listen: func(ctx context.Context, _ netip.AddrPort) (net.Listener, error) {
			bindEntered <- ctx
			<-releaseBind
			close(bindReturned)
			return nil, want
		},
		afterStopPublication: func() {
			close(stopPublished)
			<-releaseClose
		},
	})
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- runtime.Start(context.Background()) }()
	var bindContext context.Context
	select {
	case bindContext = <-bindEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("listener factory did not begin acquisition")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	select {
	case <-stopPublished:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not publish stop intent")
	}
	if err := bindContext.Err(); err != nil {
		t.Fatalf("bind context was cancelled before Close() left the lifecycle mutex: %v", err)
	}
	releaseBindOnce.Do(func() { close(releaseBind) })
	select {
	case <-bindReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("listener factory did not return its synthetic failure")
	}
	releaseCloseOnce.Do(func() { close(releaseClose) })

	select {
	case err := <-startResult:
		if !errors.Is(err, ErrClosed) || errors.Is(err, want) {
			t.Fatalf("Start() error = %v, want only %v", err, ErrClosed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not classify the bind result")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() remained blocked after startup rollback")
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped || snapshot.DNS.Running || snapshot.Relays[0].Running {
		t.Fatalf("terminal Snapshot() = %#v", snapshot)
	}
	if runtime.Err() != nil {
		t.Fatalf("Err() = %v", runtime.Err())
	}
	assertDNSRebindable(t, dnsEndpoint)
	assertTCPRebindable(t, nativeEndpoint)
}

// TestRuntimeCloseHonorsCallerDeadline verifies one waiter cannot block beyond its own lifecycle budget.
func TestRuntimeCloseHonorsCallerDeadline(t *testing.T) {
	runtime := mustRuntime(t, Config{Desired: mustDesiredState(t, ListenerPlan{}, nil, nil)})
	runtime.mutex.Lock()
	runtime.state = StateStarting
	runtime.mutex.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close(cancelled) error = %v", err)
	}
	runtime.finish(nil, true)
}

// TestRuntimeRejectsInvalidAcquiredListeners covers the ownership proof before listener handoff.
func TestRuntimeRejectsInvalidAcquiredListeners(t *testing.T) {
	ports := reserveTCPPorts(t, 3)
	desired := mustDesiredState(
		t,
		ListenerPlan{DNS: ports[0]},
		nil,
		[]NativeRoute{{ID: "tcp:mysql", Host: "mysql.app.test", Listen: ports[1], Upstream: testEndpoint("127.0.0.1:41006")}},
	)
	tests := []struct {
		name   string
		listen listenTCP
		want   string
	}{
		{name: "nil", listen: func(context.Context, netip.AddrPort) (net.Listener, error) { return nil, nil }, want: "returned nil"},
		{name: "non TCP", listen: func(context.Context, netip.AddrPort) (net.Listener, error) {
			return &staticListener{address: staticAddress{network: "udp", value: ports[1].String()}}, nil
		}, want: "must be TCP"},
		{name: "wrong socket", listen: func(context.Context, netip.AddrPort) (net.Listener, error) {
			return net.Listen("tcp4", ports[2].String())
		}, want: "unexpected socket"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := newRuntime(Config{Desired: desired}, runtimeDependencies{listen: test.listen})
			if err != nil {
				t.Fatalf("newRuntime() error = %v", err)
			}
			if err := runtime.Start(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Start() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimeReadinessOutcomes covers bounded wait signals without depending on scheduler timing.
func TestRuntimeReadinessOutcomes(t *testing.T) {
	want := errors.New("synthetic child failure")
	tests := []struct {
		name    string
		prepare func(*Runtime, *runtimeRun) context.Context
		ready   bool
		want    string
	}{
		{name: "ready child exit", ready: true, prepare: func(_ *Runtime, run *runtimeRun) context.Context {
			run.results <- childResult{name: "test"}
			return context.Background()
		}, want: "stopped unexpectedly"},
		{name: "waiting child failure", prepare: func(_ *Runtime, run *runtimeRun) context.Context {
			run.results <- childResult{name: "test", err: want}
			return context.Background()
		}, want: want.Error()},
		{name: "context", prepare: func(_ *Runtime, _ *runtimeRun) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, want: context.Canceled.Error()},
		{name: "stop", prepare: func(runtime *Runtime, _ *runtimeRun) context.Context {
			close(runtime.stop)
			return context.Background()
		}, want: ErrClosed.Error()},
		{name: "timeout", prepare: func(_ *Runtime, _ *runtimeRun) context.Context {
			return context.Background()
		}, want: "did not become ready"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &Runtime{config: Config{StartupTimeout: time.Millisecond}, stop: make(chan struct{})}
			run := &runtimeRun{results: make(chan childResult, 1)}
			ctx := test.prepare(runtime, run)
			_, _, err := runtime.awaitReadiness(ctx, run, func() bool { return test.ready })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("awaitReadiness() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimeListenerAndResultHelpers covers defensive diagnostics around untrusted listener implementations.
func TestRuntimeListenerAndResultHelpers(t *testing.T) {
	listeners := []struct {
		name     string
		listener net.Listener
		want     string
	}{
		{name: "nil address", listener: &staticListener{}, want: "must be TCP"},
		{name: "invalid address", listener: &staticListener{address: staticAddress{network: "tcp", value: "invalid"}}, want: "parse listener"},
		{name: "non loopback", listener: &staticListener{address: staticAddress{network: "tcp", value: "0.0.0.0:8080"}}, want: "IPv4 loopback"},
	}
	for _, test := range listeners {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exactListenerAddress(test.listener); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("exactListenerAddress() error = %v, want containing %q", err, test.want)
			}
		})
	}

	endpoint := reserveTCPPorts(t, 1)[0]
	occupied, err := net.Listen("tcp4", endpoint.String())
	if err != nil {
		t.Fatalf("occupy listener endpoint: %v", err)
	}
	if _, err := listenExactTCP(context.Background(), endpoint); err == nil {
		t.Fatal("listenExactTCP() acquired an occupied endpoint")
	}
	_ = occupied.Close()

	firstErr := errors.New("first")
	secondErr := errors.New("second")
	run := &runtimeRun{children: 2, results: make(chan childResult, 1)}
	run.results <- childResult{name: "second", err: secondErr}
	if err := collectChildResults(run, childResult{name: "first", err: firstErr}, true, true); !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("collectChildResults() error = %v", err)
	}

	stop := make(chan struct{})
	close(stop)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := startupInterruption(ctx, stop); !errors.Is(err, context.Canceled) {
		t.Fatalf("startupInterruption(cancelled and stopped) error = %v", err)
	}
	if err := startupInterruption(context.Background(), stop); !errors.Is(err, ErrClosed) {
		t.Fatalf("startupInterruption(stopped) error = %v", err)
	}
}

// inertCertificateProvider is valid construction wiring for tests that do not perform a TLS handshake.
func inertCertificateProvider() httpingress.CertificateProvider {
	return func(context.Context, string) (*tls.Certificate, error) {
		return nil, errors.New("certificate fixture was not configured")
	}
}

// mustDesiredState constructs a desired generation or fails its caller immediately.
func mustDesiredState(t *testing.T, listeners ListenerPlan, httpRoutes []HTTPRoute, nativeRoutes []NativeRoute) DesiredState {
	t.Helper()
	desired, err := NewDesiredState(listeners, httpRoutes, nativeRoutes, 2*time.Second)
	if err != nil {
		t.Fatalf("NewDesiredState() error = %v", err)
	}
	return desired
}

// mustRuntime constructs a runtime or fails its caller immediately.
func mustRuntime(t *testing.T, config Config) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(config)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

// waitRuntimeDone bounds every terminal-state assertion against a stranded child regression.
func waitRuntimeDone(t *testing.T, runtime *Runtime) {
	t.Helper()
	select {
	case <-runtime.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("data plane runtime did not stop")
	}
}

// reserveTCPPorts returns distinct currently available explicit loopback ports.
func reserveTCPPorts(t *testing.T, count int) []netip.AddrPort {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	endpoints := make([]netip.AddrPort, 0, count)
	for range count {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve TCP port: %v", err)
		}
		listeners = append(listeners, listener)
		endpoint, err := netip.ParseAddrPort(listener.Addr().String())
		if err != nil {
			t.Fatalf("parse reserved TCP port: %v", err)
		}
		endpoints = append(endpoints, endpoint)
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatalf("release reserved TCP port: %v", err)
		}
	}
	return endpoints
}

// reserveDNSPort returns one explicit high loopback port available to both TCP and UDP.
func reserveDNSPort(t *testing.T) netip.AddrPort {
	t.Helper()
	for range 20 {
		tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve DNS TCP port: %v", err)
		}
		endpoint, parseErr := netip.ParseAddrPort(tcpListener.Addr().String())
		if parseErr != nil {
			_ = tcpListener.Close()
			t.Fatalf("parse reserved DNS port: %v", parseErr)
		}
		udpListener, udpErr := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(endpoint))
		_ = tcpListener.Close()
		if udpErr != nil {
			continue
		}
		_ = udpListener.Close()
		return endpoint
	}
	t.Fatal("could not reserve one shared TCP and UDP DNS port")
	return netip.AddrPort{}
}

// assertDNSRebindable proves neither authoritative transport retains an exact DNS socket.
func assertDNSRebindable(t *testing.T, endpoint netip.AddrPort) {
	t.Helper()
	tcpListener, err := net.Listen("tcp4", endpoint.String())
	if err != nil {
		t.Fatalf("rebind DNS TCP %s: %v", endpoint, err)
	}
	defer tcpListener.Close()
	udpListener, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatalf("rebind DNS UDP %s: %v", endpoint, err)
	}
	if err := udpListener.Close(); err != nil {
		t.Fatalf("close rebound DNS UDP %s: %v", endpoint, err)
	}
}

// assertTCPRebindable proves a failed or stopped runtime retained no listener ownership.
func assertTCPRebindable(t *testing.T, endpoint netip.AddrPort) {
	t.Helper()
	listener, err := net.Listen("tcp4", endpoint.String())
	if err != nil {
		t.Fatalf("rebind %s: %v", endpoint, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close rebound %s: %v", endpoint, err)
	}
}

// trackingListenerFactory retains test-only handles used to inject an unexpected listener exit.
type trackingListenerFactory struct {
	mutex     sync.Mutex
	listeners map[netip.AddrPort]net.Listener
}

// gatedAddressListener pauses the child's address proof after startup has pre-bound the socket.
type gatedAddressListener struct {
	net.Listener

	mutex   sync.Mutex
	calls   int
	entered chan struct{}
	release <-chan struct{}
}

// staticAddress gives defensive listener tests complete control over network and text forms.
type staticAddress struct {
	network string
	value   string
}

// Network returns the synthetic transport label.
func (address staticAddress) Network() string {
	return address.network
}

// String returns the synthetic socket text.
func (address staticAddress) String() string {
	return address.value
}

// staticListener exposes only the listener methods needed by ownership validation.
type staticListener struct {
	address net.Addr
}

// Accept is unreachable because ownership validation rejects this synthetic listener first.
func (listener *staticListener) Accept() (net.Conn, error) {
	return nil, errors.New("static listener does not accept")
}

// Close has no resources because the static listener owns no operating-system socket.
func (listener *staticListener) Close() error {
	return nil
}

// Addr returns the exact synthetic address under test.
func (listener *staticListener) Addr() net.Addr {
	return listener.address
}

// Addr blocks the relay's first inspection while leaving the runtime's three pre-bind checks available.
func (listener *gatedAddressListener) Addr() net.Addr {
	listener.mutex.Lock()
	listener.calls++
	call := listener.calls
	listener.mutex.Unlock()
	if call == 4 {
		close(listener.entered)
		<-listener.release
	}
	return listener.Listener.Addr()
}

// newTrackingListenerFactory creates an empty exact-listener registry.
func newTrackingListenerFactory() *trackingListenerFactory {
	return &trackingListenerFactory{listeners: make(map[netip.AddrPort]net.Listener)}
}

// listen acquires and records one production-equivalent exact listener.
func (factory *trackingListenerFactory) listen(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
	listener, err := listenExactTCP(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	factory.mutex.Lock()
	factory.listeners[endpoint] = listener
	factory.mutex.Unlock()
	return listener, nil
}

// close simulates an operating-system listener disappearing beneath one child server.
func (factory *trackingListenerFactory) close(endpoint netip.AddrPort) error {
	factory.mutex.Lock()
	listener := factory.listeners[endpoint]
	factory.mutex.Unlock()
	if listener == nil {
		return errors.New("tracked listener is missing")
	}
	return listener.Close()
}
