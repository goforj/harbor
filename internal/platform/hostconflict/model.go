package hostconflict

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaximumSocketRequirements bounds the ports one candidate observation may authorize.
	MaximumSocketRequirements = 128
	maximumRouteFacts         = 512
	maximumSocketFacts        = 4096
	maximumPolicyInterfaces   = 4096
	maximumInterfaceName      = 1024
)

// Transport identifies one IPv4 socket capability Harbor intends to claim.
type Transport string

const (
	// TransportTCP4 requires an IPv4 TCP listener.
	TransportTCP4 Transport = "tcp4"
	// TransportUDP4 requires an IPv4 UDP bind.
	TransportUDP4 Transport = "udp4"
)

// SocketRequirement identifies one protocol and port Harbor needs on the candidate.
type SocketRequirement struct {
	Transport Transport
	Port      uint16
}

// Validate rejects requirements that cannot identify one concrete IPv4 socket capability.
func (r SocketRequirement) Validate() error {
	if !validTransport(r.Transport) {
		return fmt.Errorf("host conflict socket requirement: transport %q is unsupported", r.Transport)
	}
	if r.Port == 0 {
		return fmt.Errorf("host conflict socket requirement: port must be greater than zero")
	}
	return nil
}

// Purpose limits an observation to a lifecycle phase with compatible route semantics.
type Purpose string

const (
	// PurposePreAssignment proves a candidate is safe before Harbor assigns its /32 address.
	PurposePreAssignment Purpose = "pre-assignment"
)

// Request contains the canonical candidate and socket capabilities being protected.
type Request struct {
	purpose      Purpose
	candidate    netip.Addr
	requirements []SocketRequirement
}

// NewPreAssignmentRequest validates a candidate before its /32 exists and returns a sorted, immutable requirement set.
//
// An empty requirement set is valid for route-only pool admission. Repair of an
// already-owned assignment needs different route and ownership evidence and is
// intentionally outside this request purpose.
func NewPreAssignmentRequest(candidate netip.Addr, requirements []SocketRequirement) (Request, error) {
	if err := validateCandidate(candidate); err != nil {
		return Request{}, err
	}
	if len(requirements) > MaximumSocketRequirements {
		return Request{}, fmt.Errorf("host conflict request: socket requirements exceed limit %d", MaximumSocketRequirements)
	}

	canonical := slices.Clone(requirements)
	for index, requirement := range canonical {
		if err := requirement.Validate(); err != nil {
			return Request{}, fmt.Errorf("host conflict request: requirement %d: %w", index, err)
		}
	}
	slices.SortFunc(canonical, compareRequirements)
	for index := 1; index < len(canonical); index++ {
		if canonical[index] == canonical[index-1] {
			return Request{}, fmt.Errorf("host conflict request: duplicate %s port %d requirement", canonical[index].Transport, canonical[index].Port)
		}
	}
	return Request{purpose: PurposePreAssignment, candidate: candidate, requirements: canonical}, nil
}

// Purpose returns the lifecycle phase whose route semantics the request authorizes.
func (r Request) Purpose() Purpose {
	return r.purpose
}

// Candidate returns the canonical IPv4 loopback address being protected.
func (r Request) Candidate() netip.Addr {
	return r.candidate
}

// Requirements returns a copy of the canonical transport-then-port requirement order.
func (r Request) Requirements() []SocketRequirement {
	return slices.Clone(r.requirements)
}

// Validate rejects zero or internally noncanonical requests.
func (r Request) Validate() error {
	if r.purpose != PurposePreAssignment {
		return fmt.Errorf("host conflict request: purpose %q is unsupported", r.purpose)
	}
	if err := validateCandidate(r.candidate); err != nil {
		return err
	}
	if len(r.requirements) > MaximumSocketRequirements {
		return fmt.Errorf("host conflict request: socket requirements exceed limit %d", MaximumSocketRequirements)
	}
	for index, requirement := range r.requirements {
		if err := requirement.Validate(); err != nil {
			return fmt.Errorf("host conflict request: requirement %d: %w", index, err)
		}
		if index > 0 && compareRequirements(r.requirements[index-1], requirement) >= 0 {
			return fmt.Errorf("host conflict request: socket requirements are not unique canonical order")
		}
	}
	return nil
}

