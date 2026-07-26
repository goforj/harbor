package releasebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/installpaths"
)

const (
	// ManifestSchemaVersion identifies the first canonical Harbor bundle manifest.
	ManifestSchemaVersion = 1
	// InstallationSchemaVersion identifies the first protected installed-state record.
	InstallationSchemaVersion = 1
	darwinArchitecture        = "arm64"
	darwinPackageFormat       = "pkg"
	darwinMinimumOS           = "15.0"
	developmentChannel        = "development"
)

var (
	versionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Target identifies one architecture-specific native package.
type Target struct {
	// OS identifies the operating system consumed by the package.
	OS string `json:"os"`
	// Architecture identifies the package's sole executable architecture.
	Architecture string `json:"architecture"`
	// PackageFormat identifies the native outer transaction.
	PackageFormat string `json:"package_format"`
	// MinimumOS identifies the oldest product profile admitted by this package.
	MinimumOS string `json:"minimum_os"`
}

// Component binds one logical product role to its fixed installed file.
type Component struct {
	// Role is the stable logical identity used across platform layouts.
	Role string `json:"role"`
	// Destination is the absolute path compiled into the macOS package contract.
	Destination string `json:"destination"`
	// Size is the exact sealed file length.
	Size int64 `json:"size"`
	// SHA256 is the lowercase digest of the complete file.
	SHA256 string `json:"sha256"`
	// Mode is the exact four-digit installed permission mode.
	Mode string `json:"mode"`
}

// Manifest commits one complete Harbor product build before native signing.
type Manifest struct {
	// SchemaVersion identifies the manifest parser contract.
	SchemaVersion int `json:"schema_version"`
	// ProductID prevents components from another product entering Harbor's package.
	ProductID string `json:"product_id"`
	// Version is the user-facing product version shared by every executable.
	Version string `json:"version"`
	// ReleaseSequence is the monotonic installation identity.
	ReleaseSequence uint64 `json:"release_sequence"`
	// Channel prevents a development package from being promoted as stable.
	Channel string `json:"channel"`
	// SourceRevision is the exact clean Git commit used by the native build.
	SourceRevision string `json:"source_revision"`
	// Target identifies the sole native package profile.
	Target Target `json:"target"`
	// ControlProtocol records the compatible daemon protocol major.
	ControlProtocol uint16 `json:"control_protocol"`
	// SnapshotSchema records the readable and writable snapshot schema.
	SnapshotSchema uint16 `json:"snapshot_schema"`
	// HelperProtocol records the helper ticket/evidence protocol generation.
	HelperProtocol uint16 `json:"helper_protocol"`
	// InstallationSchema records the protected installed-state contract.
	InstallationSchema uint16 `json:"installation_schema"`
	// Components contains every executable admitted to the package.
	Components []Component `json:"components"`
	// BundleDigest commits the canonical manifest with this field empty.
	BundleDigest string `json:"bundle_digest"`
}

// Installation records the selected development release without containing user application state.
type Installation struct {
	// SchemaVersion identifies the protected installed-state contract.
	SchemaVersion int `json:"schema_version"`
	// ProductID prevents another package from selecting Harbor's release root.
	ProductID string `json:"product_id"`
	// Channel binds the selected version to its release stream.
	Channel string `json:"channel"`
	// ReleaseSequence selects one immutable release directory.
	ReleaseSequence uint64 `json:"release_sequence"`
	// BundleDigest binds the selection to the sealed manifest.
	BundleDigest string `json:"bundle_digest"`
}

// DarwinConfig contains the release identity supplied by the trusted build workflow.
type DarwinConfig struct {
	// Version is the semantic product version embedded in every component.
	Version string
	// ReleaseSequence is the monotonic development package identity.
	ReleaseSequence uint64
	// SourceRevision is the exact clean Git commit.
	SourceRevision string
}

// SealDarwinDevelopmentPayload verifies and records Harbor's fixed Apple-silicon package payload.
func SealDarwinDevelopmentPayload(payloadRoot string, config DarwinConfig) (Manifest, error) {
	if err := validateDarwinConfig(config); err != nil {
		return Manifest{}, err
	}
	root, err := canonicalPayloadRoot(payloadRoot)
	if err != nil {
		return Manifest{}, err
	}
	components := darwinComponents(config.ReleaseSequence)
	for index := range components {
		sealed, sealErr := sealComponent(root, components[index])
		if sealErr != nil {
			return Manifest{}, sealErr
		}
		components[index] = sealed
	}
	manifest := Manifest{
		SchemaVersion:      ManifestSchemaVersion,
		ProductID:          installpaths.ProductID,
		Version:            config.Version,
		ReleaseSequence:    config.ReleaseSequence,
		Channel:            developmentChannel,
		SourceRevision:     config.SourceRevision,
		Target:             Target{OS: "darwin", Architecture: darwinArchitecture, PackageFormat: darwinPackageFormat, MinimumOS: darwinMinimumOS},
		ControlProtocol:    1,
		SnapshotSchema:     domain.SnapshotSchemaVersion,
		HelperProtocol:     helper.ProtocolVersion,
		InstallationSchema: InstallationSchemaVersion,
		Components:         components,
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.BundleDigest = digest
	if err := writeDarwinMetadata(root, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// validateDarwinConfig rejects mutable or ambiguous release identities before reading payload files.
func validateDarwinConfig(config DarwinConfig) error {
	if !versionPattern.MatchString(config.Version) {
		return fmt.Errorf("Darwin release version %q is not canonical semantic version text", config.Version)
	}
	if config.ReleaseSequence == 0 {
		return errors.New("Darwin release sequence must be positive")
	}
	if !revisionPattern.MatchString(config.SourceRevision) {
		return fmt.Errorf("Darwin source revision %q is not a lowercase full Git revision", config.SourceRevision)
	}
	return nil
}

// canonicalPayloadRoot requires an existing absolute directory so metadata cannot escape the staging tree.
func canonicalPayloadRoot(path string) (string, error) {
	if path == "" {
		return "", errors.New("Darwin package payload root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Darwin package payload root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	information, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect Darwin package payload root: %w", err)
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Darwin package payload root must be a real directory")
	}
	return absolute, nil
}

// darwinComponents enumerates every executable role instead of accepting caller-selected paths.
func darwinComponents(sequence uint64) []Component {
	releaseRoot := "/Library/Application Support/GoForj/Harbor/releases/" + strconv.FormatUint(sequence, 10)
	return []Component{
		{Role: "desktop", Destination: "/Applications/Harbor.app/Contents/MacOS/Harbor"},
		{Role: "daemon-launcher", Destination: "/Library/Application Support/GoForj/Harbor/daemon-launcher"},
		{Role: "cli", Destination: releaseRoot + "/bin/harbor"},
		{Role: "daemon", Destination: releaseRoot + "/bin/harbord"},
		{Role: "output-broker", Destination: releaseRoot + "/bin/outputbroker"},
		{Role: "helper", Destination: "/Library/PrivilegedHelperTools/com.goforj.harbor.helper"},
		{Role: "low-port-relay", Destination: "/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay"},
	}
}

// sealComponent verifies a fixed regular executable and records its immutable content facts.
func sealComponent(root string, component Component) (Component, error) {
	path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(component.Destination, "/")))
	information, err := os.Lstat(path)
	if err != nil {
		return Component{}, fmt.Errorf("inspect release component %q: %w", component.Role, err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 || information.Mode().Perm()&0o111 == 0 {
		return Component{}, fmt.Errorf("release component %q is not a regular executable", component.Role)
	}
	file, err := os.Open(path)
	if err != nil {
		return Component{}, fmt.Errorf("open release component %q: %w", component.Role, err)
	}
	defer func() {
		_ = file.Close()
	}()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return Component{}, fmt.Errorf("hash release component %q: %w", component.Role, err)
	}
	component.Size = information.Size()
	component.SHA256 = hex.EncodeToString(digest.Sum(nil))
	component.Mode = fmt.Sprintf("%04o", information.Mode().Perm())
	return component, nil
}

// manifestDigest computes the bundle commitment without a self-referential digest value.
func manifestDigest(manifest Manifest) (string, error) {
	manifest.BundleDigest = ""
	content, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode Harbor release manifest for digest: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

// writeDarwinMetadata publishes identical manifests into the app and selected release plus protected installation state.
func writeDarwinMetadata(root string, manifest Manifest) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Harbor release manifest: %w", err)
	}
	content = append(content, '\n')
	releaseRoot := filepath.Join(
		root,
		"Library",
		"Application Support",
		"GoForj",
		"Harbor",
		"releases",
		strconv.FormatUint(manifest.ReleaseSequence, 10),
	)
	destinations := []string{
		filepath.Join(root, "Applications", "Harbor.app", "Contents", "Library", "Harbor", "release-manifest.json"),
		filepath.Join(releaseRoot, "release-manifest.json"),
	}
	for _, destination := range destinations {
		if err := writeMetadataFile(destination, content, 0o644); err != nil {
			return err
		}
	}
	installation := Installation{
		SchemaVersion:   InstallationSchemaVersion,
		ProductID:       installpaths.ProductID,
		Channel:         manifest.Channel,
		ReleaseSequence: manifest.ReleaseSequence,
		BundleDigest:    manifest.BundleDigest,
	}
	installationContent, err := json.MarshalIndent(installation, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Harbor installation record: %w", err)
	}
	installationContent = append(installationContent, '\n')
	return writeMetadataFile(
		filepath.Join(root, "Library", "Application Support", "GoForj", "Harbor", "installation.json"),
		installationContent,
		0o644,
	)
}

// writeMetadataFile creates a new metadata file without replacing an ambiguous staged artifact.
func writeMetadataFile(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create release metadata directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create release metadata %q: %w", path, err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write release metadata %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync release metadata %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close release metadata %q: %w", path, err)
	}
	complete = true
	return nil
}
