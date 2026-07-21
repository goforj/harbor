package productproof

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyDockerProjectEvidenceDirectory accepts one complete lifecycle and cleanup result for every required product platform.
func TestVerifyDockerProjectEvidenceDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	platforms := []string{"linux", "darwin", "windows"}
	for _, platform := range platforms {
		writeDockerProjectEvidenceFixture(t, root, validDockerProjectFixture(platform), validDockerCleanupFixture(platform))
	}
	if err := VerifyDockerProjectEvidenceDirectory(root, DockerProjectRequirement{
		Commit:      "abc123",
		Platforms:   platforms,
		AppPort:     3000,
		ServicePort: 3306,
	}); err != nil {
		t.Fatalf("verify Docker project evidence: %v", err)
	}
}

// TestVerifyDockerProjectEvidenceDirectoryRejectsInvalidEvidence exercises every fail-closed manifest boundary.
func TestVerifyDockerProjectEvidenceDirectoryRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*DockerProjectEvidence, *DockerCleanupEvidence)
		want   string
	}{
		{name: "wrong commit", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) { lifecycle.Runtime.Commit = "other" }, want: "instead of"},
		{name: "missing runner", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) { lifecycle.Runtime.RunnerImage = "" }, want: "missing runner identity"},
		{name: "wrong engine", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Dependencies.EngineKind = "remote-docker"
		}, want: "unsupported Docker engine kind"},
		{name: "old engine", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Dependencies.EngineVersion = "27.5.1"
		}, want: "below the supported major version"},
		{name: "invalid engine version", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Dependencies.EngineVersion = "desktop-latest"
		}, want: "invalid Docker engine version"},
		{name: "missing project", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Projects = lifecycle.Projects[:2]
		}, want: "three generated projects"},
		{name: "wrong app port", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Projects[0].AppPort = 13000
		}, want: "instead of required"},
		{name: "duplicate address", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Projects[1].Address = lifecycle.Projects[0].Address
		}, want: "duplicate project loopback"},
		{name: "shared container", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Projects[1].ContainerIDs[0] = lifecycle.Projects[0].ContainerIDs[0]
		}, want: "multiple projects"},
		{name: "failed assertion", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Assertions[0].Passed = false
		}, want: "failed assertion"},
		{name: "missing assertion", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Assertions = lifecycle.Assertions[1:]
		}, want: "missing assertion"},
		{name: "unexpected assertion", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Assertions[0].ID = "docker.projects.optional"
		}, want: "unexpected assertion"},
		{name: "missing digest", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) { lifecycle.ArtifactDigests = nil }, want: "no artifact digests"},
		{name: "cleanup mismatch", mutate: func(_ *DockerProjectEvidence, cleanup *DockerCleanupEvidence) { cleanup.ProjectIDs[0] = "foreign" }, want: "do not match"},
		{name: "cleanup missing assertion", mutate: func(_ *DockerProjectEvidence, cleanup *DockerCleanupEvidence) { cleanup.Assertions = nil }, want: "missing assertion"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lifecycle := validDockerProjectFixture("linux")
			cleanup := validDockerCleanupFixture("linux")
			test.mutate(&lifecycle, &cleanup)
			writeDockerProjectEvidenceFixture(t, root, lifecycle, cleanup)
			err := VerifyDockerProjectEvidenceDirectory(root, DockerProjectRequirement{Commit: "abc123", Platforms: []string{"linux"}, AppPort: 3000, ServicePort: 3306})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

// TestVerifyDockerProjectEvidenceDirectoryRejectsUntrustedDocuments keeps the product verifier strict before it sees platform facts.
func TestVerifyDockerProjectEvidenceDirectoryRejectsUntrustedDocuments(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "linux")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "docker-projects.json"), []byte(`{"schema_version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write lifecycle evidence: %v", err)
	}
	if err := VerifyDockerProjectEvidenceDirectory(root, DockerProjectRequirement{Platforms: []string{"linux"}, AppPort: 3000, ServicePort: 3306}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict JSON error, got %v", err)
	}
}

// TestWriteDockerProjectEvidenceCreatesOnlyTheFixedManifests keeps a native worker from uploading arbitrary or replaced artifact paths.
func TestWriteDockerProjectEvidenceCreatesOnlyTheFixedManifests(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "evidence")
	lifecycle := validDockerProjectFixture("linux")
	cleanup := validDockerCleanupFixture("linux")
	if err := WriteDockerProjectEvidence(directory, lifecycle, cleanup); err != nil {
		t.Fatalf("write Docker project evidence: %v", err)
	}
	if err := VerifyDockerProjectEvidenceDirectory(filepath.Dir(directory), DockerProjectRequirement{Commit: "abc123", Platforms: []string{"linux"}, AppPort: 3000, ServicePort: 3306}); err != nil {
		t.Fatalf("verify written Docker project evidence: %v", err)
	}
	if err := WriteDockerProjectEvidence(directory, lifecycle, cleanup); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected non-empty evidence directory rejection, got %v", err)
	}
	if err := WriteDockerProjectEvidence("relative", lifecycle, cleanup); err == nil || !strings.Contains(err.Error(), "absolute clean path") {
		t.Fatalf("expected relative evidence directory rejection, got %v", err)
	}
}

// validDockerProjectFixture builds a complete generated-project lifecycle manifest for verifier tests.
func validDockerProjectFixture(platform string) DockerProjectEvidence {
	return DockerProjectEvidence{
		SchemaVersion: DockerProjectEvidenceSchemaVersion,
		Capability:    dockerProjectCapability,
		Scope:         dockerProjectScope,
		Runtime: RuntimeEvidence{
			GOOS:               platform,
			GOARCH:             "amd64",
			Commit:             "abc123",
			RunnerName:         "product-runner",
			RunnerImage:        "harbor-product-image",
			RunnerImageVersion: "2026.07.20",
		},
		Dependencies: DependencyEvidence{
			GoForjVersion: "0.19.0",
			GoForjDigest:  strings.Repeat("a", 64),
			EngineKind:    "docker-engine",
			EngineVersion: "28.5.2",
		},
		Projects: []ProjectEvidence{
			{ID: "orders", Address: "127.77.254.10", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"orders-mysql"}},
			{ID: "billing", Address: "127.77.254.11", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"billing-mysql"}},
			{ID: "inventory", Address: "127.77.254.12", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"inventory-mysql"}},
		},
		Assertions: []AssertionEvidence{
			{ID: "docker.projects.generated", Passed: true, Detail: "three generated GoForj projects started"},
			{ID: "docker.projects.isolated", Passed: true, Detail: "admitted container identities were disjoint"},
			{ID: "docker.adapter.read_only", Passed: true, Detail: "observer calls did not change admitted container identities"},
			{ID: "docker.logs.available", Passed: true, Detail: "one exact service log follower opened for every project"},
			{ID: "docker.events.refresh", Passed: true, Detail: "container replacement woke a fenced fresh service observation"},
			{ID: "docker.projects.stop_peer_survival", Passed: true, Detail: "two peers remained ready while orders stopped"},
			{ID: "docker.projects.restart", Passed: true, Detail: "orders restarted on its original identity"},
		},
		ArtifactDigests: []string{strings.Repeat("b", 64)},
	}
}

// validDockerCleanupFixture builds matching exact cleanup evidence for verifier tests.
func validDockerCleanupFixture(platform string) DockerCleanupEvidence {
	return DockerCleanupEvidence{
		SchemaVersion: DockerProjectEvidenceSchemaVersion,
		Capability:    dockerProjectCleanupCapability,
		Scope:         dockerProjectScope,
		Runtime: RuntimeEvidence{
			GOOS:               platform,
			GOARCH:             "amd64",
			Commit:             "abc123",
			RunnerName:         "product-runner",
			RunnerImage:        "harbor-product-image",
			RunnerImageVersion: "2026.07.20",
		},
		ProjectIDs:      []string{"orders", "billing", "inventory"},
		Assertions:      []AssertionEvidence{{ID: "docker.cleanup.exact", Passed: true, Detail: "only product-worker project state was removed"}},
		ArtifactDigests: []string{strings.Repeat("c", 64)},
	}
}

// writeDockerProjectEvidenceFixture writes the exact fixed artifact names consumed by the product gate.
func writeDockerProjectEvidenceFixture(t *testing.T, root string, lifecycle DockerProjectEvidence, cleanup DockerCleanupEvidence) {
	t.Helper()
	directory := filepath.Join(root, lifecycle.Runtime.GOOS)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	for name, value := range map[string]any{"docker-projects.json": lifecycle, "docker-cleanup.json": cleanup} {
		contents, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), contents, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
