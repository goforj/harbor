package domain

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maximumProcessBirthTokenBytes  = 512
	maximumExecutableIdentityBytes = 4096
	canonicalSHA256DigestLength    = 64
)

// SessionID identifies one project lifecycle independently of the process that currently carries it.
type SessionID string

// Validate reports whether the session ID is safe to persist and correlate across reconnects.
func (id SessionID) Validate() error {
	return validateIdentifier("session ID", string(id))
}

// SessionOwner identifies the user surface that retains authority over one project lifecycle.
type SessionOwner string

const (
	// SessionOwnerHarbor means Harbor launched and supervises the outer GoForj process.
	SessionOwnerHarbor SessionOwner = "harbor"
	// SessionOwnerTerminal means a foreground GoForj process retains terminal ownership.
	SessionOwnerTerminal SessionOwner = "terminal"
)

// Validate reports whether the session owner is recognized.
func (owner SessionOwner) Validate() error {
	switch owner {
	case SessionOwnerHarbor, SessionOwnerTerminal:
		return nil
	default:
		return fmt.Errorf("unknown session owner %q", owner)
	}
}

// SessionState describes the durable attachment boundary for one active project lifecycle.
type SessionState string

const (
	// SessionPlanned means durable session intent exists before a process identity is available.
	SessionPlanned SessionState = "planned"
	// SessionAwaitingAttach means the expected process exists but has not completed its authenticated attachment.
	SessionAwaitingAttach SessionState = "awaiting_attach"
	// SessionAttached means the expected process has completed its authenticated attachment.
	SessionAttached SessionState = "attached"
	// SessionStopping means graceful shutdown has begun for the correlated process.
	SessionStopping SessionState = "stopping"
	// SessionDisconnected means attachment was lost while retained process evidence remains available for recovery.
	SessionDisconnected SessionState = "disconnected"
)

// Validate reports whether the session state is recognized.
func (state SessionState) Validate() error {
	switch state {
	case SessionPlanned, SessionAwaitingAttach, SessionAttached, SessionStopping, SessionDisconnected:
		return nil
	default:
		return fmt.Errorf("unknown session state %q", state)
	}
}

// ProcessEvidence binds process authority to one exact executable birth rather than a reusable PID.
type ProcessEvidence struct {
	PID                int64  `json:"pid"`
	BirthToken         string `json:"birth_token"`
	ExecutableIdentity string `json:"executable_identity"`
	ArgumentDigest     string `json:"argument_digest"`
}

// OutputBrokerSession binds one optional owner-private output broker to the exact project lifecycle.
//
// The attachment credential itself is never persisted here. CredentialDigest proves which private
// manifest Harbor must read when re-adoption is implemented, while ManifestPath keeps that secret
// outside the database and checkout.
type OutputBrokerSession struct {
	// EndpointReference identifies the owner-private local endpoint served by the broker.
	EndpointReference string `json:"endpoint_reference"`
	// Process identifies the broker process independently from the managed GoForj process.
	Process ProcessEvidence `json:"process"`
	// ManifestPath identifies the owner-private file that contains the opaque broker ticket.
	ManifestPath string `json:"manifest_path"`
	// CredentialDigest identifies the opaque ticket held by the private manifest.
	CredentialDigest string `json:"credential_digest"`
}

// Validate reports whether broker evidence is complete without treating it as child-process authority.
func (broker OutputBrokerSession) Validate() error {
	if err := validateOutputBrokerEndpointReference(broker.EndpointReference); err != nil {
		return err
	}
	if err := broker.Process.Validate(); err != nil {
		return fmt.Errorf("output broker process evidence: %w", err)
	}
	if broker.ManifestPath == "" || !filepath.IsAbs(broker.ManifestPath) || filepath.Clean(broker.ManifestPath) != broker.ManifestPath {
		return fmt.Errorf("output broker manifest path must be a canonical absolute path")
	}
	return validateSHA256Digest("output broker credential digest", broker.CredentialDigest)
}

// Validate reports whether the evidence is complete and safe to use for later process correlation.
func (evidence ProcessEvidence) Validate() error {
	if evidence.PID <= 0 {
		return fmt.Errorf("process PID must be positive")
	}
	if err := validateBoundedProcessIdentity("process birth token", evidence.BirthToken, maximumProcessBirthTokenBytes); err != nil {
		return err
	}
	if err := validateBoundedProcessIdentity("process executable identity", evidence.ExecutableIdentity, maximumExecutableIdentityBytes); err != nil {
		return err
	}
	if !filepath.IsAbs(evidence.ExecutableIdentity) || filepath.Clean(evidence.ExecutableIdentity) != evidence.ExecutableIdentity {
		return fmt.Errorf("process executable identity must be a canonical absolute path")
	}
	return validateSHA256Digest("process argument digest", evidence.ArgumentDigest)
}

