package goforjproject

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	minimumProjectCount  = 2
	maximumProjectCount  = 3
	maximumRenderOutput  = 64 << 10
	maximumGeneratedFile = 64 << 10
	renderWaitDelay      = time.Second
)

// Request describes one isolated multi-project render operation.
type Request struct {
	// ForjExecutable is the absolute direct path to the caller-selected forj binary.
	ForjExecutable string
	// GoForjVersion is written into every generated project's render contract.
	GoForjVersion string
	// Projects contains exactly two or three independently named applications.
	Projects []Spec
}

// Spec describes one minimal generated application.
type Spec struct {
	// Name is the human-readable application name written to GoForj and dotenv configuration.
	Name string
	// Module is the unique Go module path expected from the renderer.
	Module string
	// Port is the application HTTP port advertised to Harbor project discovery.
	Port uint16
}

// Project records validated paths and identity for one rendered application.
type Project struct {
	// Name is the configured application name.
	Name string
	// Module is the validated generated module path.
	Module string
	// Port is the configured application HTTP port.
	Port uint16
	// Root is the isolated absolute project directory.
	Root string
	// ConfigurationPath is the direct .goforj.yml source used by the renderer.
	ConfigurationPath string
	// EnvironmentPath is the direct .env file consumed by Harbor discovery and forj dev.
	EnvironmentPath string
	// ModulePath is the direct go.mod output validated after rendering.
	ModulePath string
}

// Workspace owns one disposable render root and its validated projects.
type Workspace struct {
	// Root is the isolated absolute directory containing every project.
	Root string
	// Projects contains validated metadata in Request order.
	Projects []Project

	cleanupRoot string
	closeOnce   sync.Once
	closeErr    error
}

// workspaceCreator isolates temporary-directory creation for deterministic failure tests.
type workspaceCreator func() (string, error)

// renderInvoker isolates process execution while keeping the production executable caller-selected.
type renderInvoker func(context.Context, string, string) error

// boundedOutput prevents a failed renderer from retaining unbounded diagnostics.
type boundedOutput struct {
	body bytes.Buffer
}

// Render creates, renders, and validates exactly two or three disposable projects.
func Render(ctx context.Context, request Request) (*Workspace, error) {
	return renderWith(ctx, request, createTemporaryWorkspace, invokeForj)
}

// Close removes the complete disposable workspace without following generated symlinks.
func (workspace *Workspace) Close() error {
	if workspace == nil {
		return nil
	}
	workspace.closeOnce.Do(func() {
		workspace.closeErr = os.RemoveAll(workspace.cleanupRoot)
	})
	return workspace.closeErr
}

// renderWith keeps workspace ownership and cleanup identical for native and deterministic process seams.
func renderWith(
	ctx context.Context,
	request Request,
	createWorkspace workspaceCreator,
	invoke renderInvoker,
) (*Workspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validated, err := validateRequest(request)
	if err != nil {
		return nil, err
	}
	if createWorkspace == nil {
		panic("goforjproject requires a workspace creator")
	}
	if invoke == nil {
		panic("goforjproject requires a render invoker")
	}

	root, err := createWorkspace()
	if err != nil {
		return nil, fmt.Errorf("create GoForj render workspace: %w", err)
	}
	if err := validateWorkspaceRoot(root); err != nil {
		// A rejected seam result is not known to be owned by this call and must never be removed.
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(root)
		}
	}()

	projects := make([]Project, 0, len(validated.Projects))
	for index, specification := range validated.Projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		projectRoot := filepath.Join(root, fmt.Sprintf("project-%02d", index+1))
		if err := os.Mkdir(projectRoot, 0o700); err != nil {
			return nil, fmt.Errorf("create project %d root: %w", index+1, err)
		}

		configurationPath := filepath.Join(projectRoot, ".goforj.yml")
		if err := writeExclusiveFile(configurationPath, renderConfiguration(specification, validated.GoForjVersion)); err != nil {
			return nil, fmt.Errorf("write project %d GoForj configuration: %w", index+1, err)
		}
		environmentPath := filepath.Join(projectRoot, ".env")
		if err := writeExclusiveFile(environmentPath, projectEnvironment(specification)); err != nil {
			return nil, fmt.Errorf("write project %d environment: %w", index+1, err)
		}
		if err := invoke(ctx, validated.ForjExecutable, projectRoot); err != nil {
			return nil, fmt.Errorf("render project %d: %w", index+1, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		project := Project{
			Name:              specification.Name,
			Module:            specification.Module,
			Port:              specification.Port,
			Root:              projectRoot,
			ConfigurationPath: configurationPath,
			EnvironmentPath:   environmentPath,
			ModulePath:        filepath.Join(projectRoot, "go.mod"),
		}
		if err := validateRenderedProject(project, renderConfiguration(specification, validated.GoForjVersion), projectEnvironment(specification)); err != nil {
			return nil, fmt.Errorf("validate project %d render: %w", index+1, err)
		}
		projects = append(projects, project)
	}

	succeeded = true
	return &Workspace{
		Root:        root,
		Projects:    projects,
		cleanupRoot: root,
	}, nil
}

