package state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// TestRuntimeStateValidateRequiresAnUnambiguousNetworkLifecycle verifies the public aggregate rejects contradictory flags and ownership.
func TestRuntimeStateValidateRequiresAnUnambiguousNetworkLifecycle(t *testing.T) {
	validUninitialized := RuntimeState{
		Snapshot: validRuntimeStateSnapshot(0),
		Network:  uninitializedRuntimeNetwork(),
	}
	if err := validUninitialized.Validate(); err != nil {
		t.Fatalf("validate uninitialized runtime state: %v", err)
	}

	validInitialized := RuntimeState{
		Snapshot:           validRuntimeStateSnapshot(21),
		Network:            recordTestNetworkRecord(),
		NetworkInitialized: true,
	}
	if err := validInitialized.Validate(); err != nil {
		t.Fatalf("validate initialized runtime state: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RuntimeState)
		want   string
	}{
		{
			name: "invalid snapshot",
			mutate: func(state *RuntimeState) {
				state.Snapshot.Projects = nil
			},
			want: "runtime snapshot",
		},
		{
			name: "nil absent leases",
			mutate: func(state *RuntimeState) {
				state.Network.Leases = nil
			},
			want: "leases must be initialized",
		},
		{
			name: "nil absent quarantines",
			mutate: func(state *RuntimeState) {
				state.Network.Quarantines = nil
			},
			want: "quarantines must be initialized",
		},
		{
			name: "nil absent endpoints",
			mutate: func(state *RuntimeState) {
				state.Network.Reservations.Endpoints = nil
			},
			want: "endpoints must be initialized",
		},
		{
			name: "nil absent suppressions",
			mutate: func(state *RuntimeState) {
				state.Network.Reservations.SuppressedProjectIDs = nil
			},
			want: "suppressed projects must be initialized",
		},
		{
			name: "absent durable root",
			mutate: func(state *RuntimeState) {
				state.Network.Revision = 1
			},
			want: "must not contain durable root facts",
		},
		{
			name: "absent ownership",
			mutate: func(state *RuntimeState) {
				state.Network.Ownership = identity.Ownership{InstallationID: "installation-a", Generation: 1}
			},
			want: "must not contain identity ownership",
		},
		{
			name: "absent listeners",
			mutate: func(state *RuntimeState) {
				state.Network.Reservations.Listeners.DNS.Mode = ListenerModeDirect
			},
			want: "must not contain listener reservations",
		},
		{
			name: "absent collection facts",
			mutate: func(state *RuntimeState) {
				state.Network.Reservations.SuppressedProjectIDs = []domain.ProjectID{"project-alpha"}
			},
			want: "collections must be empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validUninitialized
			candidate.Network = uninitializedRuntimeNetwork()
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("initialized flag requires network", func(t *testing.T) {
		candidate := validUninitialized
		candidate.NetworkInitialized = true
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "runtime network") {
			t.Fatalf("validation error = %v, want initialized network failure", err)
		}
	})

	t.Run("network revision cannot exceed capture", func(t *testing.T) {
		candidate := validInitialized
		candidate.Snapshot.Sequence = 20
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds snapshot sequence") {
			t.Fatalf("validation error = %v, want future network revision", err)
		}
	})

	t.Run("network owners must be captured projects", func(t *testing.T) {
		candidate := validInitialized
		candidate.Network.Leases = append(candidate.Network.Leases, recordTestLease("project-unknown", "", "127.77.0.11", 3))
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), `references unknown project "project-unknown"`) {
			t.Fatalf("validation error = %v, want unknown network project", err)
		}
	})

	t.Run("runtime-bearing project needs network lifecycle", func(t *testing.T) {
		candidate := validInitialized
		project := validRuntimeStateProject("project-beta")
		project.State = domain.ProjectReady
		project.Apps = []domain.AppSnapshot{{
			ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true,
		}}
		project.Resources = []domain.ResourceSnapshot{{
			ID: "home", Name: "Home", Kind: "application",
			Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "https://project-beta.test",
		}}
		candidate.Snapshot.Projects = append(candidate.Snapshot.Projects, project)
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), `project "project-beta" has neither`) || !strings.Contains(err.Error(), "not a literal loopback address") {
			t.Fatalf("validation error = %v, want missing project network lifecycle for runtime claim", err)
		}
	})

	t.Run("completed release tombstones may outlive projects", func(t *testing.T) {
		candidate := validInitialized
		candidate.Snapshot.Projects = []domain.ProjectSnapshot{}
		candidate.Network.Leases = []identity.Lease{}
		candidate.Network.Reservations.Endpoints = []EndpointReservation{}
		candidate.Network.Reservations.SuppressedProjectIDs = []domain.ProjectID{"project-unknown"}
		if err := candidate.Validate(); err != nil {
			t.Fatalf("validate completed release tombstone after project deletion: %v", err)
		}
	})
}

