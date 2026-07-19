package projectdiscovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// writeProjectMarker creates a marker whose non-allowlisted content remains irrelevant to discovery.
func writeProjectMarker(t *testing.T, root string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".goforj.yml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write project marker: %v", err)
	}
}

// canonicalTestPath resolves platform aliases so expectations match the identity discovery promises callers.
func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve canonical test path: %v", err)
	}
	return filepath.Clean(canonical)
}

// TestDiscoverBuildsCanonicalMetadataWithoutInterpretingLifecycle proves commands and malformed unrelated topology remain inert.
func TestDiscoverBuildsCanonicalMetadataWithoutInterpretingLifecycle(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Orders API")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	writeProjectMarker(t, root, "not: [valid yaml\nexec: rm -rf /\n")
	discoverer := NewDiscoverer()

	first, err := discoverer.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover project: %v", err)
	}
	second, err := discoverer.Discover(t.Context(), filepath.Join(parent, ".", "Orders API"))
	if err != nil {
		t.Fatalf("rediscover project alias: %v", err)
	}
	if first != second {
		t.Fatalf("discoveries differ: first %#v second %#v", first, second)
	}
	if first.Root != canonicalTestPath(t, root) || first.Name != "Orders API" || first.Slug != "orders-api" {
		t.Fatalf("discovery = %#v, want canonical Orders API metadata", first)
	}

	at := time.Date(2026, time.July, 18, 19, 0, 0, 123, time.FixedZone("offset", 3600))
	project, err := first.ProjectSnapshot("project-orders", at)
	if err != nil {
		t.Fatalf("project snapshot: %v", err)
	}
	if project.ID != "project-orders" || project.Path != first.Root || project.State != domain.ProjectStopped || project.Favorite {
		t.Fatalf("project snapshot = %#v, want stopped discovered project", project)
	}
	if project.UpdatedAt != at.UTC() {
		t.Fatalf("updated time = %s, want %s", project.UpdatedAt, at.UTC())
	}
	if project.Apps == nil || project.Services == nil || project.Resources == nil || len(project.Apps)+len(project.Services)+len(project.Resources) != 0 {
		t.Fatalf("project collections = %#v %#v %#v, want initialized empty", project.Apps, project.Services, project.Resources)
	}
}

// TestDiscoverUsesBasicGoForjNameMetadata proves runtime-facing APP_NAME wins without loading any other env key.
func TestDiscoverUsesBasicGoForjNameMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ditracker")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	writeProjectMarker(t, root, "project_name: Config Name\nmodule_name: example.test/app\ndev:\n  pre:\n    - cmd: never-run\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("APP_KEY=secret\nAPP_NAME='Diablo Immortal Tracker'\nDB_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatalf("write project env: %v", err)
	}

	discovery, err := NewDiscoverer().Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover project: %v", err)
	}
	if discovery.Name != "Diablo Immortal Tracker" || discovery.Slug != "diablo-immortal-tracker" {
		t.Fatalf("discovery name/slug = %q/%q", discovery.Name, discovery.Slug)
	}
}

// TestDiscoverUsesLastDotEnvAppName proves duplicate-key handling matches normal dotenv overwrite semantics.
func TestDiscoverUsesLastDotEnvAppName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "orders")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	writeProjectMarker(t, root, "project_name: Marker Name\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("APP_NAME=First Name\nUNRELATED=secret\nAPP_NAME='Final Name'\n"), 0o600); err != nil {
		t.Fatalf("write project env: %v", err)
	}

	discovery, err := NewDiscoverer().Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover project: %v", err)
	}
	if discovery.Name != "Final Name" || discovery.Slug != "final-name" {
		t.Fatalf("discovery name/slug = %q/%q, want final assignment", discovery.Name, discovery.Slug)
	}
}

// TestDiscoverFallsBackThroughMarkerAndExample proves only the reviewed basic name sources affect presentation.
func TestDiscoverFallsBackThroughMarkerAndExample(t *testing.T) {
	for _, test := range []struct {
		name    string
		marker  string
		example string
		want    string
	}{
		{name: "marker", marker: "project_name: Marker Name\n", example: "APP_NAME=Example Name\n", want: "Marker Name"},
		{name: "example", marker: "module_name: example.test/app\n", example: "APP_NAME=Example Name\n", want: "Example Name"},
		{name: "directory", marker: "module_name: example.test/app\n", want: "project-root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project-root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatalf("create project root: %v", err)
			}
			writeProjectMarker(t, root, test.marker)
			if test.example != "" {
				if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(test.example), 0o600); err != nil {
					t.Fatalf("write example env: %v", err)
				}
			}
			discovery, err := NewDiscoverer().Discover(t.Context(), root)
			if err != nil {
				t.Fatalf("discover project: %v", err)
			}
			if discovery.Name != test.want {
				t.Fatalf("name = %q, want %q", discovery.Name, test.want)
			}
		})
	}
}

