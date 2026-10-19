package projectcreation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/projectprocess"
)

// GoForj identifies the first supported stack; additional adapters supply their own metadata and schemas.
var GoForj = ProviderInfo{ID: "goforj", Name: "GoForj", Description: "Go applications with optional APIs, workers, and frontend starter kits"}

// Creator pins authoring to one verified executable and a replaceable process boundary.
type Creator struct {
	executable string
	run        func(context.Context, string, []string) ([]byte, error)
	stream     func(context.Context, string, []string, io.Writer) error
}

// New resolves and verifies the same canonical GoForj executable identity used for managed project launch.
func New() (*Creator, error) {
	path, err := exec.LookPath("forj")
	if err != nil {
		return nil, fmt.Errorf("find GoForj CLI: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if err := projectprocess.VerifyGoForjExecutable(path); err != nil {
		return nil, err
	}
	return &Creator{executable: path, run: runCreationCommand, stream: streamCreationCommand}, nil
}

// Schema reads only bounded JSON and fails visibly for an older or unsupported GoForj command.
func (creator *Creator) Schema(ctx context.Context) (Schema, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := creator.run(ctx, creator.executable, []string{"new", "--schema"})
	if err != nil {
		return Schema{}, fmt.Errorf("GoForj project creation is unavailable; update the CLI to a version supporting forj new --schema: %w", err)
	}
	var wire struct {
		SchemaVersion int     `json:"schema_version"`
		GoForjVersion string  `json:"goforj_version"`
		Fields        []Field `json:"fields"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Schema{}, fmt.Errorf("decode GoForj creation schema: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Schema{}, fmt.Errorf("GoForj creation schema contains trailing data")
	}
	schema := Schema{SchemaVersion: wire.SchemaVersion, ProviderID: GoForj.ID, Version: wire.GoForjVersion, Fields: wire.Fields}
	if err := schema.Validate(); err != nil {
		return schema, err
	}
	if err := validateGoForjSchema(schema); err != nil {
		return schema, err
	}
	return schema, nil
}

// validateGoForjSchema restricts the CLI adapter's argument surface independently of generic form validation.
func validateGoForjSchema(schema Schema) error {
	seen := map[string]bool{}
	for _, field := range schema.Fields {
		if field.Key == "schema" || field.Key == "no-interaction" || field.Key == "allow-non-empty" {
			return fmt.Errorf("invalid creation field %q", field.Key)
		}
		seen[field.Key] = true
	}
	if !seen["name"] || !seen["module"] || !seen["path"] {
		return fmt.Errorf("GoForj creation schema lacks project identity fields")
	}
	return nil
}

// goForjArguments implements GoForj argv construction and validates exact schema values and builds argv without passing any input through a shell.
func goForjArguments(schema Schema, request Request) ([]string, error) {
	if err := schema.Validate(); err != nil {
		return nil, err
	}
	if err := validateGoForjSchema(schema); err != nil {
		return nil, err
	}
	if request.ProviderID != "" && request.ProviderID != GoForj.ID {
		return nil, fmt.Errorf("wrong project creator")
	}
	if request.SchemaVersion != schema.SchemaVersion {
		return nil, fmt.Errorf("creation schema changed; reopen the project wizard")
	}
	known := map[string]bool{}
	args := []string{"new", "--no-interaction"}
	for _, field := range schema.Fields {
		known[field.Key] = true
		if !field.Available(request.Options) {
			continue
		}
		value, present := request.Options[field.Key]
		if !present || (field.Required && strings.TrimSpace(value) == "") {
			return nil, fmt.Errorf("%s is required", field.Label)
		}
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("%s is too long or contains invalid characters", field.Label)
		}
		allowed := map[string]bool{}
		for _, option := range field.Options {
			allowed[option.Value] = true
		}
		switch field.Kind {
		case "select":
			if !allowed[value] {
				return nil, fmt.Errorf("unsupported %s choice", field.Label)
			}
		case "multi":
			for _, item := range strings.Split(value, ",") {
				if item != "" && !allowed[item] {
					return nil, fmt.Errorf("unsupported %s choice", field.Label)
				}
			}
		case "boolean":
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("%s must be true or false", field.Label)
			}
		}
		args = append(args, "--"+field.Key+"="+value)
	}
	for key := range request.Options {
		if !known[key] {
			return nil, fmt.Errorf("unsupported creation option %q", key)
		}
	}
	if !filepath.IsAbs(request.Options["path"]) {
		return nil, fmt.Errorf("project directory must be absolute")
	}
	return args, nil
}

// Create re-reads the CLI contract before creation; successful files remain owned by the user.
func (creator *Creator) Create(ctx context.Context, request Request) (string, error) {
	output := &boundedOutput{}
	err := creator.CreateWithOutput(ctx, request, output)
	return output.String(), err
}

// CreateWithOutput streams actual CLI output while retaining the same validated argument boundary.
func (creator *Creator) CreateWithOutput(ctx context.Context, request Request, output io.Writer) error {
	schema, err := creator.Schema(ctx)
	if err != nil {
		return err
	}
	args, err := goForjArguments(schema, request)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := creator.stream(ctx, creator.executable, args, output); err != nil {
		return fmt.Errorf("create project at %s: %w", request.Options["path"], err)
	}
	return nil
}

// boundedOutput drains process output while keeping memory and error presentation bounded.
type boundedOutput struct{ bytes.Buffer }

// Write keeps the first megabyte without blocking a verbose renderer's child process.
func (output *boundedOutput) Write(data []byte) (int, error) {
	count := len(data)
	if remaining := 1024*1024 - output.Len(); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = output.Buffer.Write(data)
	}
	return count, nil
}

// runCreationCommand runs fixed argv with a bounded timeout and no TTY or shell interpolation.
func runCreationCommand(ctx context.Context, path string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = 3 * time.Second
	output := &boundedOutput{}
	errors := &boundedOutput{}
	command.Stdout, command.Stderr = output, errors
	command.Env = append(command.Environ(), "NO_COLOR=1", "TERM=dumb")
	err := command.Run()
	if err != nil {
		return append(output.Bytes(), errors.Bytes()...), fmt.Errorf("GoForj command failed: %w (%s)", err, strconv.Quote(strings.TrimSpace(errors.String())))
	}
	return output.Bytes(), nil
}

// streamCreationCommand drains stdout and stderr through the job's synchronized bounded writer as they arrive.
func streamCreationCommand(ctx context.Context, path string, args []string, output io.Writer) error {
	command := exec.CommandContext(ctx, path, args...)
	command.WaitDelay = 3 * time.Second
	command.Stdout, command.Stderr = output, output
	command.Env = append(command.Environ(), "NO_COLOR=1", "TERM=dumb")
	if err := command.Run(); err != nil {
		return fmt.Errorf("GoForj command failed: %w", err)
	}
	return nil
}
