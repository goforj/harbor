package state

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// projectStoreReadTestSchema mirrors the final projection columns and constraints without importing migrations back into state.
var projectStoreReadTestSchema = []string{
	`CREATE TABLE harbor_state (
		id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
		sequence INTEGER NOT NULL CHECK (sequence >= 0)
	)`,
	`INSERT INTO harbor_state (id, sequence) VALUES (1, 0)`,
	`CREATE TABLE projects (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		path TEXT NOT NULL UNIQUE,
		slug TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL,
		favorite BOOLEAN NOT NULL,
		updated_at DATETIME NOT NULL,
		revision INTEGER NOT NULL UNIQUE
	)`,
	`CREATE TABLE project_sessions (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL UNIQUE,
		project_id TEXT NOT NULL UNIQUE REFERENCES projects(project_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
		owner TEXT NOT NULL,
		state TEXT NOT NULL,
		descriptor_digest TEXT NOT NULL,
		credential_digest TEXT NOT NULL,
		generation INTEGER NOT NULL,
		pid INTEGER,
		birth_token TEXT,
		executable_identity TEXT,
		argument_digest TEXT,
		output_broker_endpoint_reference TEXT,
		output_broker_ticket_digest TEXT,
		output_broker_manifest_path TEXT,
		output_broker_pid INTEGER,
		output_broker_birth_token TEXT,
		output_broker_executable_identity TEXT,
		output_broker_argument_digest TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`,
	`CREATE TABLE project_apps (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		app_id TEXT NOT NULL,
		name TEXT NOT NULL,
		state TEXT NOT NULL,
		active BOOLEAN NOT NULL,
		required BOOLEAN NOT NULL,
		UNIQUE (project_id, app_id)
	)`,
	`CREATE TABLE project_services (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		service_id TEXT NOT NULL,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		state TEXT NOT NULL,
		owner TEXT NOT NULL,
		selection TEXT NOT NULL,
		required BOOLEAN NOT NULL,
		UNIQUE (project_id, service_id)
	)`,
	`CREATE TABLE project_resources (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
		resource_id TEXT NOT NULL,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		url TEXT NOT NULL,
		owner_kind TEXT NOT NULL,
		owner_app_id TEXT,
		owner_service_id TEXT,
		UNIQUE (project_id, resource_id),
		FOREIGN KEY (project_id, owner_app_id) REFERENCES project_apps(project_id, app_id) ON DELETE CASCADE,
		FOREIGN KEY (project_id, owner_service_id) REFERENCES project_services(project_id, service_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE recent_resources (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		resource_id TEXT NOT NULL,
		accessed_at DATETIME NOT NULL,
		sequence INTEGER NOT NULL UNIQUE,
		UNIQUE (project_id, resource_id),
		FOREIGN KEY (project_id, resource_id) REFERENCES project_resources(project_id, resource_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE operations (
		id TEXT NOT NULL PRIMARY KEY,
		intent_id TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		project_id TEXT,
		state TEXT NOT NULL,
		phase TEXT NOT NULL,
		problem_code TEXT,
		problem_message TEXT,
		problem_retryable BOOLEAN,
		requested_at DATETIME NOT NULL,
		started_at DATETIME,
		finished_at DATETIME,
		revision INTEGER NOT NULL UNIQUE
	)`,
	`CREATE TABLE operation_transitions (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
		ordinal INTEGER NOT NULL,
		previous_state TEXT,
		state TEXT NOT NULL,
		phase TEXT NOT NULL,
		problem_code TEXT,
		problem_message TEXT,
		problem_retryable BOOLEAN,
		occurred_at DATETIME NOT NULL,
		sequence INTEGER NOT NULL UNIQUE,
		UNIQUE (operation_id, ordinal)
	)`,
}

// TestStoreReadReturnsCanonicalFreshSnapshot verifies a new database is immediately safe to serialize to clients.
func TestStoreReadReturnsCanonicalFreshSnapshot(t *testing.T) {
	capturedAt := projectStoreReadTestTime().Add(time.Hour)
	store, _ := newProjectStoreReadTestHarness(t, 1, func() time.Time { return capturedAt })

	sequence, err := store.CurrentSequence(nil)
	if err != nil {
		t.Fatalf("read current sequence: %v", err)
	}
	if sequence != 0 {
		t.Fatalf("current sequence = %d, want 0", sequence)
	}
	snapshot, err := store.Snapshot(nil)
	if err != nil {
		t.Fatalf("read fresh snapshot: %v", err)
	}
	if snapshot.SchemaVersion != domain.SnapshotSchemaVersion || snapshot.Sequence != 0 || snapshot.CapturedAt != capturedAt {
		t.Fatalf("fresh snapshot metadata = %#v", snapshot)
	}
	if snapshot.Projects == nil || snapshot.Operations == nil || snapshot.RecentResourceIDs == nil {
		t.Fatalf("fresh snapshot contains nil collections: %#v", snapshot)
	}
	if len(snapshot.Projects) != 0 || len(snapshot.Operations) != 0 || len(snapshot.RecentResourceIDs) != 0 {
		t.Fatalf("fresh snapshot contains durable rows: %#v", snapshot)
	}
}

