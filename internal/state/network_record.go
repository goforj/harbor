package state

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dnsserver"
	"github.com/goforj/harbor/internal/network/identity"
)

const maximumNetworkEndpointIDLength = 128

// NetworkStage identifies how much host networking authority the durable aggregate has proved.
type NetworkStage string

const (
	// NetworkStageIdentity proves only machine ownership and the reserved loopback identity pool.
	NetworkStageIdentity NetworkStage = "identity"
	// NetworkStageFull proves identity, resolver, and shared listener authority.
	NetworkStageFull NetworkStage = "full"
)

// Validate rejects lifecycle stages that cannot define a safe network projection.
func (stage NetworkStage) Validate() error {
	switch stage {
	case NetworkStageIdentity, NetworkStageFull:
		return nil
	default:
		return fmt.Errorf("network stage %q is unsupported", stage)
	}
}

// ListenerMode identifies whether the daemon binds an advertised socket directly or behind an owned redirect.
type ListenerMode string

const (
	// ListenerModeDirect means the advertised socket is the socket bound by the daemon.
	ListenerModeDirect ListenerMode = "direct"
	// ListenerModeRedirect means owned host policy sends the advertised socket to a distinct daemon bind port.
	ListenerModeRedirect ListenerMode = "redirect"
)

// Validate rejects listener modes outside Harbor's two ownership mechanisms.
func (mode ListenerMode) Validate() error {
	switch mode {
	case ListenerModeDirect, ListenerModeRedirect:
		return nil
	default:
		return fmt.Errorf("network listener mode %q is unsupported", mode)
	}
}

// EndpointProtocol identifies the public routing class reserved by one durable endpoint.
type EndpointProtocol string

const (
	// EndpointProtocolHTTP reserves an exact hostname on the shared HTTPS ingress.
	EndpointProtocolHTTP EndpointProtocol = "http"
	// EndpointProtocolTCP reserves one exact project identity and native TCP port.
	EndpointProtocolTCP EndpointProtocol = "tcp"
)

// Validate rejects protocols that have no payload-free Harbor routing contract.
func (protocol EndpointProtocol) Validate() error {
	switch protocol {
	case EndpointProtocolHTTP, EndpointProtocolTCP:
		return nil
	default:
		return fmt.Errorf("network endpoint protocol %q is unsupported", protocol)
	}
}

// ListenerReservation preserves the public and process-local sockets proved by host setup.
type ListenerReservation struct {
	Mode       ListenerMode
	Advertised netip.AddrPort
	Bind       netip.AddrPort
	Generation uint64
	VerifiedAt time.Time
}

// Validate rejects mappings that cannot describe one direct or redirected loopback listener.
func (reservation ListenerReservation) Validate() error {
	if err := reservation.Mode.Validate(); err != nil {
		return err
	}
	if err := validateNetworkEndpoint("advertised listener", reservation.Advertised); err != nil {
		return err
	}
	if err := validateNetworkEndpoint("bind listener", reservation.Bind); err != nil {
		return err
	}
	if reservation.Advertised.Addr() != reservation.Bind.Addr() {
		return fmt.Errorf("network listener advertised and bind addresses must match")
	}
	switch reservation.Mode {
	case ListenerModeDirect:
		if reservation.Advertised != reservation.Bind {
			return fmt.Errorf("direct network listener must advertise its bind socket")
		}
	case ListenerModeRedirect:
		if reservation.Advertised == reservation.Bind {
			return fmt.Errorf("redirected network listener must use a distinct bind port")
		}
	}
	if reservation.Generation == 0 {
		return fmt.Errorf("network listener generation must be positive")
	}
	if _, err := unsignedToModelInt("network listener generation", reservation.Generation, false); err != nil {
		return err
	}
	return validateStoredTime("network listener verification time", reservation.VerifiedAt)
}

// SharedListenerReservations contains the three host integrations that can back one data-plane generation.
type SharedListenerReservations struct {
	DNS   ListenerReservation
	HTTP  ListenerReservation
	HTTPS ListenerReservation
}

