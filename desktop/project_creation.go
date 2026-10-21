package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/desktop/internal/projectreadiness"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectcreation"
)

// projectCreator owns one stack's schema and renderer, without exposing a command runner to the frontend.
type projectCreator interface {
	Schema(context.Context) (projectcreation.Schema, error)
	CreateWithOutput(context.Context, projectcreation.Request, io.Writer) error
}

// projectCreatorAdapter keeps registration and executable discovery with the stack that understands them.
type projectCreatorAdapter struct {
	info          projectcreation.ProviderInfo
	new           func() (projectCreator, error)
	register      func(context.Context, controlClient, string) (control.ProjectRegistration, error)
	checkCreation func(context.Context, map[string]string) projectreadiness.Report
	checkProject  func(context.Context, domain.ProjectSnapshot) projectreadiness.Report
}

// defaultProjectCreators is an explicit adapter registry; schemas cannot introduce executables or plugins.
func defaultProjectCreators() map[string]projectCreatorAdapter {
	checker := projectreadiness.New()
	return map[string]projectCreatorAdapter{projectcreation.GoForj.ID: {
		info:          projectcreation.GoForj,
		new:           func() (projectCreator, error) { return projectcreation.New() },
		checkCreation: checker.GoForjCreation,
		checkProject:  checker.GoForjProject,
		register: func(ctx context.Context, client controlClient, path string) (control.ProjectRegistration, error) {
			return client.RegisterProject(ctx, control.RegisterProjectRequest{Path: path})
		},
	}}
}