// TestStoreReadReturnsPopulatedStateInCanonicalOrder verifies query insertion order never leaks into a client snapshot.
func TestStoreReadReturnsPopulatedStateInCanonicalOrder(t *testing.T) {
	capturedAt := projectStoreReadTestTime().Add(10 * time.Hour)
	store, connection := newProjectStoreReadTestHarness(t, 1, func() time.Time { return capturedAt })
	seedPopulatedProjectStoreReadState(t, connection)

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read populated snapshot: %v", err)
	}
	want := populatedProjectStoreReadSnapshot(capturedAt)
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("populated snapshot = %#v, want %#v", snapshot, want)
	}

	project, err := store.Project(context.Background(), "project-alpha")
	if err != nil {
		t.Fatalf("read project aggregate: %v", err)
	}
	if project.Revision != 2 || !reflect.DeepEqual(project.Project, want.Projects[0]) {
		t.Fatalf("project record = %#v, want alpha at revision 2", project)
	}
	sequence, err := store.CurrentSequence(context.Background())
	if err != nil || sequence != 8 {
		t.Fatalf("current sequence = %d, error %v, want 8", sequence, err)
	}
}

// TestStoreReadReportsMissingProject verifies callers can distinguish absence from storage and validation failures.
func TestStoreReadReportsMissingProject(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)

	_, err := store.Project(context.Background(), "missing-project")
	var missing *ProjectNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("missing project error = %v, want ProjectNotFoundError", err)
	}
	if missing.ProjectID != "missing-project" || !strings.Contains(err.Error(), `project "missing-project" was not found`) {
		t.Fatalf("missing project error = %#v / %v", missing, err)
	}
}

// TestStoreReadHonorsContext verifies cancellation stops before storage while nil contexts remain usable.
func TestStoreReadHonorsContext(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-context", 1)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CurrentSequence(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sequence error = %v, want context.Canceled", err)
	}
	if _, err := store.Project(cancelled, "project-context"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled project error = %v, want context.Canceled", err)
	}
	if _, err := store.Snapshot(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled snapshot error = %v, want context.Canceled", err)
	}
	if _, err := store.Project(nil, "project-context"); err != nil {
		t.Fatalf("project with nil context: %v", err)
	}
	if _, err := store.Snapshot(nil); err != nil {
		t.Fatalf("snapshot with nil context: %v", err)
	}
	if _, err := store.Project(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "project ID must not be empty") {
		t.Fatalf("invalid project ID error = %v", err)
	}
}

// TestStoreReadNormalizesCaptureClockToUTC verifies process locale cannot leak into snapshot timestamps.
func TestStoreReadNormalizesCaptureClockToUTC(t *testing.T) {
	zone := time.FixedZone("test", 5*60*60)
	local := time.Date(2026, time.July, 18, 15, 30, 0, 123, zone)
	store, _ := newProjectStoreReadTestHarness(t, 1, func() time.Time { return local })

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read UTC snapshot: %v", err)
	}
	if snapshot.CapturedAt != local.UTC() {
		t.Fatalf("capture time = %s, want %s", snapshot.CapturedAt, local.UTC())
	}
}

// TestStoreReadRejectsInvalidCaptureClock verifies a broken clock cannot emit a snapshot that violates the domain contract.
func TestStoreReadRejectsInvalidCaptureClock(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, func() time.Time { return time.Time{} })

	_, err := store.Snapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "snapshot capture time must not be zero") {
		t.Fatalf("zero capture clock error = %v", err)
	}
}

// TestStoreReadReportsQueryFailures verifies every normalized snapshot query releases its transaction with a storage error.
func TestStoreReadReportsQueryFailures(t *testing.T) {
	tables := []string{
		"projects",
		"project_apps",
		"project_services",
		"project_resources",
		"operations",
		"operation_transitions",
		"recent_resources",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+table)

			if _, err := store.Snapshot(context.Background()); err == nil {
				t.Fatalf("snapshot unexpectedly read missing %s table", table)
			}
		})
	}

	t.Run("harbor_state", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		mustProjectStoreReadExec(t, connection, "DROP TABLE harbor_state")
		if _, err := store.CurrentSequence(context.Background()); err == nil {
			t.Fatal("sequence unexpectedly read a missing Harbor state table")
		}
	})

	for _, table := range []string{"project_apps", "project_services", "project_resources"} {
		t.Run("Project/"+table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			seedSingleProjectStoreReadState(t, connection, "project-query", 1)
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+table)
			if _, err := store.Project(context.Background(), "project-query"); err == nil {
				t.Fatalf("project unexpectedly read a missing %s table", table)
			}
		})
	}
}

