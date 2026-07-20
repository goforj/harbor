// Package goforj contains Harbor's narrow, versioned boundary to GoForj.
package goforj

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// DescriptorSchemaVersion is the static project descriptor schema Harbor understands.
	DescriptorSchemaVersion   = 1
	maximumReportBytes        = 1 << 20
	maximumDiagnosticBytes    = 16 << 10
	maximumApps               = 256
	maximumRuntimes           = 32
	maximumCapabilities       = 128
	maximumIdentifierBytes    = 128
	maximumNameBytes          = 512
	maximumModuleBytes        = 1024
	maximumPathBytes          = 2048
	maximumVersionBytes       = 256
	defaultObservationTimeout = 5 * time.Second
	commandSettlementDelay    = 250 * time.Millisecond
)

var (
	// ErrReportTooLarge means GoForj returned more descriptor data than Harbor will retain.
	ErrReportTooLarge = errors.New("GoForj project descriptor exceeds the supported size")
	// ErrObservationTimedOut means the descriptor command did not settle within its bound.
	ErrObservationTimedOut = errors.New("GoForj project descriptor observation timed out")
)

// Query identifies the exact GoForj executable, checkout, and environment used for one descriptor read.
type Query struct {
	Executable  string
	Checkout    string
	Environment []string
}

// Project identifies a checkout without exposing its filesystem path or environment.
type Project struct {
	Name         string
	Module       string
	ConfigDigest string
}

// GoForj identifies the CLI and generated-project capabilities advertised by the descriptor.
type GoForj struct {
	Version          string
	CLICapabilities  []string
	GeneratedProject GeneratedProject
}

// GeneratedProject identifies generated-project compatibility without exposing project values.
type GeneratedProject struct {
	Generation   string
	Capabilities []string
}

// App identifies one available GoForj App and its conventional runtimes.
type App struct {
	ID         string
	Name       string
	Entrypoint string
	Runtimes   []Runtime
}

// Runtime identifies one static runtime intent without claiming an effective host port.
type Runtime struct {
	ID            string
	Kind          string
	DefaultPort   int
	PublicURL     bool
	ReadinessPath string
}

// Observation is a validated static descriptor projection.
type Observation struct {
	SchemaVersion  int
	Project        Project
	GoForj         GoForj
	Apps           []App
	TopologyDigest string
}

// Observe invokes GoForj directly and validates exactly one descriptor document.
func Observe(ctx context.Context, query Query) (Observation, error) {
	return observe(ctx, query, defaultObservationTimeout)
}

