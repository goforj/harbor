package control

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
)

const (
	// CapabilityV1 identifies the first typed CLI and desktop control surface.
	CapabilityV1 rpc.Capability = "control.v1"
	// CapabilityProjectRegistrationV1 identifies the additive local-project registration surface.
	CapabilityProjectRegistrationV1 rpc.Capability = "control.project-registration.v1"

	methodDaemonStatus    = "control.v1.daemon.status"
	methodSnapshot        = "control.v1.snapshot"
	methodProjectRegister = "control.v1.project.register"
	maximumBuildToken     = 128
)

var protocolV1 = rpc.Version{Major: 1, Minor: 0}

// DaemonState identifies whether the responding daemon has completed startup.
type DaemonState string

const (
	// DaemonStateReady means startup checks have completed and the control API is serving.
	DaemonStateReady DaemonState = "ready"
)

// Build identifies the product binary serving a control connection.
type Build struct {
	// Version is the shared Harbor product release.
	Version string `json:"version"`
	// Revision is the source revision embedded by the Go toolchain when available.
	Revision string `json:"revision,omitempty"`
	// Modified records whether the executable was built from a changed checkout.
	Modified bool `json:"modified"`
}

// DaemonStatus is the standalone diagnostic returned by a ready harbord process.
type DaemonStatus struct {
	// State reports the daemon lifecycle state represented by this response.
	State DaemonState `json:"state"`
	// Build identifies the product binary serving the request.
	Build Build `json:"build"`
	// Protocol is the exact control protocol used for the response.
	Protocol rpc.Version `json:"protocol"`
	// Capabilities lists the independently negotiated product surfaces available on the connection.
	Capabilities []rpc.Capability `json:"capabilities"`
	// SnapshotSchemaVersion identifies the domain snapshot schema returned by Snapshot.
	SnapshotSchemaVersion uint16 `json:"snapshot_schema_version"`
	// Sequence is the latest durable operation-journal revision readable by the daemon.
	Sequence domain.Sequence `json:"sequence"`
}

// Validate reports whether status contains the one supported ready control shape.
func (status DaemonStatus) Validate() error {
	if status.State != DaemonStateReady {
		return fmt.Errorf("unsupported daemon state %q", status.State)
	}
	if err := validateBuild(status.Build); err != nil {
		return err
	}
	if status.Protocol.Compare(protocolV1) != 0 {
		return fmt.Errorf("unsupported control protocol %s", status.Protocol)
	}
	capabilities, err := rpc.CanonicalCapabilities(status.Capabilities)
	if err != nil {
		return fmt.Errorf("daemon capabilities: %w", err)
	}
	if !slices.Equal(capabilities, status.Capabilities) {
		return errors.New("daemon capabilities must be canonical")
	}
	if !containsCapability(capabilities, CapabilityV1) {
		return errors.New("daemon status must advertise control.v1")
	}
	if status.SnapshotSchemaVersion != domain.SnapshotSchemaVersion {
		return fmt.Errorf("unsupported snapshot schema version %d", status.SnapshotSchemaVersion)
	}
	if uint64(status.Sequence) > rpc.MaximumSequence {
		return fmt.Errorf("daemon sequence exceeds %d", rpc.MaximumSequence)
	}

	return nil
}

// validateServingStatus proves authority output describes this server and the request's negotiated session.
func validateServingStatus(status DaemonStatus, servingBuild buildinfo.Info, peer session.Peer) error {
	if err := status.Validate(); err != nil {
		return err
	}
	if status.Build != buildFromInfo(servingBuild) {
		return errors.New("daemon status build does not match the serving process")
	}

	return validateStatusNegotiation(status, peer)
}

// validateReceivedStatus proves daemon status agrees with identity established during the client handshake.
func validateReceivedStatus(status DaemonStatus, peer session.Peer) error {
	if err := status.Validate(); err != nil {
		return err
	}
	if status.Build.Version != peer.BuildVersion {
		return errors.New("daemon status version does not match the negotiated daemon")
	}

	return validateStatusNegotiation(status, peer)
}

// validateStatusNegotiation prevents a status response from claiming unnegotiated protocol features.
func validateStatusNegotiation(status DaemonStatus, peer session.Peer) error {
	if status.Protocol.Compare(peer.Protocol) != 0 {
		return errors.New("daemon status protocol does not match the negotiated session")
	}
	if !slices.Equal(status.Capabilities, peer.Capabilities) {
		return errors.New("daemon status capabilities do not match the negotiated session")
	}

	return nil
}

// validateControlSnapshot keeps sequence bounds and empty collections stable across every product client.
func validateControlSnapshot(snapshot domain.Snapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if uint64(snapshot.Sequence) > rpc.MaximumSequence {
		return fmt.Errorf("snapshot sequence exceeds %d", rpc.MaximumSequence)
	}
	if snapshot.Projects == nil || snapshot.Operations == nil || snapshot.RecentResourceIDs == nil {
		return errors.New("snapshot collections must be initialized")
	}
	for _, project := range snapshot.Projects {
		if project.Apps == nil || project.Services == nil || project.Resources == nil {
			return fmt.Errorf("project %q snapshot collections must be initialized", project.ID)
		}
	}

	return nil
}

// statusResponse keeps the method result extensible without changing the status object itself.
type statusResponse struct {
	Status DaemonStatus `json:"status"`
}

// snapshotResponse keeps the method result extensible without changing the authoritative domain object.
type snapshotResponse struct {
	Snapshot domain.Snapshot `json:"snapshot"`
}

// protocolRanges returns a fresh copy so connection configuration cannot mutate package policy.
func protocolRanges() []rpc.VersionRange {
	return []rpc.VersionRange{{Min: protocolV1, Max: protocolV1}}
}

// capabilities returns a fresh copy so connection configuration cannot mutate package policy.
func capabilities() []rpc.Capability {
	return []rpc.Capability{CapabilityProjectRegistrationV1, CapabilityV1}
}

// buildFromInfo projects process metadata into the reviewed status JSON shape.
func buildFromInfo(info buildinfo.Info) Build {
	return Build{
		Version:  info.Version,
		Revision: info.Revision,
		Modified: info.Modified,
	}
}

// validateBuild bounds diagnostic tokens before they cross the product API.
func validateBuild(build Build) error {
	if err := validateBuildToken("build version", build.Version, false); err != nil {
		return err
	}
	return validateBuildToken("build revision", build.Revision, true)
}

// validateBuildToken mirrors the portable protocol-token alphabet for standalone status fields.
func validateBuildToken(name string, value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	if len(value) > maximumBuildToken {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumBuildToken)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !isBuildTokenCharacter(byte(character)) {
			return fmt.Errorf("%s contains an unsupported character", name)
		}
	}
	return nil
}

// isBuildTokenCharacter keeps status metadata safe to place in one-line diagnostics.
func isBuildTokenCharacter(character byte) bool {
	if character >= 'a' && character <= 'z' {
		return true
	}
	if character >= 'A' && character <= 'Z' {
		return true
	}
	if character >= '0' && character <= '9' {
		return true
	}
	return character == '.' || character == '_' || character == '-' || character == ':' || character == '+'
}