// TestStoreReadReportsRepositoryOpenFailures covers generated accessor failures before a transaction can begin.
func TestStoreReadReportsRepositoryOpenFailures(t *testing.T) {
	t.Setenv("DB_HARBORD_DRIVER", "unsupported")
	connections := database.NewConnections(inspects.NewManager())
	store := NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
	)

	if _, err := store.CurrentSequence(context.Background()); err == nil || !strings.Contains(err.Error(), "open Harbor state") {
		t.Fatalf("sequence open error = %v", err)
	}
	if _, err := store.Project(context.Background(), "project-open"); err == nil || !strings.Contains(err.Error(), "open project state") {
		t.Fatalf("project open error = %v", err)
	}
	if _, err := store.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "open Harbor snapshot") {
		t.Fatalf("snapshot open error = %v", err)
	}
	if _, err := store.ActiveProjectSession(context.Background(), "project-open"); err == nil || !strings.Contains(err.Error(), "open project session state") {
		t.Fatalf("project session open error = %v", err)
	}
	if _, _, err := store.Network(context.Background()); err == nil || !strings.Contains(err.Error(), "open network state") {
		t.Fatalf("network open error = %v", err)
	}
}

// TestStoreProjectRejectsSingletonAndProjectQueryFailures covers transaction failures before aggregate conversion.
func TestStoreProjectRejectsSingletonAndProjectQueryFailures(t *testing.T) {
	t.Run("singleton", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		mustProjectStoreReadExec(t, connection, "DELETE FROM harbor_state")
		if _, err := store.Project(context.Background(), "project-singleton"); err == nil || !strings.Contains(err.Error(), "singleton row is missing") {
			t.Fatalf("project singleton error = %v", err)
		}
	})

	t.Run("projects query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "DROP TABLE projects")
		if _, err := store.Project(context.Background(), "project-query"); err == nil || !strings.Contains(err.Error(), "read project row") {
			t.Fatalf("project query error = %v", err)
		}
	})
}

// TestStoreSnapshotRejectsDuplicateProjects verifies the complete read path does not collapse weakened duplicate roots.
func TestStoreSnapshotRejectsDuplicateProjects(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	weakenProjectStoreReadProjectsTable(t, connection)
	insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/one", "one", 1)
	insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/two", "two", 2)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")

	_, err := store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "project", "project ID is duplicated")
}

