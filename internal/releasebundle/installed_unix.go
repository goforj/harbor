//go:build darwin || linux

package releasebundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/installpaths"
)

const installedMetadataLimit = 1024 * 1024

// VerifyInstalledDarwinDevelopmentRelease re-admits the fixed installed development bundle before privileged lifecycle work.
func VerifyInstalledDarwinDevelopmentRelease() (Manifest, error) {
	return verifyInstalledDarwinDevelopmentRelease("", 0, 0)
}

// verifyInstalledDarwinDevelopmentRelease permits a redirected owner-controlled root only for deterministic package tests.
func verifyInstalledDarwinDevelopmentRelease(filesystemRoot string, expectedUserID uint32, expectedGroupID uint32) (Manifest, error) {
	root, err := installedFilesystemRoot(filesystemRoot)
	if err != nil {
		return Manifest{}, err
	}
	machineRoot := installedPath(root, "/Library/Application Support/GoForj/Harbor")
	installationPath := filepath.Join(machineRoot, "installation.json")
	var installation Installation
	if err := decodeInstalledMetadata(installationPath, &installation); err != nil {
		return Manifest{}, fmt.Errorf("admit Harbor installation record: %w", err)
	}
	if err := validateInstalledSelection(installation); err != nil {
		return Manifest{}, err
	}

	sequence := strconv.FormatUint(installation.ReleaseSequence, 10)
	currentPath := filepath.Join(machineRoot, "current")
	if err := verifyInstalledSymlink(currentPath, filepath.Join("releases", sequence), expectedUserID, expectedGroupID); err != nil {
		return Manifest{}, err
	}
	releaseRoot := filepath.Join(machineRoot, "releases", sequence)
	releaseManifestPath := filepath.Join(releaseRoot, "release-manifest.json")
	appManifestPath := installedPath(root, "/Applications/Harbor.app/Contents/Library/Harbor/release-manifest.json")
	var releaseManifest Manifest
	if err := decodeInstalledMetadata(releaseManifestPath, &releaseManifest); err != nil {
		return Manifest{}, fmt.Errorf("admit selected Harbor release manifest: %w", err)
	}
	var appManifest Manifest
	if err := decodeInstalledMetadata(appManifestPath, &appManifest); err != nil {
		return Manifest{}, fmt.Errorf("admit Harbor application manifest: %w", err)
	}
	if !reflect.DeepEqual(releaseManifest, appManifest) {
		return Manifest{}, errors.New("installed Harbor application and selected release manifests differ")
	}
	if err := validateInstalledManifest(releaseManifest, installation); err != nil {
		return Manifest{}, err
	}

	for index, plan := range darwinComponentPlans(installation.ReleaseSequence) {
		component := releaseManifest.Components[index]
		if err := verifyInstalledComponent(root, component, expectedUserID, expectedGroupID); err != nil {
			return Manifest{}, err
		}
		if plan.source != "" {
			source := installedPath(root, plan.source)
			if err := verifyInstalledFile(source, component.Size, component.SHA256, 0o755, expectedUserID, expectedGroupID); err != nil {
				return Manifest{}, fmt.Errorf("admit installed Harbor %s immutable source: %w", component.Role, err)
			}
		}
	}
	cliPath := installedPath(root, "/usr/local/bin/harbor")
	if err := verifyInstalledSymlink(
		cliPath,
		"/Library/Application Support/GoForj/Harbor/current/bin/harbor",
		expectedUserID,
		expectedGroupID,
	); err != nil {
		return Manifest{}, err
	}
	for _, path := range []string{installationPath, releaseManifestPath, appManifestPath} {
		if err := verifyInstalledMetadataFile(path, expectedUserID, expectedGroupID); err != nil {
			return Manifest{}, err
		}
	}
	return releaseManifest, nil
}

// installedFilesystemRoot validates the optional test-only prefix without resolving installed symlinks through it.
func installedFilesystemRoot(root string) (string, error) {
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("installed Harbor verification root must be absolute and canonical")
	}
	information, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect installed Harbor verification root: %w", err)
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("installed Harbor verification root must be a real directory")
	}
	return root, nil
}

// installedPath maps one compiled absolute package destination beneath an optional test-only filesystem root.
func installedPath(root string, destination string) string {
	if root == "" {
		return destination
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(destination, "/")))
}

