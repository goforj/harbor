package machinepaths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveReturnsTheFixedValidatedGraph proves every consumer receives one coherent privileged layout.
func TestResolveReturnsTheFixedValidatedGraph(t *testing.T) {
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := Paths{
		Root:               root,
		StateDirectory:     filepath.Join(root, stateDirectoryName),
		OwnershipPath:      filepath.Join(root, stateDirectoryName, ownershipFilename),
		HostProjectionPath: filepath.Join(root, stateDirectoryName, hostProjectionFilename),
		ReplayDirectory:    filepath.Join(root, stateDirectoryName, replayDirectoryName),
		TicketsDirectory:   filepath.Join(root, ticketsDirectoryName),
		PendingDirectory:   filepath.Join(root, ticketsDirectoryName, pendingDirectoryName),
		ClaimsDirectory:    filepath.Join(root, ticketsDirectoryName, claimsDirectoryName),
	}
	if paths != want {
		t.Fatalf("Resolve() = %#v, want %#v", paths, want)
	}
}

// TestResolvePreservesPlatformFailure proves unsupported or failed native discovery cannot fall back elsewhere.
func TestResolvePreservesPlatformFailure(t *testing.T) {
	want := errors.New("platform root unavailable")
	_, err := resolve(func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("resolve() error = %v, want wrapped %v", err, want)
	}
}

// TestBuildPathsRejectsUnsafeRoots proves privileged state cannot become relative or retain traversal spelling.
func TestBuildPathsRejectsUnsafeRoots(t *testing.T) {
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	separator := string(filepath.Separator)
	tests := []struct {
		name string
		root string
		want string
	}{
		{name: "empty", root: "", want: "empty"},
		{name: "relative", root: filepath.Join("var", "lib", "harbor"), want: "not absolute"},
		{name: "unclean", root: root + separator + ".." + separator + filepath.Base(root), want: "not clean"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildPaths(test.root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildPaths(%q) error = %v, want substring %q", test.root, err, test.want)
			}
		})
	}
}

// TestValidatePathsRejectsRedirectedFields proves no independently supplied path can escape the fixed graph.
func TestValidatePathsRejectsRedirectedFields(t *testing.T) {
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	paths, err := buildPaths(root)
	if err != nil {
		t.Fatalf("buildPaths() error = %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*Paths)
	}{
		{name: "root", mutate: func(paths *Paths) { paths.Root = filepath.Join(root, "other") }},
		{name: "state", mutate: func(paths *Paths) { paths.StateDirectory = filepath.Join(root, "other") }},
		{name: "ownership", mutate: func(paths *Paths) { paths.OwnershipPath = filepath.Join(root, "other.json") }},
		{name: "host projection", mutate: func(paths *Paths) { paths.HostProjectionPath = filepath.Join(root, "other.json") }},
		{name: "replay", mutate: func(paths *Paths) { paths.ReplayDirectory = filepath.Join(root, "other") }},
		{name: "tickets", mutate: func(paths *Paths) { paths.TicketsDirectory = filepath.Join(root, "other") }},
		{name: "pending", mutate: func(paths *Paths) { paths.PendingDirectory = filepath.Join(root, "other") }},
		{name: "claims", mutate: func(paths *Paths) { paths.ClaimsDirectory = filepath.Join(root, "other") }},
		{name: "empty ownership", mutate: func(paths *Paths) { paths.OwnershipPath = "" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := paths
			mutation.mutate(&changed)
			if err := validatePaths(changed, root); err == nil {
				t.Fatal("validatePaths() error = nil for redirected field")
			}
		})
	}
}
