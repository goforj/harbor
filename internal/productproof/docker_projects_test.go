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

// TestVerifyDockerProjectEvidenceDirectoryRejectsUnknownPlatform keeps the product gate from accepting unreviewed OS semantics.
func TestVerifyDockerProjectEvidenceDirectoryRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	if err := VerifyDockerProjectEvidenceDirectory(t.TempDir(), DockerProjectRequirement{Platforms: []string{"plan9"}, AppPort: 3000, ServicePort: 3306}); err == nil || !strings.Contains(err.Error(), "unsupported Docker project proof platform") {
		t.Fatalf("expected unsupported platform rejection, got %v", err)
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
		{name: "Linux Docker Desktop", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.Dependencies.EngineKind = "docker-desktop"
		}, want: "requires docker-engine on Linux"},
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
		{name: "missing event target", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.TargetProjectID = ""
		}, want: "invalid target project"},
		{name: "unknown event target", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.TargetProjectID = "foreign"
		}, want: "unknown target project"},
		{name: "unchanged event target", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.AfterContainerIDs = append([]string(nil), lifecycle.EventRefresh.BeforeContainerIDs...)
		}, want: "retained a shared container"},
		{name: "non advancing event revision", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.AfterRevision = lifecycle.EventRefresh.BeforeRevision
		}, want: "revision did not advance"},
		{name: "unknown event peer", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.Peers[0].ProjectID = "foreign"
		}, want: "unknown peer project"},
		{name: "shared event peer container", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.Peers[0].BeforeContainerIDs[0] = lifecycle.EventRefresh.AfterContainerIDs[0]
			lifecycle.EventRefresh.Peers[0].AfterContainerIDs[0] = lifecycle.EventRefresh.AfterContainerIDs[0]
		}, want: "shares container"},
		{name: "changed event peer", mutate: func(lifecycle *DockerProjectEvidence, _ *DockerCleanupEvidence) {
			lifecycle.EventRefresh.Peers[0].AfterContainerIDs[0] = "billing-mysql-replaced"
		}, want: "changed container identities"},
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
		{name: "cleanup runner mismatch", mutate: func(_ *DockerProjectEvidence, cleanup *DockerCleanupEvidence) {
			cleanup.Runtime.RunnerName = "other-runner"
		}, want: "runtime identity does not match lifecycle"},
		{name: "cleanup digest mismatch", mutate: func(_ *DockerProjectEvidence, cleanup *DockerCleanupEvidence) {
			cleanup.ArtifactDigests[0] = strings.Repeat("c", 64)
		}, want: "artifact digests do not match lifecycle"},
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

// TestVerifyDockerProjectEvidenceRequiresDesktopOnClientPlatforms prevents a hosted Engine result from standing in for Docker Desktop proof.
func TestVerifyDockerProjectEvidenceRequiresDesktopOnClientPlatforms(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"darwin", "windows"} {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lifecycle := validDockerProjectFixture(platform)
			lifecycle.Dependencies.EngineKind = "docker-engine"
			writeDockerProjectEvidenceFixture(t, root, lifecycle, validDockerCleanupFixture(platform))
			err := VerifyDockerProjectEvidenceDirectory(root, DockerProjectRequirement{Commit: "abc123", Platforms: []string{platform}, AppPort: 3000, ServicePort: 3306})
			if err == nil || !strings.Contains(err.Error(), "requires docker-desktop") {
				t.Fatalf("expected Docker Desktop requirement, got %v", err)
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
	engineKind := "docker-engine"
	if platform == "darwin" || platform == "windows" {
		engineKind = "docker-desktop"
	}
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
			EngineKind:    engineKind,
			EngineVersion: "28.5.2",
		},
		Projects: []ProjectEvidence{
			{ID: "orders", Address: "127.77.254.10", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"orders-mysql"}},
			{ID: "billing", Address: "127.77.254.11", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"billing-mysql"}},
			{ID: "inventory", Address: "127.77.254.12", AppPort: 3000, ServicePort: 3306, ContainerIDs: []string{"inventory-mysql"}},
		},
		EventRefresh: EventRefreshEvidence{
			TargetProjectID:    "orders",
			TargetServiceID:    "mysql",
			BeforeRevision:     4,
			AfterRevision:      5,
			BeforeContainerIDs: []string{"orders-mysql-before"},
			AfterContainerIDs:  []string{"orders-mysql-after"},
			Peers: []EventRefreshPeerEvidence{
				{ProjectID: "billing", BeforeContainerIDs: []string{"billing-mysql"}, AfterContainerIDs: []string{"billing-mysql"}},
				{ProjectID: "inventory", BeforeContainerIDs: []string{"inventory-mysql"}, AfterContainerIDs: []string{"inventory-mysql"}},
			},
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
		ArtifactDigests: []string{strings.Repeat("b", 64)},
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
