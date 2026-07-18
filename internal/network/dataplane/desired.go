package dataplane

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/network/dnsserver"
	"github.com/goforj/harbor/internal/network/httpingress"
)

const maximumEndpointIDLength = 128

// ListenerPlan identifies the exact shared DNS and HTTP sockets Harbor will own.
//
// DNS is required whenever any route exists. HTTP and HTTPS are always configured as a pair.
// Shared listeners may remain configured before routes are published so host integration
// does not flap as projects register and unregister. Native listeners belong to routes.
type ListenerPlan struct {
	// DNS is the shared authoritative UDP and TCP socket for derived exact-name records.
	DNS netip.AddrPort
	// HTTP is the shared redirect listener for every registered HTTP-class route.
	HTTP netip.AddrPort
	// HTTPS is the shared TLS ingress listener for every registered HTTP-class route.
	HTTPS netip.AddrPort
}

// Validate reports whether every configured listener is canonical IPv4 loopback and the HTTP pair is consistent.
func (plan ListenerPlan) Validate() error {
	if err := validateOptionalListener("DNS", plan.DNS); err != nil {
		return err
	}
	if err := validateOptionalListener("HTTP", plan.HTTP); err != nil {
		return err
	}
	if err := validateOptionalListener("HTTPS", plan.HTTPS); err != nil {
		return err
	}

	hasHTTP := plan.HTTP != (netip.AddrPort{})
	hasHTTPS := plan.HTTPS != (netip.AddrPort{})
	if hasHTTP != hasHTTPS {
		return fmt.Errorf("data plane listener plan: HTTP and HTTPS listeners must be configured together")
	}
	if hasHTTP {
		if plan.HTTP.Addr() != plan.HTTPS.Addr() {
			return fmt.Errorf("data plane listener plan: HTTP and HTTPS must use one shared ingress address")
		}
		if plan.HTTP == plan.HTTPS {
			return fmt.Errorf("data plane listener plan: HTTP and HTTPS listeners must be distinct")
		}
	}
	return nil
}

// HTTPRoute maps one exact public development host to one private loopback HTTP upstream.
type HTTPRoute struct {
	// ID is an opaque stable endpoint identity used only for diagnostics and ordering.
	ID string
	// Host is the canonical lowercase exact .test name presented through Host and SNI.
	Host string
	// Upstream is the private loopback HTTP listener selected for Host.
	Upstream netip.AddrPort
}

// NativeRoute maps one exact public development host and native socket to one private loopback upstream.
type NativeRoute struct {
	// ID is an opaque stable endpoint identity used only for diagnostics and ordering.
	ID string
	// Host is the canonical lowercase exact .test name published by Harbor DNS.
	Host string
	// Listen is the exact project loopback address and native public port Harbor owns.
	Listen netip.AddrPort
	// Upstream is the private loopback publication that receives unmodified TCP bytes.
	Upstream netip.AddrPort
}

// DesiredState is one immutable, validated network generation.
//
// It deliberately contains exact endpoints rather than GoForj project or service semantics.
// A later reconciler remains responsible for proving ownership before constructing this value.
type DesiredState struct {
	listeners       ListenerPlan
	httpRoutes      []HTTPRoute
	nativeRoutes    []NativeRoute
	dnsSnapshot     dnsserver.Snapshot
	ingressSnapshot *httpingress.Snapshot
	ttl             time.Duration
	valid           bool
}

// NewDesiredState validates, canonicalizes, and defensively copies one complete data-plane generation.
func NewDesiredState(
	listeners ListenerPlan,
	httpRoutes []HTTPRoute,
	nativeRoutes []NativeRoute,
	ttl time.Duration,
) (DesiredState, error) {
	if ttl == 0 {
		ttl = dnsserver.DefaultTTL
	}
	listeners = canonicalListenerPlan(listeners)
	if err := listeners.Validate(); err != nil {
		return DesiredState{}, err
	}
	if err := validateListenerRequirements(listeners, len(httpRoutes), len(nativeRoutes)); err != nil {
		return DesiredState{}, err
	}

	canonicalHTTP, ingressSnapshot, err := canonicalHTTPRoutes(httpRoutes)
	if err != nil {
		return DesiredState{}, err
	}
	canonicalNative, err := canonicalNativeRoutes(nativeRoutes)
	if err != nil {
		return DesiredState{}, err
	}
	if err := validateEndpointIDs(canonicalHTTP, canonicalNative); err != nil {
		return DesiredState{}, err
	}
	if err := validatePublicSockets(listeners, canonicalNative); err != nil {
		return DesiredState{}, err
	}
	if err := validateNoSelfRouting(listeners, canonicalHTTP, canonicalNative); err != nil {
		return DesiredState{}, err
	}

	records := derivedDNSRecords(listeners, canonicalHTTP, canonicalNative)
	dnsSnapshot, err := dnsserver.NewSnapshot(records, ttl)
	if err != nil {
		return DesiredState{}, fmt.Errorf("data plane desired state: %w", err)
	}

	return DesiredState{
		listeners:       listeners,
		httpRoutes:      canonicalHTTP,
		nativeRoutes:    canonicalNative,
		dnsSnapshot:     dnsSnapshot,
		ingressSnapshot: ingressSnapshot,
		ttl:             ttl,
		valid:           true,
	}, nil
}

