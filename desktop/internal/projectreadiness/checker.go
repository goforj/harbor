// Package projectreadiness reports provider-selected local tools without installing software or running project hooks.
package projectreadiness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/projectcreation"
	"github.com/goforj/harbor/internal/projectprocess"
)

// Check describes one bounded, secret-free prerequisite observation.
type Check struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
	Detail      string `json:"detail"`
	Action      string `json:"action,omitempty"`
	URL         string `json:"url,omitempty"`
	RequiredFor string `json:"required_for,omitempty"`
}

// Report is shared by creator adapters and registered-project inspection.
type Report struct {
	ProviderID string  `json:"provider_id"`
	ProjectID  string  `json:"project_id,omitempty"`
	Checks     []Check `json:"checks"`
}

// Ready reports whether every required observation succeeded.
func (report Report) Ready() bool {
	for _, check := range report.Checks {
		if check.Status != "ready" {
			return false
		}
	}
	return len(report.Checks) > 0
}

// Checker keeps native process and file observations replaceable for focused platform tests.
type Checker struct {
	lookPath  func(string) (string, error)
	run       func(context.Context, string, []string) ([]byte, error)
	readFile  func(string, string) ([]byte, error)
	goos      string
	verifyCLI func(string) error
	schema    func(context.Context) (projectcreation.Schema, error)
	observe   func(context.Context, goforj.Query) (goforj.Observation, error)
}

// New creates a checker with bounded native Go process execution and checkout-local metadata reads.
func New() *Checker {
	return &Checker{lookPath: exec.LookPath, run: runQuery, readFile: readProjectFile, goos: runtime.GOOS,
		verifyCLI: projectprocess.VerifyGoForjExecutable, observe: goforj.Observe,
		schema: func(ctx context.Context) (projectcreation.Schema, error) {
			creator, err := projectcreation.New()
			if err != nil {
				return projectcreation.Schema{}, err
			}
			return creator.Schema(ctx)
		},
	}
}

// requirement is compiled-in provider policy, never a command supplied by a frontend or schema.
type requirement struct {
	id, name, executable, action, url string
	args                              []string
	modernNode                        bool
}

// numericVersion keeps raw diagnostics and unbounded version-like output off the wire.
var numericVersion = regexp.MustCompile(`(?:^|[^0-9.])v?([0-9]{1,5}\.[0-9]{1,5}(?:\.[0-9]{1,5})?)(?:$|[^0-9.])`)

// inspect runs independent read-only observations concurrently while retaining a stable display order.
func (checker *Checker) inspect(ctx context.Context, requirements []requirement) []Check {
	checks := make([]Check, len(requirements))
	var pending sync.WaitGroup
	for index, required := range requirements {
		pending.Add(1)
		go func() {
			defer pending.Done()
			checks[index] = checker.inspectTool(ctx, required)
		}()
	}
	pending.Wait()
	return checks
}

