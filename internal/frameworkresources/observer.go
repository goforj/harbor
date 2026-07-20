// Package frameworkresources reads GoForj's host-resolved, secret-free project resource catalog.
package frameworkresources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	frameworkResourceSchemaVersion = 1
	maximumReportBytes             = 1 << 20
	maximumDiagnosticBytes         = 16 << 10
	maximumResources               = 256
	maximumIdentifierBytes         = 128
	maximumNameBytes               = 512
	maximumDescriptionBytes        = 1024
	maximumURLBytes                = 2048
	maximumProblemBytes            = 4096
	defaultObservationTimeout      = 5 * time.Second
	commandSettlementDelay         = 250 * time.Millisecond
)

var (
	// ErrReportTooLarge marks a GoForj response that exceeded Harbor's bounded machine-contract input.
	ErrReportTooLarge = errors.New("GoForj framework resource report exceeds the supported size")
	// ErrObservationTimedOut marks a framework resource query that did not settle within its bounded observation window.
	ErrObservationTimedOut = errors.New("GoForj framework resource observation timed out")
)

// UnsupportedReason identifies why optional framework resource enrichment is unavailable.
type UnsupportedReason string

const (
	// UnsupportedCommand means the supplied GoForj executable predates the resource-only machine query.
	UnsupportedCommand UnsupportedReason = "command_unavailable"
	// UnsupportedReport means GoForj returned schema v1 without the additive resources field.
	UnsupportedReport UnsupportedReason = "report_unavailable"
)

// ProblemCode identifies a bounded diagnostic reported by GoForj without invalidating admitted resources.
type ProblemCode string

const (
	// ProblemStatus means GoForj could not load the project-level status context.
	ProblemStatus ProblemCode = "status_problem"
	// ProblemResources means GoForj returned a partial resource catalog.
	ProblemResources ProblemCode = "resource_problem"
)

// Query identifies the exact executable, checkout, and environment used for one resource observation.
type Query struct {
	Executable  string
	Checkout    string
	Environment []string
}

// Resource is one launchable, secret-free framework resource with explicit ownership.
type Resource struct {
	ID          string
	Name        string
	Kind        string
	URL         string
	Description string
	App         string
	Service     string
	Runtime     string
	Health      string
	Owner       string
}

// Problem is one safe diagnostic carried beside an otherwise valid observation.
type Problem struct {
	Code    ProblemCode
	Message string
}

// Observation is a complete optional resource view from one GoForj machine query.
type Observation struct {
	Supported         bool
	UnsupportedReason UnsupportedReason
	Resources         []Resource
	Problems          []Problem
}

// wireReport is the exact schema-v1 allowlist accepted from the GoForj process.
type wireReport struct {
	SchemaVersion   *int               `json:"schema_version"`
	Supported       *bool              `json:"supported"`
	Problem         string             `json:"problem,omitempty"`
	ResourceProblem string             `json:"resource_problem,omitempty"`
	Project         string             `json:"project,omitempty"`
	Services        *[]json.RawMessage `json:"services"`
	Resources       *[]wireResource    `json:"resources"`
}

// wireResource is the secret-free resource allowlist exposed by GoForj schema v1.
type wireResource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	App         string `json:"app,omitempty"`
	Service     string `json:"service,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Health      string `json:"health,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// boundedBuffer retains a fixed prefix while continuing to drain a subprocess safely.
type boundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

// Observe invokes the supplied GoForj executable directly and returns its optional resource catalog.
func Observe(ctx context.Context, query Query) (Observation, error) {
	return observe(ctx, query, defaultObservationTimeout)
}

// observe keeps timeout selection injectable for focused cancellation tests.
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
		return Observation{}, errors.New("framework resource observation timeout must be positive")
	}

	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout := &boundedBuffer{maximum: maximumReportBytes}
	stderr := &boundedBuffer{maximum: maximumDiagnosticBytes}
	command := exec.CommandContext(runContext, query.Executable, "dev:status", "--json", "--resources-only")
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
		if isUnsupportedCommand(stderr.Bytes()) {
			return unsupportedObservation(UnsupportedCommand), nil
		}
		return Observation{}, commandError(err, stderr)
	}
	return decodeReport(stdout.Bytes())
}

