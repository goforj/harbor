package productproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const linuxWorkerTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestVerifyLinuxWorkerProfileEvidenceDirectoryAcceptsExactProfile proves the intended product-worker profile contract.
func TestVerifyLinuxWorkerProfileEvidenceDirectoryAcceptsExactProfile(t *testing.T) {
	root := writeLinuxWorkerEvidence(t, validLinuxWorkerEvidence())
	err := VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{
		Commit:              linuxWorkerTestCommit,
		DistributionVersion: "24.04",
	})
	if err != nil {
		t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v", err)
	}
}

// TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles covers every release-significant identity boundary.
func TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LinuxWorkerProfileEvidence)
		want   string
	}{
		{name: "commit", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.Commit = strings.Repeat("b", 40) }, want: "instead of"},
		{name: "schema", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.SchemaVersion++ }, want: "unsupported schema"},
		{name: "wrong architecture", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.GOARCH = "arm64" }, want: "unsupported runtime"},
		{name: "missing runner", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.RunnerName = "" }, want: "incomplete runner"},
		{name: "wrong distribution", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.DistributionID = "debian" }, want: "unsupported distribution"},
		{name: "wrong release", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.DistributionVersion = "24.10" }, want: "unsupported distribution"},
		{name: "root user", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.ProcessUID = 0 }, want: "interactive non-root"},
		{name: "noninteractive", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.InteractiveSession = false }, want: "interactive non-root"},
		{name: "x11", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.SessionType = "x11" }, want: "interactive non-root"},
		{name: "wrong desktop", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.Desktop = "KDE" }, want: "interactive non-root"},
		{name: "missing user service", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.SystemdUser = false }, want: "interactive non-root"},
		{name: "wrong resolver", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.ResolverBackend = "dnsmasq" }, want: "unsupported resolver"},
		{name: "missing network manager", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.NetworkManager = false }, want: "unsupported resolver"},
		{name: "wrong firewall", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.FirewallBackend = "iptables" }, want: "unsupported resolver"},
		{name: "wrong trust path", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.SystemCAPath = "/etc/ssl/certs" }, want: "unsupported resolver"},
		{name: "invalid engine provider", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.DockerProvider = " provider " }, want: "local Docker-compatible"},
		{name: "remote engine", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.DockerEndpoint = "tcp://docker.example:2376" }, want: "local Docker-compatible"},
		{name: "old engine", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.DockerEngineVersion = "27.5.1" }, want: "below the supported"},
		{name: "failed assertion", mutate: func(evidence *LinuxWorkerProfileEvidence) { evidence.Assertions[0].Passed = false }, want: "failed assertion"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validLinuxWorkerEvidence()
			test.mutate(&evidence)
			root := writeLinuxWorkerEvidence(t, evidence)
			err := VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{
				Commit:              linuxWorkerTestCommit,
				DistributionVersion: "24.04",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsInvalidRequirements keeps artifacts bound to one reviewed release target.
func TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsInvalidRequirements(t *testing.T) {
	root := writeLinuxWorkerEvidence(t, validLinuxWorkerEvidence())
	for _, test := range []struct {
		name        string
		root        string
		requirement LinuxWorkerProfileRequirement
		want        string
	}{
		{name: "missing root", requirement: LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"}, want: "root is required"},
		{name: "invalid commit", root: root, requirement: LinuxWorkerProfileRequirement{Commit: "abc", DistributionVersion: "24.04"}, want: "canonical commit"},
		{name: "missing release", root: root, requirement: LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit}, want: "distribution version"},
		{name: "missing evidence", root: t.TempDir(), requirement: LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"}, want: "missing Linux worker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifyLinuxWorkerProfileEvidenceDirectory(test.root, test.requirement)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts keeps downloads strict and non-extensible.
func TestVerifyLinuxWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts(t *testing.T) {
	t.Run("unexpected file", func(t *testing.T) {
		root := writeLinuxWorkerEvidence(t, validLinuxWorkerEvidence())
		if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write unexpected artifact: %v", err)
		}
		err := VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"})
		if err == nil || !strings.Contains(err.Error(), "unexpected file") {
			t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want unexpected file", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		root := t.TempDir()
		contents, err := json.Marshal(validLinuxWorkerEvidence())
		if err != nil {
			t.Fatalf("encode evidence: %v", err)
		}
		contents = append(contents[:len(contents)-1], []byte(`,"unknown":true}`)...)
		if err := os.WriteFile(filepath.Join(root, "linux-worker-profile.json"), contents, 0o600); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		err = VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"})
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want unknown field", err)
		}
	})
	t.Run("duplicate profile", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"first", "second"} {
			directory := filepath.Join(root, name)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatalf("create evidence directory: %v", err)
			}
			contents, err := json.Marshal(validLinuxWorkerEvidence())
			if err != nil {
				t.Fatalf("encode evidence: %v", err)
			}
			if err := os.WriteFile(filepath.Join(directory, "linux-worker-profile.json"), contents, 0o600); err != nil {
				t.Fatalf("write evidence: %v", err)
			}
		}
		err := VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"})
		if err == nil || !strings.Contains(err.Error(), "duplicate profile") {
			t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want duplicate profile", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("ordinary Windows test tokens cannot create symlinks")
		}
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(root, "linux-worker-profile.json")); err != nil {
			t.Fatalf("create evidence symlink: %v", err)
		}
		err := VerifyLinuxWorkerProfileEvidenceDirectory(root, LinuxWorkerProfileRequirement{Commit: linuxWorkerTestCommit, DistributionVersion: "24.04"})
		if err == nil || !strings.Contains(err.Error(), "contains symlink") {
			t.Fatalf("VerifyLinuxWorkerProfileEvidenceDirectory() error = %v, want symlink", err)
		}
	})
}

