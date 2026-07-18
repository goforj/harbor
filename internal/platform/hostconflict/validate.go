package hostconflict

import (
	"fmt"
	"net/netip"
)

// validateRoutes proves the route snapshot is a bounded candidate-matching multiset with an identifiable selection.
func validateRoutes(observation Observation) error {
	snapshot := observation.Routes
	if snapshot.Complete && snapshot.Truncated {
		return fmt.Errorf("host conflict routes: complete snapshot cannot be truncated")
	}
	if len(snapshot.Matching) > maximumRouteFacts {
		return fmt.Errorf("host conflict routes: matching facts exceed limit %d", maximumRouteFacts)
	}
	for index, fact := range snapshot.Matching {
		if err := validateRouteFact(observation, fact); err != nil {
			return fmt.Errorf("host conflict routes: matching fact %d: %w", index, err)
		}
	}
	if snapshot.Selected == nil {
		if snapshot.Complete {
			return fmt.Errorf("host conflict routes: complete snapshot is missing the selected route")
		}
		return nil
	}
	if err := validateRouteFact(observation, *snapshot.Selected); err != nil {
		return fmt.Errorf("host conflict routes: selected route: %w", err)
	}
	matches := 0
	for _, fact := range snapshot.Matching {
		if fact == *snapshot.Selected {
			matches++
		}
	}
	if matches == 0 {
		return fmt.Errorf("host conflict routes: selected route is absent from matching facts")
	}
	return nil
}

// validateRouteFact rejects facts that are not canonical IPv4 routes matching this exact candidate.
func validateRouteFact(observation Observation, fact RouteFact) error {
	destination := fact.Destination
	if !destination.IsValid() || !destination.Addr().Is4() || destination.Addr() != destination.Addr().Unmap() || destination != destination.Masked() {
		return fmt.Errorf("route destination %s is not a canonical IPv4 prefix", destination)
	}
	if !destination.Contains(observation.Request.Candidate()) {
		return fmt.Errorf("route destination %s does not match candidate %s", destination, observation.Request.Candidate())
	}
	if err := fact.Interface.Validate(); err != nil {
		return err
	}
	isSelectedLoopback := fact.Interface == observation.Loopback.Interface
	if fact.NativeLoopback != isSelectedLoopback {
		return fmt.Errorf("route native-loopback fact does not match selected interface")
	}
	if fact.Gateway.IsValid() {
		if !fact.Gateway.Is4() || fact.Gateway != fact.Gateway.Unmap() || fact.Gateway.Zone() != "" || fact.Gateway.IsUnspecified() {
			return fmt.Errorf("route gateway %s is not a canonical IPv4 next hop", fact.Gateway)
		}
	}
	switch fact.Normalization {
	case RouteNormalizationDirect:
		return nil
	case RouteNormalizationMacOSCloneUnresolved:
		if observation.Scope.Platform != PlatformMacOS {
			return fmt.Errorf("unresolved macOS cloned route is invalid on %s", observation.Scope.Platform)
		}
		if destination.Bits() <= 8 || !fact.NativeLoopback || fact.Gateway.IsValid() {
			return fmt.Errorf("unresolved macOS cloned route does not have a candidate-specific loopback shape")
		}
		return nil
	default:
		return fmt.Errorf("route normalization %q is unsupported", fact.Normalization)
	}
}

// validateSockets proves the socket snapshot contains only bounded facts for requested protocol-port pairs.
func validateSockets(observation Observation) error {
	snapshot := observation.Sockets
	if snapshot.Complete && snapshot.Truncated {
		return fmt.Errorf("host conflict sockets: complete snapshot cannot be truncated")
	}
	if len(snapshot.Endpoints) > maximumSocketFacts {
		return fmt.Errorf("host conflict sockets: endpoint facts exceed limit %d", maximumSocketFacts)
	}
	for index, fact := range snapshot.Endpoints {
		if err := validateSocketFact(observation.Request, fact); err != nil {
			return fmt.Errorf("host conflict sockets: endpoint %d: %w", index, err)
		}
	}
	return nil
}