// observe keeps timeout selection injectable for cancellation and settlement tests.
func observe(ctx context.Context, query Query, timeout time.Duration) (Observation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := validateQuery(query); err != nil {
		return Observation{}, err
	}
	if timeout <= 0 {
		return Observation{}, errors.New("project descriptor observation timeout must be positive")
	}

	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := &boundedBuffer{maximum: maximumReportBytes}
	stderr := &boundedBuffer{maximum: maximumDiagnosticBytes}
	command := exec.CommandContext(runContext, query.Executable, "project:describe", "--json")
	command.Dir = query.Checkout
	command.Env = append([]string(nil), query.Environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = commandSettlementDelay
	err := command.Run()

	if parentErr := ctx.Err(); parentErr != nil {
		return Observation{}, parentErr
	}
	if errors.Is(runContext.Err(), context.DeadlineExceeded) {
		return Observation{}, ErrObservationTimedOut
	}
	if stdout.exceeded {
		return Observation{}, ErrReportTooLarge
	}
	if err != nil {
		return Observation{}, commandError(err, stderr.Bytes())
	}
	return decodeReport(stdout.Bytes())
}

// validateQuery requires exact absolute process and checkout identities instead of consulting PATH.
func validateQuery(query Query) error {
	if query.Executable == "" || !filepath.IsAbs(query.Executable) {
		return errors.New("project descriptor executable must be an absolute path")
	}
	if query.Checkout == "" || !filepath.IsAbs(query.Checkout) {
		return errors.New("project descriptor checkout must be an absolute path")
	}
	for index, entry := range query.Environment {
		if strings.IndexByte(entry, '=') < 0 || strings.IndexByte(entry, 0) >= 0 {
			return fmt.Errorf("project descriptor environment entry %d is invalid", index+1)
		}
	}
	return nil
}

// wireReport is the strict schema-v1 allowlist accepted from GoForj.
type wireReport struct {
	SchemaVersion *int         `json:"schema_version"`
	Project       *wireProject `json:"project"`
	GoForj        *wireGoForj  `json:"goforj"`
	Apps          *[]wireApp   `json:"apps"`
}

// wireProject keeps required scalar fields distinguishable from omitted JSON fields.
type wireProject struct {
	Name         string `json:"name"`
	Module       string `json:"module"`
	ConfigDigest string `json:"config_digest"`
}

// wireGoForj keeps nested descriptor metadata behind the schema allowlist.
type wireGoForj struct {
	Version          string                `json:"version"`
	CLICapabilities  *[]string             `json:"cli_capabilities"`
	GeneratedProject *wireGeneratedProject `json:"generated_project"`
}

// wireGeneratedProject is the generated-project portion of the schema-v1 allowlist.
type wireGeneratedProject struct {
	Generation   string    `json:"generation"`
	Capabilities *[]string `json:"capabilities"`
}

// wireApp keeps each App's required runtime collection explicit.
type wireApp struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Entrypoint string         `json:"entrypoint"`
	Runtimes   *[]wireRuntime `json:"runtimes"`
}

// wireRuntime keeps false and zero values distinguishable from omitted fields.
type wireRuntime struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	DefaultPort   *int   `json:"default_port"`
	PublicURL     *bool  `json:"public_url"`
	ReadinessPath string `json:"readiness_path"`
}

// boundedBuffer retains a fixed prefix while continuing to drain a child process safely.
type boundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

// Write retains bounded output and reports the complete byte count to the caller.
func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = buffer.buffer.Write(value[:remaining])
			buffer.exceeded = true
		} else {
			_, _ = buffer.buffer.Write(value)
		}
	} else if len(value) != 0 {
		buffer.exceeded = true
	}
	return len(value), nil
}

// Bytes returns the retained output prefix for bounded diagnostics.
func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

