package dataplane

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/dnsserver"
	"github.com/goforj/harbor/internal/network/httpingress"
)

// TestRuntimeActivateHTTPIngressPreservesResolverAndManagedNativeRoutes proves promotion changes only the shared web pair.
func TestRuntimeActivateHTTPIngressPreservesResolverAndManagedNativeRoutes(t *testing.T) {
	dns := reserveDNSPort(t)
	shared := reserveTCPPorts(t, 3)
	upstreams := reserveDistinctTCPPorts(t, 2, append([]netip.AddrPort{dns}, shared...)...)
	resolver := mustDesiredState(t, ListenerPlan{DNS: dns}, nil, nil)
	runtime := mustRuntime(t, Config{Desired: resolver, CertificateProvider: inertCertificateProvider()})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})

	native := NativeRoute{
		ID:       "orders:service:mysql",
		Host:     "mysql.orders.test",
		Listen:   shared[2],
		Upstream: upstreams[0],
	}
	if err := runtime.ReplaceNativeRoutes(context.Background(), []NativeRoute{native}); err != nil {
		t.Fatalf("ReplaceNativeRoutes() error = %v", err)
	}
	before := runtime.Snapshot()
	fullListeners := ListenerPlan{DNS: dns, HTTP: shared[0], HTTPS: shared[1]}
	full := mustDesiredState(t, fullListeners, nil, nil)
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err != nil {
		t.Fatalf("ActivateHTTPIngress() error = %v", err)
	}

	after := runtime.Snapshot()
	if after.State != StateReady || !after.DNS.Running || !after.Ingress.Running || len(after.Relays) != 1 || !after.Relays[0].Running {
		t.Fatalf("promoted Snapshot() = %#v", after)
	}
	if after.DNS.Address != before.DNS.Address || after.DNS.Records != before.DNS.Records ||
		after.Relays[0].ID != native.ID || after.Relays[0].ListenAddress != native.Listen || after.Relays[0].Upstream != native.Upstream {
		t.Fatalf("promotion changed resolver or native route: before %#v, after %#v", before, after)
	}
	if after.Ingress.HTTPAddress != fullListeners.HTTP || after.Ingress.HTTPSAddress != fullListeners.HTTPS || after.Ingress.Routes != 0 {
		t.Fatalf("promoted ingress = %#v", after.Ingress)
	}

	routed := mustDesiredState(t, fullListeners, []HTTPRoute{{
		ID:       "orders:app-http",
		Host:     "orders.test",
		Upstream: upstreams[1],
	}}, nil)
	if err := runtime.ReplaceHTTPRoutes(routed); err != nil {
		t.Fatalf("ReplaceHTTPRoutes() after promotion error = %v", err)
	}
	routedSnapshot := runtime.Snapshot()
	if routedSnapshot.DNS.Records != 2 || routedSnapshot.Ingress.Routes != 1 || len(routedSnapshot.Relays) != 1 || !routedSnapshot.Relays[0].Running {
		t.Fatalf("routed promoted Snapshot() = %#v", routedSnapshot)
	}
	if err := routedSnapshot.Validate(); err != nil {
		t.Fatalf("routed promoted Snapshot().Validate() error = %v", err)
	}
}

