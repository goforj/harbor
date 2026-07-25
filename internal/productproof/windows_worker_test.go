package productproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const windowsWorkerTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestVerifyWindowsWorkerProfileEvidenceDirectoryAcceptsExactProfile proves the intended product-worker profile contract.
func TestVerifyWindowsWorkerProfileEvidenceDirectoryAcceptsExactProfile(t *testing.T) {
	root := writeWindowsWorkerEvidence(t, validWindowsWorkerEvidence())
	err := VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{
		Commit:       windowsWorkerTestCommit,
		ProductBuild: 26100,
	})
	if err != nil {
		t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v", err)
	}
}

// TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles covers every release-significant identity boundary.
func TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WindowsWorkerProfileEvidence)
		want   string
	}{
		{name: "commit", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.Commit = strings.Repeat("b", 40) }, want: "instead of"},
		{name: "schema", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.SchemaVersion++ }, want: "unsupported schema"},
		{name: "missing runner", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.RunnerName = "" }, want: "incomplete runner"},
		{name: "server product", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.ProductName = "Windows Server 2025" }, want: "unsupported product"},
		{name: "wrong build", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.BuildNumber = 26200 }, want: "unsupported product"},
		{name: "malformed product version", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.ProductVersion = "10.0.release" }, want: "unsupported product"},
		{name: "wrong product version family", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.ProductVersion = "11.0.26100" }, want: "unsupported product"},
		{name: "wrong architecture", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.GOARCH = "arm64" }, want: "unsupported runtime"},
		{name: "missing process user", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.ProcessUser = "" }, want: "incomplete runner"},
		{name: "service SID", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.UserSID = "S-1-5-18" }, want: "filtered local-administrator"},
		{name: "malformed user SID", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.UserSID = "S-1-5-21-1000-S000-3000-1001" }, want: "filtered local-administrator"},
		{name: "noncanonical user SID", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.UserSID = "S-1-5-21-01000-2000-3000-1001" }, want: "filtered local-administrator"},
		{name: "session zero", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.SessionID = 0 }, want: "filtered local-administrator"},
		{name: "noninteractive", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.InteractiveSession = false }, want: "filtered local-administrator"},
		{name: "elevated token", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.MediumIntegrity = false }, want: "filtered local-administrator"},
		{name: "UAC disabled", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.UACEnabled = false }, want: "filtered local-administrator"},
		{name: "missing user root", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.CurrentUserRoot = false }, want: "current-user root"},
		{name: "invalid engine provider", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.DockerProvider = " provider " }, want: "local Docker-compatible"},
		{name: "remote engine", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.DockerEndpoint = "tcp://docker.example:2376" }, want: "local Docker-compatible"},
		{name: "nested pipe", mutate: func(evidence *WindowsWorkerProfileEvidence) {
			evidence.DockerEndpoint = "npipe:////./pipe/provider/docker_engine"
		}, want: "local Docker-compatible"},
		{name: "empty pipe", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.DockerEndpoint = "npipe:////./pipe/" }, want: "local Docker-compatible"},
		{name: "old engine", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.DockerEngineVersion = "27.5.1" }, want: "below the supported"},
		{name: "failed assertion", mutate: func(evidence *WindowsWorkerProfileEvidence) { evidence.Assertions[0].Passed = false }, want: "failed assertion"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validWindowsWorkerEvidence()
			test.mutate(&evidence)
			root := writeWindowsWorkerEvidence(t, evidence)
			err := VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{
				Commit:       windowsWorkerTestCommit,
				ProductBuild: 26100,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsInvalidRequirements keeps artifacts bound to one reviewed release target.
func TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsInvalidRequirements(t *testing.T) {
	root := writeWindowsWorkerEvidence(t, validWindowsWorkerEvidence())
	for _, test := range []struct {
		name        string
		root        string
		requirement WindowsWorkerProfileRequirement
		want        string
	}{
		{name: "missing root", requirement: WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100}, want: "root is required"},
		{name: "invalid commit", root: root, requirement: WindowsWorkerProfileRequirement{Commit: "abc", ProductBuild: 26100}, want: "canonical commit"},
		{name: "invalid build", root: root, requirement: WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit}, want: "positive product build"},
		{name: "missing evidence", root: t.TempDir(), requirement: WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100}, want: "missing Windows worker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifyWindowsWorkerProfileEvidenceDirectory(test.root, test.requirement)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts keeps downloads strict and non-extensible.
func TestVerifyWindowsWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts(t *testing.T) {
	t.Run("unexpected file", func(t *testing.T) {
		root := writeWindowsWorkerEvidence(t, validWindowsWorkerEvidence())
		if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write unexpected artifact: %v", err)
		}
		err := VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100})
		if err == nil || !strings.Contains(err.Error(), "unexpected file") {
			t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want unexpected file", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		root := t.TempDir()
		contents, err := json.Marshal(validWindowsWorkerEvidence())
		if err != nil {
			t.Fatalf("encode evidence: %v", err)
		}
		contents = append(contents[:len(contents)-1], []byte(`,"unknown":true}`)...)
		if err := os.WriteFile(filepath.Join(root, "windows-worker-profile.json"), contents, 0o600); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		err = VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100})
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want unknown field", err)
		}
	})
	t.Run("duplicate profile", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"first", "second"} {
			directory := filepath.Join(root, name)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatalf("create evidence directory: %v", err)
			}
			contents, err := json.Marshal(validWindowsWorkerEvidence())
			if err != nil {
				t.Fatalf("encode evidence: %v", err)
			}
			if err := os.WriteFile(filepath.Join(directory, "windows-worker-profile.json"), contents, 0o600); err != nil {
				t.Fatalf("write evidence: %v", err)
			}
		}
		err := VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100})
		if err == nil || !strings.Contains(err.Error(), "duplicate profile") {
			t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want duplicate profile", err)
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
		if err := os.Symlink(target, filepath.Join(root, "windows-worker-profile.json")); err != nil {
			t.Fatalf("create evidence symlink: %v", err)
		}
		err := VerifyWindowsWorkerProfileEvidenceDirectory(root, WindowsWorkerProfileRequirement{Commit: windowsWorkerTestCommit, ProductBuild: 26100})
		if err == nil || !strings.Contains(err.Error(), "contains symlink") {
			t.Fatalf("VerifyWindowsWorkerProfileEvidenceDirectory() error = %v, want symlink", err)
		}
	})
}