// validateRequest rejects ambiguous paths and identities before creating any temporary state.
func validateRequest(request Request) (Request, error) {
	if request.ForjExecutable == "" || !filepath.IsAbs(request.ForjExecutable) || filepath.Clean(request.ForjExecutable) != request.ForjExecutable {
		return Request{}, fmt.Errorf("forj executable %q must be an absolute clean path", request.ForjExecutable)
	}
	information, err := os.Lstat(request.ForjExecutable)
	if err != nil {
		return Request{}, fmt.Errorf("inspect forj executable: %w", err)
	}
	if !information.Mode().IsRegular() {
		return Request{}, errors.New("forj executable must be a direct regular file")
	}
	if runtime.GOOS != "windows" && information.Mode().Perm()&0o111 == 0 {
		return Request{}, errors.New("forj executable is not executable")
	}
	if !validGoForjVersion(request.GoForjVersion) {
		return Request{}, fmt.Errorf("GoForj version %q must use numeric major.minor.patch form", request.GoForjVersion)
	}
	if len(request.Projects) < minimumProjectCount || len(request.Projects) > maximumProjectCount {
		return Request{}, fmt.Errorf("project count is %d, want two or three", len(request.Projects))
	}

	validated := request
	validated.Projects = append([]Spec(nil), request.Projects...)
	names := make(map[string]struct{}, len(validated.Projects))
	modules := make(map[string]struct{}, len(validated.Projects))
	for index, specification := range validated.Projects {
		if err := validateProjectName(specification.Name); err != nil {
			return Request{}, fmt.Errorf("project %d name: %w", index+1, err)
		}
		if err := validateModulePath(specification.Module); err != nil {
			return Request{}, fmt.Errorf("project %d module: %w", index+1, err)
		}
		if specification.Port == 0 {
			return Request{}, fmt.Errorf("project %d port must be non-zero", index+1)
		}
		nameKey := strings.ToLower(specification.Name)
		if _, found := names[nameKey]; found {
			return Request{}, fmt.Errorf("project name %q is duplicated", specification.Name)
		}
		names[nameKey] = struct{}{}
		moduleKey := strings.ToLower(specification.Module)
		if _, found := modules[moduleKey]; found {
			return Request{}, fmt.Errorf("project module %q is duplicated", specification.Module)
		}
		modules[moduleKey] = struct{}{}
	}
	return validated, nil
}

// validGoForjVersion keeps the source contract explicit without accepting tags or moving channels.
func validGoForjVersion(version string) bool {
	if version == "" || version != strings.TrimSpace(version) {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

// validateProjectName prevents control characters from crossing YAML, dotenv, or diagnostics boundaries.
func validateProjectName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 128 {
		return errors.New("must be non-empty, trimmed, and at most 128 bytes")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return errors.New("contains a control character")
		}
	}
	return nil
}