// TestRuntimeActivateHTTPIngressRollsBackPairBindFailure leaves the resolver and native relay retryable.
func TestRuntimeActivateHTTPIngressRollsBackPairBindFailure(t *testing.T) {
	dns := reserveDNSPort(t)
	shared := reserveTCPPorts(t, 3)
	upstream := reserveDistinctTCPPorts(t, 1, append([]netip.AddrPort{dns}, shared...)...)[0]
	runtime := mustRuntime(t, Config{
		Desired:             mustDesiredState(t, ListenerPlan{DNS: dns}, nil, nil),
		CertificateProvider: inertCertificateProvider(),
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	native := NativeRoute{ID: "orders:mysql", Host: "mysql.orders.test", Listen: shared[2], Upstream: upstream}
	if err := runtime.ReplaceNativeRoutes(context.Background(), []NativeRoute{native}); err != nil {
		t.Fatalf("ReplaceNativeRoutes() error = %v", err)
	}

	conflict, err := net.Listen("tcp4", shared[1].String())
	if err != nil {
		t.Fatalf("reserve HTTPS conflict: %v", err)
	}
	full := mustDesiredState(t, ListenerPlan{DNS: dns, HTTP: shared[0], HTTPS: shared[1]}, nil, nil)
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err == nil || !strings.Contains(err.Error(), "bind HTTPS") {
		t.Fatalf("ActivateHTTPIngress() error = %v, want HTTPS bind failure", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateReady || !snapshot.DNS.Running || snapshot.Ingress.Configured || snapshot.DNS.Records != 1 ||
		len(snapshot.Relays) != 1 || !snapshot.Relays[0].Running {
		t.Fatalf("rolled-back bind Snapshot() = %#v", snapshot)
	}
	assertTCPRebindable(t, shared[0])
	if err := conflict.Close(); err != nil {
		t.Fatalf("close HTTPS conflict: %v", err)
	}
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err != nil {
		t.Fatalf("ActivateHTTPIngress() retry error = %v", err)
	}
}

// TestRuntimeActivateHTTPIngressRollsBackCandidateExit keeps an unadmitted pair outside the shared failure domain.
func TestRuntimeActivateHTTPIngressRollsBackCandidateExit(t *testing.T) {
	dns := reserveDNSPort(t)
	shared := reserveTCPPorts(t, 2)
	factory := newTrackingListenerFactory()
	injected := false
	runtime, err := newRuntime(
		Config{
			Desired:             mustDesiredState(t, ListenerPlan{DNS: dns}, nil, nil),
			CertificateProvider: inertCertificateProvider(),
		},
		runtimeDependencies{
			listen: factory.listen,
			beforeIngressActivationPublication: func(server *httpingress.Server) {
				if injected {
					return
				}
				injected = true
				if closeErr := factory.close(shared[0]); closeErr != nil {
					t.Errorf("close candidate HTTP listener: %v", closeErr)
					return
				}
				deadline := time.Now().Add(5 * time.Second)
				for server.Snapshot().Running && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			},
		},
	)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	full := mustDesiredState(t, ListenerPlan{DNS: dns, HTTP: shared[0], HTTPS: shared[1]}, nil, nil)
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err == nil {
		t.Fatal("ActivateHTTPIngress() candidate exit error = nil")
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateReady || !snapshot.DNS.Running || snapshot.Ingress.Configured {
		t.Fatalf("candidate-exit rollback Snapshot() = %#v", snapshot)
	}
	assertTCPRebindable(t, shared[0])
	assertTCPRebindable(t, shared[1])
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err != nil {
		t.Fatalf("ActivateHTTPIngress() retry error = %v", err)
	}
}

// TestRuntimeActivateHTTPIngressConcurrentCloseJoinsUnpublishedListeners guards the promotion ownership race.
func TestRuntimeActivateHTTPIngressConcurrentCloseJoinsUnpublishedListeners(t *testing.T) {
	dns := reserveDNSPort(t)
	shared := reserveTCPPorts(t, 2)
	secondBindEntered := make(chan struct{})
	bindCalls := 0
	listen := func(ctx context.Context, endpoint netip.AddrPort) (net.Listener, error) {
		bindCalls++
		if bindCalls == 2 {
			close(secondBindEntered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return listenExactTCP(ctx, endpoint)
	}
	runtime, err := newRuntime(
		Config{
			Desired:             mustDesiredState(t, ListenerPlan{DNS: dns}, nil, nil),
			CertificateProvider: inertCertificateProvider(),
		},
		runtimeDependencies{listen: listen},
	)
	if err != nil {
		t.Fatalf("newRuntime() error = %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	full := mustDesiredState(t, ListenerPlan{DNS: dns, HTTP: shared[0], HTTPS: shared[1]}, nil, nil)
	activationResult := make(chan error, 1)
	go func() { activationResult <- runtime.ActivateHTTPIngress(context.Background(), full) }()
	select {
	case <-secondBindEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP ingress activation did not reach the second bind")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- runtime.Close(context.Background()) }()
	select {
	case err := <-activationResult:
		if err == nil {
			t.Fatal("ActivateHTTPIngress() during Close error = nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ActivateHTTPIngress() remained blocked during Close")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not join the in-flight activation")
	}
	if snapshot := runtime.Snapshot(); snapshot.State != StateStopped || snapshot.DNS.Running || snapshot.Ingress.Running {
		t.Fatalf("concurrent Close Snapshot() = %#v", snapshot)
	}
	assertTCPRebindable(t, shared[0])
	assertTCPRebindable(t, shared[1])
}

// TestRuntimeActivateHTTPIngressRejectsInvalidLifecycleAndTopology keeps failed admission side-effect free.
func TestRuntimeActivateHTTPIngressRejectsInvalidLifecycleAndTopology(t *testing.T) {
	dns := reserveDNSPort(t)
	shared := reserveTCPPorts(t, 3)
	resolver := mustDesiredState(t, ListenerPlan{DNS: dns}, nil, nil)
	fullListeners := ListenerPlan{DNS: dns, HTTP: shared[0], HTTPS: shared[1]}
	full := mustDesiredState(t, fullListeners, nil, nil)

	var nilRuntime *Runtime
	if err := nilRuntime.ActivateHTTPIngress(context.Background(), full); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ActivateHTTPIngress(nil) error = %v, want %v", err, ErrNotReady)
	}
	notStarted := mustRuntime(t, Config{Desired: resolver, CertificateProvider: inertCertificateProvider()})
	if err := notStarted.ActivateHTTPIngress(context.Background(), full); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ActivateHTTPIngress(before Start) error = %v, want %v", err, ErrNotReady)
	}
	if err := notStarted.Close(context.Background()); err != nil {
		t.Fatalf("Close(not started) error = %v", err)
	}

	withoutProvider := mustRuntime(t, Config{Desired: resolver})
	if err := withoutProvider.Start(context.Background()); err != nil {
		t.Fatalf("Start(without provider) error = %v", err)
	}
	if err := withoutProvider.ActivateHTTPIngress(context.Background(), full); err == nil || !strings.Contains(err.Error(), "certificate provider") {
		t.Fatalf("ActivateHTTPIngress(without provider) error = %v", err)
	}
	if snapshot := withoutProvider.Snapshot(); snapshot.State != StateReady || !snapshot.DNS.Running || snapshot.Ingress.Configured {
		t.Fatalf("provider rejection Snapshot() = %#v", snapshot)
	}
	if err := withoutProvider.Close(context.Background()); err != nil {
		t.Fatalf("Close(without provider) error = %v", err)
	}

	runtime := mustRuntime(t, Config{Desired: resolver, CertificateProvider: inertCertificateProvider()})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	changedTTL, err := NewDesiredState(fullListeners, nil, nil, 3*time.Second)
	if err != nil {
		t.Fatalf("NewDesiredState(changed TTL) error = %v", err)
	}
	changedDNSListeners := fullListeners
	changedDNSListeners.DNS = reserveDNSPort(t)
	withRoute := mustDesiredState(t, fullListeners, []HTTPRoute{{
		ID:       "orders:app-http",
		Host:     "orders.test",
		Upstream: testEndpoint("127.0.0.1:41006"),
	}}, nil)
	withNative := mustDesiredState(t, fullListeners, nil, []NativeRoute{{
		ID:       "orders:mysql",
		Host:     "mysql.orders.test",
		Listen:   shared[2],
		Upstream: testEndpoint("127.0.0.1:41007"),
	}})
	tests := []struct {
		name string
		next DesiredState
		want string
	}{
		{name: "forged desired state", next: DesiredState{}, want: "NewDesiredState"},
		{name: "changed DNS", next: mustDesiredState(t, changedDNSListeners, nil, nil), want: "DNS listener"},
		{name: "missing web pair", next: resolver, want: "HTTP and HTTPS"},
		{name: "changed TTL", next: changedTTL, want: "TTL"},
		{name: "premature HTTP routes", next: withRoute, want: "routes must remain empty"},
		{name: "replacement native routes", next: withNative, want: "cannot replace managed native routes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runtime.ActivateHTTPIngress(context.Background(), test.next); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ActivateHTTPIngress() error = %v, want containing %q", err, test.want)
			}
			if snapshot := runtime.Snapshot(); snapshot.State != StateReady || !snapshot.DNS.Running || snapshot.Ingress.Configured {
				t.Fatalf("rejected activation Snapshot() = %#v", snapshot)
			}
		})
	}
	if err := runtime.ActivateHTTPIngress(context.Background(), full); err != nil {
		t.Fatalf("ActivateHTTPIngress() after rejections error = %v", err)
	}
}

// TestRuntimeReplaceNativeRoutesPublishesAndWithdrawsManagedRelays proves native publications can join a ready shared generation without rebinding its HTTP listeners.
func TestRuntimeReplaceNativeRoutesPublishesAndWithdrawsManagedRelays(t *testing.T) {
	listeners := ListenerPlan{DNS: reserveDNSPort(t)}
	shared := reserveTCPPorts(t, 2)
	listeners.HTTP = shared[0]
	listeners.HTTPS = shared[1]
	excluded := append([]netip.AddrPort{listeners.DNS}, shared...)
	upstreams := reserveDistinctTCPPorts(t, 2, excluded...)
	desired := mustDesiredState(t, listeners, []HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: upstreams[0]}}, nil)
	runtime := mustRuntime(t, Config{Desired: desired, CertificateProvider: inertCertificateProvider()})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})
	route := NativeRoute{
		ID:       "orders:service:mysql",
		Host:     "mysql.orders.test",
		Listen:   shared[0],
		Upstream: upstreams[1],
	}
	// Use a separate exact listener because the shared HTTP listener must remain untouched by native admission.
	conflict := reserveTCPPorts(t, 1)[0]
	route.Listen = conflict
	if err := runtime.ReplaceNativeRoutes(context.Background(), []NativeRoute{route}); err != nil {
		t.Fatalf("ReplaceNativeRoutes() error = %v", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateReady || len(snapshot.Relays) != 1 || snapshot.Relays[0].ID != route.ID || snapshot.Relays[0].ListenAddress != route.Listen {
		t.Fatalf("managed route snapshot = %#v", snapshot)
	}
	if snapshot.Ingress.HTTPAddress != listeners.HTTP || snapshot.Ingress.HTTPSAddress != listeners.HTTPS {
		t.Fatalf("managed route publication rebound shared listeners: %#v", snapshot.Ingress)
	}
	if snapshot.DNS.Records != 2 {
		t.Fatalf("managed route DNS records = %d, want 2", snapshot.DNS.Records)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("managed route snapshot validation error = %v", err)
	}
	if err := runtime.ReplaceNativeRoutes(context.Background(), []NativeRoute{}); err != nil {
		t.Fatalf("withdraw managed routes error = %v", err)
	}
	snapshot = runtime.Snapshot()
	if snapshot.State != StateReady || len(snapshot.Relays) != 0 || snapshot.DNS.Records != 1 {
		t.Fatalf("withdrawn managed route snapshot = %#v", snapshot)
	}
}

// TestRuntimeReplaceNativeRoutesFailsClosedOnListenerConflict proves a partially withdrawn publication cannot remain routable after replacement fails.
func TestRuntimeReplaceNativeRoutesFailsClosedOnListenerConflict(t *testing.T) {
	listeners := ListenerPlan{DNS: reserveDNSPort(t)}
	shared := reserveTCPPorts(t, 2)
	listeners.HTTP = shared[0]
	listeners.HTTPS = shared[1]
	excluded := append([]netip.AddrPort{listeners.DNS}, shared...)
	upstream := reserveDistinctTCPPorts(t, 1, excluded...)[0]
	desired := mustDesiredState(t, listeners, []HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: upstream}}, nil)
	runtime := mustRuntime(t, Config{Desired: desired, CertificateProvider: inertCertificateProvider()})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = runtime.Close(context.Background())
	}()
	conflictEndpoint := reserveTCPPorts(t, 1)[0]
	conflict, err := net.Listen("tcp4", conflictEndpoint.String())
	if err != nil {
		t.Fatalf("reserve conflict listener: %v", err)
	}
	defer conflict.Close()
	route := NativeRoute{ID: "orders:service:mysql", Host: "mysql.orders.test", Listen: conflictEndpoint, Upstream: upstream}
	if err := runtime.ReplaceNativeRoutes(context.Background(), []NativeRoute{route}); err == nil {
		t.Fatal("ReplaceNativeRoutes() conflict error = nil")
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != StateFailed || len(snapshot.Relays) != 0 || snapshot.DNS.Records != 1 {
		t.Fatalf("failed managed route snapshot = %#v", snapshot)
	}
}

// routeReplacementFixture owns one ready shared-listener generation with a stable native relay.
type routeReplacementFixture struct {
	runtime                 *Runtime
	listeners               ListenerPlan
	native                  []NativeRoute
	current                 DesiredState
	next                    DesiredState
	alternateNativeUpstream netip.AddrPort
}

// TestRuntimeReplaceHTTPRoutesOrdersPublicationAndUpdatesSnapshot verifies mixed replacements never publish a dangling DNS name.
func TestRuntimeReplaceHTTPRoutesOrdersPublicationAndUpdatesSnapshot(t *testing.T) {
	fixture := newRouteReplacementFixture(t)
	originalDNS := fixture.runtime.replaceDNS
	originalIngress := fixture.runtime.replaceIngress
	events := make([]string, 0, 3)
	fixture.runtime.replaceDNS = func(snapshot dnsserver.Snapshot) error {
		events = append(events, dnsReplacementEvent(snapshot))
		return originalDNS(snapshot)
	}
	fixture.runtime.replaceIngress = func(snapshot *httpingress.Snapshot) error {
		events = append(events, ingressReplacementEvent(snapshot))
		return originalIngress(snapshot)
	}

	if err := fixture.runtime.ReplaceHTTPRoutes(fixture.next); err != nil {
		t.Fatalf("ReplaceHTTPRoutes() error = %v", err)
	}
	wantEvents := []string{
		"dns:mysql.app.test,retain.test",
		"ingress:add.test,retain.test",
		"dns:add.test,mysql.app.test,retain.test",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("replacement events = %v, want %v", events, wantEvents)
	}

	snapshot := fixture.runtime.Snapshot()
	if snapshot.State != StateReady || snapshot.DNS.Records != 3 || snapshot.Ingress.Routes != 2 || len(snapshot.Relays) != 1 {
		t.Fatalf("replacement Snapshot() = %#v", snapshot)
	}
	if snapshot.DNS.Address != fixture.listeners.DNS || snapshot.Ingress.HTTPAddress != fixture.listeners.HTTP || snapshot.Ingress.HTTPSAddress != fixture.listeners.HTTPS {
		t.Fatalf("replacement rebound shared listeners: %#v", snapshot)
	}
	if snapshot.Relays[0].ID != fixture.native[0].ID || snapshot.Relays[0].ListenAddress != fixture.native[0].Listen {
		t.Fatalf("replacement changed native relay: %#v", snapshot.Relays[0])
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("replacement Snapshot().Validate() error = %v", err)
	}
}

// TestRuntimeReplaceHTTPRoutesRejectsInvalidLifecycleAndTopology verifies the narrow API cannot acquire or reshape infrastructure.
func TestRuntimeReplaceHTTPRoutesRejectsInvalidLifecycleAndTopology(t *testing.T) {
	fixture := newRouteReplacementFixture(t)
	var publications int
	originalDNS := fixture.runtime.replaceDNS
	originalIngress := fixture.runtime.replaceIngress
	fixture.runtime.replaceDNS = func(snapshot dnsserver.Snapshot) error {
		publications++
		return originalDNS(snapshot)
	}
	fixture.runtime.replaceIngress = func(snapshot *httpingress.Snapshot) error {
		publications++
		return originalIngress(snapshot)
	}

	if err := fixture.runtime.ReplaceHTTPRoutes(DesiredState{}); err == nil || !strings.Contains(err.Error(), "NewDesiredState") {
		t.Fatalf("ReplaceHTTPRoutes(forged) error = %v", err)
	}

	changedPorts := reserveTCPPorts(t, 2)
	changedListeners := ListenerPlan{DNS: reserveDNSPort(t), HTTP: changedPorts[0], HTTPS: changedPorts[1]}
	changedListenerState := mustDesiredState(t, changedListeners, fixture.next.HTTPRoutes(), fixture.native)
	if err := fixture.runtime.ReplaceHTTPRoutes(changedListenerState); err == nil || !strings.Contains(err.Error(), "listener topology") {
		t.Fatalf("ReplaceHTTPRoutes(changed listeners) error = %v", err)
	}

	changedNative := fixture.next.NativeRoutes()
	changedNative[0].Upstream = fixture.alternateNativeUpstream
	changedNativeState := mustDesiredState(t, fixture.listeners, fixture.next.HTTPRoutes(), changedNative)
	if err := fixture.runtime.ReplaceHTTPRoutes(changedNativeState); err == nil || !strings.Contains(err.Error(), "native route topology") {
		t.Fatalf("ReplaceHTTPRoutes(changed native) error = %v", err)
	}

	changedTTLState, err := NewDesiredState(fixture.listeners, fixture.next.HTTPRoutes(), fixture.native, 3*time.Second)
	if err != nil {
		t.Fatalf("NewDesiredState(changed TTL) error = %v", err)
	}
	if err := fixture.runtime.ReplaceHTTPRoutes(changedTTLState); err == nil || !strings.Contains(err.Error(), "TTL") {
		t.Fatalf("ReplaceHTTPRoutes(changed TTL) error = %v", err)
	}
	if publications != 0 {
		t.Fatalf("rejected replacements published %d component snapshots", publications)
	}

	notStarted := mustRuntime(t, Config{Desired: fixture.current, CertificateProvider: inertCertificateProvider()})
	if err := notStarted.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ReplaceHTTPRoutes(before Start) error = %v, want %v", err, ErrNotReady)
	}
	if err := notStarted.Close(context.Background()); err != nil {
		t.Fatalf("Close(not started) error = %v", err)
	}
	if err := notStarted.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ReplaceHTTPRoutes(after Close) error = %v, want %v", err, ErrNotReady)
	}
	var nilRuntime *Runtime
	if err := nilRuntime.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ReplaceHTTPRoutes(nil) error = %v, want %v", err, ErrNotReady)
	}
}

