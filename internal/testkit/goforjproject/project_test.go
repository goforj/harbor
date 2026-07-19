package goforjproject

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const fakeForjModeEnvironment = "HARBOR_GOForjPROJECT_FAKE_MODE"

// TestMain turns this Go test binary into a deterministic fake forj process only for selected child invocations.
func TestMain(m *testing.M) {
	if os.Getenv(fakeForjModeEnvironment) == "render" {
		os.Exit(runFakeForjProcess())
	}
	os.Exit(m.Run())
}

// runFakeForjProcess verifies the exact CLI shape before producing the minimum validated render output.
func runFakeForjProcess() int {
	if len(os.Args) != 2 || os.Args[1] != "render" {
		_, _ = fmt.Fprintf(os.Stderr, "fake forj arguments = %#v\n", os.Args[1:])
		return 20
	}
	configuration, err := os.ReadFile(".goforj.yml")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 21
	}
	module, err := fakeModuleName(configuration)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 22
	}
	configuration = fakeAddUpdatedAt(configuration)
	if err := os.WriteFile(".goforj.yml", configuration, 0o600); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 23
	}
	if err := os.WriteFile("go.mod", []byte("module "+module+"\n\ngo 1.26.1\n"), 0o600); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 24
	}
	return 0
}

// fakeModuleName reads the explicit quoted module_name field used by renderConfiguration.
func fakeModuleName(configuration []byte) (string, error) {
	for _, line := range strings.Split(string(configuration), "\n") {
		value, found := strings.CutPrefix(line, "module_name: ")
		if !found {
			continue
		}
		module, err := strconv.Unquote(value)
		if err != nil {
			return "", err
		}
		return module, nil
	}
	return "", errors.New("fake forj configuration has no module_name")
}

