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