// TestStoreSnapshotRejectsMalformedRecentRow verifies recency conversion errors stop before cross-record validation.
func TestStoreSnapshotRejectsMalformedRecentRow(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES ('', '', ?, 1)`,
		projectStoreReadTestTime(),
	)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")

	_, err := store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "recent resource", "project ID must not be empty")
}

// TestStoreReadRejectsActiveOperationSequenceReuse covers both complete and singular operation-owner collision branches.
func TestStoreReadRejectsActiveOperationSequenceReuse(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-operation-owner", 1)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
		 VALUES ('operation-owner', 'intent-owner', 'maintenance.run', 'queued', 'queued', ?, 1)`, projectStoreReadTestTime(),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES ('operation-owner', 1, 'queued', 'queued', ?, 1)`, projectStoreReadTestTime(),
	)

	_, err := store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "operation \"operation-owner\" reuses revision")
	_, err = store.Project(context.Background(), "project-operation-owner")
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "operation \"operation-owner\"")
}

// TestStoreReadCoversSequenceValidationFailures exercises hard-to-persist zero and second-pass owner branches directly.
func TestStoreReadCoversSequenceValidationFailures(t *testing.T) {
	if err := validateVisibleSequence(1, 0, "test owner", nil); err == nil || !strings.Contains(err.Error(), "zero revision") {
		t.Fatalf("zero visible sequence error = %v", err)
	}

	_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	project := ProjectRecord{Project: domain.ProjectSnapshot{ID: "project-future"}, Revision: 2}
	err := validateOperationSequenceHistory(connection, 1, []ProjectRecord{project}, nil, nil)
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "exceeds captured sequence")
	recent := RecentResourceRecord{Reference: domain.ResourceRef{ProjectID: "project", ResourceID: "resource"}, Sequence: 2}
	err = validateOperationSequenceHistory(connection, 1, nil, nil, []RecentResourceRecord{recent})
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "exceeds captured sequence")
}

// TestStoreProjectSequenceOwnerReportsQueryFailures covers each targeted owner inventory query independently.
func TestStoreProjectSequenceOwnerReportsQueryFailures(t *testing.T) {
	tables := []string{"projects", "recent_resources", "operations", "operation_transitions"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			if table != "projects" {
				insertEmptyProjectStoreReadProject(t, connection, "project-owner", "/work/owner", "owner", 1)
			}
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+table)

			err := validateProjectSequenceOwner(connection, ProjectRecord{
				Project:  domain.ProjectSnapshot{ID: "project-owner"},
				Revision: 1,
			})
			if err == nil {
				t.Fatalf("owner inventory unexpectedly read missing %s table", table)
			}
		})
	}
}

// TestStoreSnapshotRejectsBrokenOperationHistory verifies ordered edges must exactly explain an active operation header.
func TestStoreSnapshotRejectsBrokenOperationHistory(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
		want  string
	}{
		{
			name: "ordinal gap",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				requestedAt := projectStoreReadTestTime()
				startedAt := requestedAt.Add(time.Second)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, started_at, revision)
					 VALUES ('operation-gap', 'intent-gap', 'maintenance.run', 'running', 'running', ?, ?, 2)`, requestedAt, startedAt,
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
					 VALUES ('operation-gap', 2, 'queued', 'running', 'running', ?, 2)`, startedAt,
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
			},
			want: "ordinal is 2, expected 1",
		},
		{
			name: "non-increasing sequence",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				requestedAt := projectStoreReadTestTime()
				startedAt := requestedAt.Add(time.Second)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, started_at, revision)
					 VALUES ('operation-order', 'intent-order', 'maintenance.run', 'running', 'running', ?, ?, 1)`, requestedAt, startedAt,
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
					 VALUES ('operation-order', 1, 'queued', 'queued', ?, 2)`, requestedAt,
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
					 VALUES ('operation-order', 2, 'queued', 'running', 'running', ?, 1)`, startedAt,
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
			},
			want: "sequence must increase across operation history",
		},
		{
			name: "header contradiction",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				requestedAt := projectStoreReadTestTime()
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
					 VALUES ('operation-header', 'intent-header', 'maintenance.run', 'queued', 'queued', ?, 1)`, requestedAt,
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
					 VALUES ('operation-header', 1, 'queued', 'different', ?, 1)`, requestedAt,
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
			},
			want: "phase does not match latest transition",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			test.setup(t, connection)

			_, err := store.Snapshot(context.Background())
			assertProjectStoreReadHasCorruption(t, err, test.name)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("broken history error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestStoreReadRejectsCorruptSingleton verifies every read fails closed when the global sequence authority is malformed.
func TestStoreReadRejectsCorruptSingleton(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *gorm.DB)
		want    string
	}{
		{
			name: "missing",
			corrupt: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "DELETE FROM harbor_state")
			},
			want: "singleton row is missing",
		},
		{
			name: "extra authority",
			corrupt: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				mustProjectStoreReadExec(t, connection, "INSERT INTO harbor_state (id, sequence) VALUES (2, 0)")
			},
			want: "singleton ID must be 1",
		},
		{
			name: "negative sequence",
			corrupt: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = -1 WHERE id = 1")
			},
			want: "sequence must not be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			test.corrupt(t, connection)

			_, err := store.CurrentSequence(context.Background())
			assertProjectStoreReadCorruption(t, err, "harbor state", test.want)
			if _, err := store.Snapshot(context.Background()); err == nil {
				t.Fatal("snapshot unexpectedly accepted corrupt singleton")
			}
		})
	}
}

// TestStoreReadRejectsOrphanedProjectRows verifies weakened foreign keys cannot hide normalized child corruption.
func TestStoreReadRejectsOrphanedProjectRows(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		entity    string
	}{
		{
			name:      "App",
			statement: `INSERT INTO project_apps (project_id, app_id, name, state, active, required) VALUES ('missing', 'api', 'API', 'ready', 1, 1)`,
			entity:    "project App",
		},
		{
			name:      "service",
			statement: `INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required) VALUES ('missing', 'mysql', 'MySQL', 'database', 'ready', 'compose', 'selected', 1)`,
			entity:    "project service",
		},
		{
			name:      "resource",
			statement: `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id) VALUES ('missing', 'docs', 'Docs', 'documentation', 'https://missing.test', 'app', 'api')`,
			entity:    "project resource",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, test.statement)

			_, err := store.Snapshot(context.Background())
			assertProjectStoreReadCorruption(t, err, test.entity, "parent project is missing")
		})
	}
}

// TestStoreReadRejectsMalformedAggregate verifies durable rows are converted through the complete domain validator.
func TestStoreReadRejectsMalformedAggregate(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-invalid", 1)
	mustProjectStoreReadExec(t, connection, "UPDATE project_resources SET url = 'ftp://invalid.test' WHERE project_id = 'project-invalid'")

	_, err := store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "project resource", "absolute HTTP or HTTPS URL")
	if _, err := store.Project(context.Background(), "project-invalid"); err == nil {
		t.Fatal("project read unexpectedly accepted malformed resource")
	}
}