// ProjectCreationProviders lists supported stacks independently of any particular CLI's form schema.
func (a *App) ProjectCreationProviders() ([]projectcreation.ProviderInfo, error) {
	providers := []projectcreation.ProviderInfo{}
	for _, adapter := range a.projectCreators {
		providers = append(providers, adapter.info)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	return providers, nil
}

// ProjectCreationSchema loads only the selected adapter's authoring choices.
func (a *App) ProjectCreationSchema(providerID string) (projectcreation.Schema, error) {
	ctx, _, err := a.currentConnection()
	if err != nil {
		return projectcreation.Schema{}, err
	}
	adapter, ok := a.projectCreators[providerID]
	if !ok {
		return projectcreation.Schema{}, fmt.Errorf("unsupported project stack %q", providerID)
	}
	creator, err := adapter.new()
	if err != nil {
		return projectcreation.Schema{}, err
	}
	return creator.Schema(ctx)
}

// ChooseProjectParent selects a parent folder without creating or registering a project.
func (a *App) ChooseProjectParent() (string, error) {
	ctx, _, err := a.currentConnection()
	if err != nil {
		return "", err
	}
	return a.choose(ctx, directoryDialogOptions{Title: "Choose a location for the new project", ResolvesAliases: true, CanCreateDirectories: true})
}

// projectCreationJob retains bounded ephemeral output and the exact successful destination for registration retry.
type projectCreationJob struct {
	mu       sync.Mutex
	progress desktopwire.ProjectCreationProgress
	output   []byte
	offset   uint64
	done     chan struct{}
	adapter  projectCreatorAdapter
}

// Write drains child output even after old bytes roll out of the retained window.
func (job *projectCreationJob) Write(data []byte) (int, error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	count := len(data)
	const limit = 256 * 1024
	job.output = append(job.output, data...)
	if len(job.output) > limit {
		dropped := len(job.output) - limit
		job.offset += uint64(dropped)
		job.output = append([]byte(nil), job.output[dropped:]...)
	}
	return count, nil
}

// read projects a cursor-relative snapshot so polling never loses output between subscriptions.
func (job *projectCreationJob) read(cursor uint64) (desktopwire.ProjectCreationProgress, error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	end := job.offset + uint64(len(job.output))
	if cursor > end {
		return desktopwire.ProjectCreationProgress{}, fmt.Errorf("creation output cursor is ahead of the job")
	}
	progress := job.progress
	progress.Truncated = cursor < job.offset
	if cursor < job.offset {
		cursor = job.offset
	}
	progress.Output = string(job.output[cursor-job.offset:])
	progress.Cursor = end
	if job.progress.Result != nil {
		result := *job.progress.Result
		progress.Result = &result
	}
	return progress, nil
}

// phase updates presentation state without deriving readiness from renderer text.
func (job *projectCreationJob) phase(phase string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.progress.Phase = phase
}

// StartProjectCreation starts one unprivileged authoring job and returns before the renderer finishes.
func (a *App) StartProjectCreation(request projectcreation.Request) (desktopwire.ProjectCreationProgress, error) {
	ctx, _, err := a.currentConnection()
	if err != nil {
		return desktopwire.ProjectCreationProgress{}, err
	}
	adapter, ok := a.projectCreators[request.ProviderID]
	if !ok {
		return desktopwire.ProjectCreationProgress{}, fmt.Errorf("unsupported project stack %q", request.ProviderID)
	}
	creator, err := adapter.new()
	if err != nil {
		return desktopwire.ProjectCreationProgress{}, err
	}
	schema, err := creator.Schema(ctx)
	if err != nil {
		return desktopwire.ProjectCreationProgress{}, err
	}
	path, err := schema.Directory(request)
	if err != nil {
		return desktopwire.ProjectCreationProgress{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return desktopwire.ProjectCreationProgress{}, err
	}
	job := &projectCreationJob{adapter: adapter, done: make(chan struct{}), progress: desktopwire.ProjectCreationProgress{
		JobID: "creation-" + hex.EncodeToString(random[:]), ProviderID: request.ProviderID, Path: path, Phase: "creating",
	}}
	a.projectCreationMu.Lock()
	if a.projectCreationJobs == nil {
		a.projectCreationJobs = map[string]*projectCreationJob{}
	}
	for _, existing := range a.projectCreationJobs {
		existing.mu.Lock()
		active := existing.progress.Phase == "creating" || existing.progress.Phase == "registering"
		existing.mu.Unlock()
		if active {
			a.projectCreationMu.Unlock()
			return desktopwire.ProjectCreationProgress{}, fmt.Errorf("a project is already being created")
		}
	}
	if len(a.projectCreationJobs) >= 8 {
		for id, existing := range a.projectCreationJobs {
			existing.mu.Lock()
			removable := existing.progress.Phase != "registration_failed"
			existing.mu.Unlock()
			if removable {
				delete(a.projectCreationJobs, id)
				break
			}
		}
		if len(a.projectCreationJobs) >= 8 {
			a.projectCreationMu.Unlock()
			return desktopwire.ProjectCreationProgress{}, fmt.Errorf("finish registering the previously created projects before creating another")
		}
	}
	a.projectCreationJobs[job.progress.JobID] = job
	a.projectCreationMu.Unlock()
	// Copy caller-owned options before the worker outlives the binding request.
	options := make(map[string]string, len(request.Options))
	for key, value := range request.Options {
		options[key] = value
	}
	request.Options = options
	go func() {
		defer close(job.done)
		if err := creator.CreateWithOutput(ctx, request, job); err != nil {
			job.mu.Lock()
			job.progress.Phase, job.progress.Error = "failed", fmt.Sprintf("Creation did not finish: %v. Files at %s were kept. Review the output and choose a new or empty directory before trying again.", err, path)
			job.mu.Unlock()
			return
		}
		a.registerCreatedProject(job)
	}()
	return job.read(0)
}

// ProjectCreationProgress reads an admitted job without requiring the daemon to stay connected.
func (a *App) ProjectCreationProgress(jobID string, cursor uint64) (desktopwire.ProjectCreationProgress, error) {
	a.projectCreationMu.Lock()
	job, ok := a.projectCreationJobs[jobID]
	a.projectCreationMu.Unlock()
	if !ok {
		return desktopwire.ProjectCreationProgress{}, fmt.Errorf("creation job is no longer available; existing files were kept")
	}
	return job.read(cursor)
}

// registerCreatedProject registers only a renderer-completed path through its stack adapter.
func (a *App) registerCreatedProject(job *projectCreationJob) {
	job.phase("registering")
	ctx, client, err := a.currentConnection()
	result := desktopwire.ProjectCreationResult{Path: job.progress.Path}
	if err == nil {
		registration, registerErr := job.adapter.register(ctx, client, result.Path)
		err = registerErr
		if err == nil {
			result.Registration = &registration
		}
	}
	phase := "completed"
	if err != nil {
		phase = "registration_failed"
		result.RegistrationError = fmt.Sprintf("Project files are ready, but Harbor could not add the project: %v. Reconnect Harbor and retry registration. Your files will not be generated again.", err)
	}
	job.mu.Lock()
	job.progress.Phase, job.progress.Result = phase, &result
	job.mu.Unlock()
}

// RetryProjectRegistration retries only the retained successful job, never its renderer or a caller-supplied path.
func (a *App) RetryProjectRegistration(jobID string) (desktopwire.ProjectCreationProgress, error) {
	a.projectCreationMu.Lock()
	defer a.projectCreationMu.Unlock()
	job, ok := a.projectCreationJobs[jobID]
	if !ok {
		return desktopwire.ProjectCreationProgress{}, fmt.Errorf("creation job is no longer available; use Add existing project for the retained directory")
	}
	for _, existing := range a.projectCreationJobs {
		existing.mu.Lock()
		active := existing.progress.Phase == "creating" || existing.progress.Phase == "registering"
		existing.mu.Unlock()
		if active {
			return desktopwire.ProjectCreationProgress{}, fmt.Errorf("a project creation job is still running")
		}
	}
	job.mu.Lock()
	if job.progress.Phase == "completed" {
		job.mu.Unlock()
		return job.read(0)
	}
	if job.progress.Phase != "registration_failed" {
		job.mu.Unlock()
		return desktopwire.ProjectCreationProgress{}, fmt.Errorf("only a successfully created project can retry registration")
	}
	job.progress.Phase = "registering"
	job.progress.Result = nil
	job.done = make(chan struct{})
	job.mu.Unlock()
	go func() { defer close(job.done); a.registerCreatedProject(job) }()
	return job.read(0)
}

// CreateProject preserves the synchronous desktop boundary for clients that do not consume progress.
func (a *App) CreateProject(request projectcreation.Request) (desktopwire.ProjectCreationResult, error) {
	progress, err := a.StartProjectCreation(request)
	if err != nil {
		return desktopwire.ProjectCreationResult{}, err
	}
	a.projectCreationMu.Lock()
	job := a.projectCreationJobs[progress.JobID]
	done := job.done
	a.projectCreationMu.Unlock()
	<-done
	progress, _ = job.read(0)
	if progress.Error != "" {
		return desktopwire.ProjectCreationResult{}, fmt.Errorf("%s\n%s", progress.Error, progress.Output)
	}
	result := *progress.Result
	result.Output = progress.Output
	return result, nil
}

// waitProjectCreations joins canceled authoring workers during desktop shutdown without deleting their files.
func (a *App) waitProjectCreations() {
	a.projectCreationMu.Lock()
	pending := []chan struct{}{}
	for _, job := range a.projectCreationJobs {
		pending = append(pending, job.done)
	}
	a.projectCreationMu.Unlock()
	for _, done := range pending {
		<-done
	}
}