// TestRuntimeReplaceHTTPRoutesRollsBackPublicationFailures verifies every recoverable partial publication restores the prior generation.
func TestRuntimeReplaceHTTPRoutesRollsBackPublicationFailures(t *testing.T) {
	publishErr := errors.New("synthetic route publication failure")

	t.Run("DNS withdrawal", func(t *testing.T) {
		fixture := newRouteReplacementFixture(t)
		fixture.runtime.replaceDNS = func(dnsserver.Snapshot) error { return publishErr }
		if err := fixture.runtime.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, publishErr) {
			t.Fatalf("ReplaceHTTPRoutes() error = %v, want %v", err, publishErr)
		}
		assertReplacementSnapshot(t, fixture.runtime, fixture.current)
	})

	t.Run("ingress", func(t *testing.T) {
		fixture := newRouteReplacementFixture(t)
		originalDNS := fixture.runtime.replaceDNS
		dnsCalls := 0
		fixture.runtime.replaceDNS = func(snapshot dnsserver.Snapshot) error {
			dnsCalls++
			return originalDNS(snapshot)
		}
		fixture.runtime.replaceIngress = func(*httpingress.Snapshot) error { return publishErr }
		if err := fixture.runtime.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, publishErr) {
			t.Fatalf("ReplaceHTTPRoutes() error = %v, want %v", err, publishErr)
		}
		if dnsCalls != 2 {
			t.Fatalf("DNS publication calls = %d, want withdrawal and rollback", dnsCalls)
		}
		assertReplacementSnapshot(t, fixture.runtime, fixture.current)
	})

	t.Run("DNS additions", func(t *testing.T) {
		fixture := newRouteReplacementFixture(t)
		originalDNS := fixture.runtime.replaceDNS
		originalIngress := fixture.runtime.replaceIngress
		dnsCalls := 0
		ingressCalls := 0
		fixture.runtime.replaceDNS = func(snapshot dnsserver.Snapshot) error {
			dnsCalls++
			if dnsCalls == 2 {
				return publishErr
			}
			return originalDNS(snapshot)
		}
		fixture.runtime.replaceIngress = func(snapshot *httpingress.Snapshot) error {
			ingressCalls++
			return originalIngress(snapshot)
		}
		if err := fixture.runtime.ReplaceHTTPRoutes(fixture.next); !errors.Is(err, publishErr) {
			t.Fatalf("ReplaceHTTPRoutes() error = %v, want %v", err, publishErr)
		}
		if dnsCalls != 3 || ingressCalls != 2 {
			t.Fatalf("publication calls = DNS %d, ingress %d, want 3 and 2", dnsCalls, ingressCalls)
		}
		assertReplacementSnapshot(t, fixture.runtime, fixture.current)
	})
}

