// Package projectdiscovery validates a selected checkout and reads only allowlisted presentation metadata.
package projectdiscovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"github.com/goforj/harbor/internal/domain"
)

const (
	maximumProjectPathBytes = 32 << 10
	maximumMetadataBytes    = 1 << 20
	maximumMetadataLine     = 64 << 10
)

var errMetadataLineTooLong = errors.New("metadata line exceeds 64 kibibytes")

// Discovery is the canonical checkout path and allowlisted presentation metadata used for registration.
type Discovery struct {
	Root string
	Name string
	Slug string
}

// Discoverer canonicalizes selected directories and derives registration metadata from marker presence and basic names.
type Discoverer struct{}

// NewDiscoverer creates the execution-free project discovery service.
func NewDiscoverer() *Discoverer {
	return &Discoverer{}
}

// Discover resolves one canonical checkout without executing it or parsing GoForj lifecycle and topology configuration.
func (*Discoverer) Discover(ctx context.Context, selectedPath string) (Discovery, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	root, err := canonicalProjectRoot(selectedPath)
	if err != nil {
		return Discovery{}, err
	}
	if err := validateProjectMarker(root); err != nil {
		return Discovery{}, err
	}
	name, err := discoverProjectName(root)
	if err != nil {
		return Discovery{}, err
	}
	if err := validateProjectName(name); err != nil {
		return Discovery{}, invalidProjectError(err)
	}
	discovery := Discovery{
		Root: root,
		Name: name,
		Slug: projectSlug(name),
	}
	return discovery, nil
}

// discoverProjectName follows GoForj's generated APP_NAME convention without loading project secrets into the daemon environment.
func discoverProjectName(root string) (string, error) {
	name, err := readDotEnvAppName(filepath.Join(root, ".env"))
	if err != nil {
		return "", err
	}
	if name == "" {
		name, err = readProjectMarkerName(filepath.Join(root, ".goforj.yml"))
		if err != nil {
			return "", err
		}
	}
	if name == "" {
		name, err = readDotEnvAppName(filepath.Join(root, ".env.example"))
		if err != nil {
			return "", err
		}
	}
	if name == "" {
		name = filepath.Base(root)
	}
	name = strings.TrimSpace(name)
	if err := validateProjectName(name); err != nil {
		return "", invalidProjectError(err)
	}
	return name, nil
}

// readDotEnvAppName parses only the APP_NAME assignment instead of materializing unrelated project values.
func readDotEnvAppName(filename string) (string, error) {
	var appName string
	err := scanMetadataLines(filename, func(line string) (bool, error) {
		candidate := strings.TrimSpace(line)
		if strings.HasPrefix(candidate, "export ") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "export "))
		}
		key, _, found := strings.Cut(candidate, "=")
		if !found || strings.TrimSpace(key) != "APP_NAME" {
			return false, nil
		}
		values, err := godotenv.Unmarshal(candidate)
		if err != nil {
			return false, invalidProjectError(fmt.Errorf("parse APP_NAME in %s: %w", filename, err))
		}
		appName = values["APP_NAME"]
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return appName, nil
}

// readProjectMarkerName decodes only one root-level project_name scalar rather than the GoForj configuration model.
func readProjectMarkerName(filename string) (string, error) {
	var projectName string
	err := scanMetadataLines(filename, func(line string) (bool, error) {
		if line == "" || unicode.IsSpace(rune(line[0])) {
			return false, nil
		}
		key, _, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "project_name" {
			return false, nil
		}
		var metadata struct {
			ProjectName string `yaml:"project_name"`
		}
		if err := yaml.Unmarshal([]byte(line), &metadata); err != nil {
			return false, invalidProjectError(fmt.Errorf("parse project_name in %s: %w", filename, err))
		}
		projectName = metadata.ProjectName
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return projectName, nil
}

// scanMetadataLines bounds optional metadata reads and lets the caller stop once it has enough information.
func scanMetadataLines(filename string, visit func(string) (bool, error)) error {
	return scanMetadataLinesWithOpener(filename, visit, openMetadataFile)
}

// scanMetadataLinesWithOpener keeps resource-failure classification directly testable without mutating process globals.
func scanMetadataLinesWithOpener(filename string, visit func(string) (bool, error), open func(string) (*os.File, error)) error {
	beforeOpen, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		inspectErr := fmt.Errorf("inspect project metadata %s: %w", filename, err)
		if isInvalidProjectFilesystemError(err) {
			return invalidProjectError(inspectErr)
		}
		return inspectErr
	}
	if !beforeOpen.Mode().IsRegular() {
		return invalidProjectError(fmt.Errorf("project metadata %s must be a regular file", filename))
	}

	file, err := open(filename)
	if err != nil {
		openErr := fmt.Errorf("open project metadata %s: %w", filename, err)
		if isInvalidProjectFilesystemError(err) {
			return invalidProjectError(openErr)
		}
		return openErr
	}
	defer file.Close()
	if err := validateOpenedMetadataFile(filename, beforeOpen, file); err != nil {
		return err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maximumMetadataLine+1)
	scanner.Split(boundedMetadataLines)
	read := 0
	for scanner.Scan() {
		read += len(scanner.Bytes()) + 1
		if read > maximumMetadataBytes {
			return invalidProjectError(fmt.Errorf("project metadata %s exceeds one mebibyte before the requested field", filename))
		}
		stop, err := visit(scanner.Text())
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		readErr := fmt.Errorf("read project metadata %s: %w", filename, err)
		if errors.Is(err, errMetadataLineTooLong) || errors.Is(err, os.ErrPermission) {
			return invalidProjectError(readErr)
		}
		return readErr
	}
	return nil
}

// boundedMetadataLines turns oversized metadata lines into a correctable project error before Scanner's opaque limit error.
func boundedMetadataLines(data []byte, atEOF bool) (int, []byte, error) {
	advance, token, err := bufio.ScanLines(data, atEOF)
	if err != nil {
		return 0, nil, err
	}
	if token != nil && len(token) > maximumMetadataLine {
		return 0, nil, errMetadataLineTooLong
	}
	if token == nil && len(data) > maximumMetadataLine {
		return 0, nil, errMetadataLineTooLong
	}
	return advance, token, nil
}

// validateOpenedMetadataFile binds scanning to the same regular filesystem object checked before open.
func validateOpenedMetadataFile(filename string, beforeOpen os.FileInfo, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		inspectErr := fmt.Errorf("inspect opened project metadata %s: %w", filename, err)
		if isInvalidProjectFilesystemError(err) {
			return invalidProjectError(inspectErr)
		}
		return inspectErr
	}
	if !opened.Mode().IsRegular() {
		return invalidProjectError(fmt.Errorf("opened project metadata %s must be a regular file", filename))
	}
	if !os.SameFile(beforeOpen, opened) {
		return invalidProjectError(fmt.Errorf("project metadata %s changed while it was opened", filename))
	}
	return nil
}

