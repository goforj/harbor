package projectprocess

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc/local"
)

const maximumOutputBrokerEndpointBytes = 4096

var (
	// ErrOutputBrokerPeerMismatch rejects a broker connection whose kernel identity or observed process birth differs from the expected broker.
	ErrOutputBrokerPeerMismatch = errors.New("output broker peer does not match expected process")
	// ErrOutputBrokerEndpointMismatch rejects a broker proof that is not bound to its owner-private endpoint.
	ErrOutputBrokerEndpointMismatch = errors.New("output broker endpoint does not match expected local shape")
)

// OutputBrokerPeer binds one broker endpoint to the project/session and exact broker process that own it.
//
// The process evidence is separate from the GoForj session evidence. A broker may carry output authority
// across a Harbor restart, but it must never become a substitute for the process Harbor is allowed to stop.
type OutputBrokerPeer struct {
	// ProjectID identifies the registered project whose output the broker retains.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID identifies the exact lifecycle whose output the broker retains.
	SessionID domain.SessionID `json:"session_id"`
	// EndpointReference identifies the owner-private local broker endpoint.
	EndpointReference string `json:"endpoint_reference"`
	// Process is immutable evidence for the broker process, not the child GoForj process.
	Process domain.ProcessEvidence `json:"process"`
}

// Validate reports whether the broker proof is complete without treating its endpoint or process fields as authority by themselves.
func (peer OutputBrokerPeer) Validate() error {
	if err := peer.ProjectID.Validate(); err != nil {
		return fmt.Errorf("output broker project ID: %w", err)
	}
	if err := peer.SessionID.Validate(); err != nil {
		return fmt.Errorf("output broker session ID: %w", err)
	}
	if err := validateOutputBrokerEndpointReference(peer.EndpointReference); err != nil {
		return err
	}
	if err := peer.Process.Validate(); err != nil {
		return fmt.Errorf("output broker process evidence: %w", err)
	}
	return nil
}

// AuthenticateOutputBrokerPeer combines operating-system peer admission with a fresh full process observation.
//
// The caller must obtain observedProcess from the platform process observer immediately before this check.
// The endpoint is authenticated by the local transport and the process evidence is compared field-for-field;
// no request-supplied PID, path, or endpoint can create authority on its own.
func AuthenticateOutputBrokerPeer(connection local.Conn, expected OutputBrokerPeer, observedProcess domain.ProcessEvidence) error {
	if connection == nil {
		return fmt.Errorf("%w: connection is missing", ErrOutputBrokerPeerMismatch)
	}
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("validate expected output broker peer: %w", err)
	}
	if err := observedProcess.Validate(); err != nil {
		return fmt.Errorf("validate observed output broker process: %w", err)
	}
	identity := connection.Peer()
	endpointConnection, ok := connection.(local.EndpointConn)
	if !ok || endpointConnection.EndpointReference() != expected.EndpointReference {
		return fmt.Errorf("%w: authenticated connection endpoint does not match expected broker endpoint", ErrOutputBrokerPeerMismatch)
	}
	if identity.UserID == "" || strings.TrimSpace(identity.UserID) != identity.UserID || identity.ProcessID == 0 {
		return fmt.Errorf("%w: local transport returned an incomplete operating-system identity", ErrOutputBrokerPeerMismatch)
	}
	if int64(identity.ProcessID) != expected.Process.PID {
		return fmt.Errorf("%w: kernel process ID %d differs from expected broker process %d", ErrOutputBrokerPeerMismatch, identity.ProcessID, expected.Process.PID)
	}
	if observedProcess != expected.Process {
		return fmt.Errorf("%w: observed broker process evidence differs from expected birth", ErrOutputBrokerPeerMismatch)
	}
	return nil
}

// validateOutputBrokerEndpointReference accepts only canonical owner-private local endpoint shapes.
func validateOutputBrokerEndpointReference(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("%w: endpoint reference is required", ErrOutputBrokerEndpointMismatch)
	}
	if !utf8.ValidString(endpoint) || len([]byte(endpoint)) > maximumOutputBrokerEndpointBytes {
		return fmt.Errorf("%w: endpoint reference must be valid UTF-8 of at most %d bytes", ErrOutputBrokerEndpointMismatch, maximumOutputBrokerEndpointBytes)
	}
	if strings.IndexByte(endpoint, 0) >= 0 {
		return fmt.Errorf("%w: endpoint reference contains NUL", ErrOutputBrokerEndpointMismatch)
	}
	for _, character := range endpoint {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: endpoint reference contains a control character", ErrOutputBrokerEndpointMismatch)
		}
	}
	if strings.HasPrefix(endpoint, `\\.\pipe\`) {
		if len(endpoint) == len(`\\.\pipe\`) {
			return fmt.Errorf("%w: named pipe must include a name", ErrOutputBrokerEndpointMismatch)
		}
		if strings.ContainsAny(endpoint[len(`\\.\pipe\`):], `/\\`) {
			return fmt.Errorf("%w: named pipe name contains a path separator", ErrOutputBrokerEndpointMismatch)
		}
		return nil
	}
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint {
		return fmt.Errorf("%w: Unix endpoint must be a canonical absolute path", ErrOutputBrokerEndpointMismatch)
	}
	return nil
}
