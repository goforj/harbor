package trustedhttpsharness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

// TestHappyPathProjectsPinsThreeSamePortPublicIdentities keeps every later platform job on one behavioral fixture.
func TestHappyPathProjectsPinsThreeSamePortPublicIdentities(t *testing.T) {
	projects := HappyPathProjects()
	rendered, err := RenderSpecs(projects)
	if err != nil {
		t.Fatalf("RenderSpecs() error = %v", err)
	}
	endpoints, err := ProbeEndpoints(projects)
	if err != nil {
		t.Fatalf("ProbeEndpoints() error = %v", err)
	}
	if len(rendered) != 3 || len(endpoints) != 3 {
		t.Fatalf("rendered = %#v, endpoints = %#v", rendered, endpoints)
	}
	for index, project := range projects {
		if rendered[index].Name != project.Name || rendered[index].Module != project.Module || rendered[index].Port != happyPathAppPort || rendered[index].MySQL {
			t.Fatalf("rendered project %d = %#v", index, rendered[index])
		}
		if endpoints[index] != (Endpoint{
			Domain:       project.Domain,
			OpenAPITitle: project.Name,
		}) {
			t.Fatalf("endpoint %d = %#v", index, endpoints[index])
		}
	}
}

// TestProjectSpecValidationRejectsNarrowedOrAmbiguousProofs covers every identity and same-port requirement.
func TestProjectSpecValidationRejectsNarrowedOrAmbiguousProofs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]ProjectSpec) []ProjectSpec
	}{
		{
			name: "too few",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				return projects[:2]
			},
		},
		{
			name: "name empty",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[0].Name = ""
				return projects
			},
		},
		{
			name: "name padded",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[0].Name = " Orders"
				return projects
			},
		},
		{
			name: "module empty",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[0].Module = ""
				return projects
			},
		},
		{
			name: "domain",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[0].Domain = "orders.test:443"
				return projects
			},
		},
		{
			name: "translated port",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[0].AppPort = 3100
				return projects
			},
		},
		{
			name: "name duplicate",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[1].Name = projects[0].Name
				return projects
			},
		},
		{
			name: "module duplicate",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[1].Module = projects[0].Module
				return projects
			},
		},
		{
			name: "domain duplicate",
			mutate: func(projects []ProjectSpec) []ProjectSpec {
				projects[1].Domain = projects[0].Domain
				return projects
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projects := append([]ProjectSpec(nil), HappyPathProjects()...)
			if _, err := RenderSpecs(test.mutate(projects)); err == nil {
				t.Fatal("RenderSpecs() error = nil")
			}
		})
	}
}

// TestCheckoutBaselinesDetectAndAcceptExactCleanup proves the harness checks content only after restoration.
func TestCheckoutBaselinesDetectAndAcceptExactCleanup(t *testing.T) {
	projects := make([]goforjproject.Project, 0, 3)
	for _, name := range []string{"orders", "billing", "inventory"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create generated checkout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_HTTP_PORT=3000\n"), 0o600); err != nil {
			t.Fatalf("write generated checkout: %v", err)
		}
		if err := writeFixtureAppReady(root, "initial"); err != nil {
			t.Fatalf("write generated build marker: %v", err)
		}
		projects = append(projects, goforjproject.Project{
			Name: name,
			Root: root,
		})
	}
	baselines, err := CaptureBaselines(projects)
	if err != nil {
		t.Fatalf("CaptureBaselines() error = %v", err)
	}
	hostEnvironment := filepath.Join(projects[0].Root, ".env.host")
	if err := os.WriteFile(hostEnvironment, []byte("# harbor managed\n"), 0o600); err != nil {
		t.Fatalf("write managed host environment: %v", err)
	}
	if err := VerifyBaselines(baselines); err == nil || !strings.Contains(err.Error(), "added .env.host") {
		t.Fatalf("VerifyBaselines(changed) error = %v", err)
	}
	if err := os.Remove(hostEnvironment); err != nil {
		t.Fatalf("remove managed host environment: %v", err)
	}
	if err := VerifyBaselines(baselines); err != nil {
		t.Fatalf("VerifyBaselines(restored) error = %v", err)
	}
}