// validateModulePath admits a conservative portable subset of Go module paths.
func validateModulePath(module string) error {
	if module == "" || module != strings.TrimSpace(module) || module != path.Clean(module) || strings.HasPrefix(module, "/") {
		return errors.New("must be a non-empty clean relative module path")
	}
	for _, character := range module {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune("./-_~", character):
		default:
			return fmt.Errorf("contains unsupported character %q", character)
		}
	}
	return nil
}

// createTemporaryWorkspace refuses temp roots nested beneath any Go module before creating output.
func createTemporaryWorkspace() (string, error) {
	base, err := filepath.EvalSymlinks(filepath.Clean(os.TempDir()))
	if err != nil {
		return "", fmt.Errorf("resolve operating-system temp directory: %w", err)
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("operating-system temp directory %q is not absolute", base)
	}
	if moduleRoot, found, err := enclosingGoModule(base); err != nil {
		return "", err
	} else if found {
		return "", fmt.Errorf("refuse GoForj render beneath Go module %q", moduleRoot)
	}
	return os.MkdirTemp(base, "harbor-goforj-projects-")
}

// validateWorkspaceRoot proves an injected creator returned one isolated direct directory outside every Go module.
func validateWorkspaceRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("render workspace %q must be an absolute clean path", root)
	}
	information, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect render workspace: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return errors.New("render workspace must be a direct directory")
	}
	if moduleRoot, found, err := enclosingGoModule(root); err != nil {
		return err
	} else if found {
		return fmt.Errorf("refuse GoForj render beneath Go module %q", moduleRoot)
	}
	return nil
}

// enclosingGoModule walks only ancestors so an existing checkout can never become a render workspace.
func enclosingGoModule(location string) (string, bool, error) {
	current := filepath.Clean(location)
	for {
		marker := filepath.Join(current, "go.mod")
		information, err := os.Lstat(marker)
		switch {
		case err == nil && information.Mode().IsRegular():
			return current, true, nil
		case err == nil:
			return "", false, fmt.Errorf("Go module marker %q is not a direct regular file", marker)
		case !errors.Is(err, os.ErrNotExist):
			return "", false, fmt.Errorf("inspect Go module marker %q: %w", marker, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, nil
		}
		current = parent
	}
}

// renderConfiguration emits a complete non-interactive generator and forj-dev contract.
func renderConfiguration(specification Spec, version string) []byte {
	return []byte(fmt.Sprintf(
		"project_name: %s\nmodule_name: %s\ndev:\n  run:\n    app: run\nrender:\n  components:\n    - cli\n    - web_api\n  help_format: framework\n  goforj_version: %s\n",
		strconv.Quote(specification.Name),
		strconv.Quote(specification.Module),
		strconv.Quote(version),
	))
}

// projectEnvironment pins discovery to one port while leaving the loopback address for Harbor to inject.
func projectEnvironment(specification Spec) []byte {
	return []byte(fmt.Sprintf(
		"APP_NAME=%s\nAPP_ENV=testing\nAPP_DEBUG=0\nAPI_HTTP_PORT=%d\n",
		strconv.Quote(specification.Name),
		specification.Port,
	))
}

// writeExclusiveFile prevents an unexpected renderer artifact from being adopted as test input.
func writeExclusiveFile(filename string, contents []byte) (writeErr error) {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		writeErr = errors.Join(writeErr, file.Close())
	}()
	written, err := file.Write(contents)
	if err != nil {
		return err
	}
	if written != len(contents) {
		return io.ErrShortWrite
	}
	return nil
}