// Platform identifies the host networking model that produced an observation.
type Platform string

const (
	// PlatformLinux identifies a Linux network namespace.
	PlatformLinux Platform = "linux"
	// PlatformMacOS identifies the process-global macOS network stack.
	PlatformMacOS Platform = "macos"
	// PlatformWindows identifies a Windows network compartment.
	PlatformWindows Platform = "windows"
)

// LinuxNamespaceIdentity identifies one Linux network namespace by its filesystem identity.
type LinuxNamespaceIdentity struct {
	Device uint64
	Inode  uint64
}

// WindowsCompartmentIdentity identifies one Windows IP Helper compartment.
type WindowsCompartmentIdentity struct {
	ID uint32
}

// NetworkScope binds route, socket, and policy facts to the host networking scope that produced them.
type NetworkScope struct {
	Platform           Platform
	LinuxNamespace     *LinuxNamespaceIdentity
	WindowsCompartment *WindowsCompartmentIdentity
}

// NewLinuxScope creates a validated Linux network-namespace scope.
func NewLinuxScope(device uint64, inode uint64) (NetworkScope, error) {
	scope := NetworkScope{
		Platform:       PlatformLinux,
		LinuxNamespace: &LinuxNamespaceIdentity{Device: device, Inode: inode},
	}
	if err := scope.Validate(); err != nil {
		return NetworkScope{}, err
	}
	return scope, nil
}

// NewMacOSScope creates the fixed process-global macOS networking scope.
func NewMacOSScope() NetworkScope {
	return NetworkScope{Platform: PlatformMacOS}
}

// NewWindowsScope creates a validated Windows network-compartment scope.
func NewWindowsScope(compartmentID uint32) (NetworkScope, error) {
	scope := NetworkScope{
		Platform:           PlatformWindows,
		WindowsCompartment: &WindowsCompartmentIdentity{ID: compartmentID},
	}
	if err := scope.Validate(); err != nil {
		return NetworkScope{}, err
	}
	return scope, nil
}

// Validate rejects missing, mixed-platform, or zero networking scope identities.
func (s NetworkScope) Validate() error {
	switch s.Platform {
	case PlatformLinux:
		if s.LinuxNamespace == nil || s.LinuxNamespace.Device == 0 || s.LinuxNamespace.Inode == 0 {
			return fmt.Errorf("host conflict scope: Linux network namespace identity is required")
		}
		if s.WindowsCompartment != nil {
			return fmt.Errorf("host conflict scope: Linux scope contains Windows compartment facts")
		}
	case PlatformMacOS:
		if s.LinuxNamespace != nil || s.WindowsCompartment != nil {
			return fmt.Errorf("host conflict scope: macOS global scope contains platform-specific identity facts")
		}
	case PlatformWindows:
		if s.WindowsCompartment == nil || s.WindowsCompartment.ID == 0 {
			return fmt.Errorf("host conflict scope: Windows compartment identity is required")
		}
		if s.LinuxNamespace != nil {
			return fmt.Errorf("host conflict scope: Windows scope contains Linux namespace facts")
		}
	default:
		return fmt.Errorf("host conflict scope: platform %q is unsupported", s.Platform)
	}
	return nil
}

// InterfaceIdentity identifies one interface within the observation's networking scope.
type InterfaceIdentity struct {
	Name        string
	Index       uint32
	WindowsLUID uint64
}

// Validate rejects interface identities that are not bounded canonical host facts.
func (i InterfaceIdentity) Validate() error {
	if i.Index == 0 {
		return fmt.Errorf("host conflict interface: index must be greater than zero")
	}
	if err := validateBoundedText("interface name", i.Name, maximumInterfaceName); err != nil {
		return fmt.Errorf("host conflict interface: %w", err)
	}
	return nil
}