// TestCheckoutBaselinesPermitOnlyGeneratedDerivedOutputContentRefresh keeps GoForj's content exception narrowly scoped.
func TestCheckoutBaselinesPermitOnlyGeneratedDerivedOutputContentRefresh(t *testing.T) {
	projects := make([]goforjproject.Project, 0, 3)
	for _, name := range []string{"orders", "billing", "inventory"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create generated checkout: %v", err)
		}
		if err := writeFixtureDerivedOutputs(root, "initial"); err != nil {
			t.Fatalf("write generated outputs: %v", err)
		}
		projects = append(projects, goforjproject.Project{
			Name: name,
			Root: root,
		})
	}
	baselines, err := CaptureBaselines(projects)
	if err != nil {
		t.Fatalf("CaptureBaselines() error = %v", err)
	}
	for path := range generatedDerivedOutputPaths {
		filename := filepath.Join(projects[0].Root, filepath.FromSlash(path))
		if err := os.WriteFile(filename, []byte("new dev-session output"), 0o600); err != nil {
			t.Fatalf("refresh generated output %q: %v", path, err)
		}
	}
	if err := VerifyBaselines(baselines); err != nil {
		t.Fatalf("VerifyBaselines(refreshed outputs) error = %v", err)
	}

	ready := filepath.Join(projects[0].Root, "bin", ".app.ready")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(ready, 0o644); err != nil {
			t.Fatalf("change generated build marker mode: %v", err)
		}
		if err := VerifyBaselines(baselines); err == nil || !strings.Contains(err.Error(), "changed bin/.app.ready") {
			t.Fatalf("VerifyBaselines(changed marker mode) error = %v", err)
		}
	}
	if err := os.Remove(ready); err != nil {
		t.Fatalf("remove generated build marker: %v", err)
	}
	if err := VerifyBaselines(baselines); err == nil || !strings.Contains(err.Error(), "removed bin/.app.ready") {
		t.Fatalf("VerifyBaselines(removed marker) error = %v", err)
	}
}

