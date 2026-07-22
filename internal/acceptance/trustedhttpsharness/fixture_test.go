package trustedhttpsharness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

// TestPrepareGeneratedResponsesBuildsAndValidatesEveryProject proves baseline-ready identity generation.
func TestPrepareGeneratedResponsesBuildsAndValidatesEveryProject(t *testing.T) {
	executable := fixtureExecutable(t)
	projects, rendered := fixtureRenderedProjects(t)
	var invoked []string
	err := prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, func(_ context.Context, gotExecutable string, root string) error {
		if gotExecutable != executable {
			t.Fatalf("fixture executable = %q, want %q", gotExecutable, executable)
		}
		invoked = append(invoked, root)
		return writeFixtureOpenAPI(root, rendered[len(invoked)-1].Name)
	})
	if err != nil {
		t.Fatalf("prepareGeneratedResponsesWith() error = %v", err)
	}
	if len(invoked) != 3 {
		t.Fatalf("fixture invocations = %#v", invoked)
	}
	if _, err := CaptureBaselines(rendered); err != nil {
		t.Fatalf("CaptureBaselines(prepared) error = %v", err)
	}
}

// TestPrepareGeneratedResponsesRejectsMismatchesBeforeLaterBuilds covers the fixture correlation boundary.
func TestPrepareGeneratedResponsesRejectsMismatchesBeforeLaterBuilds(t *testing.T) {
	executable := fixtureExecutable(t)
	tests := []struct {
		name   string
		mutate func([]ProjectSpec, []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project)
	}{
		{name: "count", mutate: func(projects []ProjectSpec, rendered []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project) {
			return projects, rendered[:2]
		}},
		{name: "name", mutate: func(projects []ProjectSpec, rendered []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project) {
			rendered[0].Name = "Other"
			return projects, rendered
		}},
		{name: "module", mutate: func(projects []ProjectSpec, rendered []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project) {
			rendered[0].Module = "example.test/other"
			return projects, rendered
		}},
		{name: "port", mutate: func(projects []ProjectSpec, rendered []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project) {
			rendered[0].Port = 3100
			return projects, rendered
		}},
		{name: "root", mutate: func(projects []ProjectSpec, rendered []goforjproject.Project) ([]ProjectSpec, []goforjproject.Project) {
			rendered[0].Root = "relative"
			return projects, rendered
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projects, rendered := fixtureRenderedProjects(t)
			projects, rendered = test.mutate(projects, rendered)
			calls := 0
			err := prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, func(context.Context, string, string) error {
				calls++
				return nil
			})
			if err == nil {
				t.Fatal("prepareGeneratedResponsesWith() error = nil")
			}
			if test.name != "count" && calls != 0 {
				t.Fatalf("invocations = %d, want zero", calls)
			}
		})
	}
}

// TestPrepareGeneratedResponsesStopsOnBuildOrArtifactFailure proves no later checkout is treated as baseline-ready.
func TestPrepareGeneratedResponsesStopsOnBuildOrArtifactFailure(t *testing.T) {
	executable := fixtureExecutable(t)
	t.Run("build", func(t *testing.T) {
		projects, rendered := fixtureRenderedProjects(t)
		cause := errors.New("build failed")
		calls := 0
		err := prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, func(context.Context, string, string) error {
			calls++
			return cause
		})
		if !errors.Is(err, cause) || calls != 1 {
			t.Fatalf("prepareGeneratedResponsesWith() = %v, calls = %d", err, calls)
		}
	})

	t.Run("wrong identity", func(t *testing.T) {
		projects, rendered := fixtureRenderedProjects(t)
		calls := 0
		err := prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, func(_ context.Context, _ string, root string) error {
			calls++
			return writeFixtureOpenAPI(root, "Wrong Project")
		})
		if err == nil || !strings.Contains(err.Error(), "generated OpenAPI title") || calls != 1 {
			t.Fatalf("prepareGeneratedResponsesWith() = %v, calls = %d", err, calls)
		}
	})

	t.Run("missing artifact", func(t *testing.T) {
		projects, rendered := fixtureRenderedProjects(t)
		calls := 0
		err := prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, func(context.Context, string, string) error {
			calls++
			return nil
		})
		if err == nil || calls != 1 {
			t.Fatalf("prepareGeneratedResponsesWith() = %v, calls = %d", err, calls)
		}
	})
}

// TestPrepareGeneratedResponsesRejectsCancellationAndMissingDependencies covers pre-build failure paths.
func TestPrepareGeneratedResponsesRejectsCancellationAndMissingDependencies(t *testing.T) {
	executable := fixtureExecutable(t)
	projects, rendered := fixtureRenderedProjects(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareGeneratedResponsesWith(ctx, executable, projects, rendered, func(context.Context, string, string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareGeneratedResponsesWith(cancelled) error = %v", err)
	}
	t.Run("nil invoker", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("prepareGeneratedResponsesWith(nil invoker) did not panic")
			}
		}()
		_ = prepareGeneratedResponsesWith(t.Context(), executable, projects, rendered, nil)
	})
}

// fixtureExecutable creates one direct executable with the required production basename.
func fixtureExecutable(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	filename := filepath.Join(directory, "forj")
	if err := os.WriteFile(filename, []byte("fixture"), 0o700); err != nil {
		t.Fatalf("write fixture executable: %v", err)
	}
	return filename
}

// fixtureRenderedProjects creates three empty correlated checkout roots.
func fixtureRenderedProjects(t *testing.T) ([]ProjectSpec, []goforjproject.Project) {
	t.Helper()
	projects := HappyPathProjects()
	rendered := make([]goforjproject.Project, 0, len(projects))
	for _, project := range projects {
		root := filepath.Join(t.TempDir(), strings.ToLower(project.Name))
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("create rendered fixture root: %v", err)
		}
		rendered = append(rendered, goforjproject.Project{
			Name:   project.Name,
			Module: project.Module,
			Port:   project.AppPort,
			Root:   root,
		})
	}
	return projects, rendered
}

// writeFixtureOpenAPI creates one direct generated artifact for post-build validation.
func writeFixtureOpenAPI(root string, title string) error {
	build := filepath.Join(root, "build")
	if err := os.MkdirAll(build, 0o700); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(build, "openapi.json"),
		[]byte(`{"openapi":"3.0.3","info":{"title":"`+title+`"}}`),
		0o600,
	)
}