// ListenerPlan returns the exact shared sockets without exposing mutable desired-state internals.
func (state DesiredState) ListenerPlan() ListenerPlan {
	return state.listeners
}

// HTTPRoutes returns a canonical copy ordered by host and endpoint identity.
func (state DesiredState) HTTPRoutes() []HTTPRoute {
	return append(make([]HTTPRoute, 0, len(state.httpRoutes)), state.httpRoutes...)
}

// NativeRoutes returns a canonical copy ordered by host and endpoint identity.
func (state DesiredState) NativeRoutes() []NativeRoute {
	return append(make([]NativeRoute, 0, len(state.nativeRoutes)), state.nativeRoutes...)
}

// DNSRecords returns the exact records derived from the HTTP and native route tables.
func (state DesiredState) DNSRecords() []dnsserver.Record {
	records := state.dnsSnapshot.Records()
	return append(make([]dnsserver.Record, 0, len(records)), records...)
}

// TTL returns the bounded DNS cache lifetime for the desired generation.
func (state DesiredState) TTL() time.Duration {
	return state.ttl
}

// Empty reports whether this generation intentionally owns no network listeners.
func (state DesiredState) Empty() bool {
	return state.listeners == (ListenerPlan{}) && len(state.httpRoutes) == 0 && len(state.nativeRoutes) == 0
}

// validate rejects forged zero values before they can acquire listeners.
func (state DesiredState) validate() error {
	if !state.valid || state.ingressSnapshot == nil {
		return fmt.Errorf("data plane desired state: initialize with NewDesiredState")
	}
	rebuilt, err := NewDesiredState(state.listeners, state.httpRoutes, state.nativeRoutes, state.ttl)
	if err != nil {
		return err
	}
	if rebuilt.dnsSnapshot.TTL() != state.dnsSnapshot.TTL() || !sameDNSRecords(rebuilt.dnsSnapshot.Records(), state.dnsSnapshot.Records()) {
		return fmt.Errorf("data plane desired state: derived DNS records are inconsistent")
	}
	if !sameIngressRoutes(state.ingressSnapshot, rebuilt.httpRoutes) {
		return fmt.Errorf("data plane desired state: derived ingress routes are inconsistent")
	}
	return nil
}

// sameDNSRecords compares canonical projections without exposing mutable snapshot internals.
func sameDNSRecords(left []dnsserver.Record, right []dnsserver.Record) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// sameIngressRoutes proves the retained router table still matches the desired HTTP routes exactly.
func sameIngressRoutes(snapshot *httpingress.Snapshot, routes []HTTPRoute) bool {
	if snapshot == nil {
		return false
	}
	hosts := snapshot.Hosts()
	if len(hosts) != len(routes) {
		return false
	}
	for index, route := range routes {
		resolved, found := snapshot.Route(route.Host)
		if !found || hosts[index] != route.Host || resolved.Host != route.Host || resolved.Upstream != route.Upstream {
			return false
		}
	}
	return true
}

// canonicalListenerPlan removes IPv4-mapped representations before collision checks.
func canonicalListenerPlan(plan ListenerPlan) ListenerPlan {
	plan.DNS = canonicalAddressPort(plan.DNS)
	plan.HTTP = canonicalAddressPort(plan.HTTP)
	plan.HTTPS = canonicalAddressPort(plan.HTTPS)
	return plan
}

// canonicalAddressPort preserves the exact zero value while normalizing IPv4-mapped addresses.
func canonicalAddressPort(endpoint netip.AddrPort) netip.AddrPort {
	if endpoint == (netip.AddrPort{}) {
		return endpoint
	}
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
}

// validateOptionalListener distinguishes an absent socket from a malformed configured socket.
func validateOptionalListener(name string, endpoint netip.AddrPort) error {
	if endpoint == (netip.AddrPort{}) {
		return nil
	}
	return validateLoopbackEndpoint("data plane "+name+" listener", endpoint)
}