// validateForPlatform rejects interface identities that carry missing or foreign platform authority.
func (i InterfaceIdentity) validateForPlatform(platform Platform) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if platform == PlatformWindows {
		if i.WindowsLUID == 0 {
			return fmt.Errorf("host conflict interface: Windows LUID must be greater than zero")
		}
		return nil
	}
	if i.WindowsLUID != 0 {
		return fmt.Errorf("host conflict interface: Windows LUID is invalid on %s", platform)
	}
	return nil
}

// sameInterfaceAuthority compares stable native identity while treating names as display evidence.
func sameInterfaceAuthority(platform Platform, left InterfaceIdentity, right InterfaceIdentity) bool {
	if left.Index != right.Index {
		return false
	}
	if platform == PlatformWindows {
		return left.WindowsLUID != 0 && left.WindowsLUID == right.WindowsLUID
	}
	return true
}

// LoopbackKind identifies the platform proof used for Harbor's selected native loopback.
type LoopbackKind string

const (
	// LoopbackKindLinuxNative identifies Linux's native loopback interface.
	LoopbackKindLinuxNative LoopbackKind = "linux-native"
	// LoopbackKindMacOSNative identifies macOS's native loopback interface.
	LoopbackKindMacOSNative LoopbackKind = "macos-native"
	// LoopbackKindWindowsSoftware identifies a Windows software-loopback interface.
	LoopbackKindWindowsSoftware LoopbackKind = "windows-software-loopback"
)

// LoopbackIdentity identifies the selected native loopback within one networking scope.
type LoopbackIdentity struct {
	Interface InterfaceIdentity
	Kind      LoopbackKind
}

// Validate rejects loopback identities that do not match the observation platform.
func (i LoopbackIdentity) Validate(platform Platform) error {
	if err := i.Interface.validateForPlatform(platform); err != nil {
		return err
	}
	want := LoopbackKind("")
	switch platform {
	case PlatformLinux:
		want = LoopbackKindLinuxNative
	case PlatformMacOS:
		want = LoopbackKindMacOSNative
	case PlatformWindows:
		want = LoopbackKindWindowsSoftware
	default:
		return fmt.Errorf("host conflict loopback: platform %q is unsupported", platform)
	}
	if i.Kind != want {
		return fmt.Errorf("host conflict loopback: kind %q does not match platform %q", i.Kind, platform)
	}
	return nil
}

// RouteNormalization describes how an adapter normalized a matching route.
type RouteNormalization string

const (
	// RouteNormalizationDirect identifies a route reported without a derived relationship.
	RouteNormalizationDirect RouteNormalization = "direct"
	// RouteNormalizationMacOSCloneUnresolved records a macOS cloned route whose parent relationship is not yet proven.
	// It remains indeterminate until a future native adapter supplies a tested parent normalization.
	RouteNormalizationMacOSCloneUnresolved RouteNormalization = "macos-clone-unresolved"
)

// RouteFact describes one normalized route whose destination matches the candidate.
type RouteFact struct {
	Destination    netip.Prefix
	Interface      InterfaceIdentity
	NativeLoopback bool
	Gateway        netip.Addr
	Normalization  RouteNormalization
}

// RouteSnapshot contains the selected route and every normalized route matching the candidate.
//
// An adapter must mark the snapshot incomplete when a selected or matching
// native route cannot be represented losslessly. Omitting an unrepresentable
// route would turn unknown host state into false safety.
type RouteSnapshot struct {
	Complete  bool
	Truncated bool
	Selected  *RouteFact
	Matching  []RouteFact
}

// SocketProtocol identifies the protocol of one observed local socket.
type SocketProtocol string

const (
	// SocketProtocolTCP identifies a TCP endpoint.
	SocketProtocolTCP SocketProtocol = "tcp"
	// SocketProtocolUDP identifies a UDP endpoint.
	SocketProtocolUDP SocketProtocol = "udp"
)

// IPv6OnlyState records whether an IPv6 wildcard is proven unable to accept IPv4 traffic.
type IPv6OnlyState string

