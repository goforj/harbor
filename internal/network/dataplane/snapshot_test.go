package dataplane

import (
	"strings"
	"testing"
)

// TestSnapshotValidationAcceptsReadyAndEmptyRuntimeShapes verifies status supports both active and pre-project daemons.
func TestSnapshotValidationAcceptsReadyAndEmptyRuntimeShapes(t *testing.T) {
	tests := []Snapshot{
		{State: StateReady, Relays: []RelayStatus{}},
		{
			State: StateReady,
			DNS: DNSStatus{
				Configured: true,
				Address:    testEndpoint("127.0.0.1:10530"),
				Running:    true,
				Records:    2,
			},
			Ingress: IngressStatus{
				Configured:   true,
				HTTPAddress:  testEndpoint("127.0.0.1:18080"),
				HTTPSAddress: testEndpoint("127.0.0.1:18443"),
				Running:      true,
				Routes:       1,
			},
			Relays: []RelayStatus{{
				ID:            "tcp:mysql",
				Host:          "mysql.app.test",
				ListenAddress: testEndpoint("127.77.0.10:3306"),
				Upstream:      testEndpoint("127.0.0.1:41006"),
				Running:       true,
			}},
		},
	}
	for index, snapshot := range tests {
		if err := snapshot.Validate(); err != nil {
			t.Fatalf("snapshot %d Validate() error = %v", index, err)
		}
	}
}

