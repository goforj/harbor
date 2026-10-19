package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/internal/projectcreation"
)

// fakeProjectCreator records renderer calls and can hold a worker open to prove output delivery.
type fakeProjectCreator struct {
	failure error
	hold    chan struct{}
	started chan struct{}
	calls   int
}

// Schema supplies a stack-neutral destination without relying on GoForj field names.
func (creator *fakeProjectCreator) Schema(context.Context) (projectcreation.Schema, error) {
	return projectcreation.Schema{SchemaVersion: 1, ProviderID: "test-stack", Fields: []projectcreation.Field{{Key: "destination", Label: "Directory", Step: "Project", Kind: "path", Role: "directory"}}}, nil
}

// CreateWithOutput writes before completion so tests can observe true live output.
func (creator *fakeProjectCreator) CreateWithOutput(ctx context.Context, _ projectcreation.Request, writer io.Writer) error {
	creator.calls++
	_, _ = io.WriteString(writer, "rendering files\n")
	if creator.started != nil {
		close(creator.started)
	}
	if creator.hold != nil {
		select {
		case <-creator.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return creator.failure
}

// creationTestApp selects a second adapter to prove the desktop job flow has no GoForj field assumptions.
func creationTestApp(creator *fakeProjectCreator) (*App, *fakeControlClient) {
	app, client := connectedTestApp()
	adapter := app.projectCreators[projectcreation.GoForj.ID]
	adapter.info = projectcreation.ProviderInfo{ID: "test-stack", Name: "Test stack"}
	adapter.new = func() (projectCreator, error) { return creator, nil }
	app.projectCreators = map[string]projectCreatorAdapter{"test-stack": adapter}
	return app, client
}

// creationTestRequest uses an intentionally non-GoForj field key.
func creationTestRequest(t *testing.T) projectcreation.Request {
	t.Helper()
	return projectcreation.Request{ProviderID: "test-stack", SchemaVersion: 1, Options: map[string]string{"destination": filepath.Join(t.TempDir(), "new-project")}}
}

// waitCreation waits only for the worker belonging to this test.
func waitCreation(t *testing.T, app *App, id string) desktopwire.ProjectCreationProgress {
	t.Helper()
	app.projectCreationMu.Lock()
	done := app.projectCreationJobs[id].done
	app.projectCreationMu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("creation worker stalled")
	}
	progress, err := app.ProjectCreationProgress(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return progress
}

// TestCreateProjectSeparatesCreationAndRegistration verifies partial success and retry without regenerating files.
func TestCreateProjectSeparatesCreationAndRegistration(t *testing.T) {
	for _, mode := range []string{"success", "creation fails", "registration fails", "disconnected"} {
		t.Run(mode, func(t *testing.T) {
			creator := &fakeProjectCreator{}
			app, client := creationTestApp(creator)
			if mode == "creation fails" {
				creator.failure = errors.New("occupied directory")
			}
			if mode == "registration fails" {
				client.registerErr = errors.New("registration unavailable")
			}
			if mode == "disconnected" {
				app.client = nil
			}
			request := creationTestRequest(t)
			result, err := app.CreateProject(request)
			if mode == "creation fails" || mode == "disconnected" {
				if err == nil || client.registerPath != "" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result.Path != request.Options["destination"] || client.registerPath != result.Path {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if mode == "registration fails" {
				if result.Registration != nil || !strings.Contains(result.RegistrationError, "Project files are ready") {
					t.Fatal(result)
				}
				var id string
				for key := range app.projectCreationJobs {
					id = key
				}
				client.registerErr = nil
				if _, err := app.RetryProjectRegistration(id); err != nil {
					t.Fatal(err)
				}
				progress := waitCreation(t, app, id)
				if progress.Phase != "completed" || creator.calls != 1 {
					t.Fatalf("progress=%+v renderer calls=%d", progress, creator.calls)
				}
				if _, err := app.RetryProjectRegistration(id); err != nil || creator.calls != 1 {
					t.Fatal("completed retry was not idempotent")
				}
			} else if result.Registration == nil {
				t.Fatal("project not registered")
			}
		})
	}
}

// TestCreationStreamsAndRejectsUnsafeRetries proves observation precedes exit and retries cannot select arbitrary paths.
func TestCreationStreamsAndRejectsUnsafeRetries(t *testing.T) {
	creator := &fakeProjectCreator{hold: make(chan struct{}), started: make(chan struct{})}
	app, _ := creationTestApp(creator)
	progress, err := app.StartProjectCreation(creationTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	<-creator.started
	live, err := app.ProjectCreationProgress(progress.JobID, 0)
	if err != nil || !strings.Contains(live.Output, "rendering files") || live.Phase != "creating" {
		t.Fatalf("%+v %v", live, err)
	}
	if _, err := app.StartProjectCreation(creationTestRequest(t)); err == nil {
		t.Fatal("concurrent renderer accepted")
	}
	if _, err := app.RetryProjectRegistration(progress.JobID); err == nil {
		t.Fatal("active renderer retried")
	}
	if _, err := app.RetryProjectRegistration("/tmp/foreign"); err == nil {
		t.Fatal("foreign path accepted")
	}
	if _, err := app.ProjectCreationProgress(progress.JobID, live.Cursor+1); err == nil {
		t.Fatal("future cursor accepted")
	}
	if _, err := app.ProjectCreationProgress("unknown", 0); err == nil {
		t.Fatal("unknown job accepted")
	}
	close(creator.hold)
	finished := waitCreation(t, app, progress.JobID)
	delta, err := app.ProjectCreationProgress(progress.JobID, live.Cursor)
	if err != nil || delta.Output != "" || finished.Phase != "completed" {
		t.Fatalf("%+v %v", delta, err)
	}
}

// TestCreationFailureCannotRetryRegistration keeps incomplete directories out of daemon admission.
func TestCreationFailureCannotRetryRegistration(t *testing.T) {
	app, _ := creationTestApp(&fakeProjectCreator{failure: errors.New("render failed")})
	progress, err := app.StartProjectCreation(creationTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	finished := waitCreation(t, app, progress.JobID)
	if finished.Phase != "failed" || !strings.Contains(finished.Error, "were kept") {
		t.Fatal(finished)
	}
	if _, err := app.RetryProjectRegistration(progress.JobID); err == nil {
		t.Fatal("incomplete project admitted")
	}
	request := creationTestRequest(t)
	request.ProviderID = "unknown"
	if _, err := app.StartProjectCreation(request); err == nil {
		t.Fatal("unknown provider accepted")
	}
	request = creationTestRequest(t)
	request.Options["destination"] = "relative"
	if _, err := app.StartProjectCreation(request); err == nil {
		t.Fatal("relative path accepted")
	}
}

// TestCreationOutputRetention bounds memory and reports lost history without blocking the child.
func TestCreationOutputRetention(t *testing.T) {
	job := &projectCreationJob{}
	data := strings.Repeat("x", 512*1024)
	if n, err := io.WriteString(job, data); err != nil || n != len(data) {
		t.Fatal(n, err)
	}
	progress, err := job.read(0)
	if err != nil || !progress.Truncated || len(progress.Output) != 256*1024 || progress.Cursor != uint64(len(data)) {
		t.Fatalf("cursor=%d len=%d err=%v", progress.Cursor, len(progress.Output), err)
	}
}
