// Package projectcreation defines stack-neutral authoring inputs and trusted project creator adapters.
package projectcreation

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var fieldKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

// Option describes a selectable value and its dependencies within the same field.
type Option struct {
	Value          string   `json:"value"`
	Label          string   `json:"label"`
	Description    string   `json:"description"`
	Preview        string   `json:"preview,omitempty"`
	Requires       []string `json:"requires,omitempty"`
	ExclusiveGroup string   `json:"exclusive_group,omitempty"`
}

// Field describes a graphical input supplied by a project creator.
type Field struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Kind        string            `json:"kind"`
	Step        string            `json:"step"`
	Required    bool              `json:"required,omitempty"`
	Default     json.RawMessage   `json:"default"`
	Options     []Option          `json:"options,omitempty"`
	Requires    []string          `json:"requires,omitempty"`
	DependsOn   string            `json:"depends_on,omitempty"`
	Role        string            `json:"role,omitempty"`
	When        map[string]string `json:"when,omitempty"`
}

// ProviderInfo describes a compiled-in project creator, without exposing executable paths.
type ProviderInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Schema is the provider-neutral graphical authoring contract.
type Schema struct {
	SchemaVersion int     `json:"schema_version"`
	ProviderID    string  `json:"provider_id"`
	Version       string  `json:"version"`
	Fields        []Field `json:"fields"`
}

// Request contains explicit user selections rather than shell commands or renderer configuration.
type Request struct {
	ProviderID    string            `json:"provider_id"`
	SchemaVersion int               `json:"schema_version"`
	Options       map[string]string `json:"options"`
}

// Validate prevents unsupported schema shapes from silently changing a graphical creation request.
func (schema Schema) Validate() error {
	if schema.SchemaVersion != 1 {
		return fmt.Errorf("unsupported creation schema version %d", schema.SchemaVersion)
	}
	if len(schema.Fields) < 1 || len(schema.Fields) > 64 {
		return fmt.Errorf("invalid creation field count")
	}
	seen := map[string]bool{}
	for _, field := range schema.Fields {
		if !fieldKey.MatchString(field.Key) || seen[field.Key] {
			return fmt.Errorf("invalid creation field %q", field.Key)
		}
		seen[field.Key] = true
		switch field.Kind {
		case "text", "path", "select", "multi", "boolean":
		default:
			return fmt.Errorf("unsupported creation field kind %q", field.Kind)
		}
		if field.Label == "" || field.Step == "" {
			return fmt.Errorf("creation field %q has no label or step", field.Key)
		}
	}
	for _, field := range schema.Fields {
		if len(field.Requires) > 0 && !seen[field.DependsOn] {
			return fmt.Errorf("creation field %q has an unknown dependency", field.Key)
		}
		for key := range field.When {
			if !seen[key] || key == field.Key {
				return fmt.Errorf("creation field %q has an invalid condition", field.Key)
			}
		}
	}
	return nil
}

// Available evaluates declarative conditions without teaching the form about a particular stack.
func (field Field) Available(values map[string]string) bool {
	for key, value := range field.When {
		if values[key] != value {
			return false
		}
	}
	selected := strings.Split(values[field.DependsOn], ",")
	for _, required := range field.Requires {
		found := false
		for _, value := range selected {
			if value == required {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Directory extracts a provider-declared destination role without assuming a flag or field name.
func (schema Schema) Directory(request Request) (string, error) {
	if err := schema.Validate(); err != nil {
		return "", err
	}
	if request.ProviderID != schema.ProviderID || request.SchemaVersion != schema.SchemaVersion {
		return "", fmt.Errorf("creation schema changed; reopen the project wizard")
	}
	for _, field := range schema.Fields {
		if field.Role == "directory" {
			path := request.Options[field.Key]
			if !filepath.IsAbs(path) {
				return "", fmt.Errorf("project directory must be absolute")
			}
			return filepath.Clean(path), nil
		}
	}
	return "", fmt.Errorf("project creator has no destination directory")
}