// validateQuery requires exact absolute process identity instead of consulting PATH or an ambient directory.
func validateQuery(query Query) error {
	if query.Executable == "" || !filepath.IsAbs(query.Executable) {
		return errors.New("framework resource executable must be an absolute path")
	}
	if query.Checkout == "" || !filepath.IsAbs(query.Checkout) {
		return errors.New("framework resource checkout must be an absolute path")
	}
	for index, entry := range query.Environment {
		// Windows carries drive-directory pseudo-variables with an empty-looking name, so only the separator itself is portable.
		if strings.IndexByte(entry, '=') < 0 || strings.IndexByte(entry, 0) >= 0 {
			return fmt.Errorf("framework resource environment entry %d is invalid", index+1)
		}
	}
	return nil
}

// decodeReport strictly admits one schema-v1 JSON document and projects only launchable resources.
func decodeReport(payload []byte) (Observation, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var report wireReport
	if err := decoder.Decode(&report); err != nil {
		return Observation{}, fmt.Errorf("decode GoForj framework resource report: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Observation{}, err
	}
	if report.SchemaVersion == nil || *report.SchemaVersion != frameworkResourceSchemaVersion {
		return Observation{}, fmt.Errorf("GoForj framework resource schema must be %d", frameworkResourceSchemaVersion)
	}
	if report.Resources == nil {
		return unsupportedObservation(UnsupportedReport), nil
	}
	if report.Supported == nil {
		return Observation{}, errors.New("GoForj framework resource report is missing supported state")
	}
	if !*report.Supported {
		return unsupportedObservation(UnsupportedReport), nil
	}
	if report.Services == nil || len(*report.Services) != 0 {
		return Observation{}, errors.New("GoForj resource-only report must contain an empty services array")
	}
	if len(*report.Resources) > maximumResources {
		return Observation{}, fmt.Errorf("GoForj framework resource report contains more than %d resources", maximumResources)
	}
	if report.Project != "" && !validIdentifier(report.Project) {
		return Observation{}, errors.New("GoForj framework resource project identity is unsafe")
	}

	problems := make([]Problem, 0, 2)
	for _, reported := range []Problem{
		{Code: ProblemStatus, Message: report.Problem},
		{Code: ProblemResources, Message: report.ResourceProblem},
	} {
		if reported.Message == "" {
			continue
		}
		if err := validateText("framework resource problem", reported.Message, maximumProblemBytes, true); err != nil {
			return Observation{}, err
		}
		problems = append(problems, reported)
	}

	resources := make([]Resource, 0, len(*report.Resources))
	identities := make(map[string]struct{}, len(*report.Resources))
	for index, candidate := range *report.Resources {
		resource, admitted, err := admitResource(candidate)
		if err != nil {
			return Observation{}, fmt.Errorf("validate GoForj framework resource %d: %w", index+1, err)
		}
		if !admitted {
			continue
		}
		if _, exists := identities[resource.ID]; exists {
			return Observation{}, fmt.Errorf("GoForj framework resource ID %q is duplicated", resource.ID)
		}
		identities[resource.ID] = struct{}{}
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(left int, right int) bool {
		if resources[left].Kind != resources[right].Kind {
			return resources[left].Kind < resources[right].Kind
		}
		if resources[left].ID != resources[right].ID {
			return resources[left].ID < resources[right].ID
		}
		return resources[left].Name < resources[right].Name
	})
	return Observation{Supported: true, Resources: resources, Problems: problems}, nil
}

// admitResource ignores non-launchable entries and validates every field copied across the trust boundary.
func admitResource(candidate wireResource) (Resource, bool, error) {
	if candidate.URL == "" {
		return Resource{}, false, nil
	}
	if !validIdentifier(candidate.ID) {
		return Resource{}, false, errors.New("ID is missing or unsafe")
	}
	if err := validateText("name", candidate.Name, maximumNameBytes, true); err != nil {
		return Resource{}, false, err
	}
	if !validIdentifier(candidate.Kind) {
		return Resource{}, false, errors.New("kind is missing or unsafe")
	}
	if err := validateHTTPURL("URL", candidate.URL, true); err != nil {
		return Resource{}, false, err
	}
	if err := validateText("description", candidate.Description, maximumDescriptionBytes, false); err != nil {
		return Resource{}, false, err
	}
	if (candidate.App == "") == (candidate.Service == "") {
		return Resource{}, false, errors.New("exactly one App or service owner is required")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "App", value: candidate.App},
		{name: "service", value: candidate.Service},
		{name: "runtime", value: candidate.Runtime},
		{name: "owner", value: candidate.Owner},
	} {
		if field.value != "" && !validIdentifier(field.value) {
			return Resource{}, false, fmt.Errorf("%s is unsafe", field.name)
		}
	}
	if err := validateHTTPURL("health URL", candidate.Health, false); err != nil {
		return Resource{}, false, err
	}
	return Resource{
		ID:          candidate.ID,
		Name:        candidate.Name,
		Kind:        candidate.Kind,
		URL:         candidate.URL,
		Description: candidate.Description,
		App:         candidate.App,
		Service:     candidate.Service,
		Runtime:     candidate.Runtime,
		Health:      candidate.Health,
		Owner:       candidate.Owner,
	}, true, nil
}