// TestStoreReadRejectsFutureVisibleSequences verifies materialized rows cannot claim history the singleton has not committed.
func TestStoreReadRejectsFutureVisibleSequences(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-future", 2)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")

	_, err := store.Project(context.Background(), "project-future")
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "exceeds captured sequence 1")
	_, err = store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "exceeds captured sequence 1")
}

// TestStoreReadRejectsCrossKindSequenceReuse verifies the total order is unique across projects, operations, and recency rows.
func TestStoreReadRejectsCrossKindSequenceReuse(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-duplicate", 1)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, ?, ?, ?)`,
		"project-duplicate", "docs", projectStoreReadTestTime().Add(time.Minute), 1,
	)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")

	_, err := store.Snapshot(context.Background())
	assertProjectStoreReadCorruption(t, err, "Harbor sequence", "reuses revision owned by")
}

// TestStoreReadRejectsInvalidSnapshotReferences verifies cross-record references are checked after every row is converted.
func TestStoreReadRejectsInvalidSnapshotReferences(t *testing.T) {
	t.Run("active operation project", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO operations (id, intent_id, kind, project_id, state, phase, requested_at, revision)
			 VALUES (?, ?, ?, ?, 'queued', 'queued', ?, 1)`,
			"operation-orphan", "intent-orphan", "project.start", "missing-project", projectStoreReadTestTime(),
		)
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
			 VALUES ('operation-orphan', 1, 'queued', 'queued', ?, 1)`, projectStoreReadTestTime(),
		)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")

		_, err := store.Snapshot(context.Background())
		assertProjectStoreReadCorruption(t, err, "snapshot", "references unknown project")
	})

	t.Run("recent resource", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		seedSingleProjectStoreReadState(t, connection, "project-recent-orphan", 1)
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, ?, ?, ?)`,
			"project-recent-orphan", "missing-resource", projectStoreReadTestTime().Add(time.Minute), 2,
		)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")

		_, err := store.Snapshot(context.Background())
		assertProjectStoreReadCorruption(t, err, "snapshot", "references unknown resource")
	})
}

// TestStoreReadRejectsRetainedHistorySequenceReuse verifies terminal operation history remains an owner of global sequence values.
func TestStoreReadRejectsRetainedHistorySequenceReuse(t *testing.T) {
	t.Run("project revision", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		seedSingleProjectStoreReadState(t, connection, "project-history", 2)
		seedSucceededOperationHistory(t, connection, "terminal-project", 2, 3, 4)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 4 WHERE id = 1")

		_, err := store.Snapshot(context.Background())
		assertProjectStoreReadHasCorruption(t, err, "retained operation history reused a project revision")
	})

	t.Run("recent sequence", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		seedSingleProjectStoreReadState(t, connection, "project-history", 1)
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES ('project-history', 'docs', ?, 2)`,
			projectStoreReadTestTime().Add(time.Minute),
		)
		seedSucceededOperationHistory(t, connection, "terminal-recent", 2, 3, 4)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 4 WHERE id = 1")

		_, err := store.Snapshot(context.Background())
		assertProjectStoreReadHasCorruption(t, err, "retained operation history reused a recent-resource sequence")
	})
}

// TestStoreReadRejectsInvalidRetainedHistoryOrdering verifies the high-water mark and operation revision agree with retained transitions.
func TestStoreReadRejectsInvalidRetainedHistoryOrdering(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
	}{
		{
			name: "future transition",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				seedSucceededOperationHistory(t, connection, "terminal-future", 1, 2, 3)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
			},
		},
		{
			name: "nonpositive transition",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				seedSucceededOperationHistory(t, connection, "terminal-zero", 0, 1, 2)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
			},
		},
		{
			name: "active revision is not latest transition",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
					 VALUES ('operation-mismatch', 'intent-mismatch', 'project.start', 'queued', 'queued', ?, 2)`, projectStoreReadTestTime(),
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
					 VALUES ('operation-mismatch', 1, 'queued', 'queued', ?, 1)`, projectStoreReadTestTime(),
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
			},
		},
		{
			name: "active operation has no history",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
					 VALUES ('operation-missing', 'intent-missing', 'project.start', 'queued', 'queued', ?, 1)`, projectStoreReadTestTime(),
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			test.setup(t, connection)

			_, err := store.Snapshot(context.Background())
			assertProjectStoreReadHasCorruption(t, err, test.name)
		})
	}
}

// TestStoreProjectRejectsAmbiguousDurableOwnership verifies singular reads do not bypass whole-store integrity checks.
func TestStoreProjectRejectsAmbiguousDurableOwnership(t *testing.T) {
	t.Run("duplicate project ID", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		weakenProjectStoreReadProjectsTable(t, connection)
		insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/one", "one", 1)
		insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/two", "two", 2)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")

		_, err := store.Project(context.Background(), "project-duplicate")
		assertProjectStoreReadHasCorruption(t, err, "duplicate project ID")
	})

	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
	}{
		{
			name: "another project",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				weakenProjectStoreReadProjectsTable(t, connection)
				insertEmptyProjectStoreReadProject(t, connection, "project-target", "/work/target", "target", 1)
				insertEmptyProjectStoreReadProject(t, connection, "project-other", "/work/other", "other", 1)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
			},
		},
		{
			name: "recent resource",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				seedSingleProjectStoreReadState(t, connection, "project-target", 1)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES ('project-target', 'docs', ?, 1)`,
					projectStoreReadTestTime().Add(time.Minute),
				)
			},
		},
		{
			name: "operation transition",
			setup: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				seedSingleProjectStoreReadState(t, connection, "project-target", 2)
				seedSucceededOperationHistory(t, connection, "terminal-target", 2, 3, 4)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 4 WHERE id = 1")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			test.setup(t, connection)

			_, err := store.Project(context.Background(), "project-target")
			assertProjectStoreReadHasCorruption(t, err, test.name)
		})
	}
}

