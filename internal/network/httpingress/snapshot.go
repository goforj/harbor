// Package httpingress routes registered Harbor domains to private loopback HTTP upstreams.
package httpingress

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

const maximumDomainLength = 253

// Route binds one exact public development domain to one private HTTP listener.
type Route struct {
	// Host is the exact registered .test domain presented to clients.
	Host string
	// Upstream is the private loopback HTTP listener owned by a managed project session.
	Upstream netip.AddrPort
}

// Snapshot is an immutable, collision-free ingress routing table.
type Snapshot struct {
	routes map[string]Route
	hosts  []string
}

// NewSnapshot validates and canonicalizes a complete replacement routing table.
func NewSnapshot(routes []Route) (*Snapshot, error) {
	result := &Snapshot{
		routes: make(map[string]Route, len(routes)),
		hosts:  make([]string, 0, len(routes)),
	}
	for index, route := range routes {
		host, err := canonicalDomain(route.Host)
		if err != nil {
			return nil, fmt.Errorf("route %d host: %w", index, err)
		}
		upstream := route.Upstream
		if err := validateUpstream(upstream); err != nil {
			return nil, fmt.Errorf("route %q: %w", host, err)
		}
		upstream = netip.AddrPortFrom(upstream.Addr().Unmap(), upstream.Port())
		if _, exists := result.routes[host]; exists {
			return nil, fmt.Errorf("route host %q is duplicated", host)
		}
		canonical := Route{Host: host, Upstream: upstream}
		result.routes[host] = canonical
		result.hosts = append(result.hosts, host)
	}
	sort.Strings(result.hosts)
	return result, nil
}

// Hosts returns the canonical route names in deterministic order.
func (snapshot *Snapshot) Hosts() []string {
	return append(make([]string, 0, len(snapshot.hosts)), snapshot.hosts...)
}

// Route returns the exact registered route for a DNS host without accepting a port.
func (snapshot *Snapshot) Route(host string) (Route, bool) {
	canonical, err := canonicalDomain(host)
	if err != nil {
		return Route{}, false
	}
	route, found := snapshot.routes[canonical]
	return route, found
}

// routeForAuthority accepts the optional port carried by an HTTP Host header.
func (snapshot *Snapshot) routeForAuthority(authority string) (Route, bool) {
	host, err := hostFromAuthority(authority)
	if err != nil {
		return Route{}, false
	}
	return snapshot.Route(host)
}

// canonicalDomain normalizes case and the DNS absolute-name dot while enforcing Harbor's exact .test policy.
func canonicalDomain(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("domain must not be empty")
	}
	if strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("domain must not contain surrounding whitespace")
	}
	if strings.HasSuffix(raw, ".") {
		raw = strings.TrimSuffix(raw, ".")
	}
	host := strings.ToLower(raw)
	if len(host) > maximumDomainLength {
		return "", fmt.Errorf("domain must not exceed %d bytes", maximumDomainLength)
	}
	if !strings.HasSuffix(host, ".test") || host == ".test" {
		return "", fmt.Errorf("domain %q must be beneath .test", host)
	}
	for _, label := range strings.Split(host, ".") {
		if err := validateDomainLabel(label); err != nil {
			return "", err
		}
	}
	return host, nil
}

// validateDomainLabel keeps ingress names within the portable ASCII DNS label contract.
func validateDomainLabel(label string) error {
	if label == "" {
		return fmt.Errorf("domain labels must not be empty")
	}
	if len(label) > 63 {
		return fmt.Errorf("domain label %q must not exceed 63 bytes", label)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("domain label %q must start and end with a letter or digit", label)
	}
	for _, character := range label {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return fmt.Errorf("domain label %q must contain only ASCII letters, digits, and hyphens", label)
	}
	return nil
}

// validateUpstream prevents ingress configuration from becoming a general-purpose network proxy.
func validateUpstream(upstream netip.AddrPort) error {
	if !upstream.IsValid() {
		return fmt.Errorf("upstream must be a valid address and port")
	}
	address := upstream.Addr().Unmap()
	if !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("upstream %q must use IPv4 loopback", upstream)
	}
	if upstream.Port() == 0 {
		return fmt.Errorf("upstream port must not be zero")
	}
	return nil
}

// hostFromAuthority separates an HTTP authority without interpreting ambiguous colon-bearing input.
func hostFromAuthority(authority string) (string, error) {
	if authority == "" {
		return "", fmt.Errorf("authority must not be empty")
	}
	if strings.Contains(authority, ":") {
		host, port, err := net.SplitHostPort(authority)
		if err != nil || host == "" || port == "" {
			return "", fmt.Errorf("authority %q must contain a valid host and port", authority)
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", fmt.Errorf("authority %q must contain a numeric port", authority)
		}
		return host, nil
	}
	return authority, nil
}