// inspectTool reveals only parsed versions and reviewed guidance, never arbitrary command diagnostics.
func (checker *Checker) inspectTool(ctx context.Context, required requirement) Check {
	check := Check{ID: required.id, Name: required.name, Status: "ready", Action: required.action, URL: required.url}
	path, err := checker.lookPath(required.executable)
	if err != nil {
		check.Status, check.Detail = "missing", required.name+" was not found in Harbor's PATH."
		if !errors.Is(err, exec.ErrNotFound) {
			check.Status, check.Detail = "unavailable", required.name+" could not be resolved safely from Harbor's PATH."
		}
		return check
	}
	args := required.args
	if required.id == "npm" && checker.goos == "windows" {
		// Windows npm launchers are batch files. Invoke its JS entrypoint with Node instead of introducing a shell.
		entrypoint := filepath.Join(filepath.Dir(path), "node_modules", "npm", "bin", "npm-cli.js")
		path, err = checker.lookPath("node")
		if err != nil {
			check.Status, check.Detail = "unavailable", "npm needs an available Node.js installation."
			return check
		}
		args = []string{entrypoint, "--version"}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := checker.run(ctx, path, args)
	if err != nil {
		check.Status = "unavailable"
		check.Detail = required.name + " is installed but did not respond successfully."
		if required.id == "docker-engine" {
			check.Detail = "Docker is installed, but Harbor could not reach the selected engine. Start Docker and check its active context."
		}
		return check
	}
	version := numericVersion.FindSubmatch(output)
	if len(version) != 2 {
		check.Status, check.Detail = "unavailable", required.name+" returned an unrecognized version."
		return check
	}
	check.Version = string(version[1])
	check.Detail = required.name + " is available."
	parts := strings.Split(check.Version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if required.id == "compose" && major < 2 {
		check.Status, check.Detail = "incompatible", "This project needs Docker Compose v2 or newer."
	}
	if required.id == "node" && required.modernNode && !((major == 20 && minor >= 19) || (major == 22 && minor >= 12) || major > 22) {
		check.Status, check.Detail = "incompatible", "This frontend uses Vite 7 or newer, which needs Node.js 20.19 or 22.12 or newer (Node.js 21 is unsupported)."
	}
	return check
}

// requirements selects only the tools needed by the provider's observed project shape.
func requirements(frontend, containers, modernNode bool) []requirement {
	checks := []requirement{{id: "go", name: "Go", executable: "go", args: []string{"version"}, action: "Install Go, then reopen Harbor if PATH changed.", url: "https://go.dev/doc/install"}}
	if frontend {
		checks = append(checks,
			requirement{id: "node", name: "Node.js", executable: "node", args: []string{"--version"}, modernNode: modernNode, action: "Install a supported Node.js LTS version, then recheck.", url: "https://nodejs.org/en/download"},
			requirement{id: "npm", name: "npm", executable: "npm", args: []string{"--version"}, action: "Install or repair npm with your Node.js installation, then recheck.", url: "https://docs.npmjs.com/downloading-and-installing-node-js-and-npm"})
	}
	if containers {
		checks = append(checks,
			requirement{id: "docker", name: "Docker CLI", executable: "docker", args: []string{"--version"}, action: "Install Docker with the Compose plugin, then recheck.", url: "https://docs.docker.com/get-started/get-docker/"},
			requirement{id: "compose", name: "Docker Compose", executable: "docker", args: []string{"compose", "version", "--short"}, action: "Install or update the Docker Compose v2 plugin, then recheck.", url: "https://docs.docker.com/compose/install/"},
			requirement{id: "docker-engine", name: "Docker engine", executable: "docker", args: []string{"info", "--format", "{{.ServerVersion}}"}, action: "Start your Docker engine and select the intended Docker context, then recheck.", url: "https://docs.docker.com/engine/manage-resources/contexts/"})
	}
	return checks
}

// queryOutput drains verbose child output while bounding retained metadata.
type queryOutput struct{ bytes.Buffer }

// Write keeps version commands from exhausting memory or blocking their output pipes.
func (output *queryOutput) Write(data []byte) (int, error) {
	count := len(data)
	if remaining := 16*1024 - output.Len(); remaining > 0 {
		_, _ = output.Buffer.Write(data[:min(len(data), remaining)])
	}
	return count, nil
}

// runQuery uses fixed argv and a neutral working directory without evaluating project scripts or shell launchers.
func runQuery(ctx context.Context, path string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = os.TempDir()
	command.WaitDelay = 250 * time.Millisecond
	command.Env = append(command.Environ(), "NO_COLOR=1", "GOWORK=off", "GOTOOLCHAIN=local")
	output := &queryOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	err := command.Run()
	return output.Bytes(), err
}

// readProjectFile accepts only bounded regular metadata files that resolve inside the registered checkout.
func readProjectFile(root, relative string) ([]byte, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(canonicalRoot, relative))
	if err != nil {
		return nil, err
	}
	inside, err := filepath.Rel(canonicalRoot, path)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("project metadata is outside the registered checkout")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > 1024*1024 {
		return nil, fmt.Errorf("project metadata must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, fmt.Errorf("project metadata must be a regular file no larger than 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("project metadata exceeds 1 MiB")
	}
	return data, err
}
