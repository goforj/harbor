// Package projectenvironment resolves repository-owned environment bindings from Harbor facts.
package projectenvironment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	currentVersion           = 1
	maximumConfigBytes       = 64 * 1024
	maximumEnvironmentValues = 128

	// Filename is the direct repository file containing Harbor project configuration.
	Filename = ".harbor.yml"
	// SourceProjectAddress resolves to Harbor's assigned project loopback address.
	SourceProjectAddress = "project.address"
)

var (
	// ErrConfigurationChanged reports that a save no longer matches the displayed repository revision.
	ErrConfigurationChanged = errors.New("Harbor project configuration changed outside Harbor; reload before saving")
)

// Facts contains Harbor-owned values available while preparing a project launch.
type Facts struct {
	ProjectAddress netip.Addr
}

// Binding maps one project environment name to an allowlisted Harbor fact.
type Binding struct {
	Name   string
	Source string
}

// Configuration is the structured repository contract and its precise file revision.
type Configuration struct {
	Bindings []Binding
	Revision string
}

// Override is one repository-declared environment binding after Harbor resolves its source.
type Override struct {
	Name   string
	Value  string
	Source string
}

type config struct {
	Version     int                `yaml:"version"`
	Environment map[string]binding `yaml:"environment"`
}

type binding struct {
	From string `yaml:"from"`
}

// Resolve loads the optional repository contract and resolves every binding from Harbor-owned facts.
func Resolve(checkoutRoot string, facts Facts) ([]Override, error) {
	configuration, found, _, err := load(checkoutRoot)
	if err != nil {
		return nil, err
	}
	if !found {
		return []Override{}, nil
	}
	names := make([]string, 0, len(configuration.Environment))
	for name := range configuration.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	overrides := make([]Override, 0, len(names))
	for _, name := range names {
		selected := configuration.Environment[name]
		value, err := resolveSource(selected.From, facts)
		if err != nil {
			return nil, fmt.Errorf("resolve %s from %s: %w", name, selected.From, err)
		}
		overrides = append(overrides, Override{Name: name, Value: value, Source: selected.From})
	}
	return overrides, nil
}

// Inspect returns the optional repository bindings without requiring runtime facts.
func Inspect(checkoutRoot string) (Configuration, error) {
	configuration, found, contents, err := load(checkoutRoot)
	if err != nil {
		return Configuration{}, err
	}
	if !found {
		return Configuration{Bindings: []Binding{}}, nil
	}
	names := make([]string, 0, len(configuration.Environment))
	for name := range configuration.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make([]Binding, 0, len(names))
	for _, name := range names {
		bindings = append(bindings, Binding{
			Name:   name,
			Source: configuration.Environment[name].From,
		})
	}
	return Configuration{
		Bindings: bindings,
		Revision: revision(contents),
	}, nil
}

// Save replaces the repository contract only when its displayed revision still matches.
func Save(checkoutRoot string, contents string, expectedRevision string) (Configuration, error) {
	if len(contents) > maximumConfigBytes {
		return Configuration{}, fmt.Errorf("%s exceeds %d bytes", Filename, maximumConfigBytes)
	}
	if _, err := decode([]byte(contents)); err != nil {
		return Configuration{}, err
	}
	_, found, currentContents, err := read(checkoutRoot)
	if err != nil {
		return Configuration{}, err
	}
	currentRevision := ""
	if found {
		currentRevision = revision(currentContents)
	}
	if expectedRevision != currentRevision {
		return Configuration{}, ErrConfigurationChanged
	}
	if err := replace(checkoutRoot, []byte(contents), currentRevision); err != nil {
		return Configuration{}, err
	}
	return Inspect(checkoutRoot)
}

// load reads one strict, bounded regular config file without following a repository symlink.
func load(checkoutRoot string) (config, bool, []byte, error) {
	_, found, contents, err := read(checkoutRoot)
	if err != nil || !found {
		return config{}, found, contents, err
	}
	configuration, err := decode(contents)
	if err != nil {
		return config{}, false, nil, err
	}
	return configuration, true, contents, nil
}