// fakeAddUpdatedAt models the only source-file metadata mutation accepted from the real renderer.
func fakeAddUpdatedAt(configuration []byte) []byte {
	lines := strings.Split(string(configuration), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "module_name: ") {
			lines = append(lines[:index+1], append([]string{`updated_at: "test-render"`}, lines[index+1:]...)...)
			break
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// TestRenderCreatesThreeUniquePort3000Projects verifies the public API uses isolated roots and an exact fake process.
func TestRenderCreatesThreeUniquePort3000Projects(t *testing.T) {
	t.Setenv(fakeForjModeEnvironment, "render")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve fake forj executable: %v", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatalf("make fake forj executable absolute: %v", err)
	}
	request := Request{
		ForjExecutable: executable,
		GoForjVersion:  "0.19.0",
		Projects: []Spec{
			{Name: "Harbor Orders", Module: "example.test/harbor/orders", Port: 3000},
			{Name: "Harbor Billing", Module: "example.test/harbor/billing", Port: 3000},
			{Name: "Harbor Reports", Module: "example.test/harbor/reports", Port: 3000},
		},
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve test working directory: %v", err)
	}
	checkoutRoot, found, err := enclosingGoModule(workingDirectory)
	if err != nil || !found {
		t.Fatalf("resolve checkout root = %q, %t, %v", checkoutRoot, found, err)
	}
	checkoutModuleBefore, err := os.ReadFile(filepath.Join(checkoutRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read checkout go.mod before render: %v", err)
	}

	workspace, err := Render(t.Context(), request)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(workspace.Projects) != 3 {
		t.Fatalf("projects = %d", len(workspace.Projects))
	}
	if testPathContains(checkoutRoot, workspace.Root) {
		t.Fatalf("render workspace %q is inside checkout %q", workspace.Root, checkoutRoot)
	}

	roots := make(map[string]struct{}, len(workspace.Projects))
	for index, project := range workspace.Projects {
		want := request.Projects[index]
		if project.Name != want.Name || project.Module != want.Module || project.Port != 3000 {
			t.Fatalf("project %d = %#v, want %#v on port 3000", index, project, want)
		}
		if !filepath.IsAbs(project.Root) || !testPathContains(workspace.Root, project.Root) {
			t.Fatalf("project root = %q, workspace = %q", project.Root, workspace.Root)
		}
		if _, duplicated := roots[project.Root]; duplicated {
			t.Fatalf("duplicated project root %q", project.Root)
		}
		roots[project.Root] = struct{}{}
		environment, err := os.ReadFile(project.EnvironmentPath)
		if err != nil {
			t.Fatalf("read project %d environment: %v", index, err)
		}
		if !matchesProjectEnvironment(environment, projectEnvironment(want)) || !bytes.Contains(environment, []byte("API_HTTP_PORT=3000\n")) {
			t.Fatalf("project %d environment = %q", index, environment)
		}
		configuration, err := os.ReadFile(project.ConfigurationPath)
		if err != nil {
			t.Fatalf("read project %d configuration: %v", index, err)
		}
		if !matchesRenderConfiguration(configuration, renderConfiguration(want, request.GoForjVersion)) {
			t.Fatalf("project %d configuration = %q", index, configuration)
		}
	}

	checkoutModuleAfter, err := os.ReadFile(filepath.Join(checkoutRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read checkout go.mod after render: %v", err)
	}
	if !bytes.Equal(checkoutModuleBefore, checkoutModuleAfter) {
		t.Fatal("render changed the checkout go.mod")
	}
	root := workspace.Root
	if err := workspace.Close(); err != nil {
		t.Fatalf("close workspace: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close workspace twice: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after Close(): %v", err)
	}
}

// TestRenderConfigurationSelectsTheGeneratedAppRuntime prevents a successful render from producing an inert forj dev graph.
func TestRenderConfigurationSelectsTheGeneratedAppRuntime(t *testing.T) {
	configuration := string(renderConfiguration(
		Spec{Name: "Harbor Orders", Module: "example.test/harbor/orders", Port: 3000},
		"0.19.0",
	))
	if !strings.Contains(configuration, "dev:\n  apps:\n    app: true\n") {
		t.Fatalf("render configuration does not select the generated App runtime:\n%s", configuration)
	}
	if strings.Contains(configuration, "dev:\n  run:") {
		t.Fatalf("render configuration used a watcher allowlist without defining any watchers:\n%s", configuration)
	}
}

// TestMatchesProjectEnvironmentAllowsGeneratedSecretsButRejectsShadowing verifies renderer-owned additions cannot change launch inputs.
func TestMatchesProjectEnvironmentAllowsGeneratedSecretsButRejectsShadowing(t *testing.T) {
	expected := []byte("APP_NAME=Orders\nAPI_HTTP_PORT=3000\n")
	if !matchesProjectEnvironment(
		[]byte("APP_NAME=Orders\nAPI_HTTP_PORT=3000\n\nAPP_KEY=generated\nAPP_DIAG_TOKEN=generated\n"),
		expected,
	) {
		t.Fatal("matchesProjectEnvironment() rejected generated secret additions")
	}
	for _, contents := range [][]byte{
		[]byte("APP_NAME=Changed\nAPI_HTTP_PORT=3000\n"),
		[]byte("APP_NAME=Orders\nAPI_HTTP_PORT=3000\nAPI_HTTP_PORT=4000\n"),
		[]byte("APP_NAME=Orders\nAPI_HTTP_PORT=3000\nexport APP_NAME=Changed\n"),
		[]byte("APP_NAME=Orders\nAPI_HTTP_PORT=3000\napi_http_port=4000\n"),
	} {
		if matchesProjectEnvironment(contents, expected) {
			t.Fatalf("matchesProjectEnvironment(%q) accepted changed or shadowed input", contents)
		}
	}
}

// TestRenderFailureRemovesEveryPartialProject verifies one failed render cannot strand earlier generated output.
func TestRenderFailureRemovesEveryPartialProject(t *testing.T) {
	request := testRenderRequest(t)
	root, creator := testWorkspaceCreator(t)
	renderFailure := errors.New("deterministic render failure")
	calls := 0
	_, err := renderWith(t.Context(), request, creator, func(_ context.Context, _ string, projectRoot string) error {
		calls++
		if calls == 2 {
			return renderFailure
		}
		return writeFakeModuleOutput(projectRoot)
	})
	if !errors.Is(err, renderFailure) || calls != 2 {
		t.Fatalf("renderWith() error = %v, calls = %d", err, calls)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial workspace remains: %v", err)
	}
}

// TestRenderRejectsMissingOutput verifies a zero exit cannot substitute for required generated metadata.
func TestRenderRejectsMissingOutput(t *testing.T) {
	request := testRenderRequest(t)
	root, creator := testWorkspaceCreator(t)
	_, err := renderWith(t.Context(), request, creator, func(context.Context, string, string) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "generated go.mod") {
		t.Fatalf("renderWith() error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output-less workspace remains: %v", err)
	}
}

// TestRenderHonorsCancellationBeforeAndDuringRender verifies abandoned work never advances to another project.
func TestRenderHonorsCancellationBeforeAndDuringRender(t *testing.T) {
	t.Run("before workspace", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		creatorCalls := 0
		invokeCalls := 0
		_, err := renderWith(ctx, testRenderRequest(t), func() (string, error) {
			creatorCalls++
			return "", errors.New("unexpected workspace creation")
		}, func(context.Context, string, string) error {
			invokeCalls++
			return nil
		})
		if !errors.Is(err, context.Canceled) || creatorCalls != 0 || invokeCalls != 0 {
			t.Fatalf("renderWith() = %v, creator calls = %d, invoke calls = %d", err, creatorCalls, invokeCalls)
		}
	})

	t.Run("during first render", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		root, creator := testWorkspaceCreator(t)
		invokeCalls := 0
		_, err := renderWith(ctx, testRenderRequest(t), creator, func(context.Context, string, string) error {
			invokeCalls++
			cancel()
			return context.Canceled
		})
		if !errors.Is(err, context.Canceled) || invokeCalls != 1 {
			t.Fatalf("renderWith() = %v, invoke calls = %d", err, invokeCalls)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled workspace remains: %v", err)
		}
	})
}

// TestRenderRefusesCheckoutWorkspace verifies even an injected creator cannot point rendering at Harbor's module.
func TestRenderRefusesCheckoutWorkspace(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	checkoutRoot, found, err := enclosingGoModule(workingDirectory)
	if err != nil || !found {
		t.Fatalf("resolve checkout root = %q, %t, %v", checkoutRoot, found, err)
	}
	moduleBefore, err := os.ReadFile(filepath.Join(checkoutRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read checkout module: %v", err)
	}
	invocations := 0
	_, err = renderWith(t.Context(), testRenderRequest(t), func() (string, error) {
		return checkoutRoot, nil
	}, func(context.Context, string, string) error {
		invocations++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "refuse GoForj render beneath Go module") || invocations != 0 {
		t.Fatalf("renderWith() error = %v, invocations = %d", err, invocations)
	}
	moduleAfter, err := os.ReadFile(filepath.Join(checkoutRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read checkout module after rejection: %v", err)
	}
	if !bytes.Equal(moduleBefore, moduleAfter) {
		t.Fatal("checkout module changed during rejected render")
	}
}

// testRenderRequest returns two valid projects and this test binary as an inspected executable.
func testRenderRequest(t *testing.T) Request {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatalf("make test executable absolute: %v", err)
	}
	return Request{
		ForjExecutable: executable,
		GoForjVersion:  "0.19.0",
		Projects: []Spec{
			{Name: "Test App One", Module: "example.test/test/app-one", Port: 3000},
			{Name: "Test App Two", Module: "example.test/test/app-two", Port: 3000},
		},
	}
}

// testWorkspaceCreator returns one known isolated directory whose cleanup can be asserted after failure.
func testWorkspaceCreator(t *testing.T) (string, workspaceCreator) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	return root, func() (string, error) {
		if err := os.Mkdir(root, 0o700); err != nil {
			return "", err
		}
		return root, nil
	}
}

// writeFakeModuleOutput derives the exact expected module from the generated source configuration.
func writeFakeModuleOutput(projectRoot string) error {
	configuration, err := os.ReadFile(filepath.Join(projectRoot, ".goforj.yml"))
	if err != nil {
		return err
	}
	module, err := fakeModuleName(configuration)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectRoot, "go.mod"), []byte("module "+module+"\n"), 0o600)
}

// testPathContains reports path containment without treating textual sibling prefixes as descendants.
func testPathContains(parent string, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
