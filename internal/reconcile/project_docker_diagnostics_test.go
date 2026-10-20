//go:build (linux || darwin || windows) && projectidentityacceptance && projectdockeracceptance

package reconcile

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const generatedComposeDiagnosticBytes = 64 * 1024

// retainGeneratedComposeFailureDiagnostics preserves only disposable fixture evidence before LIFO teardown.
func retainGeneratedComposeFailureDiagnostics(
	t *testing.T,
	fixtureRoot *os.Root,
	evidenceDirectory string,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	projects []projectIdentityAcceptanceProject,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var destination *os.Root
	if evidenceDirectory != "" {
		var err error
		destination, err = generatedComposeFailureDirectory(evidenceDirectory)
		if err != nil {
			t.Logf("retain Docker failure diagnostics: %v", err)
		} else {
			defer destination.Close()
		}
	}
	for index, project := range projects {
		name := fmt.Sprintf("project-%02d", index+1)
		// Render owns exactly these direct children, never arbitrary registered checkout paths.
		if filepath.Clean(project.project.Root) != filepath.Join(fixtureRoot.Name(), name) {
			t.Logf("refuse Docker diagnostics for foreign fixture %q", project.id)
			continue
		}
		var snapshot strings.Builder
		fmt.Fprintf(&snapshot, "FAILURE DIAGNOSTICS ONLY, not product success evidence\nproject=%s\n", project.id)
		record, projectErr := store.Project(ctx, project.id)
		fmt.Fprintf(&snapshot, "project_state=%s revision=%d error=%v\n", record.Project.State, record.Revision, projectErr)
		operation, operationErr := journal.OperationByIntent(ctx, project.intent)
		fmt.Fprintf(&snapshot, "start_operation=%s state=%s phase=%s error=%v\n", operation.Operation.ID, operation.Operation.State, operation.Operation.Phase, operationErr)
		if operation.Operation.Problem != nil {
			fmt.Fprintf(&snapshot, "start_problem=%s: %s\n", operation.Operation.Problem.Code, operation.Operation.Problem.Message)
		}
		session, sessionErr := store.ActiveProjectSession(ctx, project.id)
		fmt.Fprintf(&snapshot, "session=%s state=%s error=%v\n", session.ID, session.State, sessionErr)
		if sessionErr == nil {
			output, outputErr := supervisor.ReadOutputHistory(project.id, session.ID, 0)
			fmt.Fprintf(&snapshot, "supervisor_output available=%t historical=%t truncated=%t has_more=%t next_cursor=%d error=%v\n%s", output.Available, output.Historical, output.Truncated, output.HasMore, output.NextCursor, outputErr, output.Text)
		}
		retainGeneratedComposeDiagnostic(t, destination, name+"-state-and-output.txt", []byte(snapshot.String()))
		trace, err := readGeneratedComposeLaunchTrace(fixtureRoot, name)
		if err != nil {
			t.Logf("read Docker fixture %q forj-dev.log: %v", project.id, err)
			continue
		}
		retainGeneratedComposeDiagnostic(t, destination, name+"-forj-dev.log", trace)
	}
}

// retainGeneratedComposeDiagnostic bounds both uploaded files and test output, without overwriting existing files.
func retainGeneratedComposeDiagnostic(t *testing.T, destination *os.Root, name string, contents []byte) {
	t.Helper()
	if len(contents) > generatedComposeDiagnosticBytes {
		marker := []byte("\n[additional failure diagnostics omitted]\n")
		contents = append(contents[:generatedComposeDiagnosticBytes-len(marker):generatedComposeDiagnosticBytes-len(marker)], marker...)
	}
	t.Logf("Docker failure diagnostics %s:\n%s", name, contents)
	if destination == nil {
		return
	}
	file, err := destination.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Logf("create Docker failure diagnostic %s: %v", name, err)
		return
	}
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		t.Logf("write Docker failure diagnostic %s: %v", name, err)
	}
}

// generatedComposeFailureDirectory keeps failure files apart from the unchanged success receipt schema.
func generatedComposeFailureDirectory(directory string) (*os.Root, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := parent.Mkdir(filepath.Base(absolute), 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	evidence, err := openGeneratedComposeDirectDirectory(parent, filepath.Base(absolute))
	if err != nil {
		return nil, err
	}
	defer evidence.Close()
	if err := evidence.Mkdir("failure-diagnostics", 0o700); err != nil {
		return nil, err
	}
	return openGeneratedComposeDirectDirectory(evidence, "failure-diagnostics")
}

// openGeneratedComposeDirectDirectory rejects symlinks and identity changes before any fixture bytes are read.
func openGeneratedComposeDirectDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("diagnostic directory %q must be a direct directory", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = child.Close()
		return nil, fmt.Errorf("diagnostic directory %q changed identity: %v", name, err)
	}
	return child, nil
}