// TestCheckoutBaselinesRejectDerivedOutputDeletionAndTypeOrModeChanges keeps the content-only allowance from masking checkout drift.
func TestCheckoutBaselinesRejectDerivedOutputDeletionAndTypeOrModeChanges(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    string
		mutate  func(string) error
		wantErr string
	}{
		{name: "deletion", path: "bin/app", mutate: os.Remove, wantErr: "removed bin/app"},
		{name: "type", path: "bin/.app.ready", mutate: func(filename string) error {
			if err := os.Remove(filename); err != nil {
				return err
			}
			return os.Mkdir(filename, 0o700)
		}, wantErr: "changed bin/.app.ready"},
		{name: "mode", path: "bin/.forj-build-cache/app.target/app", mutate: func(filename string) error {
			return os.Chmod(filename, 0o644)
		}, wantErr: "changed bin/.forj-build-cache/app.target/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projects := fixtureProjectsWithDerivedOutputs(t)
			baselines, err := CaptureBaselines(projects)
			if err != nil {
				t.Fatalf("CaptureBaselines() error = %v", err)
			}
			filename := filepath.Join(projects[0].Root, filepath.FromSlash(test.path))
			if err := test.mutate(filename); err != nil {
				t.Fatalf("mutate generated output: %v", err)
			}
			if err := VerifyBaselines(baselines); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("VerifyBaselines() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// TestRestoreReadyMarkersRestoresBytesAndMode proves the diagnostic restoration helper preserves an exact snapshot.
func TestRestoreReadyMarkersRestoresBytesAndMode(t *testing.T) {
	projects := fixtureProjectsWithDerivedOutputs(t)
	ready := filepath.Join(projects[0].Root, filepath.FromSlash(generatedAppReadyPath))
	if err := os.Chmod(ready, 0o644); err != nil {
		t.Fatalf("set baseline marker mode: %v", err)
	}
	baselines, err := CaptureBaselines(projects)
	if err != nil {
		t.Fatalf("CaptureBaselines() error = %v", err)
	}
	if err := os.WriteFile(ready, []byte("changed marker"), 0o600); err != nil {
		t.Fatalf("change generated build marker: %v", err)
	}
	if err := os.Chmod(ready, 0o600); err != nil {
		t.Fatalf("change generated build marker mode: %v", err)
	}
	if err := VerifyBaselinesExact(baselines); err == nil || !strings.Contains(err.Error(), "changed bin/.app.ready") {
		t.Fatalf("VerifyBaselinesExact(changed marker) error = %v", err)
	}
	if err := RestoreReadyMarkers(baselines); err != nil {
		t.Fatalf("RestoreReadyMarkers() error = %v", err)
	}
	if err := VerifyBaselinesExact(baselines); err != nil {
		t.Fatalf("VerifyBaselinesExact(restored marker) error = %v", err)
	}
}

// TestCheckoutBaselinesRejectNonDerivedSourceAndEnvironmentChanges proves the derived-output exception cannot hide project mutations.
func TestCheckoutBaselinesRejectNonDerivedSourceAndEnvironmentChanges(t *testing.T) {
	projects := fixtureProjectsWithDerivedOutputs(t)
	baselines, err := CaptureBaselines(projects)
	if err != nil {
		t.Fatalf("CaptureBaselines() error = %v", err)
	}
	for _, path := range []string{"main.go", ".env", ".env.host"} {
		t.Run(path, func(t *testing.T) {
			filename := filepath.Join(projects[0].Root, path)
			if err := os.WriteFile(filename, []byte("changed\n"), 0o600); err != nil {
				t.Fatalf("change %q: %v", path, err)
			}
			if err := VerifyBaselines(baselines); err == nil || !strings.Contains(err.Error(), "changed "+path) {
				t.Fatalf("VerifyBaselines() error = %v, want changed %s", err, path)
			}
			if err := os.WriteFile(filename, []byte("baseline\n"), 0o600); err != nil {
				t.Fatalf("restore %q: %v", path, err)
			}
		})
	}
}

// TestCheckoutBaselineValidationRejectsMissingAndDuplicatedRoots covers harness authority before filesystem reads.
func TestCheckoutBaselineValidationRejectsMissingAndDuplicatedRoots(t *testing.T) {
	root := t.TempDir()
	projects := []goforjproject.Project{
		{
			Name: "orders",
			Root: root,
		},
		{
			Name: "billing",
			Root: root,
		},
		{
			Name: "inventory",
			Root: "relative",
		},
	}
	if _, err := CaptureBaselines(projects); err == nil {
		t.Fatal("CaptureBaselines() error = nil")
	}
	if err := VerifyBaselines(nil); err == nil {
		t.Fatal("VerifyBaselines(nil) error = nil")
	}
}

// fixtureProjectsWithDerivedOutputs creates three baseline-ready checkouts with generated outputs and protected project files.
func fixtureProjectsWithDerivedOutputs(t *testing.T) []goforjproject.Project {
	t.Helper()

	projects := make([]goforjproject.Project, 0, 3)
	for _, name := range []string{"orders", "billing", "inventory"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create generated checkout: %v", err)
		}
		if err := writeFixtureDerivedOutputs(root, "baseline"); err != nil {
			t.Fatalf("write generated outputs: %v", err)
		}
		for _, path := range []string{"main.go", ".env", ".env.host"} {
			if err := os.WriteFile(filepath.Join(root, path), []byte("baseline\n"), 0o600); err != nil {
				t.Fatalf("write baseline %q: %v", path, err)
			}
		}
		projects = append(projects, goforjproject.Project{Name: name, Root: root})
	}
	return projects
}