// ProjectSession is the one durable active session correlated to a registered project.
type ProjectSession struct {
	ID               SessionID            `json:"id"`
	ProjectID        ProjectID            `json:"project_id"`
	Owner            SessionOwner         `json:"owner"`
	State            SessionState         `json:"state"`
	DescriptorDigest string               `json:"descriptor_digest"`
	CredentialDigest string               `json:"credential_digest"`
	Generation       uint64               `json:"generation"`
	Process          *ProcessEvidence     `json:"process,omitempty"`
	OutputBroker     *OutputBrokerSession `json:"output_broker,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

// Validate reports whether the session has one complete lifecycle and process-ownership shape.
func (session ProjectSession) Validate() error {
	if err := session.ID.Validate(); err != nil {
		return err
	}
	if err := session.ProjectID.Validate(); err != nil {
		return err
	}
	if err := session.Owner.Validate(); err != nil {
		return err
	}
	if err := session.State.Validate(); err != nil {
		return err
	}
	if err := validateSHA256Digest("session descriptor digest", session.DescriptorDigest); err != nil {
		return err
	}
	if err := validateSHA256Digest("session credential digest", session.CredentialDigest); err != nil {
		return err
	}
	if session.Generation == 0 {
		return fmt.Errorf("session generation must be positive")
	}
	if session.State == SessionPlanned {
		if session.Process != nil {
			return fmt.Errorf("planned session must not contain process evidence")
		}
		if session.OutputBroker != nil {
			return fmt.Errorf("planned session must not contain output broker evidence")
		}
	} else {
		if session.Process == nil {
			return fmt.Errorf("%s session must contain process evidence", session.State)
		}
		if err := session.Process.Validate(); err != nil {
			return err
		}
		if session.OutputBroker != nil {
			if session.Owner != SessionOwnerHarbor {
				return fmt.Errorf("output broker evidence requires a Harbor-owned session")
			}
			if err := session.OutputBroker.Validate(); err != nil {
				return err
			}
		}
	}
	if err := validateCanonicalSessionTime("session creation time", session.CreatedAt); err != nil {
		return err
	}
	if err := validateCanonicalSessionTime("session update time", session.UpdatedAt); err != nil {
		return err
	}
	if session.UpdatedAt.Before(session.CreatedAt) {
		return fmt.Errorf("session update time must not precede creation time")
	}
	return nil
}

// validateSHA256Digest rejects alternative encodings so equality remains a stable security decision.
func validateSHA256Digest(name, digest string) error {
	if len(digest) != canonicalSHA256DigestLength {
		return fmt.Errorf("%s must contain exactly %d lowercase hexadecimal characters", name, canonicalSHA256DigestLength)
	}
	for _, character := range digest {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return fmt.Errorf("%s must contain exactly %d lowercase hexadecimal characters", name, canonicalSHA256DigestLength)
	}
	return nil
}

// validateBoundedProcessIdentity keeps platform evidence unambiguous without truncating security comparisons.
func validateBoundedProcessIdentity(name, value string, maximumBytes int) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	if containsControlCharacter(value) {
		return fmt.Errorf("%s must not contain control characters", name)
	}
	if len(value) > maximumBytes {
		return fmt.Errorf("%s must not exceed %d bytes", name, maximumBytes)
	}
	return nil
}

// validateOutputBrokerEndpointReference admits only canonical local endpoint shapes on both supported platforms.
func validateOutputBrokerEndpointReference(endpoint string) error {
	if err := validateBoundedProcessIdentity("output broker endpoint reference", endpoint, 4096); err != nil {
		return err
	}
	if strings.HasPrefix(endpoint, `\\.\pipe\`) {
		name := strings.TrimPrefix(endpoint, `\\.\pipe\`)
		if name == "" || strings.ContainsAny(name, `/\\`) {
			return fmt.Errorf("output broker named pipe reference must contain one name")
		}
		return nil
	}
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint {
		return fmt.Errorf("output broker endpoint reference must be a canonical absolute path")
	}
	return nil
}

// validateCanonicalSessionTime excludes local zones and monotonic readings from durable equality decisions.
func validateCanonicalSessionTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s must not be zero", name)
	}
	if value.Location() != time.UTC {
		return fmt.Errorf("%s must use canonical UTC", name)
	}
	if value != value.Round(0) {
		return fmt.Errorf("%s must not contain a monotonic reading", name)
	}
	return nil
}
