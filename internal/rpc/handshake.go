package rpc

import (
	"errors"
	"fmt"
	"sort"
)

// Role identifies a peer's authorization boundary on a local IPC connection.
type Role string

const (
	// RoleDaemon identifies the Harbor daemon.
	RoleDaemon Role = "daemon"
	// RoleCLI identifies the user-facing Harbor command-line client.
	RoleCLI Role = "cli"
	// RoleDesktop identifies the Harbor desktop backend.
	RoleDesktop Role = "desktop"
	// RoleGoForjSession identifies a managed or terminal-owned GoForj session.
	RoleGoForjSession Role = "goforj_session"
)

// Validate verifies that a role is known to this authorization boundary.
func (r Role) Validate() error {
	switch r {
	case RoleDaemon, RoleCLI, RoleDesktop, RoleGoForjSession:
		return nil
	default:
		return fmt.Errorf("unsupported IPC role %q", r)
	}
}

// ValidateClient verifies that a role is permitted to initiate a daemon connection.
func (r Role) ValidateClient() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r == RoleDaemon {
		return errors.New("daemon cannot initiate a daemon client handshake")
	}

	return nil
}

// Capability is an independently versioned IPC feature advertised by a peer.
// Unknown capability names remain valid so newer peers can negotiate additively.
type Capability string

// CanonicalCapabilities validates, de-duplicates, and sorts capabilities for
// deterministic negotiation and fixtures.
func CanonicalCapabilities(capabilities []Capability) ([]Capability, error) {
	unique := make(map[Capability]struct{}, len(capabilities))
	for i, capability := range capabilities {
		if err := validateWireToken("capability", string(capability), maxCapabilityLength); err != nil {
			return nil, fmt.Errorf("capability %d: %w", i, err)
		}
		unique[capability] = struct{}{}
	}

	canonical := make([]Capability, 0, len(unique))
	for capability := range unique {
		canonical = append(canonical, capability)
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i] < canonical[j]
	})

	return canonical, nil
}

// Hello starts a connection by advertising the client's compatible protocol
// ranges, role, build version, and independently versioned capabilities.
type Hello struct {
	ProtocolRanges []VersionRange `json:"protocol_ranges"`
	Role           Role           `json:"role"`
	ClientVersion  string         `json:"client_version"`
	Capabilities   []Capability   `json:"capabilities"`
}

// Validate verifies client-controlled handshake input without requiring a
// particular set of known capabilities.
func (h Hello) Validate() error {
	if _, err := CanonicalVersionRanges(h.ProtocolRanges); err != nil {
		return fmt.Errorf("protocol ranges: %w", err)
	}
	if err := h.Role.ValidateClient(); err != nil {
		return fmt.Errorf("role: %w", err)
	}
	if err := validateWireToken("client version", h.ClientVersion, maxVersionLength); err != nil {
		return err
	}
	if _, err := CanonicalCapabilities(h.Capabilities); err != nil {
		return err
	}

	return nil
}

// Welcome completes negotiation and states the exact protocol and capabilities
// available for this connection.
type Welcome struct {
	Protocol       Version        `json:"protocol"`
	ProtocolRanges []VersionRange `json:"protocol_ranges"`
	Role           Role           `json:"role"`
	DaemonVersion  string         `json:"daemon_version"`
	Capabilities   []Capability   `json:"capabilities"`
}

// Validate verifies a daemon handshake response.
func (w Welcome) Validate() error {
	if err := w.Protocol.Validate(); err != nil {
		return fmt.Errorf("selected protocol: %w", err)
	}
	ranges, err := CanonicalVersionRanges(w.ProtocolRanges)
	if err != nil {
		return fmt.Errorf("protocol ranges: %w", err)
	}
	if !versionInRanges(w.Protocol, ranges) {
		return errors.New("selected protocol is outside the daemon ranges")
	}
	if w.Role != RoleDaemon {
		return errors.New("welcome role must be daemon")
	}
	if err := validateWireToken("daemon version", w.DaemonVersion, maxVersionLength); err != nil {
		return err
	}
	if _, err := CanonicalCapabilities(w.Capabilities); err != nil {
		return err
	}

	return nil
}