// invokeForj runs exactly one render command in the isolated project root without a shell or PATH lookup.
func invokeForj(ctx context.Context, executable string, projectRoot string) error {
	stdout := &boundedOutput{}
	stderr := &boundedOutput{}
	command := exec.CommandContext(ctx, executable, "render")
	command.Dir = projectRoot
	command.Env = os.Environ()
	command.Stdin = strings.NewReader("")
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = renderWaitDelay
	err := command.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf(
			"forj render failed: %w (stdout: %q, stderr: %q)",
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
	return nil
}

// validateRenderedProject rejects missing, redirected, mutated, or mismatched render artifacts.
func validateRenderedProject(project Project, configuration []byte, environment []byte) error {
	configurationBody, err := readDirectBoundedFile(project.ConfigurationPath)
	if err != nil {
		return fmt.Errorf("GoForj configuration: %w", err)
	}
	if !matchesRenderConfiguration(configurationBody, configuration) {
		return errors.New("renderer changed the explicit GoForj configuration beyond updated_at metadata")
	}
	environmentBody, err := readDirectBoundedFile(project.EnvironmentPath)
	if err != nil {
		return fmt.Errorf("project environment: %w", err)
	}
	if !matchesProjectEnvironment(environmentBody, environment) {
		return errors.New("renderer changed or shadowed the explicit project environment")
	}
	moduleBody, err := readDirectBoundedFile(project.ModulePath)
	if err != nil {
		return fmt.Errorf("generated go.mod: %w", err)
	}
	module, err := decodeModuleDirective(moduleBody)
	if err != nil {
		return err
	}
	if module != project.Module {
		return fmt.Errorf("generated module is %q, want %q", module, project.Module)
	}
	return nil
}

// matchesProjectEnvironment permits generated secrets while preventing later assignments from shadowing explicit test inputs.
func matchesProjectEnvironment(contents []byte, expected []byte) bool {
	if !bytes.HasPrefix(contents, expected) {
		return false
	}
	explicit := make(map[string]struct{})
	for _, line := range strings.Split(string(expected), "\n") {
		key, _, found := strings.Cut(line, "=")
		if found {
			explicit[strings.ToUpper(key)] = struct{}{}
		}
	}
	for _, line := range strings.Split(string(contents[len(expected):]), "\n") {
		candidate := strings.TrimSpace(line)
		if strings.HasPrefix(candidate, "export ") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "export "))
		}
		key, _, found := strings.Cut(candidate, "=")
		if !found {
			continue
		}
		if _, shadows := explicit[strings.ToUpper(strings.TrimSpace(key))]; shadows {
			return false
		}
	}
	return true
}

// matchesRenderConfiguration permits only GoForj's generated top-level updated_at metadata.
func matchesRenderConfiguration(contents []byte, expected []byte) bool {
	lines := strings.Split(string(contents), "\n")
	normalized := make([]string, 0, len(lines))
	metadataFound := false
	for _, line := range lines {
		if strings.HasPrefix(line, "updated_at:") {
			if metadataFound || strings.TrimSpace(strings.TrimPrefix(line, "updated_at:")) == "" {
				return false
			}
			metadataFound = true
			continue
		}
		normalized = append(normalized, line)
	}
	return bytes.Equal([]byte(strings.Join(normalized, "\n")), expected)
}

// readDirectBoundedFile accepts only one small direct regular output artifact.
func readDirectBoundedFile(filename string) ([]byte, error) {
	information, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !information.Mode().IsRegular() {
		return nil, errors.New("artifact is not a direct regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximumGeneratedFile+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maximumGeneratedFile {
		return nil, errors.New("artifact exceeds the testkit size bound")
	}
	return body, nil
}

// decodeModuleDirective requires exactly one canonical module directive from generated go.mod output.
func decodeModuleDirective(contents []byte) (string, error) {
	var module string
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "module" {
			continue
		}
		if len(fields) != 2 || module != "" {
			return "", errors.New("generated go.mod has an invalid module directive")
		}
		module = fields[1]
	}
	if module == "" {
		return "", errors.New("generated go.mod is missing its module directive")
	}
	return module, nil
}

// Write drains renderer output while retaining only the bounded diagnostic prefix.
func (output *boundedOutput) Write(body []byte) (int, error) {
	written := len(body)
	remaining := maximumRenderOutput - output.body.Len()
	if remaining <= 0 {
		return written, nil
	}
	if len(body) > remaining {
		body = body[:remaining]
	}
	_, _ = output.body.Write(body)
	return written, nil
}

// String returns the retained diagnostic prefix after process completion.
func (output *boundedOutput) String() string {
	return output.body.String()
}