// decodeInstalledMetadata rejects oversized, unknown, trailing, and ambiguous installation metadata.
func decodeInstalledMetadata(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := json.NewDecoder(io.LimitReader(file, installedMetadataLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if position > installedMetadataLimit {
		return errors.New("metadata exceeds the installed Harbor size limit")
	}
	return nil
}

// validateInstalledSelection rejects an installation record that cannot select one known development release.
func validateInstalledSelection(installation Installation) error {
	if installation.SchemaVersion != InstallationSchemaVersion {
		return fmt.Errorf("installed Harbor schema version is %d, want %d", installation.SchemaVersion, InstallationSchemaVersion)
	}
	if installation.ProductID != installpaths.ProductID {
		return fmt.Errorf("installed product ID is %q, want %q", installation.ProductID, installpaths.ProductID)
	}
	if installation.Channel != developmentChannel {
		return fmt.Errorf("installed Harbor channel is %q, want %q", installation.Channel, developmentChannel)
	}
	if installation.ReleaseSequence == 0 {
		return errors.New("installed Harbor release sequence is zero")
	}
	if !validInstalledDigest(installation.BundleDigest) {
		return errors.New("installed Harbor bundle digest is not canonical SHA-256")
	}
	return nil
}

// validateInstalledManifest binds every admitted field and component to the package's compiled Darwin contract.
func validateInstalledManifest(manifest Manifest, installation Installation) error {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("installed Harbor manifest schema version is %d, want %d", manifest.SchemaVersion, ManifestSchemaVersion)
	}
	if manifest.ProductID != installation.ProductID ||
		manifest.Channel != installation.Channel ||
		manifest.ReleaseSequence != installation.ReleaseSequence ||
		manifest.BundleDigest != installation.BundleDigest {
		return errors.New("installed Harbor manifest does not match the installation selection")
	}
	if !versionPattern.MatchString(manifest.Version) || !revisionPattern.MatchString(manifest.SourceRevision) {
		return errors.New("installed Harbor manifest release identity is not canonical")
	}
	if manifest.Target != (Target{
		OS:            "darwin",
		Architecture:  darwinArchitecture,
		PackageFormat: darwinPackageFormat,
		MinimumOS:     darwinMinimumOS,
	}) {
		return fmt.Errorf("installed Harbor target is %#v, want the fixed Darwin package target", manifest.Target)
	}
	if manifest.ControlProtocol != 1 ||
		manifest.SnapshotSchema != domain.SnapshotSchemaVersion ||
		manifest.HelperProtocol != helper.ProtocolVersion ||
		manifest.InstallationSchema != InstallationSchemaVersion {
		return errors.New("installed Harbor protocol or schema identity is incompatible")
	}
	plans := darwinComponentPlans(installation.ReleaseSequence)
	if len(manifest.Components) != len(plans) {
		return fmt.Errorf("installed Harbor component count is %d, want %d", len(manifest.Components), len(plans))
	}
	for index, plan := range plans {
		component := manifest.Components[index]
		wantMode := plan.mode
		if wantMode == "" {
			wantMode = "0755"
		}
		if component.Role != plan.component.Role ||
			component.Destination != plan.component.Destination ||
			component.Mode != wantMode ||
			component.Size <= 0 ||
			!validInstalledDigest(component.SHA256) {
			return fmt.Errorf("installed Harbor component %d does not match the fixed package plan", index)
		}
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	if !bytes.Equal([]byte(digest), []byte(manifest.BundleDigest)) {
		return errors.New("installed Harbor manifest bundle digest does not verify")
	}
	return nil
}

// validInstalledDigest admits only lowercase fixed-width SHA-256 text.
func validInstalledDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// verifyInstalledComponent validates one manifest-selected fixed executable without following a substituted symlink.
func verifyInstalledComponent(root string, component Component, expectedUserID uint32, expectedGroupID uint32) error {
	mode, err := strconv.ParseUint(component.Mode, 8, 32)
	if err != nil {
		return fmt.Errorf("parse installed Harbor %s mode: %w", component.Role, err)
	}
	path := installedPath(root, component.Destination)
	if err := verifyInstalledFile(path, component.Size, component.SHA256, uint32(mode), expectedUserID, expectedGroupID); err != nil {
		return fmt.Errorf("admit installed Harbor %s: %w", component.Role, err)
	}
	return nil
}

// verifyInstalledFile compares content, mode, and native ownership for one exact regular file.
func verifyInstalledFile(path string, size int64, digest string, mode uint32, expectedUserID uint32, expectedGroupID uint32) error {
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return errors.New("object is not a regular file")
	}
	if information.Size() != size || nativeMode(information.Mode()) != mode {
		return errors.New("file size or mode differs from the sealed identity")
	}
	if err := verifyInstalledOwner(information, expectedUserID, expectedGroupID); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("file digest differs from the sealed identity")
	}
	return nil
}

// verifyInstalledMetadataFile pins protected metadata to an ordinary root-owned regular file.
func verifyInstalledMetadataFile(path string, expectedUserID uint32, expectedGroupID uint32) error {
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !information.Mode().IsRegular() || nativeMode(information.Mode()) != 0o644 {
		return fmt.Errorf("installed Harbor metadata %q has an unsafe file identity", path)
	}
	if err := verifyInstalledOwner(information, expectedUserID, expectedGroupID); err != nil {
		return fmt.Errorf("installed Harbor metadata %q: %w", path, err)
	}
	return nil
}

// verifyInstalledSymlink admits only a package-owned symlink with an exact non-ambient target.
func verifyInstalledSymlink(path string, target string, expectedUserID uint32, expectedGroupID uint32) error {
	information, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if information.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("installed Harbor path %q is not a symlink", path)
	}
	if err := verifyInstalledOwner(information, expectedUserID, expectedGroupID); err != nil {
		return fmt.Errorf("installed Harbor symlink %q: %w", path, err)
	}
	observed, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if observed != target {
		return fmt.Errorf("installed Harbor symlink %q targets %q, want %q", path, observed, target)
	}
	return nil
}

// verifyInstalledOwner reads native identities because os.FileInfo does not expose package ownership portably.
func verifyInstalledOwner(information os.FileInfo, expectedUserID uint32, expectedGroupID uint32) error {
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("native file ownership is unavailable")
	}
	if status.Uid != expectedUserID || status.Gid != expectedGroupID {
		return fmt.Errorf(
			"native owner is %d:%d, want %d:%d",
			status.Uid,
			status.Gid,
			expectedUserID,
			expectedGroupID,
		)
	}
	return nil
}
