package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRunBuildsOnlyFixedRootArtifacts verifies the hook cannot redirect either privileged executable.
func TestRunBuildsOnlyFixedRootArtifacts(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	workingDirectory := filepath.Join(repositoryRoot, "desktop", "build", "bin")
	if err := os.MkdirAll(workingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	runner := func(_ context.Context, directory string, name string, arguments ...string) error {
		calls = append(calls, append([]string{directory, name}, arguments...))
		if len(arguments) != 4 || arguments[0] != "build" || arguments[1] != "-o" {
			return fmt.Errorf("unexpected arguments: %v", arguments)
		}
		return os.WriteFile(arguments[2], []byte(arguments[3]), 0o600)
	}

	if err := run(t.Context(), workingDirectory, nil, runner); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	wantCalls := [][]string{
		{repositoryRoot, "go", "build", "-o", filepath.Join(workingDirectory, "devtools", "helper"), "./cmd/helper"},
		{repositoryRoot, "go", "build", "-o", filepath.Join(workingDirectory, "devtools", "devbootstrap"), "./cmd/devbootstrap"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("build calls = %#v, want %#v", calls, wantCalls)
	}
	for _, name := range []string{"helper", "devbootstrap"} {
		information, err := os.Stat(filepath.Join(workingDirectory, "devtools", name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if information.Mode() != artifactMode {
			t.Fatalf("%s mode = %v, want %v", name, information.Mode(), artifactMode)
		}
	}
}

// TestDevelopmentPathsAcceptsWailsWorkingDirectories keeps the hook independent of Wails' internal execution directory.
func TestDevelopmentPathsAcceptsWailsWorkingDirectories(t *testing.T) {
	t.Parallel()

	repositoryRoot := t.TempDir()
	desktopDirectory := filepath.Join(repositoryRoot, "desktop")
	binDirectory := filepath.Join(desktopDirectory, "build", "bin")
	for _, directory := range []string{desktopDirectory, binDirectory} {
		gotRoot, gotOutput, err := developmentPaths(directory)
		if err != nil {
			t.Fatalf("developmentPaths(%q) error = %v", directory, err)
		}
		if gotRoot != repositoryRoot || gotOutput != filepath.Join(binDirectory, "devtools") {
			t.Fatalf("developmentPaths(%q) = (%q, %q), want (%q, %q)", directory, gotRoot, gotOutput, repositoryRoot, filepath.Join(binDirectory, "devtools"))
		}
	}
}

// TestRunRejectsInputsOutsideTheWailsContract ensures invocation arguments and alternate destinations fail closed.
func TestRunRejectsInputsOutsideTheWailsContract(t *testing.T) {
	t.Parallel()

	runner := func(context.Context, string, string, ...string) error {
		t.Fatal("rejected run invoked the builder")
		return nil
	}
	tests := []struct {
		name      string
		directory string
		arguments []string
		want      string
	}{
		{name: "argument", directory: filepath.Join(t.TempDir(), "desktop", "build", "bin"), arguments: []string{"--output", "/tmp"}, want: "arguments are not supported"},
		{name: "directory", directory: filepath.Join(t.TempDir(), "application"), want: "is not desktop or desktop/build/bin"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(t.Context(), test.directory, test.arguments, runner)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