// Validate rejects incomplete ingress pairs and socket ownership collisions.
func (reservations SharedListenerReservations) Validate() error {
	for _, candidate := range []struct {
		name        string
		reservation ListenerReservation
	}{
		{name: "DNS", reservation: reservations.DNS},
		{name: "HTTP", reservation: reservations.HTTP},
		{name: "HTTPS", reservation: reservations.HTTPS},
	} {
		if err := candidate.reservation.Validate(); err != nil {
			return fmt.Errorf("%s listener: %w", candidate.name, err)
		}
	}
	if reservations.HTTP.Advertised.Addr() != reservations.HTTPS.Advertised.Addr() {
		return fmt.Errorf("HTTP and HTTPS listeners must share one ingress address")
	}
	if reservations.HTTP.Advertised.Port() != 80 {
		return fmt.Errorf("HTTP listener must advertise port 80")
	}
	if reservations.HTTPS.Advertised.Port() != 443 {
		return fmt.Errorf("HTTPS listener must advertise port 443")
	}

	owners := make(map[netip.AddrPort]string, 6)
	for _, candidate := range []struct {
		name        string
		reservation ListenerReservation
	}{
		{name: "DNS", reservation: reservations.DNS},
		{name: "HTTP", reservation: reservations.HTTP},
		{name: "HTTPS", reservation: reservations.HTTPS},
	} {
		for _, socket := range []struct {
			name     string
			endpoint netip.AddrPort
		}{
			{name: candidate.name + " advertised", endpoint: candidate.reservation.Advertised},
			{name: candidate.name + " bind", endpoint: candidate.reservation.Bind},
		} {
			if owner, exists := owners[socket.endpoint]; exists && owner != candidate.name {
				return fmt.Errorf("%s socket %s collides with %s", socket.name, socket.endpoint, owner)
			}
			owners[socket.endpoint] = candidate.name
		}
	}
	return nil
}

// EndpointReservationKey identifies one project-scoped durable endpoint without pretending its ID is generation-global.
type EndpointReservationKey struct {
	ProjectID  domain.ProjectID
	EndpointID string
}

// Validate rejects project or endpoint identities that cannot become stable diagnostic keys.
func (key EndpointReservationKey) Validate() error {
	if err := key.ProjectID.Validate(); err != nil {
		return err
	}
	return validateNetworkEndpointID(key.EndpointID)
}

// EndpointReservation describes a public socket without persisting or synthesizing a private upstream.
type EndpointReservation struct {
	Key        EndpointReservationKey
	Protocol   EndpointProtocol
	Host       string
	Public     netip.AddrPort
	Identity   *identity.LeaseKey
	Generation uint64
}

// Validate rejects reservations that cannot be joined safely to a later verified runtime upstream.
func (reservation EndpointReservation) Validate() error {
	if err := reservation.Key.Validate(); err != nil {
		return err
	}
	if err := reservation.Protocol.Validate(); err != nil {
		return err
	}
	if err := validateNetworkEndpoint("public endpoint", reservation.Public); err != nil {
		return err
	}
	if reservation.Generation == 0 {
		return fmt.Errorf("network endpoint generation must be positive")
	}
	if _, err := unsignedToModelInt("network endpoint generation", reservation.Generation, false); err != nil {
		return err
	}
	if err := dnsserver.ValidateName(reservation.Host); err != nil {
		return fmt.Errorf("network endpoint host: %w", err)
	}

	switch reservation.Protocol {
	case EndpointProtocolHTTP:
		if reservation.Identity != nil {
			return fmt.Errorf("HTTP endpoint must not reference a project identity")
		}
	case EndpointProtocolTCP:
		if reservation.Identity == nil {
			return fmt.Errorf("TCP endpoint must reference a project identity")
		}
		if err := reservation.Identity.Validate(); err != nil {
			return err
		}
		if reservation.Identity.ProjectID != reservation.Key.ProjectID {
			return fmt.Errorf("TCP endpoint identity belongs to project %q, not %q", reservation.Identity.ProjectID, reservation.Key.ProjectID)
		}
	}
	return nil
}

// DataPlaneReservations is the durable, payload-free input awaiting live upstream verification.
type DataPlaneReservations struct {
	Listeners            SharedListenerReservations
	Endpoints            []EndpointReservation
	SuppressedProjectIDs []domain.ProjectID
}

