package projectenvironment

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestResolveReturnsSortedRepositoryBindings verifies the approved schema maps explicit names to Harbor facts.
func TestResolveReturnsSortedRepositoryBindings(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
version: 1
environment:
  MEILISEARCH_HOST:
    from: project.address
  API_ADVERTISED_HOST:
    from: project.address
`)

	overrides, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := []Override{
		{Name: "API_ADVERTISED_HOST", Value: "127.77.0.18", Source: SourceProjectAddress},
		{Name: "MEILISEARCH_HOST", Value: "127.77.0.18", Source: SourceProjectAddress},
	}
	if !reflect.DeepEqual(overrides, want) {
		t.Fatalf("Resolve() = %#v, want %#v", overrides, want)
	}
	configuration, err := Inspect(root)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	wantBindings := []Binding{
		{Name: "API_ADVERTISED_HOST", Source: SourceProjectAddress},
		{Name: "MEILISEARCH_HOST", Source: SourceProjectAddress},
	}
	if !reflect.DeepEqual(configuration.Bindings, wantBindings) || len(configuration.Revision) != 64 {
		t.Fatalf("Inspect() = %#v, want sorted bindings and revision", configuration)
	}
}

// TestResolveTreatsMissingConfigAsAnEmptyContract keeps adoption optional.
func TestResolveTreatsMissingConfigAsAnEmptyContract(t *testing.T) {
	overrides, err := Resolve(t.TempDir(), Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")})
	if err != nil || overrides == nil || len(overrides) != 0 {
		t.Fatalf("Resolve() = %#v, %v; want initialized empty bindings", overrides, err)
	}
}

// TestResolveRejectsBroadOrAmbiguousConfigShapes keeps repository configuration declarative and fail-closed.
func TestResolveRejectsBroadOrAmbiguousConfigShapes(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "unknown version", content: "version: 2\n"},
		{name: "unknown top-level field", content: "version: 1\ncommand: whoami\n"},
		{name: "unknown binding field", content: "version: 1\nenvironment:\n  APP_HOST:\n    value: unsafe\n"},
		{name: "missing source", content: "version: 1\nenvironment:\n  APP_HOST: {}\n"},
		{name: "unknown source", content: "version: 1\nenvironment:\n  APP_HOST:\n    from: shell.output\n"},
		{name: "empty name", content: "version: 1\nenvironment:\n  ? \"\"\n  : from: project.address\n"},
		{name: "invalid name", content: "version: 1\nenvironment:\n  APP-HOST:\n    from: project.address\n"},
		{name: "leading digit", content: "version: 1\nenvironment:\n  1APP_HOST:\n    from: project.address\n"},
		{name: "duplicate name", content: "version: 1\nenvironment:\n  APP_HOST:\n    from: project.address\n  APP_HOST:\n    from: project.address\n"},
		{name: "multiple documents", content: "version: 1\n---\nversion: 1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, test.content)
			if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
				t.Fatal("Resolve() accepted invalid configuration")
			}
		})
	}
}

// TestResolveRejectsConfigOutsideItsResourceBounds covers filesystem and collection limits.
func TestResolveRejectsConfigOutsideItsResourceBounds(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(external, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(root, Filename)); err != nil {
		t.Fatalf("symlink config: %v", err)
	}
	if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
		t.Fatal("Resolve() accepted symlink config")
	}

	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, Filename), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
		t.Fatal("Resolve() accepted directory config")
	}

	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), make([]byte, maximumConfigBytes+1), 0o600); err != nil {
		t.Fatalf("write oversized config: %v", err)
	}
	if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
		t.Fatal("Resolve() accepted oversized config")
	}

	root = t.TempDir()
	var bindings strings.Builder
	bindings.WriteString("version: 1\nenvironment:\n")
	for index := range maximumEnvironmentValues + 1 {
		_, _ = fmt.Fprintf(&bindings, "  APP_HOST_%d:\n    from: project.address\n", index)
	}
	writeConfig(t, root, bindings.String())
	if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
		t.Fatal("Resolve() accepted too many bindings")
	}
}

// TestResolveRejectsUnavailableProjectAddresses covers missing and non-loopback Harbor facts.
func TestResolveRejectsUnavailableProjectAddresses(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "version: 1\nenvironment:\n  APP_HOST:\n    from: project.address\n")
	for _, address := range []netip.Addr{
		{},
		netip.MustParseAddr("192.0.2.10"),
	} {
		if _, err := Resolve(root, Facts{ProjectAddress: address}); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("Resolve() address %v error = %v, want unavailable", address, err)
		}
	}
}

// TestResolveRejectsEnvironmentNamesOutsideThePortableBounds covers canonical name length validation.
func TestResolveRejectsEnvironmentNamesOutsideThePortableBounds(t *testing.T) {
	root := t.TempDir()
	name := "A" + strings.Repeat("B", 128)
	writeConfig(t, root, fmt.Sprintf("version: 1\nenvironment:\n  %s:\n    from: project.address\n", name))
	if _, err := Resolve(root, Facts{ProjectAddress: netip.MustParseAddr("127.77.0.18")}); err == nil {
		t.Fatal("Resolve() accepted oversized environment name")
	}
}

// TestSaveCreatesAndRevisionFencesTheRepositoryContract verifies the visual editor cannot overwrite external changes.
func TestSaveCreatesAndRevisionFencesTheRepositoryContract(t *testing.T) {
	root := t.TempDir()
	missing, err := Inspect(root)
	if err != nil || missing.Bindings == nil || missing.Revision != "" {
		t.Fatalf("Inspect() missing = %#v, %v", missing, err)
	}
	contents := "version: 1\nenvironment:\n  MEILISEARCH_HOST:\n    from: project.address\n"
	saved, err := Save(root, contents, missing.Revision)
	if err != nil {
		t.Fatalf("Save() create error = %v", err)
	}
	want := []Binding{{Name: "MEILISEARCH_HOST", Source: SourceProjectAddress}}
	if !reflect.DeepEqual(saved.Bindings, want) || len(saved.Revision) != 64 {
		t.Fatalf("Save() create = %#v, want binding and revision", saved)
	}
	info, err := os.Stat(filepath.Join(root, Filename))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("created %s mode = %v, %v, want 0644", Filename, info, err)
	}
	if _, err := Save(root, "version: 1\nenvironment: {}\n", missing.Revision); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatalf("Save() stale error = %v, want ErrConfigurationChanged", err)
	}
	reloaded, err := Inspect(root)
	if err != nil || !reflect.DeepEqual(reloaded, saved) {
		t.Fatalf("Inspect() after stale save = %#v, %v, want %#v", reloaded, err, saved)
	}
}

// TestSaveValidatesNewContentsBeforeReplacingTheRepositoryContract preserves the last valid file.
func TestSaveValidatesNewContentsBeforeReplacingTheRepositoryContract(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "version: 1\nenvironment: {}\n")
	before, err := Inspect(root)
	if err != nil {
		t.Fatalf("Inspect() before invalid save error = %v", err)
	}
	if _, err := Save(root, "version: 1\ncommand: whoami\n", before.Revision); err == nil {
		t.Fatal("Save() accepted executable configuration")
	}
	after, err := Inspect(root)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("Inspect() after invalid save = %#v, %v, want %#v", after, err, before)
	}
}

// writeConfig publishes one test repository contract.
func writeConfig(t *testing.T, root string, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