// Reject terminates handshake negotiation with a safe error and the daemon's
// supported ranges so the client can provide concrete upgrade guidance.
type Reject struct {
	ProtocolRanges []VersionRange `json:"protocol_ranges,omitempty"`
	Role           Role           `json:"role"`
	DaemonVersion  string         `json:"daemon_version,omitempty"`
	Error          WireError      `json:"error"`
}

// Validate verifies a rejection without requiring protocol compatibility.
func (r Reject) Validate() error {
	if len(r.ProtocolRanges) > 0 {
		if _, err := CanonicalVersionRanges(r.ProtocolRanges); err != nil {
			return fmt.Errorf("protocol ranges: %w", err)
		}
	}
	if r.Role != RoleDaemon {
		return errors.New("rejection role must be daemon")
	}
	if r.DaemonVersion != "" {
		if err := validateWireToken("daemon version", r.DaemonVersion, maxVersionLength); err != nil {
			return err
		}
	}
	if err := r.Error.Validate(); err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}

	return nil
}

// NegotiateHello selects the highest shared protocol and capability
// intersection. Configuration failures use the same redacted internal error as
// other daemon failures rather than exposing implementation details.
func NegotiateHello(
	hello Hello,
	daemonVersion string,
	daemonRanges []VersionRange,
	daemonCapabilities []Capability,
) (Welcome, *Reject) {
	serverRanges, rangeErr := CanonicalVersionRanges(daemonRanges)
	if rangeErr != nil {
		return Welcome{}, internalHandshakeReject()
	}
	if err := validateWireToken("daemon version", daemonVersion, maxVersionLength); err != nil {
		return Welcome{}, internalHandshakeReject()
	}
	serverCapabilities, capabilityErr := CanonicalCapabilities(daemonCapabilities)
	if capabilityErr != nil {
		return Welcome{}, internalHandshakeReject()
	}
	if err := hello.Validate(); err != nil {
		return Welcome{}, &Reject{
			ProtocolRanges: serverRanges,
			Role:           RoleDaemon,
			DaemonVersion:  daemonVersion,
			Error:          NewWireError(ErrorCodeInvalidHandshake),
		}
	}

	selected, err := NegotiateVersion(hello.ProtocolRanges, serverRanges)
	if err != nil {
		return Welcome{}, &Reject{
			ProtocolRanges: serverRanges,
			Role:           RoleDaemon,
			DaemonVersion:  daemonVersion,
			Error:          NewWireError(ErrorCodeUnsupportedProtocol),
		}
	}
	clientCapabilities, _ := CanonicalCapabilities(hello.Capabilities)

	return Welcome{
		Protocol:       selected,
		ProtocolRanges: serverRanges,
		Role:           RoleDaemon,
		DaemonVersion:  daemonVersion,
		Capabilities:   intersectCapabilities(clientCapabilities, serverCapabilities),
	}, nil
}

// internalHandshakeReject avoids exposing a daemon configuration or startup
// failure before protocol negotiation has established a trusted shape.
func internalHandshakeReject() *Reject {
	return &Reject{
		Role:  RoleDaemon,
		Error: NewWireError(ErrorCodeInternal),
	}
}

// versionInRanges reports whether a selected version was advertised.
func versionInRanges(version Version, ranges []VersionRange) bool {
	for _, candidate := range ranges {
		if version.Major == candidate.Min.Major && version.Compare(candidate.Min) >= 0 && version.Compare(candidate.Max) <= 0 {
			return true
		}
	}

	return false
}

// intersectCapabilities returns a deterministic capability intersection.
func intersectCapabilities(client []Capability, server []Capability) []Capability {
	clientSet := make(map[Capability]struct{}, len(client))
	for _, capability := range client {
		clientSet[capability] = struct{}{}
	}

	intersection := make([]Capability, 0)
	for _, capability := range server {
		if _, ok := clientSet[capability]; ok {
			intersection = append(intersection, capability)
		}
	}

	return intersection
}