// validateSocketFact rejects endpoints whose family, state, or requested capability is ambiguous.
func validateSocketFact(request Request, fact SocketFact) error {
	if fact.Protocol != SocketProtocolTCP && fact.Protocol != SocketProtocolUDP {
		return fmt.Errorf("socket protocol %q is unsupported", fact.Protocol)
	}
	if fact.Port == 0 {
		return fmt.Errorf("socket port must be greater than zero")
	}
	if !requestHasSocket(request, fact.Protocol, fact.Port) {
		return fmt.Errorf("socket %s port %d is not requested", fact.Protocol, fact.Port)
	}
	if !fact.Address.IsValid() || (!fact.Address.Is4() && !fact.Address.Is6()) || fact.Address != fact.Address.Unmap() || fact.Address.Zone() != "" {
		return fmt.Errorf("socket address %s is not a canonical unzoned IP address", fact.Address)
	}
	if fact.Protocol == SocketProtocolUDP && fact.TCPAccepting {
		return fmt.Errorf("UDP socket cannot contain TCP accepting state")
	}
	if fact.Address.Is6() && fact.Address.IsUnspecified() {
		switch fact.IPv6Only {
		case IPv6OnlyEnabled, IPv6OnlyDisabled, IPv6OnlyUnknown:
			return nil
		default:
			return fmt.Errorf("IPv6 wildcard requires explicit IPv6-only state")
		}
	}
	if fact.IPv6Only != IPv6OnlyNotApplicable {
		return fmt.Errorf("IPv6-only state is only valid for an IPv6 wildcard")
	}
	return nil
}

// validatePolicy requires Linux policy evidence in Linux scopes and rejects it everywhere else.
func validatePolicy(observation Observation) error {
	if observation.Scope.Platform != PlatformLinux {
		if observation.Policy.Linux != nil {
			return fmt.Errorf("host conflict policy: Linux facts are invalid on %s", observation.Scope.Platform)
		}
		return nil
	}
	if observation.Policy.Linux == nil {
		return fmt.Errorf("host conflict policy: Linux policy facts are required")
	}
	facts := observation.Policy.Linux
	if facts.Complete && facts.Truncated {
		return fmt.Errorf("host conflict policy: complete Linux policy snapshot cannot be truncated")
	}
	if len(facts.RouteLocalnet) > maximumPolicyInterfaces {
		return fmt.Errorf("host conflict policy: route_localnet facts exceed limit %d", maximumPolicyInterfaces)
	}
	seen := make(map[InterfaceIdentity]struct{}, len(facts.RouteLocalnet))
	loopbackSeen := false
	for index, fact := range facts.RouteLocalnet {
		if err := fact.Interface.Validate(); err != nil {
			return fmt.Errorf("host conflict policy: route_localnet fact %d: %w", index, err)
		}
		if _, exists := seen[fact.Interface]; exists {
			return fmt.Errorf("host conflict policy: duplicate route_localnet interface %s", fact.Interface.Name)
		}
		seen[fact.Interface] = struct{}{}
		if fact.Interface == observation.Loopback.Interface {
			loopbackSeen = true
		}
	}
	if facts.Complete && !loopbackSeen {
		return fmt.Errorf("host conflict policy: complete Linux policy snapshot omits the selected loopback")
	}
	return nil
}

// requestHasSocket checks the canonical request without broadening an adapter's filtered snapshot.
func requestHasSocket(request Request, protocol SocketProtocol, port uint16) bool {
	transport := TransportTCP4
	if protocol == SocketProtocolUDP {
		transport = TransportUDP4
	}
	for _, requirement := range request.requirements {
		if requirement.Transport == transport && requirement.Port == port {
			return true
		}
	}
	return false
}

// noGateway returns whether a route uses the normalized absence representation.
func noGateway(address netip.Addr) bool {
	return !address.IsValid()
}