// decodeReport strictly admits one schema-v1 descriptor and projects only bounded, non-secret fields.
func decodeReport(payload []byte) (Observation, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var report wireReport
	if err := decoder.Decode(&report); err != nil {
		return Observation{}, fmt.Errorf("decode GoForj project descriptor: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Observation{}, err
	}
	if report.SchemaVersion == nil || *report.SchemaVersion != DescriptorSchemaVersion {
		return Observation{}, fmt.Errorf("GoForj project descriptor schema must be %d", DescriptorSchemaVersion)
	}
	if report.Project == nil || report.GoForj == nil || report.Apps == nil {
		return Observation{}, errors.New("GoForj project descriptor is missing a required section")
	}
	if report.GoForj.GeneratedProject == nil || report.GoForj.CLICapabilities == nil || report.GoForj.GeneratedProject.Capabilities == nil {
		return Observation{}, errors.New("GoForj project descriptor is missing capability metadata")
	}
	if err := validateProject(*report.Project); err != nil {
		return Observation{}, err
	}
	if err := validateGoForj(*report.GoForj); err != nil {
		return Observation{}, err
	}
	apps, err := projectApps(*report.Apps)
	if err != nil {
		return Observation{}, err
	}
	digest := strings.TrimPrefix(report.Project.ConfigDigest, "sha256:")
	return Observation{
		SchemaVersion:  DescriptorSchemaVersion,
		Project:        Project{Name: report.Project.Name, Module: report.Project.Module, ConfigDigest: report.Project.ConfigDigest},
		GoForj:         GoForj{Version: report.GoForj.Version, CLICapabilities: append([]string(nil), (*report.GoForj.CLICapabilities)...), GeneratedProject: GeneratedProject{Generation: report.GoForj.GeneratedProject.Generation, Capabilities: append([]string(nil), (*report.GoForj.GeneratedProject.Capabilities)...)}},
		Apps:           apps,
		TopologyDigest: digest,
	}, nil
}

// validateProject checks identity and canonical digest shape before it can become durable session evidence.
func validateProject(project wireProject) error {
	if err := validateText("project name", project.Name, maximumNameBytes, false); err != nil {
		return err
	}
	if err := validateText("project module", project.Module, maximumModuleBytes, false); err != nil {
		return err
	}
	if err := validateDigest(project.ConfigDigest); err != nil {
		return err
	}
	return nil
}

// validateGoForj checks capability metadata while allowing future capability names to remain additive.
func validateGoForj(value wireGoForj) error {
	if err := validateText("GoForj version", value.Version, maximumVersionBytes, true); err != nil {
		return err
	}
	if err := validateCapabilities("GoForj CLI capabilities", *value.CLICapabilities); err != nil {
		return err
	}
	if !contains(*value.CLICapabilities, "project-descriptor.v1") {
		return errors.New("GoForj project descriptor does not advertise project-descriptor.v1")
	}
	if err := validateText("generated project generation", value.GeneratedProject.Generation, maximumVersionBytes, true); err != nil {
		return err
	}
	return validateCapabilities("generated project capabilities", *value.GeneratedProject.Capabilities)
}

// projectApps validates stable App/runtime identity while preserving GoForj ordering.
func projectApps(source []wireApp) ([]App, error) {
	if len(source) > maximumApps {
		return nil, fmt.Errorf("GoForj project descriptor contains more than %d Apps", maximumApps)
	}
	apps := make([]App, 0, len(source))
	seenApps := make(map[string]struct{}, len(source))
	for index, sourceApp := range source {
		if err := validateToken(fmt.Sprintf("App %d ID", index+1), sourceApp.ID, maximumIdentifierBytes); err != nil {
			return nil, err
		}
		if _, exists := seenApps[sourceApp.ID]; exists {
			return nil, fmt.Errorf("duplicate App ID %q in GoForj project descriptor", sourceApp.ID)
		}
		seenApps[sourceApp.ID] = struct{}{}
		if err := validateText(fmt.Sprintf("App %q name", sourceApp.ID), sourceApp.Name, maximumNameBytes, false); err != nil {
			return nil, err
		}
		if err := validateRelativePath(fmt.Sprintf("App %q entrypoint", sourceApp.ID), sourceApp.Entrypoint); err != nil {
			return nil, err
		}
		if sourceApp.Runtimes == nil {
			return nil, fmt.Errorf("App %q runtimes must be an array", sourceApp.ID)
		}
		if len(*sourceApp.Runtimes) > maximumRuntimes {
			return nil, fmt.Errorf("App %q contains more than %d runtimes", sourceApp.ID, maximumRuntimes)
		}
		runtimes := make([]Runtime, 0, len(*sourceApp.Runtimes))
		seenRuntimes := make(map[string]struct{}, len(*sourceApp.Runtimes))
		for runtimeIndex, sourceRuntime := range *sourceApp.Runtimes {
			if err := validateToken(fmt.Sprintf("App %q runtime %d ID", sourceApp.ID, runtimeIndex+1), sourceRuntime.ID, maximumIdentifierBytes); err != nil {
				return nil, err
			}
			if _, exists := seenRuntimes[sourceRuntime.ID]; exists {
				return nil, fmt.Errorf("duplicate runtime ID %q in App %q", sourceRuntime.ID, sourceApp.ID)
			}
			seenRuntimes[sourceRuntime.ID] = struct{}{}
			if err := validateToken(fmt.Sprintf("App %q runtime %q kind", sourceApp.ID, sourceRuntime.ID), sourceRuntime.Kind, maximumIdentifierBytes); err != nil {
				return nil, err
			}
			if sourceRuntime.DefaultPort == nil || *sourceRuntime.DefaultPort < 1 || *sourceRuntime.DefaultPort > 65535 {
				return nil, fmt.Errorf("App %q runtime %q default port must be between 1 and 65535", sourceApp.ID, sourceRuntime.ID)
			}
			if sourceRuntime.PublicURL == nil {
				return nil, fmt.Errorf("App %q runtime %q public_url is required", sourceApp.ID, sourceRuntime.ID)
			}
			if err := validatePath(fmt.Sprintf("App %q runtime %q readiness path", sourceApp.ID, sourceRuntime.ID), sourceRuntime.ReadinessPath); err != nil {
				return nil, err
			}
			runtimes = append(runtimes, Runtime{ID: sourceRuntime.ID, Kind: sourceRuntime.Kind, DefaultPort: *sourceRuntime.DefaultPort, PublicURL: *sourceRuntime.PublicURL, ReadinessPath: sourceRuntime.ReadinessPath})
		}
		apps = append(apps, App{ID: sourceApp.ID, Name: sourceApp.Name, Entrypoint: sourceApp.Entrypoint, Runtimes: runtimes})
	}
	return apps, nil
}

// validateCapabilities bounds capability arrays and excludes invisible text from diagnostics and durable projections.
func validateCapabilities(name string, values []string) error {
	if len(values) > maximumCapabilities {
		return fmt.Errorf("%s contains more than %d entries", name, maximumCapabilities)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateToken(fmt.Sprintf("%s entry %d", name, index+1), value, maximumIdentifierBytes); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// validateDigest accepts only the descriptor's explicit sha256 encoding.
func validateDigest(value string) error {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return errors.New("GoForj project config_digest must be sha256 followed by 64 hexadecimal characters")
	}
	digest := value[len("sha256:"):]
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return errors.New("GoForj project config_digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	return nil
}

// validateToken admits stable identifier-like values without allowing whitespace or control text.
func validateToken(name, value string, maximum int) error {
	if err := validateText(name, value, maximum, false); err != nil {
		return err
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain whitespace or control characters", name)
		}
	}
	return nil
}

// validateText bounds UTF-8 text and rejects control characters before it reaches clients or state.
func validateText(name, value string, maximum int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s must not exceed %d bytes", name, maximum)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

// validateRelativePath rejects absolute and traversal paths before Harbor treats an entrypoint as project-relative metadata.
func validateRelativePath(name, value string) error {
	if err := validateText(name, value, maximumPathBytes, false); err != nil {
		return err
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	cleaned := path.Clean(normalized)
	if filepath.IsAbs(value) || strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && normalized[1] == ':') || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%s must be a project-relative path", name)
	}
	return nil
}

// validatePath admits only absolute HTTP-style readiness paths.
func validatePath(name, value string) error {
	if err := validateText(name, value, maximumPathBytes, false); err != nil {
		return err
	}
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "//") {
		return fmt.Errorf("%s must be an absolute URL path", name)
	}
	return nil
}

// contains reports whether a capability is advertised exactly once or more.
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// requireJSONEnd rejects a second document or non-whitespace trailing bytes.
func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("GoForj project descriptor contains multiple JSON documents")
		}
		return fmt.Errorf("read GoForj project descriptor terminator: %w", err)
	}
	return nil
}

// commandError preserves a bounded printable diagnostic without copying arbitrary child output into state.
func commandError(err error, diagnostic []byte) error {
	message := strings.Join(strings.Fields(strings.ToValidUTF8(string(diagnostic), "�")), " ")
	if len(message) > 1024 {
		message = message[:1024]
	}
	if message == "" {
		return fmt.Errorf("run GoForj project descriptor: %w", err)
	}
	return fmt.Errorf("run GoForj project descriptor: %w: %s", err, message)
}