// TestReadDotEnvAppNameRejectsMetadataDirectories proves optional metadata cannot be scanned through non-file paths.
func TestReadDotEnvAppNameRejectsMetadataDirectories(t *testing.T) {
	for _, name := range []string{".env", ".env.example"} {
		t.Run(name, func(t *testing.T) {
			metadata := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(metadata, 0o700); err != nil {
				t.Fatalf("create metadata directory: %v", err)
			}
			if _, err := readDotEnvAppName(metadata); err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("readDotEnvAppName() error = %v, want regular-file rejection", err)
			} else {
				var invalid *InvalidProjectError
				if !errors.As(err, &invalid) {
					t.Fatalf("readDotEnvAppName() error = %T, want InvalidProjectError", err)
				}
			}
		})
	}
}

// TestValidateOpenedMetadataFileRejectsDescriptorSubstitution covers both non-file and changed-object open races.
func TestValidateOpenedMetadataFileRejectsDescriptorSubstitution(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original")
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(original, []byte("APP_NAME=Original\n"), 0o600); err != nil {
		t.Fatalf("write original metadata: %v", err)
	}
	if err := os.WriteFile(replacement, []byte("APP_NAME=Replacement\n"), 0o600); err != nil {
		t.Fatalf("write replacement metadata: %v", err)
	}
	beforeOpen, err := os.Stat(original)
	if err != nil {
		t.Fatalf("inspect original metadata: %v", err)
	}

	replacementFile, err := os.Open(replacement)
	if err != nil {
		t.Fatalf("open replacement metadata: %v", err)
	}
	defer replacementFile.Close()
	if err := validateOpenedMetadataFile(original, beforeOpen, replacementFile); err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("replacement validation error = %v, want changed-object rejection", err)
	} else {
		var invalid *InvalidProjectError
		if !errors.As(err, &invalid) {
			t.Fatalf("replacement validation error = %T, want InvalidProjectError", err)
		}
	}

	directoryFile, err := os.Open(root)
	if err != nil {
		t.Fatalf("open metadata directory: %v", err)
	}
	defer directoryFile.Close()
	if err := validateOpenedMetadataFile(original, beforeOpen, directoryFile); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory validation error = %v, want regular-file rejection", err)
	} else {
		var invalid *InvalidProjectError
		if !errors.As(err, &invalid) {
			t.Fatalf("directory validation error = %T, want InvalidProjectError", err)
		}
	}

	closedFile, err := os.Open(original)
	if err != nil {
		t.Fatalf("open metadata for closed-descriptor test: %v", err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatalf("close metadata descriptor: %v", err)
	}
	err = validateOpenedMetadataFile(original, beforeOpen, closedFile)
	var invalid *InvalidProjectError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("closed descriptor validation error = %T / %v, want raw I/O failure", err, err)
	}
}