// validateLoopbackEndpoint prevents any route or listener from widening beyond IPv4 loopback.
func validateLoopbackEndpoint(name string, endpoint netip.AddrPort) error {
	if !endpoint.IsValid() {
		return fmt.Errorf("%s must be a valid address and port", name)
	}
	address := endpoint.Addr().Unmap()
	if !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("%s %q must use IPv4 loopback", name, endpoint)
	}
	if endpoint.Port() == 0 {
		return fmt.Errorf("%s must use an explicit nonzero port", name)
	}
	return nil
}

// validateListenerRequirements prevents dormant or partial shared listener configurations.
func validateListenerRequirements(plan ListenerPlan, httpRoutes int, nativeRoutes int) error {
	hasRoutes := httpRoutes != 0 || nativeRoutes != 0
	hasDNS := plan.DNS != (netip.AddrPort{})
	hasIngress := plan.HTTP != (netip.AddrPort{}) || plan.HTTPS != (netip.AddrPort{})
	if hasRoutes && !hasDNS {
		return fmt.Errorf("data plane desired state: DNS listener is required when routes exist")
	}
	if httpRoutes != 0 && !hasIngress {
		return fmt.Errorf("data plane desired state: HTTP and HTTPS listeners are required for HTTP routes")
	}
	return nil
}

// canonicalHTTPRoutes delegates host and upstream policy to the exact ingress snapshot.
func canonicalHTTPRoutes(routes []HTTPRoute) ([]HTTPRoute, *httpingress.Snapshot, error) {
	ingressRoutes := make([]httpingress.Route, 0, len(routes))
	for index, route := range routes {
		if strings.Contains(route.Host, "*") {
			return nil, nil, fmt.Errorf("data plane desired state: HTTP route %d host %q: wildcard hosts are unsupported", index, route.Host)
		}
		ingressRoutes = append(ingressRoutes, httpingress.Route{Host: route.Host, Upstream: route.Upstream})
	}
	snapshot, err := httpingress.NewSnapshot(ingressRoutes)
	if err != nil {
		return nil, nil, fmt.Errorf("data plane desired state: %w", err)
	}

	canonical := make([]HTTPRoute, 0, len(routes))
	for index, route := range routes {
		resolved, found := snapshot.Route(route.Host)
		if !found {
			return nil, nil, fmt.Errorf("data plane desired state: HTTP route %d could not be recovered from its validated snapshot", index)
		}
		if route.Host != resolved.Host {
			return nil, nil, fmt.Errorf("data plane desired state: HTTP host %q must use canonical form %q", route.Host, resolved.Host)
		}
		canonical = append(canonical, HTTPRoute{ID: route.ID, Host: resolved.Host, Upstream: resolved.Upstream})
	}
	sort.Slice(canonical, func(left int, right int) bool {
		if canonical[left].Host != canonical[right].Host {
			return canonical[left].Host < canonical[right].Host
		}
		return canonical[left].ID < canonical[right].ID
	})
	return canonical, snapshot, nil
}

// canonicalNativeRoutes validates exact sockets before DNS derivation and relay construction.
func canonicalNativeRoutes(routes []NativeRoute) ([]NativeRoute, error) {
	canonical := make([]NativeRoute, 0, len(routes))
	for index, route := range routes {
		if strings.Contains(route.Host, "*") {
			return nil, fmt.Errorf("data plane desired state: native route %d host %q: wildcard hosts are unsupported", index, route.Host)
		}
		route.Listen = canonicalAddressPort(route.Listen)
		route.Upstream = canonicalAddressPort(route.Upstream)
		if err := validateLoopbackEndpoint("native route listener", route.Listen); err != nil {
			return nil, fmt.Errorf("data plane desired state: native route %d: %w", index, err)
		}
		if err := validateLoopbackEndpoint("native route upstream", route.Upstream); err != nil {
			return nil, fmt.Errorf("data plane desired state: native route %d: %w", index, err)
		}
		canonical = append(canonical, route)
	}
	sort.Slice(canonical, func(left int, right int) bool {
		if canonical[left].Host != canonical[right].Host {
			return canonical[left].Host < canonical[right].Host
		}
		return canonical[left].ID < canonical[right].ID
	})
	return canonical, nil
}