// Validate rejects noncanonical or publishable projections that could alias public authority.
func (reservations DataPlaneReservations) Validate() error {
	if reservations.Endpoints == nil {
		return fmt.Errorf("network endpoint reservations must be initialized")
	}
	if reservations.SuppressedProjectIDs == nil {
		return fmt.Errorf("suppressed network projects must be initialized")
	}
	if err := reservations.Listeners.Validate(); err != nil {
		return err
	}

	suppressed, err := validateSuppressedNetworkProjects(reservations.SuppressedProjectIDs)
	if err != nil {
		return err
	}

	keys := make(map[EndpointReservationKey]struct{}, len(reservations.Endpoints))
	hosts := make(map[string]struct{}, len(reservations.Endpoints))
	tcpSockets := make(map[netip.AddrPort]struct{}, len(reservations.Endpoints))
	for index, reservation := range reservations.Endpoints {
		if err := reservation.Validate(); err != nil {
			return fmt.Errorf("network endpoint reservation %d: %w", index, err)
		}
		if index > 0 && !endpointReservationLess(reservations.Endpoints[index-1], reservation) {
			return fmt.Errorf("network endpoint reservations must be unique and ordered")
		}
		if _, exists := suppressed[reservation.Key.ProjectID]; exists {
			return fmt.Errorf("network endpoint %q belongs to suppressed project %q", reservation.Host, reservation.Key.ProjectID)
		}
		if _, duplicate := keys[reservation.Key]; duplicate {
			return fmt.Errorf("duplicate network endpoint key %q/%q", reservation.Key.ProjectID, reservation.Key.EndpointID)
		}
		keys[reservation.Key] = struct{}{}
		if _, duplicate := hosts[reservation.Host]; duplicate {
			return fmt.Errorf("duplicate network endpoint host %q", reservation.Host)
		}
		hosts[reservation.Host] = struct{}{}
		switch reservation.Protocol {
		case EndpointProtocolHTTP:
			if reservation.Public != reservations.Listeners.HTTPS.Advertised {
				return fmt.Errorf("HTTP endpoint %q does not use the advertised HTTPS socket", reservation.Host)
			}
		case EndpointProtocolTCP:
			if _, duplicate := tcpSockets[reservation.Public]; duplicate {
				return fmt.Errorf("duplicate native network socket %s", reservation.Public)
			}
			tcpSockets[reservation.Public] = struct{}{}
			if owner, collision := sharedSocketOwner(reservations.Listeners, reservation.Public); collision {
				return fmt.Errorf("native endpoint %q socket %s collides with %s", reservation.Host, reservation.Public, owner)
			}
		}
	}
	return nil
}

// validateIdentityDataPlaneReservations prevents an identity-only aggregate from claiming resolver or ingress authority.
func validateIdentityDataPlaneReservations(reservations DataPlaneReservations) error {
	if reservations.Endpoints == nil {
		return fmt.Errorf("network endpoint reservations must be initialized")
	}
	if reservations.SuppressedProjectIDs == nil {
		return fmt.Errorf("suppressed network projects must be initialized")
	}
	if reservations.Listeners != (SharedListenerReservations{}) {
		return fmt.Errorf("identity-stage network must not contain listener reservations")
	}
	if len(reservations.Endpoints) != 0 {
		return fmt.Errorf("identity-stage network must not contain endpoint reservations")
	}
	_, err := validateSuppressedNetworkProjects(reservations.SuppressedProjectIDs)
	return err
}

// validateSuppressedNetworkProjects requires canonical teardown suppressions without granting them routing authority.
func validateSuppressedNetworkProjects(projectIDs []domain.ProjectID) (map[domain.ProjectID]struct{}, error) {
	suppressed := make(map[domain.ProjectID]struct{}, len(projectIDs))
	for index, projectID := range projectIDs {
		if err := projectID.Validate(); err != nil {
			return nil, err
		}
		if index > 0 && projectIDs[index-1] >= projectID {
			return nil, fmt.Errorf("suppressed network projects must be unique and ordered")
		}
		suppressed[projectID] = struct{}{}
	}
	return suppressed, nil
}

// NetworkRecord is one initialized durable network aggregate at its sole global revision.
type NetworkRecord struct {
	Stage        NetworkStage
	Revision     domain.Sequence
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Ownership    identity.Ownership
	Pool         identity.Pool
	Leases       []identity.Lease
	Quarantines  []identity.Quarantine
	Reservations DataPlaneReservations
}