// TestRuntimeReplaceHTTPRoutesFailsClosedWhenRollbackFails verifies an unprovable partial generation terminates listener ownership.
func TestRuntimeReplaceHTTPRoutesFailsClosedWhenRollbackFails(t *testing.T) {
	fixture := newRouteReplacementFixture(t)
	publishErr := errors.New("synthetic DNS addition failure")
	rollbackErr := errors.New("synthetic ingress rollback failure")
	originalDNS := fixture.runtime.replaceDNS
	originalIngress := fixture.runtime.replaceIngress
	dnsCalls := 0
	ingressCalls := 0
	fixture.runtime.replaceDNS = func(snapshot dnsserver.Snapshot) error {
		dnsCalls++
		if dnsCalls == 2 {
			return publishErr
		}
		return originalDNS(snapshot)
	}
	fixture.runtime.replaceIngress = func(snapshot *httpingress.Snapshot) error {
		ingressCalls++
		if ingressCalls == 2 {
			return rollbackErr
		}
		return originalIngress(snapshot)
	}

	err := fixture.runtime.ReplaceHTTPRoutes(fixture.next)
	if !errors.Is(err, publishErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("ReplaceHTTPRoutes() error = %v, want publication and rollback failures", err)
	}
	waitRuntimeDone(t, fixture.runtime)
	if terminal := fixture.runtime.Err(); !errors.Is(terminal, publishErr) || !errors.Is(terminal, rollbackErr) {
		t.Fatalf("Err() = %v, want retained publication and rollback failures", terminal)
	}
	snapshot := fixture.runtime.Snapshot()
	if snapshot.State != StateFailed || snapshot.DNS.Running || snapshot.Ingress.Running || snapshot.Relays[0].Running {
		t.Fatalf("fail-closed Snapshot() = %#v", snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("fail-closed Snapshot().Validate() error = %v", err)
	}
	if err := fixture.runtime.ReplaceHTTPRoutes(fixture.current); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ReplaceHTTPRoutes(after failure) error = %v, want %v", err, ErrNotReady)
	}
}

