package dataplane

import (
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/network/dnsserver"
)

// State identifies one data-plane runtime's process-local lifecycle.
type State string

const (
	// StateNew means no listener acquisition has been attempted.
	StateNew State = "new"
	// StateStarting means validated listeners and child servers are being acquired.
	StateStarting State = "starting"
	// StateReady means every configured child currently owns its exact listener.
	StateReady State = "ready"
	// StateStopping means an intentional shutdown is draining child servers.
	StateStopping State = "stopping"
	// StateStopped means shutdown completed without a terminal runtime failure.
	StateStopped State = "stopped"
	// StateFailed means startup, a child listener, or shutdown failed.
	StateFailed State = "failed"
)

// Validate reports whether the lifecycle state is recognized.
func (state State) Validate() error {
	switch state {
	case StateNew, StateStarting, StateReady, StateStopping, StateStopped, StateFailed:
		return nil
	default:
		return fmt.Errorf("unknown data plane state %q", state)
	}
}

// DNSStatus reports authoritative listener ownership without exposing server internals.
type DNSStatus struct {
	// Configured reports whether the desired generation contains any exact DNS records.
	Configured bool
	// Address is the exact shared UDP and TCP listener when configured.
	Address netip.AddrPort
	// Running reports whether both DNS transports currently own Address.
	Running bool
	// Records is the number of exact names in the immutable record snapshot.
	Records int
}

// IngressStatus reports shared HTTP and HTTPS listener ownership.
type IngressStatus struct {
	// Configured reports whether the desired generation contains HTTP-class routes.
	Configured bool
	// HTTPAddress is the exact redirect listener when configured.
	HTTPAddress netip.AddrPort
	// HTTPSAddress is the exact Host/SNI ingress listener when configured.
	HTTPSAddress netip.AddrPort
	// Running reports whether both paired listeners are serving.
	Running bool
	// Routes is the number of exact Host/SNI routes.
	Routes int
}

// RelayStatus reports one native TCP relay's exact route and payload-free counters.
type RelayStatus struct {
	// ID is the stable opaque endpoint identity from desired state.
	ID string
	// Host is the exact DNS name derived for the public native listener.
	Host string
	// ListenAddress is the exact project loopback socket owned by the relay.
	ListenAddress netip.AddrPort
	// Upstream is the fixed private loopback destination for new connections.
	Upstream netip.AddrPort
	// Running reports whether the relay currently owns ListenAddress.
	Running bool
	// ActiveConnections is the number of admitted connections not yet closed.
	ActiveConnections uint64
	// AcceptedConnections is the lifetime number of admitted client connections.
	AcceptedConnections uint64
	// CompletedConnections is the lifetime number of closed client connections.
	CompletedConnections uint64
	// DialFailures is the lifetime number of private-upstream connection failures.
	DialFailures uint64
	// ClientBytes is the lifetime number of bytes copied from clients to upstreams.
	ClientBytes uint64
	// UpstreamBytes is the lifetime number of bytes copied from upstreams to clients.
	UpstreamBytes uint64
	// DroppedDiagnostics is the lifetime number of bounded observer events that were discarded.
	DroppedDiagnostics uint64
}

// Snapshot is a concurrency-safe, payload-free observation of one data-plane runtime.
type Snapshot struct {
	// State is the process-local lifecycle state represented by this observation.
	State State
	// DNS reports the authoritative exact-name server.
	DNS DNSStatus
	// Ingress reports the paired HTTP redirect and HTTPS proxy.
	Ingress IngressStatus
	// Relays reports native endpoints in canonical host and identity order.
	Relays []RelayStatus
}

// configuredChildrenRunning reports one coherent readiness predicate across every child class.
func (snapshot Snapshot) configuredChildrenRunning() bool {
	if (snapshot.DNS.Configured && !snapshot.DNS.Running) || (snapshot.Ingress.Configured && !snapshot.Ingress.Running) {
		return false
	}
	for _, relay := range snapshot.Relays {
		if !relay.Running {
			return false
		}
	}
	return true
}