// validateEndpointIDs gives every child a stable, printable diagnostic identity.
func validateEndpointIDs(httpRoutes []HTTPRoute, nativeRoutes []NativeRoute) error {
	seen := make(map[string]struct{}, len(httpRoutes)+len(nativeRoutes))
	for _, route := range httpRoutes {
		if err := validateEndpointID(route.ID); err != nil {
			return fmt.Errorf("data plane desired state: HTTP route: %w", err)
		}
		if _, duplicate := seen[route.ID]; duplicate {
			return fmt.Errorf("data plane desired state: duplicate endpoint ID %q", route.ID)
		}
		seen[route.ID] = struct{}{}
	}
	for _, route := range nativeRoutes {
		if err := validateEndpointID(route.ID); err != nil {
			return fmt.Errorf("data plane desired state: native route: %w", err)
		}
		if _, duplicate := seen[route.ID]; duplicate {
			return fmt.Errorf("data plane desired state: duplicate endpoint ID %q", route.ID)
		}
		seen[route.ID] = struct{}{}
	}
	return nil
}

// validateEndpointID keeps runtime labels bounded without assigning project semantics to them.
func validateEndpointID(id string) error {
	if id == "" {
		return fmt.Errorf("endpoint ID is required")
	}
	if len(id) > maximumEndpointIDLength {
		return fmt.Errorf("endpoint ID %q exceeds %d bytes", id, maximumEndpointIDLength)
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("endpoint ID %q must not contain surrounding whitespace", id)
	}
	for _, character := range id {
		alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphanumeric && character != '.' && character != '_' && character != ':' && character != '-' {
			return fmt.Errorf("endpoint ID %q contains an unsupported character", id)
		}
	}
	return nil
}

// validatePublicSockets rejects collisions before any listener can be acquired.
func validatePublicSockets(plan ListenerPlan, nativeRoutes []NativeRoute) error {
	owners := make(map[netip.AddrPort]string, 3+len(nativeRoutes))
	for _, candidate := range []struct {
		name     string
		endpoint netip.AddrPort
	}{
		{name: "DNS", endpoint: plan.DNS},
		{name: "HTTP", endpoint: plan.HTTP},
		{name: "HTTPS", endpoint: plan.HTTPS},
	} {
		if candidate.endpoint == (netip.AddrPort{}) {
			continue
		}
		if owner, duplicate := owners[candidate.endpoint]; duplicate {
			return fmt.Errorf("data plane desired state: %s listener %s collides with %s", candidate.name, candidate.endpoint, owner)
		}
		owners[candidate.endpoint] = candidate.name
	}
	for _, route := range nativeRoutes {
		if owner, duplicate := owners[route.Listen]; duplicate {
			return fmt.Errorf("data plane desired state: native route %q listener %s collides with %s", route.ID, route.Listen, owner)
		}
		owners[route.Listen] = "native route " + route.ID
	}
	return nil
}

// validateNoSelfRouting prevents exact public sockets from becoming upstream cycles.
func validateNoSelfRouting(plan ListenerPlan, httpRoutes []HTTPRoute, nativeRoutes []NativeRoute) error {
	public := make(map[netip.AddrPort]string, 3+len(nativeRoutes))
	for _, candidate := range []struct {
		name     string
		endpoint netip.AddrPort
	}{
		{name: "DNS", endpoint: plan.DNS},
		{name: "HTTP", endpoint: plan.HTTP},
		{name: "HTTPS", endpoint: plan.HTTPS},
	} {
		if candidate.endpoint != (netip.AddrPort{}) {
			public[candidate.endpoint] = candidate.name
		}
	}
	for _, route := range nativeRoutes {
		public[route.Listen] = "native route " + route.ID
	}
	for _, route := range httpRoutes {
		if owner, found := public[route.Upstream]; found {
			return fmt.Errorf("data plane desired state: HTTP route %q upstream %s points to public %s listener", route.ID, route.Upstream, owner)
		}
	}
	for _, route := range nativeRoutes {
		if owner, found := public[route.Upstream]; found {
			return fmt.Errorf("data plane desired state: native route %q upstream %s points to public %s listener", route.ID, route.Upstream, owner)
		}
	}
	return nil
}

// derivedDNSRecords makes DNS a projection of routes rather than an independently mutable input.
func derivedDNSRecords(plan ListenerPlan, httpRoutes []HTTPRoute, nativeRoutes []NativeRoute) []dnsserver.Record {
	records := make([]dnsserver.Record, 0, len(httpRoutes)+len(nativeRoutes))
	for _, route := range httpRoutes {
		records = append(records, dnsserver.Record{Name: route.Host, Address: plan.HTTPS.Addr()})
	}
	for _, route := range nativeRoutes {
		records = append(records, dnsserver.Record{Name: route.Host, Address: route.Listen.Addr()})
	}
	return records
}
