package productproof

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// WindowsWorkerProfileEvidenceSchemaVersion identifies the protected Windows worker profile schema.
	WindowsWorkerProfileEvidenceSchemaVersion = 1
	windowsWorkerProfileCapability            = "windows_product_worker_profile"
	windowsWorkerProfileScope                 = "preflight"
	windowsWorkerProfileName                  = "windows-11-24h2-amd64"
)

var requiredWindowsWorkerAssertions = []string{
	"worker.clean_harbor_state",
	"worker.current_user_root",
	"worker.filtered_administrator",
	"worker.interactive_login",
	"worker.local_docker",
	"worker.windows_11_24h2",
}

// WindowsWorkerProfileEvidence records the immutable and native prerequisites of one protected Windows product worker.
type WindowsWorkerProfileEvidence struct {
	SchemaVersion       int                 `json:"schema_version"`
	Capability          string              `json:"capability"`
	Scope               string              `json:"scope"`
	Profile             string              `json:"profile"`
	Commit              string              `json:"commit"`
	RunnerName          string              `json:"runner_name"`
	GOOS                string              `json:"goos"`
	GOARCH              string              `json:"goarch"`
	ProductName         string              `json:"product_name"`
	ProductVersion      string              `json:"product_version"`
	BuildNumber         int                 `json:"build_number"`
	ProcessUser         string              `json:"process_user"`
	UserSID             string              `json:"user_sid"`
	SessionID           int                 `json:"session_id"`
	InteractiveSession  bool                `json:"interactive_session"`
	MediumIntegrity     bool                `json:"medium_integrity"`
	UACEnabled          bool                `json:"uac_enabled"`
	CurrentUserRoot     bool                `json:"current_user_root"`
	DockerProvider      string              `json:"docker_provider"`
	DockerEndpoint      string              `json:"docker_endpoint"`
	DockerEngineVersion string              `json:"docker_engine_version"`
	Assertions          []AssertionEvidence `json:"assertions"`
}

// WindowsWorkerProfileRequirement binds a profile artifact to one approved source and Windows product build.
type WindowsWorkerProfileRequirement struct {
	Commit       string
	ProductBuild int
}

// VerifyWindowsWorkerProfileEvidenceDirectory verifies exactly one protected Windows product-worker profile artifact.
func VerifyWindowsWorkerProfileEvidenceDirectory(root string, requirement WindowsWorkerProfileRequirement) error {
	if root == "" {
		return errors.New("Windows worker evidence root is required")
	}
	if !canonicalEvidenceDigest(requirement.Commit) {
		return errors.New("Windows worker requirement needs one canonical commit SHA")
	}
	if requirement.ProductBuild <= 0 {
		return errors.New("Windows worker requirement needs a positive product build")
	}

	var evidence *WindowsWorkerProfileEvidence
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Windows worker evidence contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != "windows-worker-profile.json" {
			return fmt.Errorf("Windows worker evidence contains unexpected file %s", path)
		}
		if evidence != nil {
			return errors.New("Windows worker evidence contains duplicate profile manifests")
		}
		var decoded WindowsWorkerProfileEvidence
		if err := decodeEvidence(path, &decoded); err != nil {
			return err
		}
		evidence = &decoded
		return nil
	})
	if err != nil {
		return fmt.Errorf("collect Windows worker evidence: %w", err)
	}
	if evidence == nil {
		return errors.New("missing Windows worker profile evidence")
	}
	return verifyWindowsWorkerProfile(*evidence, requirement)
}

// verifyWindowsWorkerProfile rejects hosted-server, elevated, non-interactive, wrong-build, or remote Engine profiles.
func verifyWindowsWorkerProfile(evidence WindowsWorkerProfileEvidence, requirement WindowsWorkerProfileRequirement) error {
	if evidence.SchemaVersion != WindowsWorkerProfileEvidenceSchemaVersion ||
		evidence.Capability != windowsWorkerProfileCapability ||
		evidence.Scope != windowsWorkerProfileScope ||
		evidence.Profile != windowsWorkerProfileName {
		return errors.New("Windows worker evidence has unsupported schema, capability, scope, or profile")
	}
	if evidence.Commit != requirement.Commit {
		return fmt.Errorf("Windows worker evidence reports commit %q instead of %q", evidence.Commit, requirement.Commit)
	}
	if evidence.GOOS != "windows" || evidence.GOARCH != "amd64" {
		return fmt.Errorf("Windows worker evidence reports unsupported runtime %s/%s", evidence.GOOS, evidence.GOARCH)
	}
	if !boundedEvidenceText(evidence.RunnerName, 256) ||
		!boundedEvidenceText(evidence.ProductName, 128) ||
		!boundedEvidenceText(evidence.ProductVersion, 64) ||
		!boundedEvidenceText(evidence.ProcessUser, 256) {
		return errors.New("Windows worker evidence has incomplete runner, product, or user identity")
	}
	if !strings.Contains(evidence.ProductName, "Windows 11") ||
		evidence.BuildNumber != requirement.ProductBuild ||
		windowsProductBuild(evidence.ProductVersion) != requirement.ProductBuild {
		return fmt.Errorf("Windows worker evidence reports unsupported product %q version %q build %d", evidence.ProductName, evidence.ProductVersion, evidence.BuildNumber)
	}
	if !canonicalWindowsUserSID(evidence.UserSID) ||
		evidence.SessionID <= 0 ||
		!evidence.InteractiveSession ||
		!evidence.MediumIntegrity ||
		!evidence.UACEnabled {
		return errors.New("Windows worker evidence does not identify one interactive filtered local-administrator session")
	}
	if !evidence.CurrentUserRoot {
		return errors.New("Windows worker evidence does not expose the current-user root certificate store")
	}
	if !boundedEvidenceText(evidence.DockerProvider, 128) || !canonicalWindowsDockerEndpoint(evidence.DockerEndpoint) {
		return errors.New("Windows worker evidence does not identify a local Docker-compatible Engine")
	}
	if err := verifyDockerEngineVersion(evidence.DockerEngineVersion, "windows"); err != nil {
		return err
	}
	return verifyAssertions(evidence.Assertions, requiredWindowsWorkerAssertions, "windows")
}

// windowsProductBuild parses the numeric build from one canonical Windows major.minor.build version.
func windowsProductBuild(version string) int {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[0] != "10" || parts[1] != "0" {
		return 0
	}
	for _, part := range parts {
		if part == "" {
			return 0
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return 0
			}
		}
	}
	build, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0
	}
	return build
}

// canonicalWindowsUserSID accepts only the stable numeric shape of a local interactive user identity.
func canonicalWindowsUserSID(value string) bool {
	if !strings.HasPrefix(value, "S-1-5-21-") || !boundedEvidenceText(value, 184) {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) != 8 || parts[0] != "S" || parts[1] != "1" || parts[2] != "5" || parts[3] != "21" {
		return false
	}
	for _, part := range parts[4:] {
		if part == "" {
			return false
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

// canonicalWindowsDockerEndpoint accepts one local named-pipe URI without admitting a nested path.
func canonicalWindowsDockerEndpoint(value string) bool {
	const prefix = "npipe:////./pipe/"
	if !strings.HasPrefix(value, prefix) || !boundedEvidenceText(value, 256) {
		return false
	}
	name := strings.TrimPrefix(value, prefix)
	if name == "" {
		return false
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' &&
			character != '_' &&
			character != '.' {
			return false
		}
	}
	return true
}
