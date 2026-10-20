package main

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestWorkerGuard rejects local, self-hosted, and unrelated repository execution.
func TestWorkerGuard(t *testing.T) {
	valid := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "goforj/harbor", "RUNNER_ENVIRONMENT": "github-hosted"}
	if err := workerGuard(func(key string) string { return valid[key] }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_REPOSITORY", "RUNNER_ENVIRONMENT"} {
		t.Run(key, func(t *testing.T) {
			if err := workerGuard(func(candidate string) string {
				if candidate == key {
					return "other"
				}
				return valid[candidate]
			}); err == nil {
				t.Fatal("unsafe worker admitted")
			}
		})
	}
}

// TestFixtureRoot rejects aliases, escaped roots, and malformed disposable names.
func TestFixtureRoot(t *testing.T) {
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.MkdirTemp(temporary, "harbor-package-ui-guard-")
	if err != nil {
		t.Fatal(err)
	}
	// These empty unit-test directories have no generated source or installed project state.
	t.Cleanup(func() { os.Remove(filepath.Join(parent, "project")); os.Remove(parent) })
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureRoot(project); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "project", parent, project + string(os.PathSeparator), filepath.Join(t.TempDir(), "project")} {
		if _, err := fixtureRoot(path); err == nil {
			t.Errorf("admitted %q", path)
		}
	}
}

// TestInitialProject requires exactly the selected Ready project and configured networking.
func TestInitialProject(t *testing.T) {
	project := domain.ProjectSnapshot{ID: "original", Path: "/fixture/project", State: domain.ProjectReady}
	snapshot := domain.Snapshot{Projects: []domain.ProjectSnapshot{project}, NetworkSetup: &domain.NetworkSetupSnapshot{Configured: true}}
	if _, err := initialProject(snapshot, project.ID, project.Path); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Snapshot){
		func(s *domain.Snapshot) { s.Projects = nil }, func(s *domain.Snapshot) { s.Projects = append(s.Projects, project) },
		func(s *domain.Snapshot) { s.NetworkSetup = nil }, func(s *domain.Snapshot) { s.NetworkSetup = &domain.NetworkSetupSnapshot{} },
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: "other", Path: project.Path, State: domain.ProjectReady}}
		},
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: project.ID, Path: "/other", State: domain.ProjectReady}}
		},
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: project.ID, Path: project.Path, State: domain.ProjectStarting}}
		},
	} {
		copy := snapshot
		mutate(&copy)
		if _, err := initialProject(copy, project.ID, project.Path); err == nil {
			t.Fatal("unsafe initial snapshot admitted")
		}
	}
}

// TestContinuityAndDistinctProjects checks exact sessions, reconnect generations, and stable unique addresses.
func TestContinuityAndDistinctProjects(t *testing.T) {
	before := projectEvidence{ID: "original", Path: "/fixture/project", Host: "original.test", Address: "127.0.0.2", SessionID: "session-original", Generation: 2, HTTPS200: true}
	after := before
	after.Generation++
	if err := continuity(before, after); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){
		func(e *projectEvidence) { e.ID = "other" }, func(e *projectEvidence) { e.Path = "/other" }, func(e *projectEvidence) { e.Host = "other.test" },
		func(e *projectEvidence) { e.Address = "127.0.0.3" }, func(e *projectEvidence) { e.SessionID = "replacement" }, func(e *projectEvidence) { e.Generation-- }, func(e *projectEvidence) { e.HTTPS200 = false },
	} {
		changed := before
		mutate(&changed)
		if err := continuity(before, changed); err == nil {
			t.Fatal("continuity mismatch admitted")
		}
	}
	peer := projectEvidence{ID: "peer", Host: "peer.test", Address: "127.0.0.3", SessionID: "peer-session"}
	if err := distinctProjects(before, peer); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){func(e *projectEvidence) { e.ID = before.ID }, func(e *projectEvidence) { e.Host = before.Host }, func(e *projectEvidence) { e.Address = before.Address }, func(e *projectEvidence) { e.SessionID = before.SessionID }, func(e *projectEvidence) { e.SessionID = "" }} {
		changed := peer
		mutate(&changed)
		if err := distinctProjects(before, changed); err == nil {
			t.Fatal("collision admitted")
		}
	}
}