// validLinuxWorkerEvidence returns one complete deterministic profile artifact.
func validLinuxWorkerEvidence() LinuxWorkerProfileEvidence {
	assertions := make([]AssertionEvidence, 0, len(requiredLinuxWorkerAssertions))
	for _, identifier := range requiredLinuxWorkerAssertions {
		assertions = append(assertions, AssertionEvidence{ID: identifier, Passed: true, Detail: "verified by protected worker profile"})
	}
	return LinuxWorkerProfileEvidence{
		SchemaVersion:       LinuxWorkerProfileEvidenceSchemaVersion,
		Capability:          linuxWorkerProfileCapability,
		Scope:               linuxWorkerProfileScope,
		Profile:             linuxWorkerProfileName,
		Commit:              linuxWorkerTestCommit,
		RunnerName:          "linux-product-01",
		GOOS:                "linux",
		GOARCH:              "amd64",
		DistributionID:      "ubuntu",
		DistributionVersion: "24.04",
		KernelVersion:       "6.8.0-64-generic",
		ProcessUser:         "harbor",
		ProcessUID:          1000,
		InteractiveSession:  true,
		SessionType:         "wayland",
		Desktop:             "ubuntu:GNOME",
		SystemdUser:         true,
		ResolverBackend:     "systemd-resolved",
		NetworkManager:      true,
		FirewallBackend:     "nftables",
		SystemCAPath:        "/usr/local/share/ca-certificates",
		DockerProvider:      "user-selected local runtime",
		DockerEndpoint:      "unix:///run/user/1000/docker.sock",
		DockerEngineVersion: "28.3.2",
		Assertions:          assertions,
	}
}

// writeLinuxWorkerEvidence writes the fixed test manifest into one isolated evidence root.
func writeLinuxWorkerEvidence(t *testing.T, evidence LinuxWorkerProfileEvidence) string {
	t.Helper()
	root := t.TempDir()
	contents, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "linux-worker-profile.json"), contents, 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	return root
}
