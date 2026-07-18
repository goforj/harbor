package dataplane

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/dnsserver"
	"github.com/goforj/harbor/internal/network/httpingress"
)

// TestNewDesiredStateDerivesCanonicalDNSAndCopiesInputs verifies routes are one immutable source for every listener projection.
func TestNewDesiredStateDerivesCanonicalDNSAndCopiesInputs(t *testing.T) {
	listeners := testListenerPlan()
	httpRoutes := []HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: testEndpoint("127.0.0.1:41001")}}
	nativeRoutes := []NativeRoute{{
		ID:       "tcp:mysql",
		Host:     "mysql.app.test",
		Listen:   testEndpoint("127.77.0.10:3306"),
		Upstream: testEndpoint("127.0.0.1:41006"),
	}}

	desired, err := NewDesiredState(listeners, httpRoutes, nativeRoutes, 45*time.Second)
	if err != nil {
		t.Fatalf("NewDesiredState() error = %v", err)
	}
	httpRoutes[0].Host = "changed.test"
	nativeRoutes[0].Host = "changed-native.test"

	if desired.Empty() {
		t.Fatal("DesiredState.Empty() = true for configured routes")
	}
	if desired.ListenerPlan() != listeners {
		t.Fatalf("ListenerPlan() = %#v, want %#v", desired.ListenerPlan(), listeners)
	}
	if desired.TTL() != 45*time.Second {
		t.Fatalf("TTL() = %s, want 45s", desired.TTL())
	}
	wantRecords := []string{"app.test=127.0.0.1", "mysql.app.test=127.77.0.10"}
	if got := recordStrings(desired.DNSRecords()); !reflect.DeepEqual(got, wantRecords) {
		t.Fatalf("DNSRecords() = %v, want %v", got, wantRecords)
	}
	if got := desired.HTTPRoutes(); len(got) != 1 || got[0].Host != "app.test" {
		t.Fatalf("HTTPRoutes() = %#v", got)
	} else {
		got[0].Host = "caller-change.test"
	}
	if got := desired.HTTPRoutes()[0].Host; got != "app.test" {
		t.Fatalf("HTTPRoutes() retained caller mutation %q", got)
	}
	if got := desired.NativeRoutes(); len(got) != 1 || got[0].Host != "mysql.app.test" {
		t.Fatalf("NativeRoutes() = %#v", got)
	}
	if err := desired.validate(); err != nil {
		t.Fatalf("DesiredState.validate() error = %v", err)
	}
}

// TestNewDesiredStateAcceptsEmptyGeneration verifies daemon composition can exist before project semantics produce routes.
func TestNewDesiredStateAcceptsEmptyGeneration(t *testing.T) {
	desired, err := NewDesiredState(ListenerPlan{}, nil, nil, 0)
	if err != nil {
		t.Fatalf("NewDesiredState(empty) error = %v", err)
	}
	if !desired.Empty() {
		t.Fatal("DesiredState.Empty() = false")
	}
	if desired.TTL() != dnsserver.DefaultTTL {
		t.Fatalf("TTL() = %s, want %s", desired.TTL(), dnsserver.DefaultTTL)
	}
	if desired.HTTPRoutes() == nil || desired.NativeRoutes() == nil || desired.DNSRecords() == nil {
		t.Fatal("empty desired-state accessors must return initialized collections")
	}
}

// TestNewDesiredStateAcceptsInfrastructureOnlyGeneration keeps installed shared listeners stable between project routes.
func TestNewDesiredStateAcceptsInfrastructureOnlyGeneration(t *testing.T) {
	listeners := testListenerPlan()
	desired, err := NewDesiredState(listeners, nil, nil, 0)
	if err != nil {
		t.Fatalf("NewDesiredState(infrastructure only) error = %v", err)
	}
	if desired.Empty() {
		t.Fatal("DesiredState.Empty() = true for configured shared listeners")
	}
	if desired.ListenerPlan() != listeners {
		t.Fatalf("ListenerPlan() = %#v, want %#v", desired.ListenerPlan(), listeners)
	}
	if records := desired.DNSRecords(); records == nil || len(records) != 0 {
		t.Fatalf("DNSRecords() = %#v, want initialized empty collection", records)
	}
	if err := desired.validate(); err != nil {
		t.Fatalf("DesiredState.validate() error = %v", err)
	}
}