// TestSnapshotValidationRejectsInconsistentObservations covers every cross-field lifecycle invariant.
func TestSnapshotValidationRejectsInconsistentObservations(t *testing.T) {
	validRelay := RelayStatus{
		ID:            "tcp:mysql",
		Host:          "mysql.app.test",
		ListenAddress: testEndpoint("127.77.0.10:3306"),
		Upstream:      testEndpoint("127.0.0.1:41006"),
	}
	tests := []struct {
		name     string
		snapshot Snapshot
		want     string
	}{
		{name: "unknown state", snapshot: Snapshot{State: "unknown", Relays: []RelayStatus{}}, want: "unknown data plane state"},
		{name: "nil relays", snapshot: Snapshot{State: StateNew}, want: "must be initialized"},
		{name: "unconfigured DNS data", snapshot: Snapshot{State: StateNew, DNS: DNSStatus{Address: testEndpoint("127.0.0.1:10530")}, Relays: []RelayStatus{}}, want: "unconfigured"},
		{name: "configured DNS absent", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Records: 1}, Relays: []RelayStatus{}}, want: "valid address"},
		{name: "configured DNS no records", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530")}, Relays: []RelayStatus{}}, want: "must contain records"},
		{name: "DNS without routes", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Records: 1}, Relays: []RelayStatus{}}, want: "without configured routes"},
		{name: "unconfigured ingress data", snapshot: Snapshot{State: StateNew, Ingress: IngressStatus{Routes: 1}, Relays: []RelayStatus{}}, want: "unconfigured"},
		{name: "configured ingress absent HTTP", snapshot: Snapshot{State: StateStarting, Ingress: IngressStatus{Configured: true, HTTPSAddress: testEndpoint("127.0.0.1:18443"), Routes: 1}, Relays: []RelayStatus{}}, want: "valid address"},
		{name: "configured ingress absent HTTPS", snapshot: Snapshot{State: StateStarting, Ingress: IngressStatus{Configured: true, HTTPAddress: testEndpoint("127.0.0.1:18080"), Routes: 1}, Relays: []RelayStatus{}}, want: "valid address"},
		{name: "different ingress address", snapshot: Snapshot{State: StateStarting, Ingress: IngressStatus{Configured: true, HTTPAddress: testEndpoint("127.0.0.1:18080"), HTTPSAddress: testEndpoint("127.0.0.2:18443"), Routes: 1}, Relays: []RelayStatus{}}, want: "inconsistent"},
		{name: "ingress no routes", snapshot: Snapshot{State: StateStarting, Ingress: IngressStatus{Configured: true, HTTPAddress: testEndpoint("127.0.0.1:18080"), HTTPSAddress: testEndpoint("127.0.0.1:18443")}, Relays: []RelayStatus{}}, want: "must contain routes"},
		{name: "duplicate relay ID", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{validRelay, {ID: validRelay.ID, Host: "redis.app.test", ListenAddress: testEndpoint("127.77.0.10:6379"), Upstream: testEndpoint("127.0.0.1:41007")}}}, want: "duplicate relay ID"},
		{name: "invalid relay ID", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: "bad id", Host: validRelay.Host, ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream}}}, want: "unsupported character"},
		{name: "duplicate relay host", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{validRelay, {ID: "tcp:redis", Host: validRelay.Host, ListenAddress: testEndpoint("127.77.0.10:6379"), Upstream: testEndpoint("127.0.0.1:41007")}}}, want: "duplicate relay host"},
		{name: "noncanonical relay host", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: "MYSQL.app.test", ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream}}}, want: "must be lowercase"},
		{name: "wildcard relay host", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: "*.app.test", ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream}}}, want: "unsupported character"},
		{name: "relay host outside test", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: "mysql.example.com", ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream}}}, want: "outside the .test zone"},
		{name: "relay order drift", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: "tcp:redis", Host: "redis.app.test", ListenAddress: testEndpoint("127.77.0.10:6379"), Upstream: testEndpoint("127.0.0.1:41007")}, validRelay}}, want: "ordered by host"},
		{name: "duplicate relay listener", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{validRelay, {ID: "tcp:redis", Host: "redis.app.test", ListenAddress: validRelay.ListenAddress, Upstream: testEndpoint("127.0.0.1:41007")}}}, want: "duplicate relay listener"},
		{name: "invalid relay listener", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: validRelay.Host, ListenAddress: testEndpoint("0.0.0.0:3306"), Upstream: validRelay.Upstream}}}, want: "IPv4 loopback"},
		{name: "invalid relay upstream", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: validRelay.Host, ListenAddress: validRelay.ListenAddress, Upstream: testEndpoint("0.0.0.0:41006")}}}, want: "IPv4 loopback"},
		{name: "relay self route", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{{ID: validRelay.ID, Host: validRelay.Host, ListenAddress: validRelay.ListenAddress, Upstream: validRelay.ListenAddress}}}, want: "own listener"},
		{name: "routes without DNS", snapshot: Snapshot{State: StateStarting, Relays: []RelayStatus{validRelay}}, want: "require configured DNS"},
		{name: "DNS count drift", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Records: 2}, Relays: []RelayStatus{validRelay}}, want: "does not match"},
		{name: "relay collides DNS", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: validRelay.ListenAddress, Records: 1}, Relays: []RelayStatus{validRelay}}, want: "collides with DNS"},
		{name: "shared listener collision", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:18080"), Records: 1}, Ingress: IngressStatus{Configured: true, HTTPAddress: testEndpoint("127.0.0.1:18080"), HTTPSAddress: testEndpoint("127.0.0.1:18443"), Routes: 1}, Relays: []RelayStatus{}}, want: "collides with DNS"},
		{name: "relay cross route", snapshot: Snapshot{State: StateStarting, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Records: 2}, Relays: []RelayStatus{{ID: "tcp:mysql", Host: "mysql.app.test", ListenAddress: testEndpoint("127.77.0.10:3306"), Upstream: testEndpoint("127.77.0.10:6379")}, {ID: "tcp:redis", Host: "redis.app.test", ListenAddress: testEndpoint("127.77.0.10:6379"), Upstream: testEndpoint("127.0.0.1:41007")}}}, want: "points to public"},
		{name: "ready stopped DNS", snapshot: Snapshot{State: StateReady, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Records: 1}, Relays: []RelayStatus{{ID: validRelay.ID, Host: validRelay.Host, ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream, Running: true}}}, want: "stopped configured child"},
		{name: "ready stopped ingress", snapshot: Snapshot{State: StateReady, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Running: true, Records: 1}, Ingress: IngressStatus{Configured: true, HTTPAddress: testEndpoint("127.0.0.1:18080"), HTTPSAddress: testEndpoint("127.0.0.1:18443"), Routes: 1}, Relays: []RelayStatus{}}, want: "stopped configured child"},
		{name: "ready stopped relay", snapshot: Snapshot{State: StateReady, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Running: true, Records: 1}, Relays: []RelayStatus{validRelay}}, want: "stopped configured child"},
		{name: "new running relay", snapshot: Snapshot{State: StateNew, DNS: DNSStatus{Configured: true, Address: testEndpoint("127.0.0.1:10530"), Records: 1}, Relays: []RelayStatus{{ID: validRelay.ID, Host: validRelay.Host, ListenAddress: validRelay.ListenAddress, Upstream: validRelay.Upstream, Running: true}}}, want: "running child"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.snapshot.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Snapshot.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestSnapshotConfiguredChildrenRunningCoversEachClass verifies readiness does not omit any child class.
func TestSnapshotConfiguredChildrenRunningCoversEachClass(t *testing.T) {
	if (Snapshot{DNS: DNSStatus{Configured: true}}).configuredChildrenRunning() {
		t.Fatal("configuredChildrenRunning() = true for stopped DNS")
	}
	if (Snapshot{Ingress: IngressStatus{Configured: true}}).configuredChildrenRunning() {
		t.Fatal("configuredChildrenRunning() = true for stopped ingress")
	}
	if (Snapshot{Relays: []RelayStatus{{}}}).configuredChildrenRunning() {
		t.Fatal("configuredChildrenRunning() = true for stopped relay")
	}
}

// TestStateValidationCoversAllValues protects the status contract from accidental lifecycle additions.
func TestStateValidationCoversAllValues(t *testing.T) {
	for _, state := range []State{StateNew, StateStarting, StateReady, StateStopping, StateStopped, StateFailed} {
		if err := state.Validate(); err != nil {
			t.Fatalf("State(%q).Validate() error = %v", state, err)
		}
	}
}