// TestScanMetadataLinesPreservesOpenResourceFailures proves daemon exhaustion and device faults are not mislabeled as user metadata invalidity.
func TestScanMetadataLinesPreservesOpenResourceFailures(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(filename, []byte("APP_NAME=Orders\n"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	for _, resourceErr := range []error{syscall.EMFILE, syscall.EIO} {
		err := scanMetadataLinesWithOpener(filename, func(string) (bool, error) {
			return false, nil
		}, func(string) (*os.File, error) {
			return nil, resourceErr
		})
		var invalid *InvalidProjectError
		if !errors.Is(err, resourceErr) || errors.As(err, &invalid) {
			t.Fatalf("open failure = %T / %v, want raw %v", err, err, resourceErr)
		}
	}
}

// TestScanMetadataLinesClassifiesOpenPermission proves unreadable selected metadata is actionable user input.
func TestScanMetadataLinesClassifiesOpenPermission(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(filename, []byte("APP_NAME=Orders\n"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	err := scanMetadataLinesWithOpener(filename, func(string) (bool, error) {
		return false, nil
	}, func(string) (*os.File, error) {
		return nil, os.ErrPermission
	})
	var invalid *InvalidProjectError
	if !errors.Is(err, os.ErrPermission) || !errors.As(err, &invalid) {
		t.Fatalf("permission failure = %T / %v, want wrapped InvalidProjectError", err, err)
	}
}

// TestScanMetadataLinesClassifiesOpenPathReplacement proves a disappearing checked file remains a correctable selection error.
func TestScanMetadataLinesClassifiesOpenPathReplacement(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".env.example")
	if err := os.WriteFile(filename, []byte("APP_NAME=Orders\n"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	err := scanMetadataLinesWithOpener(filename, func(string) (bool, error) {
		return false, nil
	}, func(string) (*os.File, error) {
		return nil, os.ErrNotExist
	})
	var invalid *InvalidProjectError
	if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &invalid) {
		t.Fatalf("replacement failure = %T / %v, want wrapped InvalidProjectError", err, err)
	}
}

// TestDiscoverClassifiesInvalidMetadata covers parser, line, and total-size failures at the user-correctable boundary.
func TestDiscoverClassifiesInvalidMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		marker   string
		dotenv   string
		want     string
		filename string
	}{
		{name: "dotenv syntax", marker: "module_name: example.test/app\n", dotenv: "APP_NAME='unterminated\n", want: "parse APP_NAME", filename: ".env"},
		{name: "marker syntax", marker: "project_name: [unterminated\n", want: "parse project_name"},
		{name: "line bound", marker: "module_name: example.test/app\n", dotenv: strings.Repeat("x", maximumMetadataLine+1), want: "64 kibibytes", filename: ".env.example"},
		{name: "file bound", marker: "module_name: example.test/app\n", dotenv: strings.Repeat("X=1\n", maximumMetadataBytes/4+1), want: "one mebibyte", filename: ".env.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatalf("create project root: %v", err)
			}
			writeProjectMarker(t, root, test.marker)
			if test.filename != "" {
				if err := os.WriteFile(filepath.Join(root, test.filename), []byte(test.dotenv), 0o600); err != nil {
					t.Fatalf("write project metadata: %v", err)
				}
			}
			_, err := NewDiscoverer().Discover(t.Context(), root)
			var invalid *InvalidProjectError
			if err == nil || !strings.Contains(err.Error(), test.want) || !errors.As(err, &invalid) {
				t.Fatalf("Discover() error = %T / %v, want InvalidProjectError containing %q", err, err, test.want)
			}
		})
	}
}

// TestDiscoverResolvesSymlinkAliases proves one checkout cannot acquire two canonical registration paths.
func TestDiscoverResolvesSymlinkAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hosted Windows symlink creation requires an optional developer policy")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "orders")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	writeProjectMarker(t, root, "opaque")
	alias := filepath.Join(parent, "orders-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("create project alias: %v", err)
	}
	discoverer := NewDiscoverer()

	direct, err := discoverer.Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover direct root: %v", err)
	}
	linked, err := discoverer.Discover(t.Context(), alias)
	if err != nil {
		t.Fatalf("discover linked root: %v", err)
	}
	if direct != linked {
		t.Fatalf("direct discovery %#v differs from symlink %#v", direct, linked)
	}
}

// TestDiscoverRejectsInvalidSelections covers every filesystem validation branch without invoking project code.
func TestDiscoverRejectsInvalidSelections(t *testing.T) {
	discoverer := NewDiscoverer()
	file := filepath.Join(t.TempDir(), "project-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write selection file: %v", err)
	}
	missingMarker := t.TempDir()
	directoryMarker := t.TempDir()
	if err := os.Mkdir(filepath.Join(directoryMarker, ".goforj.yml"), 0o700); err != nil {
		t.Fatalf("create marker directory: %v", err)
	}

	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: "", want: "non-empty"},
		{name: "whitespace", path: " " + missingMarker, want: "surrounding whitespace"},
		{name: "control", path: missingMarker + "\n", want: "surrounding whitespace"},
		{name: "missing path", path: filepath.Join(t.TempDir(), "missing"), want: "resolve project path"},
		{name: "file", path: file, want: "not a directory"},
		{name: "missing marker", path: missingMarker, want: ".goforj.yml was not found"},
		{name: "non-file marker", path: directoryMarker, want: "regular file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := discoverer.Discover(context.Background(), test.path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Discover(%q) error = %v, want %q", test.path, err, test.want)
			}
			var invalid *InvalidProjectError
			if !errors.As(err, &invalid) {
				t.Fatalf("Discover(%q) error = %T, want InvalidProjectError", test.path, err)
			}
		})
	}
}

// TestDiscoverHonorsCancellationBeforeFilesystemWork proves cancelled clients cannot start discovery.
func TestDiscoverHonorsCancellationBeforeFilesystemWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewDiscoverer().Discover(ctx, filepath.Join(t.TempDir(), "missing"))
	if err != context.Canceled {
		t.Fatalf("Discover() error = %v, want context canceled", err)
	}
}