// TestRuntimeReplaceHTTPRoutesIsConcurrentWithSnapshots exercises serialized writers and defensive readers under the race detector.
func TestRuntimeReplaceHTTPRoutesIsConcurrentWithSnapshots(t *testing.T) {
	fixture := newRouteReplacementFixture(t)
	const iterations = 100
	errorsChannel := make(chan error, iterations*6)
	var workers sync.WaitGroup
	for worker := range 4 {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := range iterations {
				next := fixture.current
				if (worker+iteration)%2 == 0 {
					next = fixture.next
				}
				if err := fixture.runtime.ReplaceHTTPRoutes(next); err != nil {
					errorsChannel <- err
				}
			}
		}()
	}
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range iterations {
				if err := fixture.runtime.Snapshot().Validate(); err != nil {
					errorsChannel <- err
				}
			}
		}()
	}
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent route replacement error = %v", err)
	}
}

// newRouteReplacementFixture starts one reusable mixed-route generation and registers bounded cleanup.
func newRouteReplacementFixture(t *testing.T) routeReplacementFixture {
	t.Helper()
	ports := reserveTCPPorts(t, 3)
	listeners := ListenerPlan{DNS: reserveDNSPort(t), HTTP: ports[0], HTTPS: ports[1]}
	excluded := append([]netip.AddrPort{listeners.DNS}, ports...)
	upstreams := reserveDistinctTCPPorts(t, 5, excluded...)
	native := []NativeRoute{{
		ID:       "tcp:mysql",
		Host:     "mysql.app.test",
		Listen:   ports[2],
		Upstream: upstreams[4],
	}}
	current := mustDesiredState(t, listeners, []HTTPRoute{
		{ID: "http:remove", Host: "remove.test", Upstream: upstreams[0]},
		{ID: "http:retain", Host: "retain.test", Upstream: upstreams[1]},
	}, native)
	next := mustDesiredState(t, listeners, []HTTPRoute{
		{ID: "http:add", Host: "add.test", Upstream: upstreams[2]},
		{ID: "http:retain", Host: "retain.test", Upstream: upstreams[3]},
	}, native)
	runtime := mustRuntime(t, Config{Desired: current, CertificateProvider: inertCertificateProvider()})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})
	return routeReplacementFixture{runtime: runtime, listeners: listeners, native: native, current: current, next: next, alternateNativeUpstream: upstreams[0]}
}