// validateHTTPURL accepts bounded absolute HTTP links without embedded user information.
func validateHTTPURL(name string, rawURL string, required bool) error {
	if rawURL == "" {
		if required {
			return fmt.Errorf("%s is missing", name)
		}
		return nil
	}
	if len(rawURL) > maximumURLBytes || !utf8.ValidString(rawURL) || strings.TrimSpace(rawURL) != rawURL {
		return fmt.Errorf("%s is unsafe or exceeds %d bytes", name, maximumURLBytes)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an absolute HTTP URL without user information", name)
	}
	return nil
}

// validateText constrains human-readable fields to canonical UTF-8 without terminal control characters.
func validateText(name string, value string, maximum int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is missing", name)
		}
		return nil
	}
	if len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is unsafe or exceeds %d bytes", name, maximum)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

// validIdentifier constrains identities to the portable GoForj machine-contract vocabulary.
func validIdentifier(value string) bool {
	if value == "" || len(value) > maximumIdentifierBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '-', '_', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}

// requireJSONEnd rejects additional documents after the one expected status object.
func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing GoForj framework resource JSON: %w", err)
	}
	return errors.New("GoForj framework resource output contains multiple JSON documents")
}

// unsupportedObservation preserves a non-nil empty shape for optional older-GoForj enrichment.
func unsupportedObservation(reason UnsupportedReason) Observation {
	return Observation{
		Supported:         false,
		UnsupportedReason: reason,
		Resources:         make([]Resource, 0),
		Problems:          make([]Problem, 0),
	}
}

// isUnsupportedCommand recognizes only parser failures naming the exact additive command surface.
func isUnsupportedCommand(stderr []byte) bool {
	message := strings.ToLower(string(stderr))
	if !strings.Contains(message, "resources-only") && !strings.Contains(message, "dev:status") {
		return false
	}
	for _, marker := range []string{
		"unexpected argument",
		"unknown argument",
		"unknown command",
		"unknown flag",
		"unrecognized option",
		"flag provided but not defined",
		"no such command",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// commandError returns one bounded printable diagnostic without reflecting the full environment or command payload.
func commandError(runErr error, stderr *boundedBuffer) error {
	diagnostic := strings.TrimSpace(string(stderr.Bytes()))
	if diagnostic == "" {
		return fmt.Errorf("query GoForj framework resources: %w", runErr)
	}
	diagnostic = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\t' && character != '\n' && character != '\r' {
			return -1
		}
		return character
	}, diagnostic)
	if stderr.exceeded {
		diagnostic += "..."
	}
	return fmt.Errorf("query GoForj framework resources: %w: %s", runErr, diagnostic)
}

// Write retains a bounded prefix and reports full consumption so the child cannot block on a closed reader.
func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return written, nil
	}
	if len(payload) > remaining {
		buffer.exceeded = true
		payload = payload[:remaining]
	}
	_, _ = buffer.buffer.Write(payload)
	return written, nil
}

// Bytes returns a copy-free view of the retained bounded prefix.
func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}
