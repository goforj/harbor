package productproof

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

const (
	// LinuxWorkerProfileEvidenceSchemaVersion identifies the protected Linux worker profile schema.
	LinuxWorkerProfileEvidenceSchemaVersion = 1
	linuxWorkerProfileCapability            = "linux_product_worker_profile"
	linuxWorkerProfileScope                 = "preflight"
	linuxWorkerProfileName                  = "ubuntu-24.04-amd64"
)

var requiredLinuxWorkerAssertions = []string{
	"worker.clean_harbor_state",
	"worker.gnome_wayland",
	"worker.local_docker",
	"worker.network_manager_resolved",
	"worker.nftables",
	"worker.system_ca",
	"worker.systemd_user",
	"worker.ubuntu_24_04",
}

// LinuxWorkerProfileEvidence records the immutable and native prerequisites of one protected Linux product worker.
type LinuxWorkerProfileEvidence struct {
	SchemaVersion       int                 `json:"schema_version"`
	Capability          string              `json:"capability"`
	Scope               string              `json:"scope"`
	Profile             string              `json:"profile"`
	Commit              string              `json:"commit"`
	RunnerName          string              `json:"runner_name"`
	GOOS                string              `json:"goos"`
	GOARCH              string              `json:"goarch"`
	DistributionID      string              `json:"distribution_id"`
	DistributionVersion string              `json:"distribution_version"`
	KernelVersion       string              `json:"kernel_version"`
	ProcessUser         string              `json:"process_user"`
	ProcessUID          int                 `json:"process_uid"`
	InteractiveSession  bool                `json:"interactive_session"`
	SessionType         string              `json:"session_type"`
	Desktop             string              `json:"desktop"`
	SystemdUser         bool                `json:"systemd_user"`
	ResolverBackend     string              `json:"resolver_backend"`
	NetworkManager      bool                `json:"network_manager"`
	FirewallBackend     string              `json:"firewall_backend"`
	SystemCAPath        string              `json:"system_ca_path"`
	DockerProvider      string              `json:"docker_provider"`
	DockerEndpoint      string              `json:"docker_endpoint"`
	DockerEngineVersion string              `json:"docker_engine_version"`
	Assertions          []AssertionEvidence `json:"assertions"`
}

// LinuxWorkerProfileRequirement binds a profile artifact to one approved source and Ubuntu release.
type LinuxWorkerProfileRequirement struct {
	Commit              string
	DistributionVersion string
}

// VerifyLinuxWorkerProfileEvidenceDirectory verifies exactly one protected Linux product-worker profile artifact.
func VerifyLinuxWorkerProfileEvidenceDirectory(root string, requirement LinuxWorkerProfileRequirement) error {
	if root == "" {
		return errors.New("Linux worker evidence root is required")
	}
	if !canonicalEvidenceDigest(requirement.Commit) {
		return errors.New("Linux worker requirement needs one canonical commit SHA")
	}
	if requirement.DistributionVersion == "" {
		return errors.New("Linux worker requirement needs one distribution version")
	}

	var evidence *LinuxWorkerProfileEvidence
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Linux worker evidence contains symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != "linux-worker-profile.json" {
			return fmt.Errorf("Linux worker evidence contains unexpected file %s", path)
		}
		if evidence != nil {
			return errors.New("Linux worker evidence contains duplicate profile manifests")
		}
		var decoded LinuxWorkerProfileEvidence
		if err := decodeEvidence(path, &decoded); err != nil {
			return err
		}
		evidence = &decoded
		return nil
	})
	if err != nil {
		return fmt.Errorf("collect Linux worker evidence: %w", err)
	}
	if evidence == nil {
		return errors.New("missing Linux worker profile evidence")
	}
	return verifyLinuxWorkerProfile(*evidence, requirement)
}

// verifyLinuxWorkerProfile rejects hosted, non-interactive, wrong-release, or remote Engine profiles.
func verifyLinuxWorkerProfile(evidence LinuxWorkerProfileEvidence, requirement LinuxWorkerProfileRequirement) error {
	if evidence.SchemaVersion != LinuxWorkerProfileEvidenceSchemaVersion ||
		evidence.Capability != linuxWorkerProfileCapability ||
		evidence.Scope != linuxWorkerProfileScope ||
		evidence.Profile != linuxWorkerProfileName {
		return errors.New("Linux worker evidence has unsupported schema, capability, scope, or profile")
	}
	if evidence.Commit != requirement.Commit {
		return fmt.Errorf("Linux worker evidence reports commit %q instead of %q", evidence.Commit, requirement.Commit)
	}
	if evidence.GOOS != "linux" || evidence.GOARCH != "amd64" {
		return fmt.Errorf("Linux worker evidence reports unsupported runtime %s/%s", evidence.GOOS, evidence.GOARCH)
	}
	if !boundedEvidenceText(evidence.RunnerName, 256) ||
		!boundedEvidenceText(evidence.KernelVersion, 128) ||
		!boundedEvidenceText(evidence.ProcessUser, 256) {
		return errors.New("Linux worker evidence has incomplete runner, kernel, or user identity")
	}
	if evidence.DistributionID != "ubuntu" || evidence.DistributionVersion != requirement.DistributionVersion {
		return fmt.Errorf("Linux worker evidence reports unsupported distribution %q %q", evidence.DistributionID, evidence.DistributionVersion)
	}
	if evidence.ProcessUID <= 0 ||
		!evidence.InteractiveSession ||
		evidence.SessionType != "wayland" ||
		!strings.Contains(strings.ToLower(evidence.Desktop), "gnome") ||
		!evidence.SystemdUser {
		return errors.New("Linux worker evidence does not identify one interactive non-root GNOME Wayland systemd user")
	}
	if evidence.ResolverBackend != "systemd-resolved" ||
		!evidence.NetworkManager ||
		evidence.FirewallBackend != "nftables" ||
		evidence.SystemCAPath != "/usr/local/share/ca-certificates" {
		return errors.New("Linux worker evidence reports unsupported resolver, firewall, or trust mechanism")
	}
	if !boundedEvidenceText(evidence.DockerProvider, 128) ||
		!strings.HasPrefix(evidence.DockerEndpoint, "unix://") ||
		!filepath.IsAbs(strings.TrimPrefix(evidence.DockerEndpoint, "unix://")) {
		return errors.New("Linux worker evidence does not identify a local Docker-compatible Engine")
	}
	if err := verifyDockerEngineVersion(evidence.DockerEngineVersion, "linux"); err != nil {
		return err
	}
	return verifyAssertions(evidence.Assertions, requiredLinuxWorkerAssertions, "linux")
}
