package projectprocess

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const (
	// ManagedLaunchContextSchemaVersion identifies the owner-only inherited launch context wire shape.
	ManagedLaunchContextSchemaVersion = "managed-launch-context.v1"
	// ManagedLaunchContextEnvironment names the reserved environment value carrying the context file path.
	ManagedLaunchContextEnvironment   = "FORJ_INTERNAL_MANAGED_CONTEXT"
	managedLaunchContextFilePrefix    = "managed-launch-"
	managedLaunchContextMaximumBytes  = 16 << 10
	managedLaunchContextMaximumPath   = 4096
	managedLaunchContextMinimumTicket = 32
	managedLaunchContextMaximumTicket = 512
)

// ManagedLaunchContext binds one Harbor-launched GoForj process to its exact session authority.
//
// The ticket is intentionally kept only in this owner-private, one-use file. It is not written to
// the checkout, durable state, command arguments, or the ordinary project environment.
type ManagedLaunchContext struct {
	// SchemaVersion identifies the context shape understood by both launchers.
	SchemaVersion string `json:"schema_version"`
	// ProjectID binds the launch to the project admission that created the context.
	ProjectID domain.ProjectID `json:"project_id"`
	// SessionID binds the launch to one durable lifecycle generation.
	SessionID domain.SessionID `json:"session_id"`
	// ProjectRoot binds the launch to the canonical checkout path admitted by Harbor.
	ProjectRoot string `json:"project_root"`
	// ExpectedSessionGeneration prevents a context from attaching a later replacement session.
	ExpectedSessionGeneration uint64 `json:"expected_session_generation"`
	// DescriptorDigest binds the child to the descriptor preflight used for this launch.
	DescriptorDigest string `json:"descriptor_digest"`
	// EndpointReference identifies the authenticated Harbor IPC endpoint without carrying a socket secret.
	EndpointReference string `json:"endpoint_reference"`
	// Owner identifies the lifecycle authority that issued the context.
	Owner domain.SessionOwner `json:"owner"`
	// Ticket is the one-use credential presented by the future managed-session handshake.
	Ticket string `json:"ticket"`
}

// Validate rejects incomplete or ambiguous inherited launch authority before a process is started.
func (context ManagedLaunchContext) Validate() error {
	if context.SchemaVersion != ManagedLaunchContextSchemaVersion {
		return fmt.Errorf("managed launch context schema version %q is unsupported", context.SchemaVersion)
	}
	if err := context.ProjectID.Validate(); err != nil {
		return fmt.Errorf("managed launch context project ID: %w", err)
	}
	if err := context.SessionID.Validate(); err != nil {
		return fmt.Errorf("managed launch context session ID: %w", err)
	}
	if err := validateManagedLaunchRoot(context.ProjectRoot); err != nil {
		return err
	}
	if context.ExpectedSessionGeneration == 0 {
		return errors.New("managed launch context session generation must be positive")
	}
	if err := validateManagedLaunchDigest(context.DescriptorDigest); err != nil {
		return err
	}
	if err := validateManagedLaunchEndpoint(context.EndpointReference); err != nil {
		return err
	}
	if context.Owner != domain.SessionOwnerHarbor {
		return fmt.Errorf("managed launch context owner %q is not Harbor", context.Owner)
	}
	if err := validateManagedLaunchTicket(context.Ticket); err != nil {
		return err
	}
	return nil
}

// Clone returns an immutable-by-convention copy suitable for a StartRequest boundary.
func (context ManagedLaunchContext) Clone() ManagedLaunchContext {
	return context
}

// writeManagedLaunchContext writes one complete context before its path is inherited by a child.
func writeManagedLaunchContext(context ManagedLaunchContext) (string, error) {
	if err := context.Validate(); err != nil {
		return "", fmt.Errorf("validate managed launch context: %w", err)
	}
	directory, err := runtimepath.Directory()
	if err != nil {
		return "", fmt.Errorf("resolve managed launch runtime directory: %w", err)
	}
	if err := prepareManagedLaunchDirectory(directory); err != nil {
		return "", err
	}
	randomPart := make([]byte, 16)
	if _, err := rand.Read(randomPart); err != nil {
		return "", fmt.Errorf("generate managed launch context name: %w", err)
	}
	path := filepath.Join(directory, managedLaunchContextFilePrefix+hex.EncodeToString(randomPart)+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create managed launch context: %w", err)
	}
	encoded, encodeErr := json.Marshal(context)
	if encodeErr == nil {
		_, encodeErr = file.Write(encoded)
	}
	if encodeErr == nil {
		encodeErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		var writeErr error
		if encodeErr != nil {
			writeErr = fmt.Errorf("write managed launch context: %w", encodeErr)
		}
		return "", errors.Join(writeErr, closeErr)
	}
	return path, nil
}

// removeManagedLaunchContext retires a context after child capture or failed process setup.
func removeManagedLaunchContext(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove managed launch context: %w", err)
	}
	return nil
}

// prepareManagedLaunchDirectory keeps inherited credentials inside an owner-only runtime leaf.
func prepareManagedLaunchDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("managed launch runtime directory %q is not an absolute path", path)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create managed launch runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect managed launch runtime directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed launch runtime directory %q is a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed launch runtime path %q is not a directory", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure managed launch runtime directory: %w", err)
	}
	return nil
}

// validateManagedLaunchRoot keeps the project identity canonical without resolving the checkout twice.
func validateManagedLaunchRoot(root string) error {
	if root == "" || !utf8.ValidString(root) || len([]byte(root)) > managedLaunchContextMaximumPath {
		return errors.New("managed launch context project root is invalid")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("managed launch context project root must be a canonical absolute path")
	}
	return nil
}

// validateManagedLaunchDigest keeps descriptor equality independent of alternate encodings.
func validateManagedLaunchDigest(digest string) error {
	if len(digest) != 64 {
		return errors.New("managed launch context descriptor digest must contain 64 lowercase hexadecimal characters")
	}
	for _, character := range digest {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return errors.New("managed launch context descriptor digest must contain 64 lowercase hexadecimal characters")
	}
	return nil
}

// validateManagedLaunchEndpoint accepts only platform-local absolute endpoint references.
func validateManagedLaunchEndpoint(endpoint string) error {
	if endpoint == "" || !utf8.ValidString(endpoint) || len([]byte(endpoint)) > managedLaunchContextMaximumPath {
		return errors.New("managed launch context endpoint reference is invalid")
	}
	if !filepath.IsAbs(endpoint) && !strings.HasPrefix(endpoint, `\\.\pipe\`) {
		return errors.New("managed launch context endpoint reference must be local and absolute")
	}
	if strings.IndexByte(endpoint, 0) >= 0 {
		return errors.New("managed launch context endpoint reference contains NUL")
	}
	return nil
}

// validateManagedLaunchTicket bounds opaque credential material without exposing its value in errors.
func validateManagedLaunchTicket(ticket string) error {
	if len([]byte(ticket)) < managedLaunchContextMinimumTicket || len([]byte(ticket)) > managedLaunchContextMaximumTicket {
		return fmt.Errorf("managed launch context ticket must contain between %d and %d bytes", managedLaunchContextMinimumTicket, managedLaunchContextMaximumTicket)
	}
	if strings.TrimSpace(ticket) != ticket || !utf8.ValidString(ticket) || strings.IndexByte(ticket, 0) >= 0 {
		return errors.New("managed launch context ticket is invalid")
	}
	return nil
}