// TestStoreReadUsesOneConsistentTransaction verifies concurrent commits cannot produce a root/child hybrid snapshot.
func TestStoreReadUsesOneConsistentTransaction(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 4, projectStoreReadTestClock)
	seedSingleProjectStoreReadState(t, connection, "project-consistent", 1)

	projectRead := make(chan struct{})
	releaseRead := make(chan struct{})
	var pause sync.Once
	if err := connection.Callback().Query().After("gorm:query").Register("harbor:test_snapshot_pause", func(tx *gorm.DB) {
		if tx.Statement.Table == "projects" {
			pause.Do(func() {
				close(projectRead)
				<-releaseRead
			})
		}
	}); err != nil {
		t.Fatalf("register snapshot query pause: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Query().Remove("harbor:test_snapshot_pause")
	})

	type snapshotResult struct {
		snapshot domain.Snapshot
		err      error
	}
	result := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := store.Snapshot(context.Background())
		result <- snapshotResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-projectRead:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot did not reach project query")
	}

	updatedAt := projectStoreReadTestTime().Add(2 * time.Hour)
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- connection.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE harbor_state SET sequence = 2 WHERE id = 1").Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE projects SET name = 'After', updated_at = ?, revision = 2 WHERE project_id = 'project-consistent'", updatedAt).Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE project_apps SET name = 'After App' WHERE project_id = 'project-consistent'").Error
		})
	}()
	select {
	case err := <-writeResult:
		close(releaseRead)
		t.Fatalf("concurrent mutation finished before snapshot released it: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRead)

	read := <-result
	if read.err != nil {
		t.Fatalf("read concurrent snapshot: %v", read.err)
	}
	if read.snapshot.Sequence != 1 || read.snapshot.Projects[0].Name != "Before" || read.snapshot.Projects[0].Apps[0].Name != "Before App" {
		t.Fatalf("transaction snapshot mixed projection generations: %#v", read.snapshot)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("commit concurrent projection: %v", err)
	}

	after, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read snapshot after concurrent commit: %v", err)
	}
	if after.Sequence != 2 || after.Projects[0].Name != "After" || after.Projects[0].Apps[0].Name != "After App" {
		t.Fatalf("post-commit snapshot = %#v", after)
	}
}

// newProjectStoreReadTestHarness creates a generated-repository Store over one isolated named SQLite database.
func newProjectStoreReadTestHarness(t *testing.T, maximumConnections int, clock func() time.Time) (*Store, *gorm.DB) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", string(rune('0'+maximumConnections)))
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", string(rune('0'+maximumConnections)))

	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close project Store database: %v", err)
		}
	})
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open project Store database: %v", err)
	}
	for _, statement := range projectStoreReadTestSchema {
		mustProjectStoreReadExec(t, connection, statement)
	}

	return newStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
		clock,
	), connection
}