// ProjectSnapshot creates the inert stopped projection for one daemon-issued opaque identity.
func (discovery Discovery) ProjectSnapshot(projectID domain.ProjectID, updatedAt time.Time) (domain.ProjectSnapshot, error) {
	project := domain.ProjectSnapshot{
		ID:        projectID,
		Name:      discovery.Name,
		Path:      discovery.Root,
		Slug:      discovery.Slug,
		State:     domain.ProjectStopped,
		Favorite:  false,
		UpdatedAt: updatedAt.UTC().Round(0),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	if err := project.Validate(); err != nil {
		return domain.ProjectSnapshot{}, fmt.Errorf("project registration projection: %w", err)
	}
	return project, nil
}

// canonicalProjectRoot resolves symlinks before path matching so aliases cannot register twice.
func canonicalProjectRoot(selectedPath string) (string, error) {
	if err := validateSelectedPath(selectedPath); err != nil {
		return "", invalidProjectError(err)
	}
	absolute, err := filepath.Abs(selectedPath)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		resolveErr := fmt.Errorf("resolve project path %q: %w", absolute, err)
		if isInvalidProjectFilesystemError(err) {
			return "", invalidProjectError(resolveErr)
		}
		return "", resolveErr
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("resolve canonical project path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		inspectErr := fmt.Errorf("inspect project path %q: %w", canonical, err)
		if isInvalidProjectFilesystemError(err) {
			return "", invalidProjectError(inspectErr)
		}
		return "", inspectErr
	}
	if !info.IsDir() {
		return "", invalidProjectError(fmt.Errorf("project path %q is not a directory", canonical))
	}
	return filepath.Clean(canonical), nil
}

// validateSelectedPath bounds untrusted protocol input before filesystem resolution.
func validateSelectedPath(selectedPath string) error {
	if selectedPath == "" || strings.TrimSpace(selectedPath) != selectedPath {
		return errors.New("project path must be non-empty without surrounding whitespace")
	}
	if !utf8.ValidString(selectedPath) {
		return errors.New("project path must be valid UTF-8")
	}
	if len(selectedPath) > maximumProjectPathBytes {
		return fmt.Errorf("project path exceeds %d bytes", maximumProjectPathBytes)
	}
	for _, character := range selectedPath {
		if unicode.IsControl(character) {
			return errors.New("project path must not contain control characters")
		}
	}
	return nil
}

// validateProjectMarker requires the authoritative configuration marker before allowlisted metadata is inspected.
func validateProjectMarker(root string) error {
	marker := filepath.Join(root, ".goforj.yml")
	info, err := os.Stat(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return invalidProjectError(fmt.Errorf("%s is not a GoForj project: .goforj.yml was not found", root))
		}
		inspectErr := fmt.Errorf("inspect GoForj project marker: %w", err)
		if isInvalidProjectFilesystemError(err) {
			return invalidProjectError(inspectErr)
		}
		return inspectErr
	}
	if !info.Mode().IsRegular() {
		return invalidProjectError(errors.New("GoForj project marker .goforj.yml must be a regular file"))
	}
	return nil
}

// validateProjectName keeps a directory-derived display name representable in durable snapshots.
func validateProjectName(name string) error {
	if name == "" || name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) != name {
		return errors.New("canonical project directory must have a non-empty display name")
	}
	if !utf8.ValidString(name) {
		return errors.New("project directory name must be valid UTF-8")
	}
	if len(name) > 512 {
		return errors.New("project directory name must not exceed 512 bytes")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return errors.New("project directory name must not contain control characters")
		}
	}
	return nil
}

// projectSlug derives a bounded DNS label without treating presentation punctuation as identity.
func projectSlug(name string) string {
	var builder strings.Builder
	separator := false
	for _, character := range strings.ToLower(name) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			if separator && builder.Len() != 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(character)
			separator = false
			continue
		}
		separator = true
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

// normalizeContext preserves public nil-context ergonomics without weakening discovery behavior.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