const (
	// IPv6OnlyNotApplicable marks facts that are not IPv6 wildcard binds.
	IPv6OnlyNotApplicable IPv6OnlyState = "not-applicable"
	// IPv6OnlyEnabled proves an IPv6 wildcard is restricted to IPv6 traffic.
	IPv6OnlyEnabled IPv6OnlyState = "enabled"
	// IPv6OnlyDisabled proves an IPv6 wildcard also accepts IPv4 traffic.
	IPv6OnlyDisabled IPv6OnlyState = "disabled"
	// IPv6OnlyUnknown means the adapter could not prove the wildcard's IPv4 behavior.
	IPv6OnlyUnknown IPv6OnlyState = "unknown"
)

// SocketFact describes one local endpoint on a requested protocol and port.
type SocketFact struct {
	Protocol     SocketProtocol
	Address      netip.Addr
	Port         uint16
	TCPAccepting bool
	IPv6Only     IPv6OnlyState
}

// SocketSnapshot contains every endpoint relevant to the request's protocol and port pairs.
//
// Complete means enumeration covered every requested protocol and port across
// the exact IPv4 candidate, the IPv4 wildcard, and the IPv6 wildcard, including
// an explicit IPv6-only fact for each IPv6 wildcard.
type SocketSnapshot struct {
	Complete  bool
	Truncated bool
	Endpoints []SocketFact
}

// RouteLocalnetFact records Linux's effective route_localnet policy for one interface.
type RouteLocalnetFact struct {
	Interface InterfaceIdentity
	Enabled   bool
}

// LinuxPolicyFacts contains bounded Linux policy facts that affect loopback isolation.
//
// Complete means the adapter observed effective route_localnet policy for every
// interface in the network namespace, not merely every interface in this slice.
type LinuxPolicyFacts struct {
	Complete       bool
	Truncated      bool
	IPNonlocalBind bool
	RouteLocalnet  []RouteLocalnetFact
}

// PolicyFacts contains platform-specific host networking policy.
type PolicyFacts struct {
	Linux *LinuxPolicyFacts
}

// Observation contains the pre-assignment route, socket, and policy facts for one candidate claim.
//
// Even a StateSafe assessment covers only this fact set. Admission must compose
// it with assignment, ownership, resolver, and post-assignment retained-bind
// evidence before treating the candidate as usable.
type Observation struct {
	Request  Request
	Scope    NetworkScope
	Loopback LoopbackIdentity
	Routes   RouteSnapshot
	Sockets  SocketSnapshot
	Policy   PolicyFacts
}

// Validate rejects malformed, contradictory, unbounded, or cross-platform facts.
func (o Observation) Validate() error {
	if err := o.Request.Validate(); err != nil {
		return err
	}
	if err := o.Scope.Validate(); err != nil {
		return err
	}
	if err := o.Loopback.Validate(o.Scope.Platform); err != nil {
		return err
	}
	if err := validateRoutes(o); err != nil {
		return err
	}
	if err := validateSockets(o); err != nil {
		return err
	}
	if err := validatePolicy(o); err != nil {
		return err
	}
	return nil
}

// validateCandidate requires an unmapped canonical IPv4 loopback address.
func validateCandidate(candidate netip.Addr) error {
	if !candidate.IsValid() || !candidate.Is4() || !candidate.IsLoopback() || candidate != candidate.Unmap() || candidate.Zone() != "" {
		return fmt.Errorf("host conflict request: candidate %s is not canonical IPv4 loopback", candidate)
	}
	return nil
}

// validTransport recognizes the bounded request transport vocabulary.
func validTransport(transport Transport) bool {
	return transport == TransportTCP4 || transport == TransportUDP4
}

// compareRequirements establishes the public transport-then-port canonical order.
func compareRequirements(left SocketRequirement, right SocketRequirement) int {
	if left.Transport < right.Transport {
		return -1
	}
	if left.Transport > right.Transport {
		return 1
	}
	if left.Port < right.Port {
		return -1
	}
	if left.Port > right.Port {
		return 1
	}
	return 0
}

// validateBoundedText permits platform names with spaces while excluding ambiguous boundaries and controls.
func validateBoundedText(label string, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain control characters", label)
		}
	}
	return nil
}
