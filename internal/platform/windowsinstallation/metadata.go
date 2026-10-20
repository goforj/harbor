// Package windowsinstallation admits Harbor's fixed, selected Windows development installation.
package windowsinstallation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	// ProductID binds the Windows installation to Harbor's immutable product identity.
	ProductID = "com.goforj.harbor"
	// SchemaVersion identifies the first Windows-only protected installation contract.
	SchemaVersion = 1
	// MetadataLimit bounds each protected metadata document before parsing.
	MetadataLimit = 1024 * 1024
)

var (
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	thumbprintPattern = regexp.MustCompile(`^[A-F0-9]{40}$`)
	userSIDPattern    = regexp.MustCompile(`^S-1-(?:5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+|12-1-[0-9]+-[0-9]+-[0-9]+-[0-9]+)$`)
	identityPattern   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	revisionPattern   = regexp.MustCompile(`^[a-f0-9]{40}$`)
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-(dev|rc)\.[0-9]+$`)
)

// Selection commits one complete release without containing per-user application state.
type Selection struct {
	// SchemaVersion rejects unknown installation contracts before paths are selected.
	SchemaVersion int `json:"schema_version"`
	// ProductID prevents another product from selecting Harbor components.
	ProductID string `json:"product_id"`
	// InstallationID binds owned task definitions to this machine installation.
	InstallationID string `json:"installation_id"`
	// UserSID binds this first same-user Windows installation to its intended interactive account.
	UserSID string `json:"user_sid"`
	// ReleaseSequence selects one immutable payload directory.
	ReleaseSequence uint64 `json:"release_sequence"`
	// BundleDigest binds the complete selected component set.
	BundleDigest string `json:"bundle_digest"`
	// SignerThumbprint pins the admitted Authenticode publisher.
	SignerThumbprint string `json:"signer_thumbprint"`
}

// Component commits one role at its compiled relative installed destination.
type Component struct {
	// Role identifies one compiled product capability.
	Role string `json:"role"`
	// Path is fixed by the component plan rather than supplied by clients.
	Path string `json:"path"`
	// Size bounds the exact signed payload admitted from disk.
	Size int64 `json:"size"`
	// SHA256 detects signed-file replacement within the selected payload.
	SHA256 string `json:"sha256"`
}

// Manifest commits the complete signed payload selected by the machine-owned record.
type Manifest struct {
	// Architecture prevents a signed component set for another machine profile being selected.
	Architecture string `json:"architecture"`
	// SchemaVersion rejects unknown installation contracts before paths are selected.
	SchemaVersion int `json:"schema_version"`
	// ProductID prevents another product from selecting Harbor components.
	ProductID string `json:"product_id"`
	// Version limits this unsigned distribution channel to development candidates.
	Version string `json:"version"`
	// SourceRevision identifies the exact reviewed product source.
	SourceRevision string `json:"source_revision"`
	// ReleaseSequence selects one immutable payload directory.
	ReleaseSequence uint64 `json:"release_sequence"`
	// SignerThumbprint pins the admitted Authenticode publisher.
	SignerThumbprint string `json:"signer_thumbprint"`
	// Components requires the complete ordered product role set.
	Components []Component `json:"components"`
	// BundleDigest binds the complete selected component set.
	BundleDigest string `json:"bundle_digest"`
}

// Plans returns the only component destinations the Windows installer and launchers may select.
func Plans(sequence uint64) []Component {
	release := "releases/" + strconv.FormatUint(sequence, 10) + "/"
	return []Component{
		{Role: "desktop-launcher", Path: "desktop-launcher.exe"},
		{Role: "daemon-launcher", Path: "daemon-launcher.exe"},
		{Role: "cli-launcher", Path: "harbor.exe"},
		{Role: "desktop", Path: release + "harbor-desktop.exe"},
		{Role: "daemon", Path: release + "harbord.exe"},
		{Role: "cli", Path: release + "harbor.exe"},
		{Role: "output-broker", Path: release + "outputbroker.exe"},
		{Role: "helper", Path: "harbor-helper.exe"},
		{Role: "installer", Path: release + "harbor-installer.exe"},
		{Role: "uninstaller", Path: "Uninstall.exe"},
		{Role: "task-policy", Path: "harbor-daemon-task.ps1"},
		{Role: "uninstall-policy", Path: "harbor-uninstall.ps1"},
		{Role: "signer-certificate", Path: "harbor-helper-signing.cer"},
		{Role: "signer-thumbprint", Path: "harbor-helper-signing-thumbprint.txt"},
	}
}

// Decode reads bounded canonical JSON so duplicate keys and ambiguous metadata cannot enter admission.
func Decode(reader io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(reader, MetadataLimit+1))
	if err != nil {
		return err
	}
	if len(data) > MetadataLimit {
		return errors.New("Windows installation metadata exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	canonical, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(data), canonical) {
		return errors.New("Windows installation metadata must be canonical JSON")
	}
	return nil
}

// ValidateSelection rejects incomplete, foreign, or mutable installed selections before selecting paths.
func ValidateSelection(selection Selection) error {
	if !validUserSID(selection.UserSID) {
		return errors.New("Windows installation requires its intended interactive account SID")
	}
	if selection.SchemaVersion != SchemaVersion || selection.ProductID != ProductID || !identityPattern.MatchString(selection.InstallationID) || selection.ReleaseSequence == 0 || !digestPattern.MatchString(selection.BundleDigest) || !thumbprintPattern.MatchString(selection.SignerThumbprint) {
		return errors.New("Windows installation selection has incompatible or incomplete identity")
	}
	return nil
}

// Digest commits canonical metadata with its self-referential digest omitted.
func Digest(manifest Manifest) (string, error) {
	manifest.BundleDigest = ""
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateManifest binds every component and publisher to the committed selection and fixed product plan.
func ValidateManifest(manifest Manifest, selection Selection) error {
	if err := ValidateSelection(selection); err != nil {
		return err
	}
	if manifest.Architecture != "amd64" || manifest.SchemaVersion != SchemaVersion || manifest.ProductID != ProductID || manifest.ReleaseSequence != selection.ReleaseSequence || manifest.SignerThumbprint != selection.SignerThumbprint || manifest.BundleDigest != selection.BundleDigest || !versionPattern.MatchString(manifest.Version) || !revisionPattern.MatchString(manifest.SourceRevision) {
		return errors.New("Windows release manifest differs from committed installation identity")
	}
	plans := Plans(selection.ReleaseSequence)
	if len(manifest.Components) != len(plans) {
		return errors.New("Windows release component set differs from fixed plan")
	}
	for index, component := range manifest.Components {
		if component.Role != plans[index].Role || component.Path != plans[index].Path || component.Size <= 0 || !digestPattern.MatchString(component.SHA256) || path.Clean(component.Path) != component.Path || strings.Contains(component.Path, `\`) {
			return fmt.Errorf("Windows component %d differs from its fixed role/path/digest contract", index)
		}
	}
	digest, err := Digest(manifest)
	if err != nil {
		return err
	}
	if digest != manifest.BundleDigest {
		return errors.New("Windows release manifest digest does not verify")
	}
	return nil
}

// ComponentPath selects one admitted logical role without accepting a caller-selected filesystem destination.
func ComponentPath(manifest Manifest, role string) (string, error) {
	for _, component := range manifest.Components {
		if component.Role == role {
			return component.Path, nil
		}
	}
	return "", fmt.Errorf("Windows installed role %q is unavailable", role)
}

// validUserSID accepts canonical account identifiers without adopting system, service or builtin group principals.
func validUserSID(value string) bool {
	if !userSIDPattern.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, "-")[2:] {
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(parsed, 10) != part {
			return false
		}
	}
	return true
}
