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
	// MacOSWorkerProfileEvidenceSchemaVersion identifies the protected macOS worker preflight schema.
	MacOSWorkerProfileEvidenceSchemaVersion = 1
	macOSWorkerProfileCapability            = "macos_product_worker_profile"
	macOSWorkerProfileScope                 = "preflight"
	macOSWorkerProfileName                  = "macos-15-arm64"
)

var requiredMacOSWorkerAssertions = []string{
	"worker.apple_silicon",
	"worker.clean_harbor_state",
	"worker.current_user_keychain",
	"worker.interactive_login",
	"worker.local_docker",
}

// MacOSWorkerProfileEvidence records the immutable and native prerequisites of one protected product worker.
type MacOSWorkerProfileEvidence struct {
	SchemaVersion       int                 `json:"schema_version"`
	Capability          string              `json:"capability"`
	Scope               string              `json:"scope"`
	Profile             string              `json:"profile"`
	Commit              string              `json:"commit"`
	RunnerName          string              `json:"runner_name"`
	GOOS                string              `json:"goos"`
	GOARCH              string              `json:"goarch"`
	ProductVersion      string              `json:"product_version"`
	BuildVersion        string              `json:"build_version"`
	ProcessUser         string              `json:"process_user"`
	ConsoleUser         string              `json:"console_user"`
	InteractiveSession  bool                `json:"interactive_session"`
	UserKeychain        string              `json:"user_keychain"`
	ResolverDirectory   string              `json:"resolver_directory"`
	LowPortMechanism    string              `json:"low_port_mechanism"`
	DockerProvider      string              `json:"docker_provider"`
	DockerEndpoint      string              `json:"docker_endpoint"`
	DockerEngineVersion string              `json:"docker_engine_version"`
	Assertions          []AssertionEvidence `json:"assertions"`
}

// MacOSWorkerProfileRequirement binds a preflight artifact to one approved source and product family.
type MacOSWorkerProfileRequirement struct {
	Commit       string
	ProductMajor int
}

// VerifyMacOSWorkerProfileEvidenceDirectory verifies exactly one protected macOS product-worker preflight artifact.
func VerifyMacOSWorkerProfileEvidenceDirectory(root string, requirement MacOSWorkerProfileRequirement) error {
	if root == "" {
		return errors.New("macOS worker evidence root is required")
	}
	if !canonicalEvidenceDigest(requirement.Commit) {
		return errors.New("macOS worker requirement needs one canonical commit SHA")
	}
	if requirement.ProductMajor <= 0 {
		return errors.New("macOS worker requirement needs a positive product major version")
	}

	var evidence *MacOSWorkerProfileEvidence
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("macOS worker evidence contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != "macos-worker-profile.json" {
			return fmt.Errorf("macOS worker evidence contains unexpected file %s", path)
		}
		if evidence != nil {
			return errors.New("macOS worker evidence contains duplicate profile manifests")
		}
		var decoded MacOSWorkerProfileEvidence
		if err := decodeEvidence(path, &decoded); err != nil {
			return err
		}
		evidence = &decoded
		return nil
	})
	if err != nil {
		return fmt.Errorf("collect macOS worker evidence: %w", err)
	}
	if evidence == nil {
		return errors.New("missing macOS worker profile evidence")
	}
	return verifyMacOSWorkerProfile(*evidence, requirement)
}

// verifyMacOSWorkerProfile rejects a hosted, non-interactive, wrong-version, or remote Engine profile.
func verifyMacOSWorkerProfile(evidence MacOSWorkerProfileEvidence, requirement MacOSWorkerProfileRequirement) error {
	if evidence.SchemaVersion != MacOSWorkerProfileEvidenceSchemaVersion ||
		evidence.Capability != macOSWorkerProfileCapability ||
		evidence.Scope != macOSWorkerProfileScope ||
		evidence.Profile != macOSWorkerProfileName {
		return errors.New("macOS worker evidence has unsupported schema, capability, scope, or profile")
	}
	if evidence.Commit != requirement.Commit {
		return fmt.Errorf("macOS worker evidence reports commit %q instead of %q", evidence.Commit, requirement.Commit)
	}
	if evidence.GOOS != "darwin" || evidence.GOARCH != "arm64" {
		return fmt.Errorf("macOS worker evidence reports unsupported runtime %s/%s", evidence.GOOS, evidence.GOARCH)
	}
	if !boundedEvidenceText(evidence.RunnerName, 256) || !boundedEvidenceText(evidence.BuildVersion, 128) {
		return errors.New("macOS worker evidence has incomplete runner or build identity")
	}
	productMajor, err := macOSProductMajor(evidence.ProductVersion)
	if err != nil || productMajor != requirement.ProductMajor {
		return fmt.Errorf("macOS worker evidence reports unsupported product version %q", evidence.ProductVersion)
	}
	if !evidence.InteractiveSession ||
		!boundedEvidenceText(evidence.ProcessUser, 256) ||
		evidence.ProcessUser == "root" ||
		evidence.ProcessUser != evidence.ConsoleUser {
		return errors.New("macOS worker evidence does not identify one interactive non-root console user")
	}
	if !filepath.IsAbs(evidence.UserKeychain) || filepath.Clean(evidence.UserKeychain) != evidence.UserKeychain {
		return errors.New("macOS worker evidence does not identify a canonical user keychain")
	}
	if evidence.ResolverDirectory != "/etc/resolver" || evidence.LowPortMechanism != "launchd-socket" {
		return errors.New("macOS worker evidence reports unsupported resolver or low-port mechanism")
	}
	if !boundedEvidenceText(evidence.DockerProvider, 128) ||
		!strings.HasPrefix(evidence.DockerEndpoint, "unix://") ||
		!filepath.IsAbs(strings.TrimPrefix(evidence.DockerEndpoint, "unix://")) {
		return errors.New("macOS worker evidence does not identify a local Docker-compatible Engine")
	}
	if err := verifyDockerEngineVersion(evidence.DockerEngineVersion, "darwin"); err != nil {
		return err
	}
	return verifyAssertions(evidence.Assertions, requiredMacOSWorkerAssertions, "darwin")
}

// macOSProductMajor parses only canonical dotted numeric macOS versions.
func macOSProductMajor(version string) (int, error) {
	if !boundedEvidenceText(version, 64) {
		return 0, errors.New("invalid macOS product version")
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errors.New("invalid macOS product version")
	}
	for _, part := range parts {
		if part == "" {
			return 0, errors.New("invalid macOS product version")
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return 0, errors.New("invalid macOS product version")
			}
		}
	}
	return strconv.Atoi(parts[0])
}

// canonicalEvidenceDigest accepts the lowercase full SHA shape used by GitHub commit identities.
func canonicalEvidenceDigest(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// boundedEvidenceText rejects empty, padded, control-bearing, or oversized native identity fields.
func boundedEvidenceText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
