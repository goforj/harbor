package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectruntime"
)

// genericRuntimeFixture implements Harbor's mandatory lifecycle without GoForj discovery, commands, or process types.
type genericRuntimeFixture struct {
	mutex          sync.Mutex
	handles        map[domain.SessionID]*genericRuntimeHandle
	preparations   int
	resets         int
	launches       int
	launchRequests []projectruntime.LaunchRequest
}

// genericRuntimeHandle publishes deterministic process evidence and completion for the neutral lifecycle proof.
type genericRuntimeHandle struct {
	info   projectruntime.Info
	done   chan struct{}
	once   sync.Once
	mutex  sync.RWMutex
	exit   projectruntime.Exit
	exited bool
}

// genericRuntimeReadinessProbe proves readiness without using GoForj's generated JSON contract.
type genericRuntimeReadinessProbe struct{}

// Prepare returns a primary HTTP plan derived only from Harbor's assigned address.
func (runtime *genericRuntimeFixture) Prepare(
	_ context.Context,
	request projectruntime.PreparationRequest,
) (projectruntime.Plan, error) {
	runtime.mutex.Lock()
	runtime.preparations++
	runtime.mutex.Unlock()
	const port uint16 = 3000
	return projectruntime.Plan{
		NetworkAssignment: projectruntime.NetworkAssignment{
			Address:     request.Address,
			PrimaryPort: port,
		},
		Readiness: genericRuntimeReadinessProbe{},
		Presentation: projectruntime.Presentation{
			AppID:       "app",
			Name:        "Application",
			ResourceURL: fmt.Sprintf("http://%s:%d", request.Address, port),
		},
	}, nil
}

// Probe reports ready through the neutral readiness contract.
func (genericRuntimeReadinessProbe) Probe(context.Context) (projectruntime.ReadinessState, error) {
	return projectruntime.ReadinessReady, nil
}

// Launch creates one provider-owned handle without invoking a GoForj executable.
func (runtime *genericRuntimeFixture) Launch(
	_ context.Context,
	request projectruntime.LaunchRequest,
) (projectruntime.Handle, error) {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.launches++
	runtime.launchRequests = append(runtime.launchRequests, request)
	handle := &genericRuntimeHandle{
		info: projectruntime.Info{
			ProjectID:    request.ProjectID,
			SessionID:    request.SessionID,
			CheckoutRoot: request.CheckoutRoot,
			Evidence: projectruntime.Evidence{
				PID:                int64(10_000 + runtime.launches),
				BirthToken:         fmt.Sprintf("generic-runtime-birth-%d", runtime.launches),
				ExecutableIdentity: "/usr/local/bin/generic-project-runtime",
				ArgumentDigest:     strings.Repeat("a", 64),
			},
			StartedAt: time.Now().UTC().Round(0),
		},
		done: make(chan struct{}),
	}
	runtime.handles[request.SessionID] = handle
	return handle, nil
}

// Reset records the pre-launch cleanup edge without depending on a framework command.
func (runtime *genericRuntimeFixture) Reset(context.Context, projectruntime.ResetRequest) error {
	runtime.mutex.Lock()
	runtime.resets++
	runtime.mutex.Unlock()
	return nil
}

// Stop completes the exact provider-owned handle selected by durable identities.
func (runtime *genericRuntimeFixture) Stop(
	_ context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) error {
	runtime.mutex.Lock()
	handle := runtime.handles[sessionID]
	if handle != nil && handle.info.ProjectID == projectID {
		delete(runtime.handles, sessionID)
	}
	runtime.mutex.Unlock()
	if handle == nil || handle.info.ProjectID != projectID {
		return projectruntime.ErrNotRunning
	}
	handle.complete(projectruntime.Exit{
		StopRequested: true,
		ExitedAt:      time.Now().UTC().Round(0),
	})
	return nil
}

// ObservePriorProcess reports absent evidence because this fixture never survives its coordinator.
func (*genericRuntimeFixture) ObservePriorProcess(
	context.Context,
	domain.ProcessEvidence,
) (projectruntime.PriorProcessObservation, error) {
	return projectruntime.PriorProcessObservation{State: projectruntime.PriorProcessAbsent}, nil
}

// SettlePriorProcess reports an already absent process for the neutral recovery contract.
func (*genericRuntimeFixture) SettlePriorProcess(
	context.Context,
	domain.ProcessEvidence,
) (projectruntime.PriorProcessSettlement, error) {
	return projectruntime.PriorProcessSettlement{Outcome: projectruntime.PriorProcessSettlementAbsent}, nil
}

// Close retires every remaining provider-owned handle during daemon shutdown.
func (runtime *genericRuntimeFixture) Close(context.Context) error {
	runtime.mutex.Lock()
	handles := make([]*genericRuntimeHandle, 0, len(runtime.handles))
	for sessionID, handle := range runtime.handles {
		handles = append(handles, handle)
		delete(runtime.handles, sessionID)
	}
	runtime.mutex.Unlock()
	for _, handle := range handles {
		handle.complete(projectruntime.Exit{
			StopRequested: true,
			ExitedAt:      time.Now().UTC().Round(0),
		})
	}
	return nil
}

// Info returns immutable launch evidence.
func (handle *genericRuntimeHandle) Info() projectruntime.Info {
	return handle.info
}

// Done closes after the fixture publishes one terminal exit.
func (handle *genericRuntimeHandle) Done() <-chan struct{} {
	return handle.done
}

// Result returns the terminal exit only after completion.
func (handle *genericRuntimeHandle) Result() (projectruntime.Exit, bool) {
	handle.mutex.RLock()
	defer handle.mutex.RUnlock()
	return handle.exit, handle.exited
}

