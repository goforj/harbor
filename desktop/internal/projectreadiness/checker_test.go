package projectreadiness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/projectcreation"
)

// testChecker replaces effects while retaining the production provider selection and version policy.
func testChecker(t *testing.T) *Checker {
	t.Helper()
	root := t.TempDir()
	cli := filepath.Join(root, "forj")
	if err := os.WriteFile(cli, nil, 0600); err != nil {
		t.Fatal(err)
	}
	checker := New()
	checker.goos = "linux"
	checker.lookPath = func(name string) (string, error) { return filepath.Join(root, name), nil }
	checker.verifyCLI = func(string) error { return nil }
	checker.schema = func(context.Context) (projectcreation.Schema, error) {
		return projectcreation.Schema{Version: "v0.35.1"}, nil
	}
	checker.observe = func(context.Context, goforj.Query) (goforj.Observation, error) {
		return goforj.Observation{ServiceRequirementsSupported: true}, nil
	}
	checker.run = func(_ context.Context, path string, args []string) ([]byte, error) {
		switch filepath.Base(path) {
		case "go":
			return []byte("go version go1.27.0 linux/amd64"), nil
		case "node":
			return []byte("v22.12.0"), nil
		case "npm":
			return []byte("10.9.0"), nil
		case "docker":
			if args[0] == "compose" {
				return []byte("v2.40.0"), nil
			}
			return []byte("28.4.0"), nil
		}
		return nil, errors.New("unexpected executable")
	}
	return checker
}

// checkByID requires the provider to expose an expected requirement rather than silently omitting it.
func checkByID(t *testing.T, report Report, id string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("missing check %q: %+v", id, report)
	return Check{}
}

// TestGoForjCreationSelectsOnlyRequiredTools proves optional frontends and containers do not burden a plain Go project.
func TestGoForjCreationSelectsOnlyRequiredTools(t *testing.T) {
	checker := testChecker(t)
	minimal := checker.GoForjCreation(context.Background(), map[string]string{"components": "cli", "starter-kit": "none"})
	if !minimal.Ready() || len(minimal.Checks) != 2 {
		t.Fatalf("minimal = %+v", minimal)
	}
	for _, starter := range []string{"vue", "react", "templ_htmx"} {
		report := checker.GoForjCreation(context.Background(), map[string]string{"components": "cli,web_ui,docker", "starter-kit": starter})
		if !report.Ready() || len(report.Checks) != 7 {
			t.Fatalf("%s = %+v", starter, report)
		}
		if checkByID(t, report, "node").RequiredFor != "start" || checkByID(t, report, "forj").RequiredFor != "creation" {
			t.Fatal("creation/runtime requirements were mixed")
		}
	}
	demo := checker.GoForjCreation(context.Background(), map[string]string{"demo": "true"})
	if len(demo.Checks) != 7 {
		t.Fatalf("demo = %+v", demo)
	}
}

