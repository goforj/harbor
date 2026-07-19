package reconcile

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/migrations"
)

const projectLifecycleHelperEnvironment = "HARBOR_PROJECT_LIFECYCLE_HELPER"

// TestMain turns a copy of this portable test binary into the fake forj executable used by the integration test.
func TestMain(m *testing.M) {
	if os.Getenv(projectLifecycleHelperEnvironment) == "1" {
		runProjectLifecycleHelper()
		return
	}
	os.Exit(m.Run())
}

// runProjectLifecycleHelper exposes the generated readiness shape until Harbor stops the owned process.
func runProjectLifecycleHelper() {
	address := os.Getenv("HARBOR_PROJECT_LIFECYCLE_ADDRESS")
	listener, err := net.Listen("tcp", address)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/-/ready" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"ready","app":"app"}`))
	})}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt)
	go func() {
		_ = server.Serve(listener)
	}()
	<-stopped
	_ = server.Close()
}

// TestProjectLifecycleCoordinatorBringsForjDevOnlineAndStopsIt proves the complete durable process and readiness vertical.
func TestProjectLifecycleCoordinatorBringsForjDevOnlineAndStopsIt(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	installProjectLifecycleIntegrationForj(t, port)

	supervisor := projectprocess.New(projectprocess.Options{GracePeriod: 500 * time.Millisecond})
	coordinator := NewProjectLifecycleCoordinator(store, journal, supervisor)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	queued, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start", IntentID: "intent-start",
	})
	if err != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("Start() = %#v, %v", queued, err)
	}
	ready := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	if len(ready.Project.Apps) != 1 || ready.Project.Apps[0].ID != "app" || len(ready.Project.Resources) != 1 || ready.Project.Resources[0].Kind != "application" || ready.Project.Resources[0].URL != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("ready project = %#v", ready.Project)
	}
	startOperation, err := journal.OperationByIntent(t.Context(), "intent-start")
	if err != nil || startOperation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("start operation = %#v, %v", startOperation, err)
	}

	stopping, err := coordinator.Stop(t.Context(), ProjectStopRequest{
		ProjectID: project.ID, OperationID: "operation-stop", IntentID: "intent-stop",
	})
	if err != nil || stopping.Operation.State != domain.OperationQueued {
		t.Fatalf("Stop() = %#v, %v", stopping, err)
	}
	stopped := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if len(stopped.Project.Apps) != 1 || stopped.Project.Apps[0].State != domain.EntityStopped || len(stopped.Project.Resources) != 0 {
		t.Fatalf("stopped project = %#v", stopped.Project)
	}
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("stopped project retained an active session")
	}
	stopOperation, err := journal.OperationByIntent(t.Context(), "intent-stop")
	if err != nil || stopOperation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("stop operation = %#v, %v", stopOperation, err)
	}
}

// TestProjectLifecycleCoordinatorCloseRetiresReadyProcessAuthority proves a daemon restart does not preserve a phantom online session.
func TestProjectLifecycleCoordinatorCloseRetiresReadyProcessAuthority(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	installProjectLifecycleIntegrationForj(t, port)
	coordinator := NewProjectLifecycleCoordinator(store, journal, projectprocess.New(projectprocess.Options{GracePeriod: 500 * time.Millisecond}))

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-close", IntentID: "intent-start-close",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("daemon close retained an active project session")
	}
}

// newProjectLifecycleIntegrationState creates one fully migrated named harbord database.
func newProjectLifecycleIntegrationState(t *testing.T) (*state.Store, *state.OperationJournal) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbord.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle database: %v", err)
		}
	})
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open lifecycle database: %v", err)
	}
	registered := append([]migrations.Migration(nil), migrations.GetMigrations()...)
	sort.Slice(registered, func(left int, right int) bool { return registered[left].Name() < registered[right].Name() })
	for _, migration := range registered {
		if migration.App() != "harbord" || migration.Connection() != "default" || (migration.Driver() != "" && migration.Driver() != "sqlite") {
			continue
		}
		if err := migration.Up(connection); err != nil {
			t.Fatalf("apply lifecycle migration %s: %v", migration.Name(), err)
		}
	}
	mutations := state.NewMutationCoordinator(connections)
	store := state.NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		mutations,
	)
	journal := state.NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		mutations,
	)
	return store, journal
}

// newProjectLifecycleIntegrationCheckout creates the minimum real checkout metadata used by discovery and readiness.
func newProjectLifecycleIntegrationCheckout(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve lifecycle port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release lifecycle port: %v", err)
	}
	root := filepath.Join(t.TempDir(), "orders")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create lifecycle checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goforj.yml"), []byte("project_name: Orders\n"), 0o600); err != nil {
		t.Fatalf("write lifecycle marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(fmt.Sprintf("APP_NAME=Orders\nAPI_HTTP_PORT=%d\n", port)), 0o600); err != nil {
		t.Fatalf("write lifecycle environment: %v", err)
	}
	return root, port
}

// registerProjectLifecycleIntegrationProject commits the same inert project shape used by control registration.
func registerProjectLifecycleIntegrationProject(t *testing.T, store *state.Store, root string) domain.ProjectSnapshot {
	t.Helper()
	discovery, err := projectdiscovery.NewDiscoverer().Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover lifecycle checkout: %v", err)
	}
	project, err := discovery.ProjectSnapshot("project-orders", time.Now().UTC())
	if err != nil {
		t.Fatalf("create lifecycle project: %v", err)
	}
	if _, err := store.RegisterProject(t.Context(), project); err != nil {
		t.Fatalf("register lifecycle project: %v", err)
	}
	return project
}

// installProjectLifecycleIntegrationForj places a portable test-binary copy where exec.LookPath resolves forj.
func installProjectLifecycleIntegrationForj(t *testing.T, port int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve lifecycle test executable: %v", err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("read lifecycle test executable: %v", err)
	}
	name := "forj"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := t.TempDir()
	forj := filepath.Join(bin, name)
	if err := os.WriteFile(forj, data, 0o700); err != nil {
		t.Fatalf("install lifecycle forj: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(projectLifecycleHelperEnvironment, "1")
	t.Setenv("HARBOR_PROJECT_LIFECYCLE_ADDRESS", fmt.Sprintf("127.0.0.1:%d", port))
}

// waitForProjectLifecycleState polls durable projection because control intentionally returns after journaling.
func waitForProjectLifecycleState(t *testing.T, store *state.Store, projectID domain.ProjectID, want domain.ProjectState) state.ProjectRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		record, err := store.Project(t.Context(), projectID)
		if err == nil && record.Project.State == want {
			return record
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, err := store.Project(t.Context(), projectID)
	t.Fatalf("project state = %#v, %v, want %q", record.Project, err, want)
	return state.ProjectRecord{}
}