// Validate rejects aggregates that cannot be passed safely to identity planning and runtime reconciliation.
func (record NetworkRecord) Validate() error {
	if err := record.Stage.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("network revision", record.Revision, false); err != nil {
		return err
	}
	if err := validateStoredTime("network creation time", record.CreatedAt); err != nil {
		return err
	}
	if err := validateStoredTime("network update time", record.UpdatedAt); err != nil {
		return err
	}
	if record.UpdatedAt.Before(record.CreatedAt) {
		return fmt.Errorf("network update time must not precede creation time")
	}
	if err := record.Ownership.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network ownership generation", record.Ownership.Generation, false); err != nil {
		return err
	}
	if err := record.Pool.Validate(); err != nil {
		return err
	}
	if record.Leases == nil {
		return fmt.Errorf("network leases must be initialized")
	}
	if record.Quarantines == nil {
		return fmt.Errorf("network quarantines must be initialized")
	}
	switch record.Stage {
	case NetworkStageIdentity:
		if err := validateIdentityDataPlaneReservations(record.Reservations); err != nil {
			return err
		}
	case NetworkStageFull:
		if err := record.Reservations.Validate(); err != nil {
			return err
		}
	}

	if record.Stage == NetworkStageFull {
		for _, listener := range []ListenerReservation{
			record.Reservations.Listeners.DNS,
			record.Reservations.Listeners.HTTP,
			record.Reservations.Listeners.HTTPS,
		} {
			for _, address := range []netip.Addr{listener.Advertised.Addr(), listener.Bind.Addr()} {
				if record.Pool.Contains(address) {
					return fmt.Errorf("shared listener address %s is also a project pool candidate", address)
				}
			}
		}
	}

	leaseKeys := make(map[identity.LeaseKey]identity.Lease, len(record.Leases))
	leaseAddresses := make(map[netip.Addr]struct{}, len(record.Leases))
	primaries := make(map[domain.ProjectID]struct{}, len(record.Leases))
	for index, lease := range record.Leases {
		if err := lease.Validate(); err != nil {
			return err
		}
		if lease.Address != lease.Address.Unmap() {
			return fmt.Errorf("network lease address %s must use canonical IPv4 form", lease.Address)
		}
		if _, err := unsignedToModelInt("network lease ownership generation", lease.Ownership.Generation, false); err != nil {
			return err
		}
		if lease.Ownership.InstallationID != record.Ownership.InstallationID {
			return fmt.Errorf("network lease for project %q belongs to installation %q, not %q", lease.Key.ProjectID, lease.Ownership.InstallationID, record.Ownership.InstallationID)
		}
		if !record.Pool.Contains(lease.Address) {
			return fmt.Errorf("network lease address %s is not a pool candidate", lease.Address)
		}
		if index > 0 && !networkLeaseLess(record.Leases[index-1], lease) {
			return fmt.Errorf("network leases must be unique and ordered")
		}
		if _, duplicate := leaseKeys[lease.Key]; duplicate {
			return fmt.Errorf("duplicate network lease key for project %q", lease.Key.ProjectID)
		}
		leaseKeys[lease.Key] = lease
		if _, duplicate := leaseAddresses[lease.Address]; duplicate {
			return fmt.Errorf("duplicate network lease address %s", lease.Address)
		}
		leaseAddresses[lease.Address] = struct{}{}
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primaries[lease.Key.ProjectID] = struct{}{}
		}
	}
	for _, lease := range record.Leases {
		if lease.Key.Kind() == identity.LeaseKindSecondary {
			if _, exists := primaries[lease.Key.ProjectID]; !exists {
				return fmt.Errorf("secondary network lease for project %q requires a primary lease", lease.Key.ProjectID)
			}
		}
	}

	for index, quarantine := range record.Quarantines {
		if err := quarantine.Validate(record.Pool); err != nil {
			return err
		}
		if quarantine.Address != quarantine.Address.Unmap() {
			return fmt.Errorf("network quarantine address %s must use canonical IPv4 form", quarantine.Address)
		}
		if err := validateBoundedNetworkText("quarantine reason", quarantine.Reason, maximumNetworkQuarantineReasonLength); err != nil {
			return err
		}
		if index > 0 && record.Quarantines[index-1].Address.Compare(quarantine.Address) >= 0 {
			return fmt.Errorf("network quarantines must be unique and ordered")
		}
		if _, leased := leaseAddresses[quarantine.Address]; leased {
			return fmt.Errorf("network address %s is both leased and quarantined", quarantine.Address)
		}
	}

	for _, endpoint := range record.Reservations.Endpoints {
		if _, exists := primaries[endpoint.Key.ProjectID]; !exists {
			return fmt.Errorf("network endpoint %q requires a primary lease for project %q", endpoint.Host, endpoint.Key.ProjectID)
		}
		if endpoint.Protocol != EndpointProtocolTCP {
			continue
		}
		lease, exists := leaseKeys[*endpoint.Identity]
		if !exists {
			return fmt.Errorf("TCP endpoint %q references an unknown network lease", endpoint.Host)
		}
		if lease.Address != endpoint.Public.Addr() {
			return fmt.Errorf("TCP endpoint %q address %s does not match lease address %s", endpoint.Host, endpoint.Public.Addr(), lease.Address)
		}
	}
	return nil
}