// TestNewDesiredStateCanonicalizesIPv4MappedEndpoints verifies socket collision checks use one IPv4 representation.
func TestNewDesiredStateCanonicalizesIPv4MappedEndpoints(t *testing.T) {
	mapped := func(value string, port uint16) netip.AddrPort {
		return netip.AddrPortFrom(netip.MustParseAddr(value), port)
	}
	desired, err := NewDesiredState(
		ListenerPlan{DNS: mapped("::ffff:127.0.0.1", 10530)},
		nil,
		[]NativeRoute{{
			ID:       "tcp:mysql",
			Host:     "mysql.app.test",
			Listen:   mapped("::ffff:127.77.0.10", 3306),
			Upstream: mapped("::ffff:127.0.0.1", 41006),
		}},
		0,
	)
	if err != nil {
		t.Fatalf("NewDesiredState(mapped) error = %v", err)
	}
	if got := desired.ListenerPlan().DNS.String(); got != "127.0.0.1:10530" {
		t.Fatalf("DNS listener = %s", got)
	}
	if got := desired.NativeRoutes()[0].Listen.String(); got != "127.77.0.10:3306" {
		t.Fatalf("native listener = %s", got)
	}
}

// TestListenerPlanValidationRejectsPartialOrUnsafeSharedSockets covers standalone listener-plan policy.
func TestListenerPlanValidationRejectsPartialOrUnsafeSharedSockets(t *testing.T) {
	tests := []struct {
		name string
		plan ListenerPlan
		want string
	}{
		{name: "HTTP only", plan: ListenerPlan{HTTP: testEndpoint("127.0.0.1:18080")}, want: "configured together"},
		{name: "HTTPS only", plan: ListenerPlan{HTTPS: testEndpoint("127.0.0.1:18443")}, want: "configured together"},
		{name: "different ingress addresses", plan: ListenerPlan{HTTP: testEndpoint("127.0.0.1:18080"), HTTPS: testEndpoint("127.0.0.2:18443")}, want: "shared ingress address"},
		{name: "same ingress socket", plan: ListenerPlan{HTTP: testEndpoint("127.0.0.1:18080"), HTTPS: testEndpoint("127.0.0.1:18080")}, want: "must be distinct"},
		{name: "wildcard", plan: ListenerPlan{DNS: testEndpoint("0.0.0.0:10530")}, want: "IPv4 loopback"},
		{name: "wildcard HTTP", plan: ListenerPlan{HTTP: testEndpoint("0.0.0.0:18080"), HTTPS: testEndpoint("0.0.0.0:18443")}, want: "IPv4 loopback"},
		{name: "wildcard HTTPS", plan: ListenerPlan{HTTP: testEndpoint("127.0.0.1:18080"), HTTPS: testEndpoint("0.0.0.0:18443")}, want: "IPv4 loopback"},
		{name: "IPv6", plan: ListenerPlan{DNS: testEndpoint("[::1]:10530")}, want: "IPv4 loopback"},
		{name: "zero port", plan: ListenerPlan{DNS: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)}, want: "explicit nonzero port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.plan.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ListenerPlan.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewDesiredStateRejectsInconsistentRoutes exercises cross-component invariants before socket acquisition.
func TestNewDesiredStateRejectsInconsistentRoutes(t *testing.T) {
	validHTTP := HTTPRoute{ID: "http:app", Host: "app.test", Upstream: testEndpoint("127.0.0.1:41001")}
	validNative := NativeRoute{ID: "tcp:mysql", Host: "mysql.app.test", Listen: testEndpoint("127.77.0.10:3306"), Upstream: testEndpoint("127.0.0.1:41006")}
	tests := []struct {
		name      string
		listeners ListenerPlan
		http      []HTTPRoute
		native    []NativeRoute
		ttl       time.Duration
		want      string
	}{
		{name: "routes need DNS", listeners: ListenerPlan{HTTP: testEndpoint("127.0.0.1:18080"), HTTPS: testEndpoint("127.0.0.1:18443")}, http: []HTTPRoute{validHTTP}, want: "DNS listener is required"},
		{name: "HTTP needs ingress", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, http: []HTTPRoute{validHTTP}, want: "listeners are required"},
		{name: "noncanonical HTTP host", listeners: testListenerPlan(), http: []HTTPRoute{{ID: validHTTP.ID, Host: "App.Test.", Upstream: validHTTP.Upstream}}, want: "must use canonical form"},
		{name: "wildcard HTTP host", listeners: testListenerPlan(), http: []HTTPRoute{{ID: validHTTP.ID, Host: "*.app.test", Upstream: validHTTP.Upstream}}, want: "unsupported"},
		{name: "noncanonical native host", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: validNative.ID, Host: "MYSQL.app.test", Listen: validNative.Listen, Upstream: validNative.Upstream}}, want: "must be lowercase"},
		{name: "duplicate endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{validHTTP}, native: []NativeRoute{{ID: validHTTP.ID, Host: validNative.Host, Listen: validNative.Listen, Upstream: validNative.Upstream}}, want: "duplicate endpoint ID"},
		{name: "duplicate HTTP endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{validHTTP, {ID: validHTTP.ID, Host: "other.test", Upstream: testEndpoint("127.0.0.1:41002")}}, want: "duplicate endpoint ID"},
		{name: "duplicate native endpoint ID", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{validNative, {ID: validNative.ID, Host: "redis.app.test", Listen: testEndpoint("127.77.0.10:6379"), Upstream: testEndpoint("127.0.0.1:41007")}}, want: "duplicate endpoint ID"},
		{name: "invalid endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{{ID: "bad id", Host: validHTTP.Host, Upstream: validHTTP.Upstream}}, want: "unsupported character"},
		{name: "missing endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{{Host: validHTTP.Host, Upstream: validHTTP.Upstream}}, want: "endpoint ID is required"},
		{name: "long endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{{ID: strings.Repeat("a", maximumEndpointIDLength+1), Host: validHTTP.Host, Upstream: validHTTP.Upstream}}, want: "exceeds"},
		{name: "spaced endpoint ID", listeners: testListenerPlan(), http: []HTTPRoute{{ID: " http:app", Host: validHTTP.Host, Upstream: validHTTP.Upstream}}, want: "surrounding whitespace"},
		{name: "invalid native endpoint ID", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: "bad id", Host: validNative.Host, Listen: validNative.Listen, Upstream: validNative.Upstream}}, want: "unsupported character"},
		{name: "duplicate HTTP host", listeners: testListenerPlan(), http: []HTTPRoute{validHTTP, {ID: "http:other", Host: validHTTP.Host, Upstream: testEndpoint("127.0.0.1:41002")}}, want: "duplicated"},
		{name: "cross-protocol host", listeners: testListenerPlan(), http: []HTTPRoute{validHTTP}, native: []NativeRoute{{ID: validNative.ID, Host: validHTTP.Host, Listen: validNative.Listen, Upstream: validNative.Upstream}}, want: "duplicate name"},
		{name: "duplicate native listener", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{validNative, {ID: "tcp:redis", Host: "redis.app.test", Listen: validNative.Listen, Upstream: testEndpoint("127.0.0.1:41007")}}, want: "collides"},
		{name: "duplicate native host", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{validNative, {ID: "tcp:other", Host: validNative.Host, Listen: testEndpoint("127.77.0.10:3307"), Upstream: testEndpoint("127.0.0.1:41007")}}, want: "duplicate name"},
		{name: "native collides DNS", listeners: ListenerPlan{DNS: validNative.Listen}, native: []NativeRoute{validNative}, want: "collides with DNS"},
		{name: "DNS collides HTTP", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:18080"), HTTP: testEndpoint("127.0.0.1:18080"), HTTPS: testEndpoint("127.0.0.1:18443")}, http: []HTTPRoute{validHTTP}, want: "collides with DNS"},
		{name: "native self route", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: validNative.ID, Host: validNative.Host, Listen: validNative.Listen, Upstream: validNative.Listen}}, want: "points to public"},
		{name: "native cross route", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{validNative, {ID: "tcp:redis", Host: "redis.app.test", Listen: testEndpoint("127.77.0.10:6379"), Upstream: validNative.Listen}}, want: "points to public"},
		{name: "HTTP routes to ingress", listeners: testListenerPlan(), http: []HTTPRoute{{ID: validHTTP.ID, Host: validHTTP.Host, Upstream: testEndpoint("127.0.0.1:18443")}}, want: "points to public"},
		{name: "native wildcard listener", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: validNative.ID, Host: validNative.Host, Listen: testEndpoint("0.0.0.0:3306"), Upstream: validNative.Upstream}}, want: "IPv4 loopback"},
		{name: "native wildcard host", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: validNative.ID, Host: "*.app.test", Listen: validNative.Listen, Upstream: validNative.Upstream}}, want: "wildcard hosts are unsupported"},
		{name: "native zero upstream", listeners: ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")}, native: []NativeRoute{{ID: validNative.ID, Host: validNative.Host, Listen: validNative.Listen, Upstream: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)}}, want: "explicit nonzero port"},
		{name: "short TTL", listeners: testListenerPlan(), http: []HTTPRoute{validHTTP}, ttl: time.Millisecond, want: "TTL"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDesiredState(test.listeners, test.http, test.native, test.ttl)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewDesiredState() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewDesiredStateAllowsSameNativePortOnDistinctIdentities preserves Harbor's port-conflict contract.
func TestNewDesiredStateAllowsSameNativePortOnDistinctIdentities(t *testing.T) {
	desired, err := NewDesiredState(
		ListenerPlan{DNS: testEndpoint("127.0.0.1:10530")},
		nil,
		[]NativeRoute{
			{ID: "tcp:alpha", Host: "mysql.alpha.test", Listen: testEndpoint("127.77.0.10:3306"), Upstream: testEndpoint("127.0.0.1:41006")},
			{ID: "tcp:beta", Host: "mysql.beta.test", Listen: testEndpoint("127.77.0.11:3306"), Upstream: testEndpoint("127.0.0.1:42006")},
		},
		0,
	)
	if err != nil {
		t.Fatalf("NewDesiredState() error = %v", err)
	}
	if got := desired.NativeRoutes(); got[0].Listen.Port() != got[1].Listen.Port() || got[0].Listen.Addr() == got[1].Listen.Addr() {
		t.Fatalf("native routes = %#v", got)
	}
}

// TestDesiredStateRejectsForgedZeroValue protects runtime construction from bypassing constructors.
func TestDesiredStateRejectsForgedZeroValue(t *testing.T) {
	if err := (DesiredState{}).validate(); err == nil || !strings.Contains(err.Error(), "NewDesiredState") {
		t.Fatalf("DesiredState{}.validate() error = %v", err)
	}
}

// TestDesiredStateRejectsInconsistentDerivedSnapshots protects package-local construction from stale projections.
func TestDesiredStateRejectsInconsistentDerivedSnapshots(t *testing.T) {
	desired := mustDesiredState(
		t,
		testListenerPlan(),
		[]HTTPRoute{{ID: "http:app", Host: "app.test", Upstream: testEndpoint("127.0.0.1:41001")}},
		nil,
	)
	t.Run("DNS", func(t *testing.T) {
		forged := desired
		snapshot, err := dnsserver.NewSnapshot(
			[]dnsserver.Record{{Name: "app.test", Address: testEndpoint("127.0.0.9:18443").Addr()}},
			forged.TTL(),
		)
		if err != nil {
			t.Fatalf("NewSnapshot() error = %v", err)
		}
		forged.dnsSnapshot = snapshot
		if err := forged.validate(); err == nil || !strings.Contains(err.Error(), "DNS records are inconsistent") {
			t.Fatalf("forged DNS validate() error = %v", err)
		}
	})
	t.Run("DNS count", func(t *testing.T) {
		forged := desired
		snapshot, err := dnsserver.NewSnapshot(nil, forged.TTL())
		if err != nil {
			t.Fatalf("NewSnapshot() error = %v", err)
		}
		forged.dnsSnapshot = snapshot
		if err := forged.validate(); err == nil || !strings.Contains(err.Error(), "DNS records are inconsistent") {
			t.Fatalf("forged DNS validate() error = %v", err)
		}
	})
	t.Run("ingress", func(t *testing.T) {
		forged := desired
		snapshot, err := httpingress.NewSnapshot([]httpingress.Route{{Host: "app.test", Upstream: testEndpoint("127.0.0.1:41002")}})
		if err != nil {
			t.Fatalf("NewSnapshot() error = %v", err)
		}
		forged.ingressSnapshot = snapshot
		if err := forged.validate(); err == nil || !strings.Contains(err.Error(), "ingress routes are inconsistent") {
			t.Fatalf("forged ingress validate() error = %v", err)
		}
	})
	t.Run("ingress count", func(t *testing.T) {
		forged := desired
		snapshot, err := httpingress.NewSnapshot(nil)
		if err != nil {
			t.Fatalf("NewSnapshot() error = %v", err)
		}
		forged.ingressSnapshot = snapshot
		if err := forged.validate(); err == nil || !strings.Contains(err.Error(), "ingress routes are inconsistent") {
			t.Fatalf("forged ingress validate() error = %v", err)
		}
	})
	t.Run("rebuilt", func(t *testing.T) {
		forged := desired
		forged.ttl = time.Millisecond
		if err := forged.validate(); err == nil || !strings.Contains(err.Error(), "TTL") {
			t.Fatalf("forged desired validate() error = %v", err)
		}
	})
}

// testListenerPlan returns distinct explicit high ports suitable for pure desired-state tests.
func testListenerPlan() ListenerPlan {
	return ListenerPlan{
		DNS:   testEndpoint("127.0.0.1:10530"),
		HTTP:  testEndpoint("127.0.0.1:18080"),
		HTTPS: testEndpoint("127.0.0.1:18443"),
	}
}

// testEndpoint parses a compile-time endpoint and panics only when test source is malformed.
func testEndpoint(value string) netip.AddrPort {
	return netip.MustParseAddrPort(value)
}

// recordStrings renders exact derived records without coupling assertions to private snapshot fields.
func recordStrings(records []dnsserver.Record) []string {
	result := make([]string, 0, len(records))
	for _, record := range records {
		result = append(result, record.Name+"="+record.Address.String())
	}
	return result
}