// TestRuntimeStateValidatePermitsPendingProjectsAcrossNetworkLifecycle verifies registration and stopped topology need no fabricated lease.
func TestRuntimeStateValidatePermitsPendingProjectsAcrossNetworkLifecycle(t *testing.T) {
	pendingWithTopology := validRuntimeStateProject("project-alpha")
	pendingWithTopology.Apps = []domain.AppSnapshot{{
		ID: "app", Name: "App", State: domain.EntityStopped, Active: false, Required: true,
	}}
	pendingWithTopology.Services = []domain.ServiceSnapshot{{
		ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityStopped,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true,
	}}
	uninitialized := RuntimeState{
		Snapshot: func() domain.Snapshot {
			snapshot := validRuntimeStateSnapshot(0)
			snapshot.Projects = []domain.ProjectSnapshot{pendingWithTopology}
			return snapshot
		}(),
		Network: uninitializedRuntimeNetwork(),
	}
	if err := uninitialized.Validate(); err != nil {
		t.Fatalf("validate pending project before network initialization: %v", err)
	}

	initialized := RuntimeState{
		Snapshot:           validRuntimeStateSnapshot(21),
		Network:            recordTestNetworkRecord(),
		NetworkInitialized: true,
	}
	initialized.Snapshot.Projects = append(initialized.Snapshot.Projects, validRuntimeStateProject("project-beta"))
	if err := initialized.Validate(); err != nil {
		t.Fatalf("validate pending project alongside leased project: %v", err)
	}
}

// TestRuntimeStateValidateRejectsUnsafeUnleasedProjectClaims verifies stopped topology and public routes cannot bypass ownership.
func TestRuntimeStateValidateRejectsUnsafeUnleasedProjectClaims(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.ProjectSnapshot)
		want   string
	}{
		{
			name: "project state",
			mutate: func(project *domain.ProjectSnapshot) {
				project.State = domain.ProjectReady
			},
			want: "project state \"ready\" does not publish a direct loopback resource",
		},
		{
			name: "active App",
			mutate: func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped, Active: true, Required: true}}
			},
			want: `App "app" must be inactive`,
		},
		{
			name: "App state",
			mutate: func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: false, Required: true}}
			},
			want: `App "app" state "ready" must be stopped`,
		},
		{
			name: "service state",
			mutate: func(project *domain.ProjectSnapshot) {
				project.Services = []domain.ServiceSnapshot{{
					ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady,
					Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true,
				}}
			},
			want: `service "mysql" state "ready" must be stopped`,
		},
		{
			name: "published resource",
			mutate: func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped, Active: false, Required: true}}
				project.Resources = []domain.ResourceSnapshot{{
					ID: "home", Name: "Home", Kind: "site",
					Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "https://project-alpha.test",
				}}
			},
			want: "publishes 1 resources",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := RuntimeState{
				Snapshot: validRuntimeStateSnapshot(0),
				Network:  uninitializedRuntimeNetwork(),
			}
			test.mutate(&candidate.Snapshot.Projects[0])
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimeStateValidatePermitsDirectLoopbackLifecycle proves current-port development remains local without a Harbor network lease.
func TestRuntimeStateValidatePermitsDirectLoopbackLifecycle(t *testing.T) {
	for _, state := range []domain.ProjectState{
		domain.ProjectStarting,
		domain.ProjectFailed,
		domain.ProjectUnavailable,
	} {
		t.Run(string(state), func(t *testing.T) {
			candidate := RuntimeState{
				Snapshot: validRuntimeStateSnapshot(0),
				Network:  uninitializedRuntimeNetwork(),
			}
			candidate.Snapshot.Projects[0].State = state
			if err := candidate.Validate(); err != nil {
				t.Fatalf("validate %s direct project: %v", state, err)
			}
		})
	}

	for _, state := range []domain.ProjectState{
		domain.ProjectReady,
		domain.ProjectRebuilding,
		domain.ProjectDegraded,
		domain.ProjectStopping,
	} {
		t.Run(string(state), func(t *testing.T) {
			candidate := RuntimeState{
				Snapshot: validRuntimeStateSnapshot(0),
				Network:  uninitializedRuntimeNetwork(),
			}
			candidate.Snapshot.Projects[0] = directRuntimeStateProject("project-alpha", state, "http://127.0.0.1:3000")
			if err := candidate.Validate(); err != nil {
				t.Fatalf("validate %s loopback project: %v", state, err)
			}
		})
	}
}