// endpointReservationLess defines the stable order retained across persistence and runtime joins.
func endpointReservationLess(left EndpointReservation, right EndpointReservation) bool {
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	if left.Key.ProjectID != right.Key.ProjectID {
		return left.Key.ProjectID < right.Key.ProjectID
	}
	return left.Key.EndpointID < right.Key.EndpointID
}

// networkLeaseLess mirrors identity planning's project, primary, and secondary ordering.
func networkLeaseLess(left identity.Lease, right identity.Lease) bool {
	if left.Key.ProjectID != right.Key.ProjectID {
		return left.Key.ProjectID < right.Key.ProjectID
	}
	if left.Key.Kind() != right.Key.Kind() {
		return left.Key.Kind() == identity.LeaseKindPrimary
	}
	return left.Key.SecondaryID < right.Key.SecondaryID
}

// canonicalNetworkLeases returns a defensive deterministic lease slice.
func canonicalNetworkLeases(leases []identity.Lease) []identity.Lease {
	result := slices.Clone(leases)
	slices.SortFunc(result, func(left identity.Lease, right identity.Lease) int {
		if networkLeaseLess(left, right) {
			return -1
		}
		if networkLeaseLess(right, left) {
			return 1
		}
		return 0
	})
	return result
}

// canonicalNetworkQuarantines returns a defensive numeric-address ordering.
func canonicalNetworkQuarantines(quarantines []identity.Quarantine) []identity.Quarantine {
	result := slices.Clone(quarantines)
	slices.SortFunc(result, func(left identity.Quarantine, right identity.Quarantine) int {
		return left.Address.Compare(right.Address)
	})
	return result
}

// canonicalEndpointReservations returns a defensive host and composite-key ordering.
func canonicalEndpointReservations(reservations []EndpointReservation) []EndpointReservation {
	result := slices.Clone(reservations)
	for index := range result {
		if result[index].Identity == nil {
			continue
		}
		identityCopy := *result[index].Identity
		result[index].Identity = &identityCopy
	}
	slices.SortFunc(result, func(left EndpointReservation, right EndpointReservation) int {
		if endpointReservationLess(left, right) {
			return -1
		}
		if endpointReservationLess(right, left) {
			return 1
		}
		return 0
	})
	return result
}

// sharedSocketOwner reports host integration ownership for one exact shared socket.
func sharedSocketOwner(reservations SharedListenerReservations, endpoint netip.AddrPort) (string, bool) {
	for _, candidate := range []struct {
		name        string
		reservation ListenerReservation
	}{
		{name: "DNS listener", reservation: reservations.DNS},
		{name: "HTTP listener", reservation: reservations.HTTP},
		{name: "HTTPS listener", reservation: reservations.HTTPS},
	} {
		if candidate.reservation.Advertised == endpoint || candidate.reservation.Bind == endpoint {
			return candidate.name, true
		}
	}
	return "", false
}

// validateNetworkEndpoint requires one canonical, explicit IPv4 loopback socket.
func validateNetworkEndpoint(name string, endpoint netip.AddrPort) error {
	if !endpoint.IsValid() || endpoint.Port() == 0 {
		return fmt.Errorf("%s must be a valid address with a nonzero port", name)
	}
	address := endpoint.Addr().Unmap()
	if !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("%s %s must use IPv4 loopback", name, endpoint)
	}
	if address != endpoint.Addr() {
		return fmt.Errorf("%s %s must use canonical IPv4 form", name, endpoint)
	}
	return nil
}

// validateNetworkEndpointID mirrors the runtime's bounded diagnostic vocabulary without assigning global scope.
func validateNetworkEndpointID(value string) error {
	if value == "" {
		return fmt.Errorf("network endpoint ID is required")
	}
	if len(value) > maximumNetworkEndpointIDLength {
		return fmt.Errorf("network endpoint ID %q exceeds %d bytes", value, maximumNetworkEndpointIDLength)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("network endpoint ID %q must not contain surrounding whitespace", value)
	}
	for _, character := range value {
		alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphanumeric && character != '.' && character != '_' && character != ':' && character != '-' {
			return fmt.Errorf("network endpoint ID %q contains an unsupported character", value)
		}
	}
	return nil
}