// reserveDistinctTCPPorts returns ephemeral loopback endpoints that cannot alias any excluded listener.
func reserveDistinctTCPPorts(t *testing.T, count int, excluded ...netip.AddrPort) []netip.AddrPort {
	t.Helper()
	for range 20 {
		candidates := reserveTCPPorts(t, count)
		valid := true
		for _, candidate := range candidates {
			for _, excludedEndpoint := range excluded {
				if candidate == excludedEndpoint {
					valid = false
					break
				}
			}
			if !valid {
				break
			}
		}
		if valid {
			return candidates
		}
	}
	t.Fatal("could not reserve distinct upstream endpoints")
	return nil
}

// dnsReplacementEvent renders only public record names so publication order failures remain readable.
func dnsReplacementEvent(snapshot dnsserver.Snapshot) string {
	names := make([]string, 0, len(snapshot.Records()))
	for _, record := range snapshot.Records() {
		names = append(names, record.Name)
	}
	return "dns:" + strings.Join(names, ",")
}

// ingressReplacementEvent renders the exact authorized ingress hosts for readable order assertions.
func ingressReplacementEvent(snapshot *httpingress.Snapshot) string {
	return "ingress:" + strings.Join(snapshot.Hosts(), ",")
}

// assertReplacementSnapshot proves a recoverable publication error retained the prior observable generation.
func assertReplacementSnapshot(t *testing.T, runtime *Runtime, desired DesiredState) {
	t.Helper()
	snapshot := runtime.Snapshot()
	if snapshot.State != StateReady || snapshot.DNS.Records != len(desired.DNSRecords()) || snapshot.Ingress.Routes != len(desired.HTTPRoutes()) {
		t.Fatalf("rolled-back Snapshot() = %#v", snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("rolled-back Snapshot().Validate() error = %v", err)
	}
}
