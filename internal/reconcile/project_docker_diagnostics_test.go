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
	"unicode/utf8"

	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const (
	generatedComposeDiagnosticBytes      = 64 * 1024
	generatedComposeStateDiagnosticBytes = generatedComposeDiagnosticBytes + 4096
	generatedComposeOutputPages          = 8
)

// generatedComposeDiagnosticSnapshot keeps only the latest bounded evidence for one disposable fixture.
type generatedComposeDiagnosticSnapshot struct {
	StateAndOutput  []byte
	StateObservedAt time.Time
	Trace           []byte
	TraceObservedAt time.Time
}

// startGeneratedComposeDiagnosticSampler retains evidence before normal timeout cleanup retires sessions and traces.
func startGeneratedComposeDiagnosticSampler(
	ctx context.Context,
	interval time.Duration,
	capture func(context.Context) [3]generatedComposeDiagnosticSnapshot,
) func() [3]generatedComposeDiagnosticSnapshot {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var retained [3]generatedComposeDiagnosticSnapshot
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			observed := capture(ctx)
			for index, snapshot := range observed {
				if len(snapshot.StateAndOutput) > 0 {
					retained[index].StateAndOutput = boundGeneratedComposeDiagnostic(snapshot.StateAndOutput, generatedComposeStateDiagnosticBytes)
					retained[index].StateObservedAt = snapshot.StateObservedAt
				}
				if len(snapshot.Trace) > 0 {
					retained[index].Trace = boundGeneratedComposeDiagnostic(snapshot.Trace, generatedComposeDiagnosticBytes)
					retained[index].TraceObservedAt = snapshot.TraceObservedAt
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() [3]generatedComposeDiagnosticSnapshot {
		cancel()
		<-done
		return retained
	}
}

// captureGeneratedComposeDiagnostics observes only the exact three rendered fixtures without publishing or logging.
func captureGeneratedComposeDiagnostics(
	ctx context.Context,
	fixtureRoot *os.Root,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	projects []projectIdentityAcceptanceProject,
) [3]generatedComposeDiagnosticSnapshot {
	var snapshots [3]generatedComposeDiagnosticSnapshot
	for index, project := range projects {
		if index >= len(snapshots) || ctx.Err() != nil {
			break
		}
		name := fmt.Sprintf("project-%02d", index+1)
		if filepath.Clean(project.project.Root) != filepath.Join(fixtureRoot.Name(), name) {
			continue
		}
		observedAt := time.Now().UTC()
		snapshot, active := generatedComposeStateAndOutput(ctx, store, journal, supervisor, project)
		if active {
			snapshots[index].StateAndOutput = snapshot
			snapshots[index].StateObservedAt = observedAt
		}
		if trace, err := readGeneratedComposeLaunchTrace(fixtureRoot, name); err == nil {
			snapshots[index].Trace = trace
			snapshots[index].TraceObservedAt = observedAt
		}
	}
	return snapshots
}

// readGeneratedComposeOutputTail follows bounded cursor pages while retaining only the newest transcript bytes.
func readGeneratedComposeOutputTail(
	ctx context.Context,
	read func(uint64) (projectprocess.OutputChunk, error),
) (projectprocess.OutputChunk, error) {
	var retained projectprocess.OutputChunk
	var cursor uint64
	for page := 0; page < generatedComposeOutputPages; page++ {
		if err := ctx.Err(); err != nil {
			return retained, err
		}
		chunk, err := read(cursor)
		if err != nil {
			return retained, err
		}
		if !chunk.Available && !chunk.Historical {
			return retained, nil
		}
		if chunk.Reset && cursor != 0 {
			retained.Text = ""
		}
		text := retained.Text + chunk.Text
		truncated := retained.Truncated || chunk.Truncated
		if len(text) > generatedComposeDiagnosticBytes {
			text = text[len(text)-generatedComposeDiagnosticBytes:]
			for len(text) > 0 && !utf8.RuneStart(text[0]) {
				text = text[1:]
			}
			truncated = true
		}
		retained = projectprocess.OutputChunk{
			Available: chunk.Available, Historical: chunk.Historical,
			Reset: retained.Reset || chunk.Reset, Truncated: truncated,
			HasMore: chunk.HasMore, NextCursor: chunk.NextCursor, Text: text,
		}
		if !chunk.HasMore {
			return retained, nil
		}
		if chunk.NextCursor <= cursor && !chunk.Reset {
			return retained, fmt.Errorf("supervisor output cursor did not advance from %d", cursor)
		}
		cursor = chunk.NextCursor
	}
	return retained, nil
}

// generatedComposeStateAndOutput records lifecycle phase and exact session output without retaining credentials.
func generatedComposeStateAndOutput(
	ctx context.Context,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	project projectIdentityAcceptanceProject,
) ([]byte, bool) {
	var snapshot strings.Builder
	fmt.Fprintf(&snapshot, "FAILURE DIAGNOSTICS ONLY, not product success evidence\ncaptured_at=%s\nproject=%s\n", time.Now().UTC().Format(time.RFC3339Nano), project.id)
	record, projectErr := store.Project(ctx, project.id)
	fmt.Fprintf(&snapshot, "project_state=%s revision=%d error=%v\n", record.Project.State, record.Revision, projectErr)
	operation, operationErr := journal.OperationByIntent(ctx, project.intent)
	fmt.Fprintf(&snapshot, "start_operation=%s state=%s phase=%s error=%v\n", operation.Operation.ID, operation.Operation.State, operation.Operation.Phase, operationErr)
	if operation.Operation.Problem != nil {
		fmt.Fprintf(&snapshot, "start_problem=%s: %s\n", operation.Operation.Problem.Code, operation.Operation.Problem.Message)
	}
	session, sessionErr := store.ActiveProjectSession(ctx, project.id)
	fmt.Fprintf(&snapshot, "session=%s state=%s error=%v\n", session.ID, session.State, sessionErr)
	outputAvailable := false
	if sessionErr == nil {
		output, outputErr := readGeneratedComposeOutputTail(ctx, func(cursor uint64) (projectprocess.OutputChunk, error) {
			if chunk := supervisor.ReadOutput(project.id, session.ID, cursor); chunk.Available {
				return chunk, nil
			}
			return supervisor.ReadOutputHistory(project.id, session.ID, cursor)
		})
		outputAvailable = outputErr == nil && (output.Available || output.Historical)
		fmt.Fprintf(&snapshot, "supervisor_output available=%t historical=%t truncated=%t has_more=%t next_cursor=%d page_limit=%d error=%v\n", output.Available, output.Historical, output.Truncated, output.HasMore, output.NextCursor, generatedComposeOutputPages, outputErr)
		metadata := boundGeneratedComposeDiagnostic([]byte(snapshot.String()), generatedComposeStateDiagnosticBytes-generatedComposeDiagnosticBytes)
		return append(metadata, []byte(output.Text)...), outputAvailable && ctx.Err() == nil
	}
	return []byte(snapshot.String()), outputAvailable && ctx.Err() == nil
}

// retainGeneratedComposeFailureDiagnostics preserves only disposable fixture evidence before LIFO teardown.
func retainGeneratedComposeFailureDiagnostics(
	t *testing.T,
	fixtureRoot *os.Root,
	evidenceDirectory string,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	projects []projectIdentityAcceptanceProject,
	history [3]generatedComposeDiagnosticSnapshot,
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
		snapshot, _ := generatedComposeStateAndOutput(ctx, store, journal, supervisor, project)
		retainGeneratedComposeDiagnostic(t, destination, name+"-state-and-output.txt", snapshot, generatedComposeStateDiagnosticBytes)
		if index >= len(history) {
			continue
		}
		previous := history[index]
		if len(previous.StateAndOutput) > 0 {
			retainGeneratedComposeDiagnostic(t, destination, name+"-before-retirement.txt", previous.StateAndOutput, generatedComposeStateDiagnosticBytes)
		}
		trace, err := readGeneratedComposeLaunchTrace(fixtureRoot, name)
		traceObservedAt := time.Now().UTC()
		if err != nil {
			t.Logf("read Docker fixture %q forj-dev.log: %v", project.id, err)
			trace = previous.Trace
			traceObservedAt = previous.TraceObservedAt
		}
		metadata := fmt.Sprintf("active_state_captured_at=%s\ntrace_captured_at=%s\n", previous.StateObservedAt.Format(time.RFC3339Nano), traceObservedAt.Format(time.RFC3339Nano))
		retainGeneratedComposeDiagnostic(t, destination, name+"-capture-times.txt", []byte(metadata), generatedComposeDiagnosticBytes)
		if len(trace) == 0 {
			continue
		}
		retainGeneratedComposeDiagnostic(t, destination, name+"-forj-dev.log", trace, generatedComposeDiagnosticBytes)
	}
}

// retainGeneratedComposeDiagnostic bounds both uploaded files and test output, without overwriting existing files.
func retainGeneratedComposeDiagnostic(t *testing.T, destination *os.Root, name string, contents []byte, maximumBytes int) {
	t.Helper()
	contents = boundGeneratedComposeDiagnostic(contents, maximumBytes)
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

// boundGeneratedComposeDiagnostic owns one immutable bounded copy for memory, files, and test output.
func boundGeneratedComposeDiagnostic(contents []byte, maximumBytes int) []byte {
	if len(contents) > maximumBytes {
		marker := []byte("\n[additional failure diagnostics omitted]\n")
		result := append([]byte(nil), contents[:maximumBytes-len(marker)]...)
		return append(result, marker...)
	}
	return append([]byte(nil), contents...)
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
	retainGeneratedComposeDiagnostic(t, root, "project-01-state-and-output.txt", contents, generatedComposeDiagnosticBytes)
	got, err := root.ReadFile("project-01-state-and-output.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != generatedComposeDiagnosticBytes || !strings.HasSuffix(string(got), "\n[additional failure diagnostics omitted]\n") {
		t.Fatalf("artifact lacks a bounded truncation marker: %d bytes", len(got))
	}
	retainGeneratedComposeDiagnostic(t, root, "project-01-state-and-output.txt", []byte("replacement"), generatedComposeDiagnosticBytes)
	again, err := root.ReadFile("project-01-state-and-output.txt")
	if err != nil || string(again) != string(got) {
		t.Fatalf("existing diagnostics overwritten: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "failure-diagnostics" {
		t.Fatalf("failure capture created success evidence: %v, %v", entries, err)
	}
}

// TestGeneratedComposeDiagnosticSamplerBoundsReplacement proves missing later reads preserve the latest owned bounded copies.
func TestGeneratedComposeDiagnosticSamplerBoundsReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observed := make(chan int, 1)
	var calls int
	latest := []byte("latest-" + strings.Repeat("x", generatedComposeStateDiagnosticBytes))
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	stop := startGeneratedComposeDiagnosticSampler(ctx, time.Millisecond, func(ctx context.Context) [3]generatedComposeDiagnosticSnapshot {
		calls++
		var result [3]generatedComposeDiagnosticSnapshot
		for index := range result {
			switch calls {
			case 1:
				result[index] = generatedComposeDiagnosticSnapshot{StateAndOutput: []byte("old"), Trace: []byte("old"), StateObservedAt: at, TraceObservedAt: at}
			case 2:
				result[index] = generatedComposeDiagnosticSnapshot{StateAndOutput: latest, Trace: latest, StateObservedAt: at.Add(time.Second), TraceObservedAt: at.Add(time.Second)}
			}
		}
		select {
		case observed <- calls:
		case <-ctx.Done():
		}
		return result
	})
	defer stop()
	for {
		select {
		case call := <-observed:
			if call < 3 {
				continue
			}
		case <-ctx.Done():
			t.Fatal("sampler did not reach a missing observation")
		}
		break
	}
	retained := stop()
	latest[0] = 'z'
	for index, snapshot := range retained {
		for field, contents := range [][]byte{snapshot.StateAndOutput, snapshot.Trace} {
			wantBytes := []int{generatedComposeStateDiagnosticBytes, generatedComposeDiagnosticBytes}[field]
			if len(contents) != wantBytes || !strings.HasPrefix(string(contents), "latest-") || !strings.HasSuffix(string(contents), "\n[additional failure diagnostics omitted]\n") {
				t.Fatalf("fixture %d lost its latest bounded owned copy: %d bytes", index, len(contents))
			}
		}
		if !snapshot.StateObservedAt.Equal(at.Add(time.Second)) || !snapshot.TraceObservedAt.Equal(at.Add(time.Second)) {
			t.Fatalf("fixture %d lost observation times: %#v", index, snapshot)
		}
	}
}

// TestGeneratedComposeDiagnosticSamplerStopsBeforeCleanup proves cancellation joins capture before emission and fixture cleanup.
func TestGeneratedComposeDiagnosticSamplerStopsBeforeCleanup(t *testing.T) {
	var order []string
	t.Run("cleanup", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		started := make(chan struct{})
		finished := make(chan struct{})
		stop := startGeneratedComposeDiagnosticSampler(ctx, time.Hour, func(ctx context.Context) [3]generatedComposeDiagnosticSnapshot {
			close(started)
			<-ctx.Done()
			close(finished)
			return [3]generatedComposeDiagnosticSnapshot{{StateAndOutput: []byte("last snapshot")}}
		})
		t.Cleanup(func() { order = append(order, "fixture cleanup") })
		t.Cleanup(func() {
			retained := stop()
			select {
			case <-finished:
			default:
				t.Fatal("sampler stop returned while capture was still running")
			}
			order = append(order, "sampler stopped")
			if string(retained[0].StateAndOutput) != "last snapshot" {
				t.Fatal("last capture was discarded during cancellation")
			}
			order = append(order, "failure emission")
		})
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("sampler did not start")
		}
	})
	if strings.Join(order, ",") != "sampler stopped,failure emission,fixture cleanup" {
		t.Fatalf("cleanup order = %v", order)
	}
}

