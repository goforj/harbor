//go:build darwin || linux || windows

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// continuityFaultBroker keeps native launch and peer adoption while failing one exact transport boundary.
type continuityFaultBroker struct {
	*projectprocess.OutputBrokerProcessLauncher
	fault domain.ProjectID
	calls map[domain.ProjectID]int
}

// Adopt injects no process observation or settlement behavior into the production supervisor.
func (launcher *continuityFaultBroker) Adopt(ctx context.Context, spec projectprocess.OutputBrokerAdoptionSpec) (projectprocess.OutputBrokerAttachment, error) {
	launcher.calls[spec.ProjectID]++
	if spec.ProjectID == launcher.fault {
		return nil, errors.New("test-owned broker adoption transport unavailable")
	}
	return launcher.OutputBrokerProcessLauncher.Adopt(ctx, spec)
}

// continuityFaultContainers supplies a wake hint without contacting or mutating a Docker Engine.
type continuityFaultContainers struct {
	*projectLifecycleContainerRuntime
	wake chan struct{}
}

// WaitProjectChange lets recovery prove a fresh resource projection through the ordinary runtime watcher.
func (containers *continuityFaultContainers) WaitProjectChange(ctx context.Context, _ string) error {
	select {
	case <-containers.wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestProjectSessionContinuityBrokerFailureIsolatesNativePeer composes native scope, broker, SQLite and coordinator recovery.
func TestProjectSessionContinuityBrokerFailureIsolatesNativePeer(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	bin := continuityFaultExecutables(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	broker, err := projectprocess.NewOutputBrokerProcessLauncher(filepath.Join(bin, continuityFaultExecutableName("outputbroker")))
	if err != nil {
		t.Fatal(err)
	}
	spool, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := projectprocess.Options{OutputSpoolDirectory: spool, OutputBrokerLauncher: broker, GracePeriod: 500 * time.Millisecond}
	owner := newProjectLifecycleIntegrationSupervisor(options)
	admission, _ := newProjectLifecycleIntegrationCoordinator(t, store, journal, owner, netip.MustParseAddr("127.0.0.1"), 3000)
	admission.primaryLeases.loopback.(*primaryLeaseTestLoopbackObserver).facts[netip.MustParseAddr("127.0.0.2")] = primaryLeaseTestExactObservation(netip.MustParseAddr("127.0.0.2"))
	sessions := make([]domain.ProjectSession, 0, 2)
	var replacement *projectprocess.Supervisor
	var coordinator *ProjectLifecycleCoordinator
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if coordinator != nil {
			if err := coordinator.Close(ctx); err != nil {
				t.Error(err)
			}
		}
		// Each receipt came from this test's accepted launch, including the quarantined original.
		cleaner := newProjectLifecycleIntegrationSupervisor(options)
		for _, session := range sessions {
			if _, err := cleaner.SettleSession(ctx, session); err != nil {
				t.Errorf("settle test-owned session %s: %v", session.ID, err)
			}
		}
		for _, supervisor := range []*projectprocess.Supervisor{owner, replacement, cleaner} {
			if supervisor != nil {
				if err := supervisor.Close(ctx); err != nil {
					t.Error(err)
				}
			}
		}
	})
	for _, name := range []string{"faulted", "healthy"} {
		sessions = append(sessions, continuityFaultReadySession(t, store, journal, owner, admission.primaryLeases, name))
	}
	for _, session := range sessions {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		output, err := owner.WaitOutput(ctx, session.ProjectID, session.ID, 0)
		cancel()
		if err != nil || !output.Available || !strings.Contains(output.Text, "stream=") {
			t.Fatalf("native fixture did not persist output before detach: %#v, %v", output, err)
		}
		if err := owner.DetachSession(t.Context(), session.ProjectID, session.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	faulted, healthy := sessions[0], sessions[1]
	launcher := &continuityFaultBroker{OutputBrokerProcessLauncher: broker, fault: faulted.ProjectID, calls: make(map[domain.ProjectID]int)}
	containers := &continuityFaultContainers{projectLifecycleContainerRuntime: &projectLifecycleContainerRuntime{observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{}}}, wake: make(chan struct{}, 1)}
	options.OutputBrokerLauncher, options.ContainerRuntime = launcher, containers
	replacement = newProjectLifecycleIntegrationSupervisor(options)
	coordinator, _ = newProjectLifecycleIntegrationCoordinator(t, store, journal, replacement, netip.MustParseAddr("127.0.0.1"), 3000)
	coordinator.primaryLeases = admission.primaryLeases
	for attempt := 0; attempt < 2; attempt++ {
		if err := coordinator.Recover(t.Context()); err != nil {
			t.Fatalf("recovery %d: %v", attempt, err)
		}
		if err := coordinator.Resume(t.Context()); err != nil {
			t.Fatalf("resume %d: %v", attempt, err)
		}
		continuityFaultAssertQuarantined(t, store, coordinator, replacement, faulted)
		current, err := store.ActiveProjectSession(t.Context(), healthy.ProjectID)
		if err != nil || !reflect.DeepEqual(current, healthy) {
			t.Fatalf("healthy durable session changed: %#v, %v", current, err)
		}
		if handle := coordinator.handle(healthy.ProjectID, healthy.ID); handle == nil || handle.Info().Evidence.BirthToken != healthy.Process.BirthToken || handle.Info().Evidence.PID != healthy.Process.PID {
			t.Fatalf("healthy native handle lost its birth fence: %#v", handle)
		}
		continuityFaultFreshOutput(t, coordinator, healthy)
	}
	if launcher.calls[faulted.ProjectID] != 1 || launcher.calls[healthy.ProjectID] != 1 {
		t.Fatalf("recovery retried or skipped broker adoption: %#v", launcher.calls)
	}
	containers.wake <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		project, err := store.Project(t.Context(), healthy.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if project.Project.State != domain.ProjectReady || len(project.Project.Apps) != 1 || !project.Project.Apps[0].Active {
			t.Fatalf("healthy peer lost Ready authority: %#v", project.Project)
		}
		if len(project.Project.Resources) == 2 && project.Project.Resources[1].ID == "fresh-docs" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthy resource query did not refresh after recovery: %#v", project.Project.Resources)
		}
		time.Sleep(20 * time.Millisecond)
	}
	continuityFaultAssertQuarantined(t, store, coordinator, replacement, faulted)
}

// continuityFaultReadySession seeds the durable ready boundary with actual native launch and broker receipts.
func continuityFaultReadySession(t *testing.T, store *state.Store, journal *state.OperationJournal, owner *projectprocess.Supervisor, leases *projectPrimaryLeaseCoordinator, name string) domain.ProjectSession {
	t.Helper()
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := projectdiscovery.NewDiscoverer().Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := discovery.ProjectSnapshot(domain.ProjectID("project-"+name), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	project.Name, project.Slug = name, name
	registered, err := store.RegisterProject(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if name == "faulted" {
		pool, err := identity.NewPool(netip.MustParsePrefix("127.0.0.0/8"), []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.2")})
		if err != nil {
			t.Fatal(err)
		}
		ownership, err := identity.NewOwnership("continuity-fault-test", 1)
		if err != nil {
			t.Fatal(err)
		}
		at := lifecycleTime(time.Now())
		if _, err := store.InitializeNetworkIdentity(t.Context(), state.InitializeNetworkIdentityRequest{Ownership: ownership, Pool: pool, PoolGeneration: 1, At: at,
			Setup: []state.NetworkSetupProof{{Component: state.NetworkSetupComponentMachineOwnership, Evidence: "test-owned identity", Generation: 1, VerifiedAt: at}, {Component: state.NetworkSetupComponentLoopbackPool, Evidence: "injected test loopback observations", Generation: 1, VerifiedAt: at}}}); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := leases.Ensure(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	registered.Record, err = store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := lifecycleTime(time.Now())
	operation, err := domain.NewOperation(domain.OperationID("operation-"+name), domain.IntentID("intent-"+name), domain.OperationKindProjectStart, project.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := journal.Enqueue(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newHarborProjectSession(project.ID, root, at)
	if err != nil {
		t.Fatal(err)
	}
	begun, err := store.BeginProjectStart(t.Context(), state.BeginProjectStartRequest{ProjectID: project.ID, OperationID: operation.ID, ExpectedOperationRevision: queued.Revision, ExpectedProjectRevision: registered.Record.Revision, Session: session, Phase: "launching", At: at})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := owner.Start(t.Context(), projectprocess.StartRequest{ProjectID: project.ID, SessionID: session.ID, CheckoutRoot: root, EnvironmentOverrides: projectprocess.EnvironmentOverrides{"IP_ADDRESS": lease.Lease.Address.String()}})
	if err != nil {
		t.Fatal(err)
	}
	info := handle.Info()
	if info.OutputBroker == nil {
		t.Fatal("native launch fell back without an output broker")
	}
	peer := info.OutputBroker
	session, err = store.AttachProjectProcess(t.Context(), state.AttachProjectProcessRequest{ProjectID: project.ID, SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		Process:      domain.ProcessEvidence{PID: info.Evidence.PID, BirthToken: info.Evidence.BirthToken, ExecutableIdentity: info.Evidence.ExecutableIdentity, ArgumentDigest: info.Evidence.ArgumentsSHA256},
		OutputBroker: &domain.OutputBrokerSession{EndpointReference: peer.EndpointReference, Process: peer.Process, ManifestPath: peer.ManifestPath, CredentialDigest: peer.TicketDigest}, At: lifecycleTime(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", lease.Lease.Address, 3000)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteProjectStart(t.Context(), state.CompleteProjectStartRequest{ProjectID: project.ID, OperationID: operation.ID, ExpectedOperationRevision: begun.Operation.Revision, SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		Runtime: defaultRuntime(projectRuntimePlanForTest(target), []domain.ServiceSnapshot{{ID: "fixture", Name: "Fixture", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected}}, nil), Phase: "ready", At: lifecycleTime(time.Now())})
	if err != nil || completed.Session == nil {
		t.Fatalf("complete native ready boundary: %#v, %v", completed, err)
	}
	if completed.Session.State != domain.SessionAwaitingAttach {
		t.Fatalf("fixture entered managed attachment semantics: %s", completed.Session.State)
	}
	return *completed.Session
}

// continuityFaultAssertQuarantined checks that diagnostics and durable evidence never acquire live authority.
func continuityFaultAssertQuarantined(t *testing.T, store *state.Store, coordinator *ProjectLifecycleCoordinator, supervisor *projectprocess.Supervisor, session domain.ProjectSession) {
	t.Helper()
	project, err := store.Project(t.Context(), session.ProjectID)
	if err != nil || project.Project.State != domain.ProjectUnavailable || len(project.Project.Resources) != 0 {
		t.Fatalf("faulted project retained routes: %#v, %v", project, err)
	}
	if len(project.Project.Apps) != 1 || len(project.Project.Services) != 1 {
		t.Fatalf("quarantine erased fixture topology: %#v", project.Project)
	}
	for _, app := range project.Project.Apps {
		if app.Active || app.State != domain.EntityStopped {
			t.Fatalf("quarantined App remains active: %#v", app)
		}
	}
	for _, service := range project.Project.Services {
		if service.State != domain.EntityStopped {
			t.Fatalf("quarantined service remains ready: %#v", service)
		}
	}
	current, err := store.ActiveProjectSession(t.Context(), session.ProjectID)
	if err != nil || !reflect.DeepEqual(current, session) {
		t.Fatalf("quarantine changed exact durable evidence: %#v, %v", current, err)
	}
	if coordinator.handle(session.ProjectID, session.ID) != nil || supervisor.ReadOutput(session.ProjectID, session.ID, 0).Available {
		t.Fatal("failed adoption retained a handle or live output authority")
	}
	if _, err := supervisor.ObserveFrameworkResources(t.Context(), session.ProjectID, session.ID, nil); !errors.Is(err, projectprocess.ErrNotRunning) {
		t.Fatalf("failed adoption retained resource observation authority: %v", err)
	}
	observation, err := supervisor.ObservePriorProcess(t.Context(), *session.Process)
	if err != nil || observation.State != projectprocess.PriorProcessPresent {
		t.Fatalf("quarantine signaled or lost the original child: %#v, %v", observation, err)
	}
	activity, err := coordinator.ProjectActivity(t.Context(), ProjectActivityRequest{ProjectID: session.ProjectID})
	if err != nil || activity.Session == nil || activity.Session.Output.Available {
		t.Fatalf("faulted activity acquired live authority: %#v, %v", activity, err)
	}
	if !activity.Session.Output.Historical || !strings.Contains(activity.Session.Output.Text, "stream=") {
		t.Fatalf("retained spool was not labeled historical: %#v", activity.Session.Output)
	}
}

// continuityFaultFreshOutput requires a child timestamp created after observation begins, excluding broker replay.
func continuityFaultFreshOutput(t *testing.T, coordinator *ProjectLifecycleCoordinator, session domain.ProjectSession) {
	t.Helper()
	notBefore := time.Now().UnixNano()
	activity, err := coordinator.ProjectActivity(t.Context(), ProjectActivityRequest{ProjectID: session.ProjectID})
	if err != nil || activity.Session == nil {
		t.Fatalf("healthy activity: %#v, %v", activity, err)
	}
	cursor := activity.Session.Output.NextCursor
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		activity, err = coordinator.ProjectActivity(ctx, ProjectActivityRequest{ProjectID: session.ProjectID, SessionID: session.ID, Cursor: cursor, Wait: time.Second})
		if err != nil || activity.Session == nil || !activity.Session.Output.Available || activity.Session.Output.Historical {
			t.Fatalf("healthy live output: %#v, %v", activity, err)
		}
		cursor = activity.Session.Output.NextCursor
		for _, line := range strings.Split(activity.Session.Output.Text, "\n") {
			stamp, err := strconv.ParseInt(strings.TrimPrefix(line, "stream="), 10, 64)
			if strings.HasPrefix(line, "stream=") && err == nil && stamp >= notBefore {
				return
			}
		}
	}
	t.Fatal("healthy output contained no fresh native child record")
}

// continuityFaultExecutableName preserves native executable lookup on supported test platforms.
func continuityFaultExecutableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// continuityFaultExecutables builds only a tiny native streaming fixture and the production output broker.
func continuityFaultExecutables(t *testing.T) string {
	t.Helper()
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os"; "time")
// main supplies fresh output and a read-only resource response without host services.
func main() {
 if len(os.Args)>1 && os.Args[1]=="dev:status" {
  fmt.Printf(`+"`"+`{"schema_version":1,"supported":true,"services":[],"resources":[{"id":"fresh-docs","name":"Fresh docs","kind":"documentation","url":"http://%s:3000/docs","app":"app"}]}`+"`"+`, os.Getenv("IP_ADDRESS"))
  return
 }
 for range time.Tick(25*time.Millisecond) { fmt.Printf("stream=%d\n",time.Now().UnixNano()) }
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "../.."))
	for _, build := range []struct{ name, target string }{{"forj", source}, {"outputbroker", "./cmd/outputbroker"}} {
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		command := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, continuityFaultExecutableName(build.name)), build.target)
		command.Dir = root
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatal(fmt.Sprintf("build %s: %v\n%s", build.name, err, output))
		}
	}
	return bin
}