// TestRuntimeStateValidateRejectsIncoherentDirectLifecycle prevents loopback URLs from masking corrupt active or transitional projections.
func TestRuntimeStateValidateRejectsIncoherentDirectLifecycle(t *testing.T) {
	tests := []struct {
		name    string
		project domain.ProjectSnapshot
		want    string
	}{
		{
			name: "starting active App",
			project: func() domain.ProjectSnapshot {
				project := validRuntimeStateProject("project-alpha")
				project.State = domain.ProjectStarting
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
				return project
			}(),
			want: `App "app" must be inactive`,
		},
		{
			name: "ready inactive required App",
			project: func() domain.ProjectSnapshot {
				project := directRuntimeStateProject("project-alpha", domain.ProjectReady, "http://127.0.0.1:3000")
				project.Apps[0].Active = false
				return project
			}(),
			want: `required App "app" must be active`,
		},
		{
			name: "ready working required App",
			project: func() domain.ProjectSnapshot {
				project := directRuntimeStateProject("project-alpha", domain.ProjectReady, "http://127.0.0.1:3000")
				project.Apps[0].State = domain.EntityWorking
				return project
			}(),
			want: `active App "app" state "working" is inconsistent`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := RuntimeState{
				Snapshot: func() domain.Snapshot {
					snapshot := validRuntimeStateSnapshot(0)
					snapshot.Projects[0] = test.project
					return snapshot
				}(),
				Network: uninitializedRuntimeNetwork(),
			}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimeStateValidateRequiresOwnershipForNonLoopbackResources distinguishes direct development from public routing claims.
func TestRuntimeStateValidateRequiresOwnershipForNonLoopbackResources(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "IPv4 loopback", url: "http://127.0.0.1:3000"},
		{name: "IPv6 loopback", url: "http://[::1]:3000"},
		{name: "named localhost", url: "http://localhost:3000", wantErr: true},
		{name: "private network", url: "http://192.168.1.20:3000", wantErr: true},
		{name: "Harbor hostname", url: "https://project-alpha.test", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := RuntimeState{
				Snapshot: validRuntimeStateSnapshot(0),
				Network:  uninitializedRuntimeNetwork(),
			}
			candidate.Snapshot.Projects[0] = directRuntimeStateProject("project-alpha", domain.ProjectReady, test.url)
			err := candidate.Validate()
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "not a literal loopback address")) {
				t.Fatalf("validation error = %v, want unleased route rejection", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validate direct loopback URL: %v", err)
			}
		})
	}
}