// seedSingleProjectStoreReadState inserts one complete aggregate at the requested global revision.
func seedSingleProjectStoreReadState(t *testing.T, connection *gorm.DB, projectID string, revision int) {
	t.Helper()
	updatedAt := projectStoreReadTestTime()
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
		 VALUES (?, 'Before', ?, ?, 'ready', 0, ?, ?)`,
		projectID, "/work/"+projectID, projectID, updatedAt, revision,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO project_apps (project_id, app_id, name, state, active, required)
		 VALUES (?, 'api', 'Before App', 'ready', 1, 1)`, projectID,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required)
		 VALUES (?, 'mysql', 'MySQL', 'database', 'ready', 'compose', 'selected', 1)`, projectID,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		 VALUES (?, 'docs', 'Docs', 'documentation', 'https://docs.test', 'app', 'api')`, projectID,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_service_id)
		 VALUES (?, 'database', 'Database', 'database', 'https://database.test', 'service', 'mysql')`, projectID,
	)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = ? WHERE id = 1", revision)
}

// seedPopulatedProjectStoreReadState inserts rows out of presentation order to exercise every canonical sort.
func seedPopulatedProjectStoreReadState(t *testing.T, connection *gorm.DB) {
	t.Helper()
	base := projectStoreReadTestTime()
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
		 VALUES ('project-beta', 'Beta', '/work/beta', 'beta', 'stopped', 0, ?, 4)`, base.Add(4*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
		 VALUES ('project-alpha', 'Alpha', '/work/alpha', 'alpha', 'ready', 1, ?, 2)`, base.Add(2*time.Minute),
	)

	mustProjectStoreReadExec(t, connection, `INSERT INTO project_apps (project_id, app_id, name, state, active, required) VALUES ('project-alpha', 'worker', 'Worker', 'working', 1, 0)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_apps (project_id, app_id, name, state, active, required) VALUES ('project-alpha', 'api', 'API', 'ready', 1, 1)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_apps (project_id, app_id, name, state, active, required) VALUES ('project-beta', 'api', 'Beta API', 'stopped', 0, 1)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required) VALUES ('project-alpha', 'redis', 'Redis', 'cache', 'degraded', 'external', 'available', 0)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required) VALUES ('project-alpha', 'mysql', 'MySQL', 'database', 'ready', 'compose', 'selected', 1)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required) VALUES ('project-beta', 'mysql', 'Beta MySQL', 'database', 'stopped', 'compose', 'selected', 1)`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id) VALUES ('project-alpha', 'docs', 'Documentation', 'documentation', 'https://alpha.test/docs', 'app', 'api')`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_service_id) VALUES ('project-alpha', 'admin', 'Database Admin', 'admin', 'https://db.alpha.test', 'service', 'mysql')`)
	mustProjectStoreReadExec(t, connection, `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id) VALUES ('project-beta', 'docs', 'Documentation', 'documentation', 'https://beta.test/docs', 'app', 'api')`)

	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, project_id, state, phase, requested_at, revision)
		 VALUES ('operation-z', 'intent-z', 'project.start', 'project-alpha', 'queued', 'queued', ?, 5)`, base.Add(5*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES ('operation-z', 1, 'queued', 'queued', ?, 5)`, base.Add(5*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, project_id, state, phase, requested_at, revision)
		 VALUES ('operation-a', 'intent-a', 'project.stop', 'project-beta', 'queued', 'queued', ?, 7)`, base.Add(7*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES ('operation-a', 1, 'queued', 'queued', ?, 7)`, base.Add(7*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES ('project-alpha', 'docs', ?, 6)`, base.Add(6*time.Minute),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES ('project-beta', 'docs', ?, 8)`, base.Add(8*time.Minute),
	)
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 8 WHERE id = 1")
}

// seedSucceededOperationHistory inserts one valid terminal operation whose retained transitions own three global sequences.
func seedSucceededOperationHistory(t *testing.T, connection *gorm.DB, identity string, queuedSequence, runningSequence, succeededSequence int) {
	t.Helper()
	requestedAt := projectStoreReadTestTime().Add(time.Duration(succeededSequence) * time.Minute)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, started_at, finished_at, revision)
		 VALUES (?, ?, 'maintenance.run', 'succeeded', 'complete', ?, ?, ?, ?)`,
		"operation-"+identity, "intent-"+identity, requestedAt, startedAt, finishedAt, succeededSequence,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
		 VALUES (?, 1, 'queued', 'queued', ?, ?)`, "operation-"+identity, requestedAt, queuedSequence,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
		 VALUES (?, 2, 'queued', 'running', 'running', ?, ?)`, "operation-"+identity, startedAt, runningSequence,
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
		 VALUES (?, 3, 'running', 'succeeded', 'complete', ?, ?)`, "operation-"+identity, finishedAt, succeededSequence,
	)
}

// weakenProjectStoreReadProjectsTable removes uniqueness only for fail-closed singular-read corruption tests.
func weakenProjectStoreReadProjectsTable(t *testing.T, connection *gorm.DB) {
	t.Helper()
	mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
	mustProjectStoreReadExec(t, connection, "ALTER TABLE projects RENAME TO projects_strict")
	mustProjectStoreReadExec(t, connection, `CREATE TABLE projects (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		slug TEXT NOT NULL,
		state TEXT NOT NULL,
		favorite BOOLEAN NOT NULL,
		updated_at DATETIME NOT NULL,
		revision INTEGER NOT NULL
	)`)
}

// insertEmptyProjectStoreReadProject inserts a domain-valid aggregate without children into the weakened project table.
func insertEmptyProjectStoreReadProject(t *testing.T, connection *gorm.DB, projectID, path, slug string, revision int) {
	t.Helper()
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
		 VALUES (?, 'Project', ?, ?, 'stopped', 0, ?, ?)`,
		projectID, path, slug, projectStoreReadTestTime(), revision,
	)
}