// TestGeneratedComposeDiagnosticSamplerRetainsRetiredTrace proves production-style trace removal cannot erase the last observation.
func TestGeneratedComposeDiagnosticSamplerRetainsRetiredTrace(t *testing.T) {
	directory := t.TempDir()
	traceDirectory := filepath.Join(directory, "project-01", "_data", "harbor")
	if err := os.MkdirAll(traceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(traceDirectory, "forj-dev.log")
	if err := os.WriteFile(tracePath, []byte("fixture startup phase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observed := make(chan bool)
	transcript := strings.Repeat("earlier fixture output\n", 8000) + "UNIQUE FINAL STARTUP FAILURE\n"
	stop := startGeneratedComposeDiagnosticSampler(ctx, time.Millisecond, func(ctx context.Context) [3]generatedComposeDiagnosticSnapshot {
		trace, err := readGeneratedComposeLaunchTrace(root, "project-01")
		var stateAndOutput []byte
		if err == nil {
			output, readErr := readGeneratedComposeOutputTail(ctx, func(cursor uint64) (projectprocess.OutputChunk, error) {
				end := min(int(cursor)+projectprocess.MaximumOutputChunkBytes, len(transcript))
				return projectprocess.OutputChunk{Available: true, Text: transcript[int(cursor):end], NextCursor: uint64(end), HasMore: end < len(transcript)}, nil
			})
			if readErr == nil {
				stateAndOutput = append([]byte("phase=waiting-for-readiness\ncaptured_at=2026-10-20T08:00:00Z\n"), []byte(output.Text)...)
			}
		}
		select {
		case observed <- err == nil:
		case <-ctx.Done():
		}
		return [3]generatedComposeDiagnosticSnapshot{{Trace: trace, TraceObservedAt: time.Now().UTC(), StateAndOutput: stateAndOutput, StateObservedAt: time.Now().UTC()}}
	})
	defer stop()
	select {
	case available := <-observed:
		if !available {
			t.Fatal("fixture trace was unavailable before retirement")
		}
	case <-ctx.Done():
		t.Fatal("sampler did not read initial trace")
	}
	if err := os.RemoveAll(filepath.Join(directory, "project-01", "_data")); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case available := <-observed:
			if available {
				continue
			}
		case <-ctx.Done():
			t.Fatal("sampler did not observe trace retirement")
		}
		break
	}
	retained := stop()
	if !strings.HasPrefix(string(retained[0].StateAndOutput), "phase=waiting-for-readiness\ncaptured_at=") || !strings.HasSuffix(string(retained[0].StateAndOutput), "UNIQUE FINAL STARTUP FAILURE\n") || len(retained[0].StateAndOutput) > generatedComposeStateDiagnosticBytes {
		t.Fatal("retirement lost metadata or the final failure beyond the first output page")
	}
	if string(retained[0].Trace) != "fixture startup phase\n" || retained[0].TraceObservedAt.IsZero() {
		t.Fatalf("trace retirement discarded captured evidence: %#v", retained[0])
	}
}

// TestGeneratedComposeOutputTail proves cursor paging retains late failures and remains bounded under source changes.
func TestGeneratedComposeOutputTail(t *testing.T) {
	t.Run("newest output", func(t *testing.T) {
		text := strings.Repeat("old output\n", 20000) + "UNIQUE LATE FAILURE\n"
		reads := 0
		chunk, err := readGeneratedComposeOutputTail(t.Context(), func(cursor uint64) (projectprocess.OutputChunk, error) {
			reads++
			end := min(int(cursor)+projectprocess.MaximumOutputChunkBytes, len(text))
			return projectprocess.OutputChunk{Historical: true, Text: text[int(cursor):end], NextCursor: uint64(end), HasMore: end < len(text)}, nil
		})
		if err != nil || reads < 2 || len(chunk.Text) != generatedComposeDiagnosticBytes || !strings.HasSuffix(chunk.Text, "UNIQUE LATE FAILURE\n") || !chunk.Truncated || chunk.HasMore || chunk.NextCursor != uint64(len(text)) || !chunk.Historical {
			t.Fatalf("tail metadata or final failure lost: reads=%d bytes=%d cursor=%d truncated=%t has_more=%t error=%v", reads, len(chunk.Text), chunk.NextCursor, chunk.Truncated, chunk.HasMore, err)
		}
	})
	t.Run("UTF-8 tail boundary", func(t *testing.T) {
		reads := 0
		var cursor uint64
		chunk, err := readGeneratedComposeOutputTail(t.Context(), func(uint64) (projectprocess.OutputChunk, error) {
			reads++
			text := strings.Repeat("界", 20000)
			if reads == 2 {
				text += "end"
			}
			cursor += uint64(len(text))
			return projectprocess.OutputChunk{Available: true, Text: text, NextCursor: cursor, HasMore: reads == 1}, nil
		})
		if err != nil || !utf8.ValidString(chunk.Text) || len(chunk.Text) > generatedComposeDiagnosticBytes || !strings.HasSuffix(chunk.Text, "end") {
			t.Fatalf("tail split a UTF-8 code point: bytes=%d error=%v", len(chunk.Text), err)
		}
	})
	t.Run("read error", func(t *testing.T) {
		want := fmt.Errorf("fixture history unavailable")
		_, err := readGeneratedComposeOutputTail(t.Context(), func(uint64) (projectprocess.OutputChunk, error) {
			return projectprocess.OutputChunk{}, want
		})
		if err != want {
			t.Fatalf("read error = %v, want %v", err, want)
		}
	})
	t.Run("page budget", func(t *testing.T) {
		reads := 0
		chunk, err := readGeneratedComposeOutputTail(t.Context(), func(cursor uint64) (projectprocess.OutputChunk, error) {
			reads++
			return projectprocess.OutputChunk{Available: true, Text: strings.Repeat("x", generatedComposeDiagnosticBytes), NextCursor: cursor + generatedComposeDiagnosticBytes, HasMore: true}, nil
		})
		if err != nil || reads != generatedComposeOutputPages || len(chunk.Text) != generatedComposeDiagnosticBytes || !chunk.HasMore || !chunk.Truncated {
			t.Fatalf("page budget not retained: reads=%d bytes=%d has_more=%t error=%v", reads, len(chunk.Text), chunk.HasMore, err)
		}
	})
	t.Run("nonadvancing cursor", func(t *testing.T) {
		_, err := readGeneratedComposeOutputTail(t.Context(), func(uint64) (projectprocess.OutputChunk, error) {
			return projectprocess.OutputChunk{Available: true, HasMore: true}, nil
		})
		if err == nil {
			t.Fatal("nonadvancing output cursor accepted")
		}
	})
	t.Run("reset", func(t *testing.T) {
		reads := 0
		chunk, err := readGeneratedComposeOutputTail(t.Context(), func(uint64) (projectprocess.OutputChunk, error) {
			reads++
			if reads == 1 {
				return projectprocess.OutputChunk{Available: true, Text: "old", NextCursor: 3, HasMore: true}, nil
			}
			return projectprocess.OutputChunk{Available: true, Text: "new", NextCursor: 3, Reset: true}, nil
		})
		if err != nil || chunk.Text != "new" || !chunk.Reset {
			t.Fatalf("reset output mixed with old cursor: %#v, %v", chunk, err)
		}
	})
	t.Run("retired during paging", func(t *testing.T) {
		reads := 0
		chunk, err := readGeneratedComposeOutputTail(t.Context(), func(uint64) (projectprocess.OutputChunk, error) {
			reads++
			if reads == 1 {
				return projectprocess.OutputChunk{Historical: true, Text: "last available", NextCursor: 14, HasMore: true}, nil
			}
			return projectprocess.OutputChunk{}, nil
		})
		if err != nil || chunk.Text != "last available" || !chunk.Historical || !chunk.HasMore {
			t.Fatalf("retirement discarded or overstated available output: %#v, %v", chunk, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := readGeneratedComposeOutputTail(ctx, func(uint64) (projectprocess.OutputChunk, error) {
			t.Fatal("read output after cancellation")
			return projectprocess.OutputChunk{}, nil
		})
		if err != context.Canceled {
			t.Fatalf("cancelled read returned %v", err)
		}
	})
}