// TestStoreRuntimeStateAcceptsPersistedFailedDirectProject covers the restart shape produced by a failed first launch.
func TestStoreRuntimeStateAcceptsPersistedFailedDirectProject(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	projectID := "project-7f40ccf350106119028ba9394725e0a1"
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
		 VALUES (?, 'Failed project', ?, ?, 'failed', 0, ?, 1)`,
		projectID, "/work/failed-project", projectID, projectStoreReadTestTime(),
	)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")

	runtimeState, err := store.RuntimeState(t.Context())
	if err != nil {
		t.Fatalf("RuntimeState() failed direct project: %v", err)
	}
	if len(runtimeState.Snapshot.Projects) != 1 || runtimeState.Snapshot.Projects[0].State != domain.ProjectFailed {
		t.Fatalf("RuntimeState() project = %#v", runtimeState.Snapshot.Projects)
	}
}

// TestStoreRuntimeStateDistinguishesNetworkSchemaLifecycle verifies old, migrated, and initialized databases remain explicit.
func TestStoreRuntimeStateDistinguishesNetworkSchemaLifecycle(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T, *gorm.DB)
		initialized bool
	}{
		{name: "legacy"},
		{
			name: "migrated but uninitialized",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkStoreReadSchema(t, connection)
			},
		},
		{
			name: "initialized",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkStoreReadSchema(t, connection)
				seedRuntimeStateNetwork(t, connection)
			},
			initialized: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			if test.setup != nil {
				test.setup(t, connection)
			}

			state, err := store.RuntimeState(nil)
			if err != nil {
				t.Fatalf("read runtime state: %v", err)
			}
			if state.NetworkInitialized != test.initialized {
				t.Fatalf("network initialized = %t, want %t", state.NetworkInitialized, test.initialized)
			}
			if err := state.Validate(); err != nil {
				t.Fatalf("validate returned runtime state: %v", err)
			}
			if state.Snapshot.Projects == nil || state.Snapshot.Operations == nil || state.Snapshot.RecentResourceIDs == nil {
				t.Fatalf("snapshot contains nil collections: %#v", state.Snapshot)
			}
			if state.Network.Leases == nil || state.Network.Quarantines == nil || state.Network.Reservations.Endpoints == nil || state.Network.Reservations.SuppressedProjectIDs == nil {
				t.Fatalf("network contains nil collections: %#v", state.Network)
			}
			if test.initialized {
				if state.Snapshot.Sequence != 7 || state.Network.Revision != 7 || len(state.Snapshot.Projects) != 2 {
					t.Fatalf("initialized runtime authority = snapshot %d, network %d, projects %d", state.Snapshot.Sequence, state.Network.Revision, len(state.Snapshot.Projects))
				}
				return
			}
			if !reflect.DeepEqual(state.Network, uninitializedRuntimeNetwork()) {
				t.Fatalf("uninitialized network = %#v, want canonical empty", state.Network)
			}
		})
	}
}

// TestStoreRuntimeStateUsesOneDatabaseInstant verifies a concurrent durable transition cannot create a client/network hybrid.
func TestStoreRuntimeStateUsesOneDatabaseInstant(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 4, projectStoreReadTestClock)
	createNetworkStoreReadSchema(t, connection)
	seedRuntimeStateNetwork(t, connection)

	projectRead := make(chan struct{})
	releaseRead := make(chan struct{})
	var pause sync.Once
	var readTransaction *sql.Tx
	var inconsistentTransaction bool
	callback := "harbor:test_runtime_state_instant"
	if err := connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		transaction, ok := tx.Statement.ConnPool.(*sql.Tx)
		if !ok {
			inconsistentTransaction = true
		} else if readTransaction == nil {
			readTransaction = transaction
		} else if readTransaction != transaction {
			inconsistentTransaction = true
		}
		if tx.Statement.Table == "projects" {
			pause.Do(func() {
				close(projectRead)
				<-releaseRead
			})
		}
	}); err != nil {
		t.Fatalf("register runtime query observer: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Query().Remove(callback)
	})

	type runtimeResult struct {
		state RuntimeState
		err   error
	}
	result := make(chan runtimeResult, 1)
	go func() {
		state, err := store.RuntimeState(context.Background())
		result <- runtimeResult{state: state, err: err}
	}()
	select {
	case <-projectRead:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime read did not reach project projection")
	}

	writeResult := make(chan error, 1)
	go func() {
		writeResult <- connection.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE projects SET name = 'After', revision = 8 WHERE project_id = 'project-beta'").Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE network_state SET revision = 9, updated_at = ? WHERE id = 1", networkTestTime().Add(11*time.Minute)).Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE harbor_state SET sequence = 9 WHERE id = 1").Error
		})
	}()
	select {
	case err := <-writeResult:
		close(releaseRead)
		t.Fatalf("concurrent transition finished before runtime read released it: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRead)

	read := <-result
	if read.err != nil {
		t.Fatalf("read concurrent runtime state: %v", read.err)
	}
	if inconsistentTransaction || readTransaction == nil {
		t.Fatalf("runtime queries did not share one SQL transaction")
	}
	if read.state.Snapshot.Sequence != 7 || read.state.Network.Revision != 7 || read.state.Snapshot.Projects[1].Name != "Project" {
		t.Fatalf("runtime read mixed durable generations: %#v", read.state)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("commit concurrent runtime transition: %v", err)
	}

	after, err := store.RuntimeState(context.Background())
	if err != nil {
		t.Fatalf("read transitioned runtime state: %v", err)
	}
	if after.Snapshot.Sequence != 9 || after.Network.Revision != 9 || after.Snapshot.Projects[1].Name != "After" {
		t.Fatalf("transitioned runtime state = %#v", after)
	}
}

// TestStoreRuntimeStateFailsClosedWithoutPartialResults verifies schema, aggregate, and global ownership corruption never escape.
func TestStoreRuntimeStateFailsClosedWithoutPartialResults(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
		want  string
	}{
		{
			name: "partial schema",
			setup: func(t *testing.T, connection *gorm.DB) {
				mustProjectStoreReadExec(t, connection, networkStoreReadTestSchema[1])
			},
			want: "network persistence schema is incomplete",
		},
		{
			name: "orphan network child",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkStoreReadSchema(t, connection)
				mustProjectStoreReadExec(t, connection, "INSERT INTO network_pool_candidates (id, network_state_id, ordinal, address, generation) VALUES (1, 1, 1, '127.77.0.10', 1)")
			},
			want: "child rows exist without the singleton root",
		},
		{
			name: "network future revision",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkStoreReadSchema(t, connection)
				seedRuntimeStateNetwork(t, connection)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 6 WHERE id = 1")
			},
			want: "exceeds captured sequence 6",
		},
		{
			name: "network revision collision",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkStoreReadSchema(t, connection)
				seedRuntimeStateNetwork(t, connection)
				mustProjectStoreReadExec(t, connection, "UPDATE projects SET revision = 7 WHERE project_id = 'project-beta'")
			},
			want: "reuses revision owned by",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			test.setup(t, connection)

			state, err := store.RuntimeState(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runtime read error = %v, want containing %q", err, test.want)
			}
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) {
				t.Fatalf("runtime read error = %v, want CorruptStateError", err)
			}
			if !reflect.DeepEqual(state, RuntimeState{}) {
				t.Fatalf("failed runtime read returned partial state: %#v", state)
			}
		})
	}
}

// TestStoreRuntimeStatePreservesQueryFailures verifies every projection layer retains storage error identity.
func TestStoreRuntimeStatePreservesQueryFailures(t *testing.T) {
	tables := []string{
		"harbor_state",
		"projects",
		"project_apps",
		"project_services",
		"project_resources",
		"operations",
		"operation_transitions",
		"recent_resources",
	}
	tables = append(tables, networkTableNames()...)
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			createNetworkStoreReadSchema(t, connection)
			seedRuntimeStateNetwork(t, connection)
			queryErr := errors.New("runtime query failure for " + table)
			callback := "harbor:test_runtime_query_failure_" + table
			if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(queryErr)
				}
			}); err != nil {
				t.Fatalf("register %s query failure: %v", table, err)
			}
			t.Cleanup(func() {
				_ = connection.Callback().Query().Remove(callback)
			})

			state, err := store.RuntimeState(context.Background())
			if !errors.Is(err, queryErr) {
				t.Fatalf("%s query error = %v, want sentinel identity", table, err)
			}
			if !reflect.DeepEqual(state, RuntimeState{}) {
				t.Fatalf("%s query failure returned partial state: %#v", table, state)
			}
		})
	}
}

// TestStoreRuntimeStateReportsRepositoryOpenFailures verifies connection setup errors stop before any partial read begins.
func TestStoreRuntimeStateReportsRepositoryOpenFailures(t *testing.T) {
	t.Setenv("DB_HARBORD_DRIVER", "unsupported")
	connections := database.NewConnections(inspects.NewManager())
	store := NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
	)

	state, err := store.RuntimeState(context.Background())
	if err == nil || !strings.Contains(err.Error(), "open Harbor runtime state") {
		t.Fatalf("runtime open error = %v", err)
	}
	if !reflect.DeepEqual(state, RuntimeState{}) {
		t.Fatalf("runtime open failure returned partial state: %#v", state)
	}
}

// TestStoreRuntimeStateHonorsCancellationAndReturnsDefensiveState verifies caller control and ownership of returned slices.
func TestStoreRuntimeStateHonorsCancellationAndReturnsDefensiveState(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	createNetworkStoreReadSchema(t, connection)
	seedRuntimeStateNetwork(t, connection)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := store.RuntimeState(cancelled)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(state, RuntimeState{}) {
		t.Fatalf("cancelled runtime read = state %#v, error %v", state, err)
	}

	first, err := store.RuntimeState(context.Background())
	if err != nil {
		t.Fatalf("read first runtime state: %v", err)
	}
	baseline, err := store.RuntimeState(context.Background())
	if err != nil {
		t.Fatalf("read runtime baseline: %v", err)
	}
	first.Snapshot.Projects[0].Name = "Mutated"
	first.Snapshot.Operations[0].Phase = "mutated"
	first.Network.Leases[0].Key.ProjectID = "mutated-project"
	first.Network.Reservations.Endpoints[0].Host = "mutated.test"
	first.Network.Reservations.SuppressedProjectIDs[0] = "mutated-project"
	afterMutation, err := store.RuntimeState(context.Background())
	if err != nil {
		t.Fatalf("read runtime state after caller mutation: %v", err)
	}
	if !reflect.DeepEqual(afterMutation, baseline) {
		t.Fatalf("caller mutation changed durable runtime output: got %#v, want %#v", afterMutation, baseline)
	}
}

// seedRuntimeStateNetwork adds complete operation histories to the network read fixture for client snapshot validation.
func seedRuntimeStateNetwork(t *testing.T, connection *gorm.DB) {
	t.Helper()
	seedInitializedNetworkStoreReadState(t, connection)
	requestedAt := projectStoreReadTestTime()
	startedAt := requestedAt.Add(time.Minute)
	mustProjectStoreReadExec(t, connection,
		"UPDATE operations SET revision = 5, started_at = ? WHERE id = 'operation-release-alpha'",
		startedAt,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES ('operation-release-alpha', 1, 'queued', 'queued', ?, 3)`,
		requestedAt,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
		 VALUES ('operation-release-alpha', 2, 'queued', 'running', 'network.release', ?, 5)`,
		startedAt,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES ('operation-unrelated', 1, 'queued', 'queued', ?, 4)`,
		requestedAt,
	)
}

// validRuntimeStateSnapshot returns a minimal canonical client projection for aggregate validation tests.
func validRuntimeStateSnapshot(sequence domain.Sequence) domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          sequence,
		CapturedAt:        recordTestTime().Add(2 * time.Hour),
		Projects:          []domain.ProjectSnapshot{validRuntimeStateProject("project-alpha")},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// validRuntimeStateProject returns one empty but domain-valid project with the requested stable identity.
func validRuntimeStateProject(projectID domain.ProjectID) domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        projectID,
		Name:      "Project",
		Path:      "/work/" + string(projectID),
		Slug:      string(projectID),
		State:     domain.ProjectStopped,
		UpdatedAt: recordTestTime(),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
}

// directRuntimeStateProject returns the ready or stopping projection for one current-port loopback process.
func directRuntimeStateProject(projectID domain.ProjectID, state domain.ProjectState, resourceURL string) domain.ProjectSnapshot {
	project := validRuntimeStateProject(projectID)
	project.State = state
	project.Apps = []domain.AppSnapshot{{
		ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true,
	}}
	project.Resources = []domain.ResourceSnapshot{{
		ID: "home", Name: "Home", Kind: "application",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: resourceURL,
	}}
	return project
}