// populatedProjectStoreReadSnapshot returns the exact public state seeded by seedPopulatedProjectStoreReadState.
func populatedProjectStoreReadSnapshot(capturedAt time.Time) domain.Snapshot {
	base := projectStoreReadTestTime()
	return domain.Snapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:      8,
		CapturedAt:    capturedAt,
		Projects: []domain.ProjectSnapshot{
			{
				ID:        "project-alpha",
				Name:      "Alpha",
				Path:      "/work/alpha",
				Slug:      "alpha",
				State:     domain.ProjectReady,
				Favorite:  true,
				UpdatedAt: base.Add(2 * time.Minute),
				Apps: []domain.AppSnapshot{
					{ID: "api", Name: "API", State: domain.EntityReady, Active: true, Required: true},
					{ID: "worker", Name: "Worker", State: domain.EntityWorking, Active: true, Required: false},
				},
				Services: []domain.ServiceSnapshot{
					{ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
					{ID: "redis", Name: "Redis", Kind: "cache", State: domain.EntityDegraded, Owner: domain.ServiceOwnerExternal, Selection: domain.ServiceAvailable, Required: false},
				},
				Resources: []domain.ResourceSnapshot{
					{ID: "admin", Name: "Database Admin", Kind: "admin", URL: "https://db.alpha.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "mysql"}},
					{ID: "docs", Name: "Documentation", Kind: "documentation", URL: "https://alpha.test/docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "api"}},
				},
			},
			{
				ID:        "project-beta",
				Name:      "Beta",
				Path:      "/work/beta",
				Slug:      "beta",
				State:     domain.ProjectStopped,
				UpdatedAt: base.Add(4 * time.Minute),
				Apps: []domain.AppSnapshot{
					{ID: "api", Name: "Beta API", State: domain.EntityStopped, Active: false, Required: true},
				},
				Services: []domain.ServiceSnapshot{
					{ID: "mysql", Name: "Beta MySQL", Kind: "database", State: domain.EntityStopped, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
				},
				Resources: []domain.ResourceSnapshot{
					{ID: "docs", Name: "Documentation", Kind: "documentation", URL: "https://beta.test/docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "api"}},
				},
			},
		},
		Operations: []domain.Operation{
			{ID: "operation-z", IntentID: "intent-z", Kind: "project.start", ProjectID: "project-alpha", State: domain.OperationQueued, Phase: "queued", RequestedAt: base.Add(5 * time.Minute)},
			{ID: "operation-a", IntentID: "intent-a", Kind: "project.stop", ProjectID: "project-beta", State: domain.OperationQueued, Phase: "queued", RequestedAt: base.Add(7 * time.Minute)},
		},
		RecentResourceIDs: []domain.ResourceRef{
			{ProjectID: "project-beta", ResourceID: "docs"},
			{ProjectID: "project-alpha", ResourceID: "docs"},
		},
	}
}

// mustProjectStoreReadExec applies one test setup statement or stops before the read assertion becomes ambiguous.
func mustProjectStoreReadExec(t *testing.T, connection *gorm.DB, statement string, arguments ...any) {
	t.Helper()
	if err := connection.Exec(statement, arguments...).Error; err != nil {
		t.Fatalf("execute project Store setup statement: %v", err)
	}
}

// assertProjectStoreReadCorruption verifies read failures preserve Harbor's typed corruption boundary and useful cause.
func assertProjectStoreReadCorruption(t *testing.T, err error, entity, cause string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("read error = %v, want CorruptStateError", err)
	}
	if corrupt.Entity != entity {
		t.Fatalf("corrupt entity = %q, want %q", corrupt.Entity, entity)
	}
	if !strings.Contains(err.Error(), cause) {
		t.Fatalf("corruption error = %v, want cause %q", err, cause)
	}
}

// assertProjectStoreReadHasCorruption verifies an integrity test reaches Harbor's typed corruption boundary without overfitting wording.
func assertProjectStoreReadHasCorruption(t *testing.T, err error, scenario string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("%s error = %v, want CorruptStateError", scenario, err)
	}
}

// projectStoreReadTestClock returns a stable capture instant for tests that do not inspect clock behavior.
func projectStoreReadTestClock() time.Time {
	return projectStoreReadTestTime().Add(24 * time.Hour)
}

// projectStoreReadTestTime returns a stable UTC persistence instant without a monotonic component.
func projectStoreReadTestTime() time.Time {
	return time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
}