// readGeneratedComposeLaunchTrace reads only an allowlisted regular fixture trace and retains its bounded tail.
func readGeneratedComposeLaunchTrace(root *os.Root, project string) ([]byte, error) {
	if project != "project-01" && project != "project-02" && project != "project-03" {
		return nil, fmt.Errorf("refuse non-fixture project %q", project)
	}
	for _, name := range []string{project, "_data", "harbor"} {
		next, err := openGeneratedComposeDirectDirectory(root, name)
		if err != nil {
			return nil, err
		}
		defer next.Close()
		root = next
	}
	before, err := root.Lstat("forj-dev.log")
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("fixture launch trace must be a direct regular file")
	}
	file, err := root.Open("forj-dev.log")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, fmt.Errorf("fixture launch trace changed identity: %v", err)
	}
	var prefix []byte
	if after.Size() > generatedComposeDiagnosticBytes {
		prefix = []byte("[earlier fixture launch output omitted]\n")
		if _, err := file.Seek(after.Size()-int64(generatedComposeDiagnosticBytes-len(prefix)), io.SeekStart); err != nil {
			return nil, err
		}
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(generatedComposeDiagnosticBytes-len(prefix))))
	return append(prefix, contents...), err
}

// TestGeneratedComposeLaunchTraceDiagnostics proves reads remain bounded and confined to direct disposable fixture files.
func TestGeneratedComposeLaunchTraceDiagnostics(t *testing.T) {
	for _, scenario := range []string{"small", "large", "missing", "directory-file", "project-link", "data-link", "harbor-link", "file-link", "foreign", "traversal"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			traceDirectory := filepath.Join(directory, "project-01", "_data", "harbor")
			if err := os.MkdirAll(traceDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			tracePath := filepath.Join(traceDirectory, "forj-dev.log")
			contents := "fixture output\n"
			if scenario == "large" {
				contents = strings.Repeat("x", generatedComposeDiagnosticBytes+20) + "final failure\n"
			}
			if err := os.WriteFile(tracePath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			project := "project-01"
			switch scenario {
			case "missing":
				if err := os.Remove(tracePath); err != nil {
					t.Fatal(err)
				}
			case "directory-file":
				if err := os.Remove(tracePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(tracePath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "project-link", "data-link", "harbor-link", "file-link":
				paths := map[string]string{
					"project-link": filepath.Join(directory, "project-01"),
					"data-link":    filepath.Join(directory, "project-01", "_data"),
					"harbor-link":  traceDirectory,
					"file-link":    tracePath,
				}
				path := paths[scenario]
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				// An in-root link is refused too, so containment alone cannot admit another fixture's output.
				if err := os.Symlink(path+"-original", path); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				project = t.TempDir()
			case "traversal":
				project = "../project-01"
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			got, err := readGeneratedComposeLaunchTrace(root, project)
			if scenario != "small" && scenario != "large" {
				if err == nil || len(got) != 0 {
					t.Fatalf("untrusted or unavailable trace returned %d bytes, %v", len(got), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "small" && string(got) != contents {
				t.Fatalf("trace = %q, want %q", got, contents)
			}
			if scenario == "large" && (len(got) != generatedComposeDiagnosticBytes || !strings.HasPrefix(string(got), "[earlier fixture launch output omitted]\n") || !strings.HasSuffix(string(got), "final failure\n")) {
				t.Fatalf("large trace did not retain a marked, bounded tail: %d bytes", len(got))
			}
		})
	}
}

// TestGeneratedComposeFailureDirectory proves diagnostics cannot overwrite receipts or follow output-directory symlinks.
func TestGeneratedComposeFailureDirectory(t *testing.T) {
	for _, scenario := range []string{"new", "existing-evidence", "evidence-link", "existing-diagnostics", "diagnostics-link"} {
		t.Run(scenario, func(t *testing.T) {
			parent := t.TempDir()
			directory := filepath.Join(parent, "evidence")
			if scenario != "new" && scenario != "evidence-link" {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "evidence-link":
				if err := os.Symlink(t.TempDir(), directory); err != nil {
					t.Fatal(err)
				}
			case "existing-diagnostics":
				if err := os.Mkdir(filepath.Join(directory, "failure-diagnostics"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "diagnostics-link":
				if err := os.Symlink(t.TempDir(), filepath.Join(directory, "failure-diagnostics")); err != nil {
					t.Fatal(err)
				}
			}
			root, err := generatedComposeFailureDirectory(directory)
			if scenario == "new" || scenario == "existing-evidence" {
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if _, err := os.Stat(filepath.Join(directory, "failure-diagnostics")); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || root != nil {
				t.Fatalf("unsafe diagnostic destination accepted: %v", err)
			}
		})
	}
}

// TestGeneratedComposeDiagnosticRetention proves artifacts remain bounded without overwrites or success receipts.
func TestGeneratedComposeDiagnosticRetention(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "evidence")
	root, err := generatedComposeFailureDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	contents := []byte(strings.Repeat("x", generatedComposeDiagnosticBytes+10))
	retainGeneratedComposeDiagnostic(t, root, "project-01-state-and-output.txt", contents)
	got, err := root.ReadFile("project-01-state-and-output.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != generatedComposeDiagnosticBytes || !strings.HasSuffix(string(got), "\n[additional failure diagnostics omitted]\n") {
		t.Fatalf("artifact lacks a bounded truncation marker: %d bytes", len(got))
	}
	retainGeneratedComposeDiagnostic(t, root, "project-01-state-and-output.txt", []byte("replacement"))
	again, err := root.ReadFile("project-01-state-and-output.txt")
	if err != nil || string(again) != string(got) {
		t.Fatalf("existing diagnostics overwritten: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "failure-diagnostics" {
		t.Fatalf("failure capture created success evidence: %v, %v", entries, err)
	}
}