// validWindowsWorkerEvidence returns one complete deterministic profile artifact.
func validWindowsWorkerEvidence() WindowsWorkerProfileEvidence {
	assertions := make([]AssertionEvidence, 0, len(requiredWindowsWorkerAssertions))
	for _, identifier := range requiredWindowsWorkerAssertions {
		assertions = append(assertions, AssertionEvidence{ID: identifier, Passed: true, Detail: "verified by protected worker profile"})
	}
	return WindowsWorkerProfileEvidence{
		SchemaVersion:       WindowsWorkerProfileEvidenceSchemaVersion,
		Capability:          windowsWorkerProfileCapability,
		Scope:               windowsWorkerProfileScope,
		Profile:             windowsWorkerProfileName,
		Commit:              windowsWorkerTestCommit,
		RunnerName:          "windows-product-01",
		GOOS:                "windows",
		GOARCH:              "amd64",
		ProductName:         "Microsoft Windows 11 Pro",
		ProductVersion:      "10.0.26100",
		BuildNumber:         26100,
		ProcessUser:         `HARBOR\harbor`,
		UserSID:             "S-1-5-21-1000-2000-3000-1001",
		SessionID:           1,
		InteractiveSession:  true,
		MediumIntegrity:     true,
		UACEnabled:          true,
		CurrentUserRoot:     true,
		DockerProvider:      "user-selected local runtime",
		DockerEndpoint:      "npipe:////./pipe/docker_engine",
		DockerEngineVersion: "28.3.2",
		Assertions:          assertions,
	}
}

// writeWindowsWorkerEvidence writes the fixed test manifest into one isolated evidence root.
func writeWindowsWorkerEvidence(t *testing.T, evidence WindowsWorkerProfileEvidence) string {
	t.Helper()
	root := t.TempDir()
	contents, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "windows-worker-profile.json"), contents, 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	return root
}
