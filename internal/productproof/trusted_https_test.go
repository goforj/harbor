package productproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryAcceptsExactArtifact proves the protected worker's native result.
func TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryAcceptsExactArtifact(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			summary := validTrustedHTTPSSummary()
			summary.OperatingSystem = platform
			root := writeTrustedHTTPSEvidence(t, summary)
			if err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: platform}); err != nil {
				t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v", err)
			}
		})
	}
}

// TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsInvalidRequirements keeps unsupported claims out of the gate.
func TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsInvalidRequirements(t *testing.T) {
	root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
	for _, test := range []struct {
		name        string
		root        string
		requirement TrustedHTTPSLifecycleRequirement
		want        string
	}{
		{name: "missing root", requirement: TrustedHTTPSLifecycleRequirement{Platform: "darwin"}, want: "root is required"},
		{name: "unsupported platform", root: root, requirement: TrustedHTTPSLifecycleRequirement{Platform: "plan9"}, want: "not implemented"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(test.root, test.requirement)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsInvalidSummary covers each release-significant result boundary.
func TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsInvalidSummary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TrustedHTTPSLifecycleSummary)
		want   string
	}{
		{name: "schema", mutate: func(summary *TrustedHTTPSLifecycleSummary) { summary.SchemaVersion++ }, want: "unsupported schema"},
		{name: "platform", mutate: func(summary *TrustedHTTPSLifecycleSummary) { summary.OperatingSystem = "linux" }, want: "instead of"},
		{name: "missing check", mutate: func(summary *TrustedHTTPSLifecycleSummary) { summary.Checks = summary.Checks[:3] }, want: "checks ="},
		{name: "reordered check", mutate: func(summary *TrustedHTTPSLifecycleSummary) {
			summary.Checks[0], summary.Checks[1] = summary.Checks[1], summary.Checks[0]
		}, want: "checks ="},
		{name: "duplicate check", mutate: func(summary *TrustedHTTPSLifecycleSummary) { summary.Checks[3] = summary.Checks[2] }, want: "checks ="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := validTrustedHTTPSSummary()
			test.mutate(&summary)
			root := writeTrustedHTTPSEvidence(t, summary)
			err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsAmbiguousArtifacts keeps artifact collection strict.
func TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsAmbiguousArtifacts(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		if err := os.Remove(filepath.Join(root, trustedHTTPSEvidenceFiles[0])); err != nil {
			t.Fatalf("remove evidence file: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "files =") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want files", err)
		}
	})
	t.Run("unexpected file", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		if err := os.WriteFile(filepath.Join(root, "extra.log"), nil, 0o600); err != nil {
			t.Fatalf("write unexpected file: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "unexpected file") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want unexpected file", err)
		}
	})
	t.Run("nested directory", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
			t.Fatalf("create nested directory: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "nested directory") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want nested directory", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		path := filepath.Join(root, "summary.json")
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read summary: %v", err)
		}
		contents = append(contents[:len(contents)-1], []byte(`,"unknown":true}`)...)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("write unknown field: %v", err)
		}
		err = VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want unknown field", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("ordinary Windows test tokens cannot create symlinks")
		}
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		path := filepath.Join(root, trustedHTTPSEvidenceFiles[0])
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove log: %v", err)
		}
		if err := os.Symlink(os.DevNull, path); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "contains symlink") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want symlink", err)
		}
	})
}

// TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsUnsafeLogs covers bounded native diagnostic admission.
func TestVerifyTrustedHTTPSLifecycleEvidenceDirectoryRejectsUnsafeLogs(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		path := filepath.Join(root, trustedHTTPSEvidenceFiles[0])
		contents := make([]byte, maximumTrustedHTTPSLogBytes+1)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("write oversized log: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "type or size") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want size", err)
		}
	})
	t.Run("data race", func(t *testing.T) {
		root := writeTrustedHTTPSEvidence(t, validTrustedHTTPSSummary())
		path := filepath.Join(root, trustedHTTPSEvidenceFiles[0])
		if err := os.WriteFile(path, []byte("WARNING: DATA RACE"), 0o600); err != nil {
			t.Fatalf("write race log: %v", err)
		}
		err := VerifyTrustedHTTPSLifecycleEvidenceDirectory(root, TrustedHTTPSLifecycleRequirement{Platform: "darwin"})
		if err == nil || !strings.Contains(err.Error(), "data race") {
			t.Fatalf("VerifyTrustedHTTPSLifecycleEvidenceDirectory() error = %v, want data race", err)
		}
	})
}

// validTrustedHTTPSSummary returns the exact successful behavioral result.
func validTrustedHTTPSSummary() TrustedHTTPSLifecycleSummary {
	return TrustedHTTPSLifecycleSummary{
		SchemaVersion:   TrustedHTTPSLifecycleEvidenceSchemaVersion,
		OperatingSystem: "darwin",
		Checks:          append([]string(nil), requiredTrustedHTTPSChecks...),
	}
}

// writeTrustedHTTPSEvidence writes one deterministic direct evidence directory.
func writeTrustedHTTPSEvidence(t *testing.T, summary TrustedHTTPSLifecycleSummary) string {
	t.Helper()
	root := t.TempDir()
	contents, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("encode summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.json"), contents, 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	for _, name := range trustedHTTPSEvidenceFiles[:len(trustedHTTPSEvidenceFiles)-1] {
		if err := os.WriteFile(filepath.Join(root, name), []byte("bounded redacted log"), 0o600); err != nil {
			t.Fatalf("write log %s: %v", name, err)
		}
	}
	return root
}
