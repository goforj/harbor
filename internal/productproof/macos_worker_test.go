package productproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const macOSWorkerTestCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestVerifyMacOSWorkerProfileEvidenceDirectoryAcceptsExactProfile proves the intended product-worker preflight contract.
func TestVerifyMacOSWorkerProfileEvidenceDirectoryAcceptsExactProfile(t *testing.T) {
	root := writeMacOSWorkerEvidence(t, validMacOSWorkerEvidence())
	err := VerifyMacOSWorkerProfileEvidenceDirectory(root, MacOSWorkerProfileRequirement{
		Commit:       macOSWorkerTestCommit,
		ProductMajor: 15,
	})
	if err != nil {
		t.Fatalf("VerifyMacOSWorkerProfileEvidenceDirectory() error = %v", err)
	}
}

// TestVerifyMacOSWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles covers every release-significant identity boundary.
func TestVerifyMacOSWorkerProfileEvidenceDirectoryRejectsUnprovedProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*MacOSWorkerProfileEvidence)
		want   string
	}{
		{name: "commit", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.Commit = strings.Repeat("b", 40) }, want: "instead of"},
		{name: "hosted architecture", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.GOARCH = "amd64" }, want: "unsupported runtime"},
		{name: "product version", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.ProductVersion = "14.7.1" }, want: "unsupported product version"},
		{name: "root user", mutate: func(evidence *MacOSWorkerProfileEvidence) {
			evidence.ProcessUser, evidence.ConsoleUser = "root", "root"
		}, want: "interactive non-root"},
		{name: "console mismatch", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.ConsoleUser = "someone-else" }, want: "interactive non-root"},
		{name: "no interactive session", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.InteractiveSession = false }, want: "interactive non-root"},
		{name: "system keychain", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.UserKeychain = "System.keychain" }, want: "canonical user keychain"},
		{name: "resolver", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.ResolverDirectory = "/etc" }, want: "resolver or low-port"},
		{name: "invalid engine provider", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.DockerProvider = " provider " }, want: "local Docker-compatible"},
		{name: "remote engine", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.DockerEndpoint = "tcp://docker.example:2376" }, want: "local Docker-compatible"},
		{name: "old engine", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.DockerEngineVersion = "27.5.1" }, want: "below the supported"},
		{name: "failed assertion", mutate: func(evidence *MacOSWorkerProfileEvidence) { evidence.Assertions[0].Passed = false }, want: "failed assertion"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := validMacOSWorkerEvidence()
			test.mutate(&evidence)
			root := writeMacOSWorkerEvidence(t, evidence)
			err := VerifyMacOSWorkerProfileEvidenceDirectory(root, MacOSWorkerProfileRequirement{
				Commit:       macOSWorkerTestCommit,
				ProductMajor: 15,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyMacOSWorkerProfileEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyMacOSWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts keeps downloads strict and non-extensible.
func TestVerifyMacOSWorkerProfileEvidenceDirectoryRejectsAmbiguousArtifacts(t *testing.T) {
	t.Run("unexpected file", func(t *testing.T) {
		root := writeMacOSWorkerEvidence(t, validMacOSWorkerEvidence())
		if err := os.WriteFile(filepath.Join(root, "extra.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write unexpected artifact: %v", err)
		}
		err := VerifyMacOSWorkerProfileEvidenceDirectory(root, MacOSWorkerProfileRequirement{Commit: macOSWorkerTestCommit, ProductMajor: 15})
		if err == nil || !strings.Contains(err.Error(), "unexpected file") {
			t.Fatalf("VerifyMacOSWorkerProfileEvidenceDirectory() error = %v, want unexpected file", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		root := t.TempDir()
		contents, err := json.Marshal(validMacOSWorkerEvidence())
		if err != nil {
			t.Fatalf("encode evidence: %v", err)
		}
		contents = append(contents[:len(contents)-1], []byte(`,"unknown":true}`)...)
		if err := os.WriteFile(filepath.Join(root, "macos-worker-profile.json"), contents, 0o600); err != nil {
			t.Fatalf("write evidence: %v", err)
		}
		err = VerifyMacOSWorkerProfileEvidenceDirectory(root, MacOSWorkerProfileRequirement{Commit: macOSWorkerTestCommit, ProductMajor: 15})
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("VerifyMacOSWorkerProfileEvidenceDirectory() error = %v, want unknown field", err)
		}
	})
}

// validMacOSWorkerEvidence returns one complete deterministic preflight artifact.
func validMacOSWorkerEvidence() MacOSWorkerProfileEvidence {
	assertions := make([]AssertionEvidence, 0, len(requiredMacOSWorkerAssertions))
	for _, identifier := range requiredMacOSWorkerAssertions {
		assertions = append(assertions, AssertionEvidence{ID: identifier, Passed: true, Detail: "verified by protected worker preflight"})
	}
	return MacOSWorkerProfileEvidence{
		SchemaVersion:       MacOSWorkerProfileEvidenceSchemaVersion,
		Capability:          macOSWorkerProfileCapability,
		Scope:               macOSWorkerProfileScope,
		Profile:             macOSWorkerProfileName,
		Commit:              macOSWorkerTestCommit,
		RunnerName:          "macos-product-01",
		GOOS:                "darwin",
		GOARCH:              "arm64",
		ProductVersion:      "15.6.1",
		BuildVersion:        "24G90",
		ProcessUser:         "harbor",
		ConsoleUser:         "harbor",
		InteractiveSession:  true,
		UserKeychain:        "/Users/harbor/Library/Keychains/login.keychain-db",
		ResolverDirectory:   "/etc/resolver",
		LowPortMechanism:    "launchd-socket",
		DockerProvider:      "user-selected local runtime",
		DockerEndpoint:      "unix:///Users/harbor/.docker/run/docker.sock",
		DockerEngineVersion: "28.3.2",
		Assertions:          assertions,
	}
}

// writeMacOSWorkerEvidence writes the fixed test manifest into one isolated evidence root.
func writeMacOSWorkerEvidence(t *testing.T, evidence MacOSWorkerProfileEvidence) string {
	t.Helper()
	root := t.TempDir()
	contents, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "macos-worker-profile.json"), contents, 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	return root
}