// TestGateProof rejects stale markers, foreign listeners, malformed ports, and oversized fixture output.
func TestGateProof(t *testing.T) {
	proof := gateProof{Address: "127.0.0.1:12345", Marker: "marker"}
	if err := validateGateProof(proof, "marker"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"localhost:12345", "127.0.0.2:12345", "[::1]:12345", "0.0.0.0:12345", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:abc", "127.0.0.1"} {
		if err := validateGateProof(gateProof{address, "marker"}, "marker"); err == nil {
			t.Errorf("admitted %s", address)
		}
	}
	if err := validateGateProof(proof, "stale"); err == nil {
		t.Fatal("stale marker admitted")
	}
	if err := validateGateProof(gateProof{proof.Address, ""}, ""); err == nil {
		t.Fatal("empty marker admitted")
	}
	path := filepath.Join(t.TempDir(), "entered.json")
	for _, data := range []string{`{"address":"127.0.0.1:12345","marker":"marker"}`, "not JSON", strings.Repeat("x", 2049)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readGateProof(path, "marker")
		if strings.HasPrefix(data, "{") && err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(data, "{") && err == nil {
			t.Fatal("invalid output admitted")
		}
	}
}

// TestGeneratedGateCompilesAndReleases proves the gate is disabled initially and closes its live proof on release.
func TestGeneratedGateCompilesAndReleases(t *testing.T) {
	parent := t.TempDir()
	marker := "gate-native-test"
	source := gateSource(parent, marker)
	if _, err := parser.ParseFile(token.NewFileSet(), "gate.go", source, parser.AllErrors); err != nil {
		t.Fatal(err)
	}
	source += "\n// main completes the isolated gate compilation fixture.\nfunc main() {}\n"
	path := filepath.Join(parent, "gate.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(parent, "gate-test")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, path).CombinedOutput(); err != nil {
		t.Fatalf("gate compile: %v: %s", err, output)
	}
	if err := exec.CommandContext(ctx, binary).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "entered.json")); !os.IsNotExist(err) {
		t.Fatal("disabled gate wrote proof")
	}
	if err := os.WriteFile(filepath.Join(parent, "gate-enabled"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	var proof gateProof
	if err := waitFor(ctx, 5*time.Second, func(attempt context.Context) (bool, error) {
		var err error
		proof, err = readGateProof(filepath.Join(parent, "entered.json"), marker)
		return err == nil, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := probe(ctx, "http://"+proof.Address+"/", marker); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(parent, "entered.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded gateProof
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "gate-release"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := listenerAbsent(ctx, proof.Address); err != nil {
		t.Fatal(err)
	}
}

// TestRunFailureWritesIncompleteReceipt proves admission failures preserve machine-readable evidence.
func TestRunFailureWritesIncompleteReceipt(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := run([]string{"--project-id", "original", "--project-directory", "/invalid/project", "--evidence", path}); err == nil {
		t.Fatal("local mutation admitted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evidence receipt
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Complete || evidence.Error == "" || len(evidence.Crashes) != 0 {
		t.Fatalf("failure receipt: %s", data)
	}
}

// TestParseFailureWritesIncompleteReceipt retains evidence when a malformed earlier flag stops parsing.
func TestParseFailureWritesIncompleteReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parse-receipt.json")
	if err := run([]string{"--unknown-argument", "--evidence=" + path}); err == nil {
		t.Fatal("invalid argument admitted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evidence receipt
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Complete || evidence.Error == "" {
		t.Fatalf("failure receipt: %s", data)
	}
}
