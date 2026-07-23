// Package projectenvironment resolves repository-owned environment bindings from Harbor facts.
package projectenvironment

import (
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
	configFilename           = ".harbor.yml"
	currentVersion           = 1
	maximumConfigBytes       = 64 * 1024
	maximumEnvironmentValues = 128

	// SourceProjectAddress resolves to Harbor's assigned project loopback address.
	SourceProjectAddress = "project.address"
)

// Facts contains Harbor-owned values available while preparing a project launch.
type Facts struct {
	ProjectAddress netip.Addr
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
	configuration, found, err := load(checkoutRoot)
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

// load reads one strict, bounded regular config file without following a repository symlink.
func load(checkoutRoot string) (config, bool, error) {
	path := filepath.Join(checkoutRoot, configFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return config{}, false, nil
	}
	if err != nil {
		return config{}, false, fmt.Errorf("inspect %s: %w", configFilename, err)
	}
	if !info.Mode().IsRegular() {
		return config{}, false, fmt.Errorf("%s must be a regular file", configFilename)
	}
	if info.Size() > maximumConfigBytes {
		return config{}, false, fmt.Errorf("%s exceeds %d bytes", configFilename, maximumConfigBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return config{}, false, fmt.Errorf("open %s: %w", configFilename, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(io.LimitReader(file, maximumConfigBytes+1))
	decoder.KnownFields(true)
	var configuration config
	if err := decoder.Decode(&configuration); err != nil {
		return config{}, false, fmt.Errorf("decode %s: %w", configFilename, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return config{}, false, fmt.Errorf("%s must contain one YAML document", configFilename)
		}
		return config{}, false, fmt.Errorf("decode trailing %s content: %w", configFilename, err)
	}
	if err := validate(configuration); err != nil {
		return config{}, false, fmt.Errorf("validate %s: %w", configFilename, err)
	}
	return configuration, true, nil
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