// TestGoForjCreationReportsMissingIdentitySchemaAndTimeout distinguishes actionable CLI failure categories.
func TestGoForjCreationReportsMissingIdentitySchemaAndTimeout(t *testing.T) {
	for _, mode := range []string{"missing", "unsafe path", "identity", "schema", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			checker := testChecker(t)
			ctx := context.Background()
			want := "incompatible"
			switch mode {
			case "missing":
				checker.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
				want = "missing"
			case "identity":
				checker.verifyCLI = func(string) error { return errors.New("private details") }
			case "unsafe path":
				checker.lookPath = func(string) (string, error) { return "", exec.ErrDot }
				want = "unavailable"
			case "schema":
				checker.schema = func(context.Context) (projectcreation.Schema, error) {
					return projectcreation.Schema{}, errors.New("private details")
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "unavailable"
				checker.schema = func(ctx context.Context) (projectcreation.Schema, error) { return projectcreation.Schema{}, ctx.Err() }
			}
			report := checker.GoForjCreation(ctx, nil)
			check := checkByID(t, report, "forj")
			if report.Ready() || check.Status != want || strings.Contains(check.Detail, "private") {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// TestToolVersionAndAvailabilityPolicy covers supported Node versions, old Compose, failed engines, and malformed tool output.
func TestToolVersionAndAvailabilityPolicy(t *testing.T) {
	for _, test := range []struct {
		id, output, want          string
		runError, missing, unsafe bool
	}{
		{id: "node", output: "v18.20.0", want: "incompatible"},
		{id: "node", output: "v20.18.9", want: "incompatible"},
		{id: "node", output: "v20.19.0", want: "ready"},
		{id: "node", output: "v21.7.0", want: "incompatible"},
		{id: "node", output: "v22.11.0", want: "incompatible"},
		{id: "node", output: "v22.12.0", want: "ready"},
		{id: "node", output: "v24.0.0", want: "ready"},
		{id: "compose", output: "1.29.2", want: "incompatible"},
		{id: "docker-engine", want: "unavailable", runError: true},
		{id: "npm", output: "not a version secret-value", want: "unavailable"},
		{id: "npm", output: "123456789.12.0", want: "unavailable"},
		{id: "go", want: "missing", missing: true},
		{id: "go", want: "unavailable", unsafe: true},
	} {
		t.Run(test.id+test.output+test.want, func(t *testing.T) {
			checker := testChecker(t)
			if test.missing {
				checker.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			}
			if test.unsafe {
				checker.lookPath = func(string) (string, error) { return "", exec.ErrDot }
			}
			checker.run = func(context.Context, string, []string) ([]byte, error) {
				if test.runError {
					return nil, errors.New("secret-value")
				}
				return []byte(test.output), nil
			}
			check := checker.inspectTool(context.Background(), requirement{id: test.id, name: test.id, executable: test.id, modernNode: true})
			if check.Status != test.want || strings.Contains(check.Detail, "secret-value") {
				t.Fatalf("check = %+v", check)
			}
		})
	}
}

// TestWindowsNPMUsesNodeWithoutAShell proves Windows does not attempt to execute npm.cmd through a shell.
func TestWindowsNPMUsesNodeWithoutAShell(t *testing.T) {
	checker := testChecker(t)
	checker.goos = "windows"
	checker.lookPath = func(name string) (string, error) { return filepath.Join("tools", name+".exe"), nil }
	checker.run = func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != filepath.Join("tools", "node.exe") || !reflect.DeepEqual(args, []string{filepath.Join("tools", "node_modules", "npm", "bin", "npm-cli.js"), "--version"}) {
			t.Fatalf("command = %s %v", path, args)
		}
		return []byte("10.9.0"), nil
	}
	check := checker.inspectTool(context.Background(), requirement{id: "npm", name: "npm", executable: "npm", args: []string{"--version"}})
	if check.Status != "ready" {
		t.Fatal(check)
	}
	checker.lookPath = func(name string) (string, error) {
		if name == "node" {
			return "", exec.ErrNotFound
		}
		return "npm.cmd", nil
	}
	if checker.inspectTool(context.Background(), requirement{id: "npm", executable: "npm"}).Status != "unavailable" {
		t.Fatal("npm without Node was accepted")
	}
}

// TestProjectReadinessUsesDescriptorAndMetadata exercises the exact registered project with no configuration parser or hook execution.
func TestProjectReadinessUsesDescriptorAndMetadata(t *testing.T) {
	checker := testChecker(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "frontend"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "frontend", "package.json"), []byte(`{"scripts":{"build":"do-not-execute"},"devDependencies":{"vite":"^8.1.5"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\ngo 1.27.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checker.observe = func(_ context.Context, query goforj.Query) (goforj.Observation, error) {
		if query.Checkout != root {
			t.Fatalf("checkout = %q", query.Checkout)
		}
		return goforj.Observation{ServiceRequirementsSupported: true, ServiceRequirements: []goforj.ServiceRequirement{{Owner: goforj.ServiceRequirementOwnerCompose}}}, nil
	}
	report := checker.GoForjProject(context.Background(), domain.ProjectSnapshot{ID: "test", Path: root})
	if !report.Ready() || report.ProjectID != "test" || len(report.Checks) != 7 {
		t.Fatalf("report = %+v", report)
	}
	checker.observe = func(context.Context, goforj.Query) (goforj.Observation, error) {
		return goforj.Observation{}, errors.New("secret project configuration")
	}
	report = checker.GoForjProject(context.Background(), domain.ProjectSnapshot{ID: "test", Path: root})
	if report.Ready() || strings.Contains(checkByID(t, report, "forj").Detail, "secret") {
		t.Fatal(report)
	}
}

// TestFrontendMetadataErrorsAndGoToolchainPolicy keeps unreadable metadata visible while preserving native toolchain selection.
func TestFrontendMetadataErrorsAndGoToolchainPolicy(t *testing.T) {
	checker := testChecker(t)
	for _, value := range []string{"invalid", `{"scripts":{}}`, `{"scripts":{"dev":"vite"},"dependencies":{"vite":"^6.0.0"}}`} {
		checker.readFile = func(_ string, name string) ([]byte, error) {
			if name == "package.json" {
				return nil, os.ErrNotExist
			}
			return []byte(value), nil
		}
		frontend, modern, checks := checker.frontend("project")
		if value == "invalid" && len(checks) != 1 {
			t.Fatal("invalid package metadata was hidden")
		}
		if modern || (value == `{"scripts":{}}` && frontend) {
			t.Fatal("incorrect frontend requirements")
		}
	}
	checker.readFile = func(string, string) ([]byte, error) { return nil, errors.New("permission denied") }
	if _, _, checks := checker.frontend("project"); len(checks) != 1 {
		t.Fatal("unreadable frontend was hidden")
	}
	for _, mode := range []string{"auto", "local"} {
		t.Setenv("GOTOOLCHAIN", mode)
		checker.readFile = func(string, string) ([]byte, error) { return []byte("module example.com/app\ngo 1.28.0\n"), nil }
		report := Report{Checks: []Check{{ID: "go", Status: "ready", Version: "1.27.0"}}}
		checker.projectGoVersion("project", &report)
		if (report.Checks[0].Status == "incompatible") != (mode == "local") {
			t.Fatalf("%s = %+v", mode, report)
		}
	}
	checker.readFile = func(string, string) ([]byte, error) { return []byte("go 1." + strings.Repeat("9", 100)), nil }
	report := Report{Checks: []Check{{ID: "go", Status: "ready", Version: "1.27.0"}}}
	checker.projectGoVersion("project", &report)
	if checkByID(t, report, "go-module").Status != "unavailable" {
		t.Fatal("unbounded module version was accepted")
	}
}

// TestReadProjectFileBoundsAndScope rejects directories, oversized metadata, and links outside the checkout.
func TestReadProjectFileBoundsAndScope(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(path, []byte("module example.com/app"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectFile(root, "go.mod"); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectFile(root, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := readProjectFile(root, "."); err == nil {
		t.Fatal("directory accepted")
	}
	if err := os.WriteFile(path, make([]byte, 1024*1024+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectFile(root, "go.mod"); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	t.Run("outside symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "go.mod")
		if err := os.WriteFile(outside, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := readProjectFile(root, "linked"); err == nil {
			t.Fatal("outside metadata link accepted")
		}
	})
	output := &queryOutput{}
	data := make([]byte, 32*1024)
	if count, err := output.Write(data); err != nil || count != len(data) || output.Len() != 16*1024 {
		t.Fatal("query output was not bounded and drained")
	}
	if _, err := runQuery(context.Background(), filepath.Join(root, "missing-executable"), nil); err == nil {
		t.Fatal("missing process succeeded")
	}
}