// Wait observes completion or context cancellation.
func (handle *genericRuntimeHandle) Wait(ctx context.Context) (projectruntime.Exit, error) {
	select {
	case <-handle.done:
		exit, _ := handle.Result()
		return exit, nil
	case <-ctx.Done():
		return projectruntime.Exit{}, ctx.Err()
	}
}

// complete publishes exactly one terminal exit.
func (handle *genericRuntimeHandle) complete(exit projectruntime.Exit) {
	handle.once.Do(func() {
		handle.mutex.Lock()
		handle.exit = exit
		handle.exited = true
		handle.mutex.Unlock()
		close(handle.done)
	})
}

var _ projectruntime.Runtime = (*genericRuntimeFixture)(nil)

// TestGenericRuntimeCompletesLifecycleWithoutGoForj proves core start, restart, and stop need no GoForj project contract.
func TestGenericRuntimeCompletesLifecycleWithoutGoForj(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root := t.TempDir()
	if _, err := os.Stat(filepath.Join(root, ".goforj.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain project marker state = %v, want absent", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".harbor.yml"),
		[]byte("version: 1\nenvironment:\n  MEILISEARCH_HOST:\n    from: project.address\n"),
		0o600,
	); err != nil {
		t.Fatalf("write repository environment contract: %v", err)
	}
	project := domain.ProjectSnapshot{
		ID:        "project-generic",
		Name:      "Generic Project",
		Path:      root,
		Slug:      "generic-project",
		State:     domain.ProjectStopped,
		UpdatedAt: time.Now().UTC().Round(0),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	if _, err := store.RegisterProject(t.Context(), project); err != nil {
		t.Fatalf("register plain project: %v", err)
	}
	address := netip.MustParseAddr("127.0.0.1")
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, address)

	runtime := &genericRuntimeFixture{handles: make(map[domain.SessionID]*genericRuntimeHandle)}
	observer := &primaryLeaseTestLoopbackObserver{
		facts: map[netip.Addr]loopback.Observation{address: primaryLeaseTestExactObservation(address)},
		errs:  make(map[netip.Addr]error),
	}
	prober := &primaryLeaseTestPortProber{
		results: make(map[netip.Addr]identity.ProbeResult),
		errs:    make(map[netip.Addr]error),
	}
	coordinator := newProjectLifecycleCoordinator(
		store,
		journal,
		newProjectPrimaryLeaseCoordinator(store, runtime, observer, prober, time.Now),
		runtime,
		projectLifecycleTestRouteReconciler{},
		time.Now,
		newLifecycleOperationID,
		newLifecycleIntentID,
		newHarborProjectSession,
		defaultProjectStartupTimeout,
		defaultReadinessInterval,
	)
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(closeContext); err != nil {
			t.Errorf("close generic runtime coordinator: %v", err)
		}
	})

	start, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID:   project.ID,
		OperationID: "operation-generic-start",
		IntentID:    "intent-generic-start",
	})
	if err != nil || start.Operation.State != domain.OperationQueued {
		t.Fatalf("start plain project = %#v, %v", start, err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)

	restart, err := coordinator.Restart(t.Context(), ProjectRestartRequest{
		ProjectID:   project.ID,
		OperationID: "operation-generic-restart",
		IntentID:    "intent-generic-restart",
	})
	if err != nil || restart.Operation.State != domain.OperationQueued {
		t.Fatalf("restart plain project = %#v, %v", restart, err)
	}
	waitForProjectLifecycleOperationState(t, journal, "intent-generic-restart", domain.OperationSucceeded)
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)

	stop, err := coordinator.Stop(t.Context(), ProjectStopRequest{
		ProjectID:   project.ID,
		OperationID: "operation-generic-stop",
		IntentID:    "intent-generic-stop",
	})
	if err != nil || stop.Operation.State != domain.OperationQueued {
		t.Fatalf("stop plain project = %#v, %v", stop, err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)

	if err := os.WriteFile(
		filepath.Join(root, ".harbor.yml"),
		[]byte("version: 1\nenvironment:\n  MEILISEARCH_HOST:\n    from: shell.output\n"),
		0o600,
	); err != nil {
		t.Fatalf("write invalid repository environment contract: %v", err)
	}
	invalidStart, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID:   project.ID,
		OperationID: "operation-generic-invalid-environment",
		IntentID:    "intent-generic-invalid-environment",
	})
	if err != nil || invalidStart.Operation.State != domain.OperationQueued {
		t.Fatalf("start invalid repository environment = %#v, %v", invalidStart, err)
	}
	failed := waitForProjectLifecycleOperationState(
		t,
		journal,
		"intent-generic-invalid-environment",
		domain.OperationFailed,
	)
	if failed.Operation.Problem == nil || failed.Operation.Problem.Code != "project.environment.invalid" {
		t.Fatalf("invalid repository environment problem = %#v, want project.environment.invalid", failed.Operation.Problem)
	}

	runtime.mutex.Lock()
	preparations, resets, launches := runtime.preparations, runtime.resets, runtime.launches
	launchRequests := append([]projectruntime.LaunchRequest(nil), runtime.launchRequests...)
	runtime.mutex.Unlock()
	if preparations < 2 || resets != 2 || launches != 2 {
		t.Fatalf("generic runtime calls = prepare %d, reset %d, launch %d; want at least 2, 2, 2", preparations, resets, launches)
	}
	for index, request := range launchRequests {
		want := []projectruntime.EnvironmentVariable{{
			Name:   "MEILISEARCH_HOST",
			Value:  address.String(),
			Source: "project.address",
		}}
		if !reflect.DeepEqual(request.EnvironmentOverrides, want) {
			t.Fatalf("generic runtime launch %d environment overrides = %#v, want %#v", index+1, request.EnvironmentOverrides, want)
		}
	}
}