// read returns the exact bounded file bytes without decoding them.
func read(checkoutRoot string) (fs.FileMode, bool, []byte, error) {
	path := filepath.Join(checkoutRoot, Filename)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil, nil
	}
	if err != nil {
		return 0, false, nil, fmt.Errorf("inspect %s: %w", Filename, err)
	}
	if !info.Mode().IsRegular() {
		return 0, false, nil, fmt.Errorf("%s must be a regular file", Filename)
	}
	if info.Size() > maximumConfigBytes {
		return 0, false, nil, fmt.Errorf("%s exceeds %d bytes", Filename, maximumConfigBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0, false, nil, fmt.Errorf("read %s: %w", Filename, err)
	}
	if len(contents) > maximumConfigBytes {
		return 0, false, nil, fmt.Errorf("%s exceeds %d bytes", Filename, maximumConfigBytes)
	}
	return info.Mode().Perm(), true, contents, nil
}

// decode parses exactly one strict YAML document.
func decode(contents []byte) (config, error) {
	decoder := yaml.NewDecoder(io.LimitReader(strings.NewReader(string(contents)), maximumConfigBytes+1))
	decoder.KnownFields(true)
	var configuration config
	if err := decoder.Decode(&configuration); err != nil {
		return config{}, fmt.Errorf("decode %s: %w", Filename, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return config{}, fmt.Errorf("%s must contain one YAML document", Filename)
		}
		return config{}, fmt.Errorf("decode trailing %s content: %w", Filename, err)
	}
	if err := validate(configuration); err != nil {
		return config{}, fmt.Errorf("validate %s: %w", Filename, err)
	}
	return configuration, nil
}

// replace stages canonical bytes beside the repository file and rechecks the revision before publication.
func replace(checkoutRoot string, contents []byte, expectedRevision string) (replaceErr error) {
	mode, found, currentContents, err := read(checkoutRoot)
	if err != nil {
		return err
	}
	currentRevision := ""
	if found {
		currentRevision = revision(currentContents)
	} else {
		mode = 0o644
	}
	if currentRevision != expectedRevision {
		return ErrConfigurationChanged
	}
	temporary, err := os.CreateTemp(checkoutRoot, ".harbor-config-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", Filename, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && replaceErr == nil {
			replaceErr = removeErr
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set staged %s mode: %w", Filename, err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write staged %s: %w", Filename, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staged %s: %w", Filename, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged %s: %w", Filename, err)
	}
	_, found, currentContents, err = read(checkoutRoot)
	if err != nil {
		return err
	}
	currentRevision = ""
	if found {
		currentRevision = revision(currentContents)
	}
	if currentRevision != expectedRevision {
		return ErrConfigurationChanged
	}
	if err := replaceFile(temporaryPath, filepath.Join(checkoutRoot, Filename)); err != nil {
		return fmt.Errorf("publish %s: %w", Filename, err)
	}
	return nil
}

// revision returns the lowercase SHA-256 digest fencing one exact repository file.
func revision(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

// validate keeps the first schema deliberately narrow and free of implicit templates or execution.
func validate(configuration config) error {
	if configuration.Version != currentVersion {
		return fmt.Errorf("version must be %d", currentVersion)
	}
	if len(configuration.Environment) > maximumEnvironmentValues {
		return fmt.Errorf("environment contains more than %d bindings", maximumEnvironmentValues)
	}
	for name, selected := range configuration.Environment {
		if err := validateEnvironmentName(name); err != nil {
			return err
		}
		if selected.From != SourceProjectAddress {
			return fmt.Errorf("environment %s has unsupported source %q", name, selected.From)
		}
	}
	return nil
}

// validateEnvironmentName requires the portable process environment syntax Harbor already accepts.
func validateEnvironmentName(name string) error {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return errors.New("environment name must contain between 1 and 128 canonical UTF-8 bytes")
	}
	for index := range len(name) {
		character := name[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			character == '_' ||
			(index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return fmt.Errorf("environment name %q is not portable", name)
	}
	return nil
}

// resolveSource translates only explicitly allowlisted Harbor facts.
func resolveSource(source string, facts Facts) (string, error) {
	switch source {
	case SourceProjectAddress:
		if !facts.ProjectAddress.IsValid() || !facts.ProjectAddress.IsLoopback() {
			return "", errors.New("project address is unavailable")
		}
		return facts.ProjectAddress.String(), nil
	default:
		return "", fmt.Errorf("unsupported source %q", source)
	}
}
