package trustedhttpsharness

import (
	"os"
	"path/filepath"
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
		if endpoints[index] != (Endpoint{Domain: project.Domain, OpenAPITitle: project.Name}) {
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
		{name: "too few", mutate: func(projects []ProjectSpec) []ProjectSpec { return projects[:2] }},
		{name: "name empty", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[0].Name = ""; return projects }},
		{name: "name padded", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[0].Name = " Orders"; return projects }},
		{name: "module empty", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[0].Module = ""; return projects }},
		{name: "domain", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[0].Domain = "orders.test:443"; return projects }},
		{name: "translated port", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[0].AppPort = 3100; return projects }},
		{name: "name duplicate", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[1].Name = projects[0].Name; return projects }},
		{name: "module duplicate", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[1].Module = projects[0].Module; return projects }},
		{name: "domain duplicate", mutate: func(projects []ProjectSpec) []ProjectSpec { projects[1].Domain = projects[0].Domain; return projects }},
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
		projects = append(projects, goforjproject.Project{Name: name, Root: root})
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

// TestCheckoutBaselineValidationRejectsMissingAndDuplicatedRoots covers harness authority before filesystem reads.
func TestCheckoutBaselineValidationRejectsMissingAndDuplicatedRoots(t *testing.T) {
	root := t.TempDir()
	projects := []goforjproject.Project{{Name: "orders", Root: root}, {Name: "billing", Root: root}, {Name: "inventory", Root: "relative"}}
	if _, err := CaptureBaselines(projects); err == nil {
		t.Fatal("CaptureBaselines() error = nil")
	}
	if err := VerifyBaselines(nil); err == nil {
		t.Fatal("VerifyBaselines(nil) error = nil")
	}
}