// Validate reports whether a snapshot is structurally consistent and loopback-only.
func (snapshot Snapshot) Validate() error {
	if err := snapshot.State.Validate(); err != nil {
		return err
	}
	if snapshot.Relays == nil {
		return fmt.Errorf("data plane snapshot relays must be initialized")
	}
	if err := validateDNSStatus(snapshot.DNS); err != nil {
		return err
	}
	if err := validateIngressStatus(snapshot.Ingress); err != nil {
		return err
	}

	ids := make(map[string]struct{}, len(snapshot.Relays))
	hosts := make(map[string]struct{}, len(snapshot.Relays))
	listeners := make(map[netip.AddrPort]struct{}, len(snapshot.Relays))
	allRunning := !snapshot.DNS.Configured || snapshot.DNS.Running
	allRunning = allRunning && (!snapshot.Ingress.Configured || snapshot.Ingress.Running)
	anyRunning := snapshot.DNS.Running || snapshot.Ingress.Running
	for index, relay := range snapshot.Relays {
		if err := validateEndpointID(relay.ID); err != nil {
			return fmt.Errorf("data plane relay snapshot %d: %w", index, err)
		}
		if _, duplicate := ids[relay.ID]; duplicate {
			return fmt.Errorf("data plane snapshot contains duplicate relay ID %q", relay.ID)
		}
		ids[relay.ID] = struct{}{}
		if err := validateLoopbackEndpoint("data plane relay listener", relay.ListenAddress); err != nil {
			return err
		}
		if err := validateLoopbackEndpoint("data plane relay upstream", relay.Upstream); err != nil {
			return err
		}
		if err := validateRelayHost(relay.Host, relay.ListenAddress.Addr()); err != nil {
			return fmt.Errorf("data plane relay snapshot %q host: %w", relay.ID, err)
		}
		if index > 0 && relayStatusLess(relay, snapshot.Relays[index-1]) {
			return fmt.Errorf("data plane snapshot relays must be ordered by host and endpoint ID")
		}
		if _, duplicate := hosts[relay.Host]; duplicate {
			return fmt.Errorf("data plane snapshot contains duplicate relay host %q", relay.Host)
		}
		hosts[relay.Host] = struct{}{}
		if relay.ListenAddress == relay.Upstream {
			return fmt.Errorf("data plane relay snapshot %q routes to its own listener", relay.ID)
		}
		if _, duplicate := listeners[relay.ListenAddress]; duplicate {
			return fmt.Errorf("data plane snapshot contains duplicate relay listener %s", relay.ListenAddress)
		}
		listeners[relay.ListenAddress] = struct{}{}
		allRunning = allRunning && relay.Running
		anyRunning = anyRunning || relay.Running
	}
	routes := len(snapshot.Relays)
	if snapshot.Ingress.Configured {
		routes += snapshot.Ingress.Routes
	}
	if routes == 0 && snapshot.DNS.Configured {
		return fmt.Errorf("data plane DNS status has records without configured routes")
	}
	if routes != 0 && !snapshot.DNS.Configured {
		return fmt.Errorf("data plane snapshot routes require configured DNS")
	}
	if snapshot.DNS.Configured && snapshot.DNS.Records != routes {
		return fmt.Errorf("data plane DNS record count %d does not match %d configured routes", snapshot.DNS.Records, routes)
	}
	if err := validateSnapshotSockets(snapshot); err != nil {
		return err
	}
	if snapshot.State == StateReady && !allRunning {
		return fmt.Errorf("ready data plane snapshot contains a stopped configured child")
	}
	if (snapshot.State == StateNew || snapshot.State == StateStopped) && anyRunning {
		return fmt.Errorf("%s data plane snapshot contains a running child", snapshot.State)
	}
	return nil
}

// validateSnapshotSockets repeats public collision and cycle checks without exposing route payloads.
func validateSnapshotSockets(snapshot Snapshot) error {
	public := make(map[netip.AddrPort]string, 3+len(snapshot.Relays))
	for _, candidate := range []struct {
		name     string
		endpoint netip.AddrPort
	}{
		{name: "DNS", endpoint: snapshot.DNS.Address},
		{name: "HTTP", endpoint: snapshot.Ingress.HTTPAddress},
		{name: "HTTPS", endpoint: snapshot.Ingress.HTTPSAddress},
	} {
		if candidate.endpoint == (netip.AddrPort{}) {
			continue
		}
		if owner, duplicate := public[candidate.endpoint]; duplicate {
			return fmt.Errorf("data plane snapshot %s listener %s collides with %s", candidate.name, candidate.endpoint, owner)
		}
		public[candidate.endpoint] = candidate.name
	}
	for _, relay := range snapshot.Relays {
		if owner, duplicate := public[relay.ListenAddress]; duplicate {
			return fmt.Errorf("data plane snapshot relay %q listener %s collides with %s", relay.ID, relay.ListenAddress, owner)
		}
		public[relay.ListenAddress] = "relay " + relay.ID
	}
	for _, relay := range snapshot.Relays {
		if owner, found := public[relay.Upstream]; found {
			return fmt.Errorf("data plane snapshot relay %q upstream %s points to public %s listener", relay.ID, relay.Upstream, owner)
		}
	}
	return nil
}

// validateRelayHost reuses the authoritative DNS policy so status cannot claim an unpublishable name.
func validateRelayHost(host string, address netip.Addr) error {
	_, err := dnsserver.NewSnapshot(
		[]dnsserver.Record{{Name: host, Address: address}},
		dnsserver.DefaultTTL,
	)
	return err
}

// relayStatusLess defines the stable order promised by Snapshot.Relays.
func relayStatusLess(left RelayStatus, right RelayStatus) bool {
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	return left.ID < right.ID
}

// validateDNSStatus keeps absent and configured DNS representations unambiguous.
func validateDNSStatus(status DNSStatus) error {
	if !status.Configured {
		if status.Address != (netip.AddrPort{}) || status.Running || status.Records != 0 {
			return fmt.Errorf("unconfigured data plane DNS status contains listener state")
		}
		return nil
	}
	if err := validateLoopbackEndpoint("data plane DNS status", status.Address); err != nil {
		return err
	}
	if status.Records <= 0 {
		return fmt.Errorf("configured data plane DNS status must contain records")
	}
	return nil
}

// validateIngressStatus keeps the paired HTTP listener shape exact.
func validateIngressStatus(status IngressStatus) error {
	if !status.Configured {
		if status.HTTPAddress != (netip.AddrPort{}) || status.HTTPSAddress != (netip.AddrPort{}) || status.Running || status.Routes != 0 {
			return fmt.Errorf("unconfigured data plane ingress status contains listener state")
		}
		return nil
	}
	if err := validateLoopbackEndpoint("data plane HTTP status", status.HTTPAddress); err != nil {
		return err
	}
	if err := validateLoopbackEndpoint("data plane HTTPS status", status.HTTPSAddress); err != nil {
		return err
	}
	if status.HTTPAddress.Addr() != status.HTTPSAddress.Addr() || status.HTTPAddress == status.HTTPSAddress {
		return fmt.Errorf("configured data plane ingress status contains inconsistent paired listeners")
	}
	if status.Routes <= 0 {
		return fmt.Errorf("configured data plane ingress status must contain routes")
	}
	return nil
}
