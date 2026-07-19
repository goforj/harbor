package state

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// TestProjectSessionConversionRoundTripsEveryEvidenceShape proves generated models preserve exact domain security facts.
func TestProjectSessionConversionRoundTripsEveryEvidenceShape(t *testing.T) {
	for _, state := range []domain.SessionState{domain.SessionPlanned, domain.SessionAwaitingAttach, domain.SessionAttached, domain.SessionStopping, domain.SessionDisconnected} {
		session := sessionStoreTestSession(t)
		session.State = state
		if state == domain.SessionPlanned {
			session.Process = nil
		}
		row, err := projectSessionModelFromDomain(session)
		if err != nil {
			t.Fatalf("projectSessionModelFromDomain(%s) error = %v", state, err)
		}
		if row.Id != 0 || row.SessionId != string(session.ID) || row.ProjectId != string(session.ProjectID) {
			t.Fatalf("projectSessionModelFromDomain(%s) identity = %#v", state, row)
		}
		if state == domain.SessionPlanned {
			if row.Pid.Valid || row.BirthToken.Valid || row.ExecutableIdentity.Valid || row.ArgumentDigest.Valid {
				t.Fatalf("planned model contains process evidence: %#v", row)
			}
		} else if !row.Pid.Valid || !row.BirthToken.Valid || !row.ExecutableIdentity.Valid || !row.ArgumentDigest.Valid {
			t.Fatalf("%s model omits process evidence: %#v", state, row)
		}

		row.Id = 41
		converted, err := projectSessionFromModel(row)
		if err != nil {
			t.Fatalf("projectSessionFromModel(%s) error = %v", state, err)
		}
		if !reflect.DeepEqual(converted, session) {
			t.Fatalf("projectSessionFromModel(%s) = %#v, want %#v", state, converted, session)
		}
	}
}

// TestProjectSessionConversionRejectsInvalidDomainAndModelFacts keeps corrupt process authority behind a typed boundary.
func TestProjectSessionConversionRejectsInvalidDomainAndModelFacts(t *testing.T) {
	session := sessionStoreTestSession(t)
	session.Generation = math.MaxUint64
	if _, err := projectSessionModelFromDomain(session); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("projectSessionModelFromDomain(overflow) error = %v", err)
	}

	valid, err := projectSessionModelFromDomain(sessionStoreTestSession(t))
	if err != nil {
		t.Fatalf("prepare valid model: %v", err)
	}
	valid.Id = 41
	tests := []struct {
		name   string
		mutate func(*models.ProjectSession)
		want   string
	}{
		{name: "database ID", mutate: func(row *models.ProjectSession) { row.Id = 0 }, want: "database ID"},
		{name: "generation", mutate: func(row *models.ProjectSession) { row.Generation = 0 }, want: "generation"},
		{name: "partial evidence", mutate: func(row *models.ProjectSession) { row.ArgumentDigest = null.String{} }, want: "all present or all absent"},
		{name: "planned evidence", mutate: func(row *models.ProjectSession) { row.State = string(domain.SessionPlanned) }, want: "must not contain process"},
		{name: "missing evidence", mutate: func(row *models.ProjectSession) {
			row.Pid = null.Int{}
			row.BirthToken = null.String{}
			row.ExecutableIdentity = null.String{}
			row.ArgumentDigest = null.String{}
		}, want: "must contain process"},
		{name: "session correlation", mutate: func(row *models.ProjectSession) { row.ProjectId = "" }, want: "project ID"},
		{name: "descriptor digest", mutate: func(row *models.ProjectSession) { row.DescriptorDigest = strings.Repeat("A", 64) }, want: "descriptor digest"},
		{name: "process identity", mutate: func(row *models.ProjectSession) { row.ExecutableIdentity = null.StringFrom("relative/forj") }, want: "canonical absolute"},
		{name: "creation time", mutate: func(row *models.ProjectSession) { row.CreatedAt = time.Time{} }, want: "creation time"},
		{name: "time order", mutate: func(row *models.ProjectSession) { row.UpdatedAt = row.CreatedAt.Add(-time.Second) }, want: "must not precede"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			_, err := projectSessionFromModel(row)
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || corrupt.Entity != "project session" || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("projectSessionFromModel() error = %v, want typed corruption containing %q", err, test.want)
			}
		})
	}
}

// TestStoreReadsActiveProjectSessionWithExactCorrelation verifies both supported lookup boundaries share one validated row.
func TestStoreReadsActiveProjectSessionWithExactCorrelation(t *testing.T) {
	store, connection := newSessionStoreTestHarness(t)
	want := sessionStoreTestSession(t)
	seedSessionStoreTestRow(t, connection, want)

	active, err := store.ActiveProjectSession(t.Context(), want.ProjectID)
	if err != nil {
		t.Fatalf("ActiveProjectSession() error = %v", err)
	}
	if !reflect.DeepEqual(active, want) {
		t.Fatalf("ActiveProjectSession() = %#v, want %#v", active, want)
	}
	exact, err := store.ProjectSession(t.Context(), want.ProjectID, want.ID)
	if err != nil {
		t.Fatalf("ProjectSession() error = %v", err)
	}
	if !reflect.DeepEqual(exact, want) {
		t.Fatalf("ProjectSession() = %#v, want %#v", exact, want)
	}
}

// TestStoreProjectSessionClassifiesAbsenceCorrelationAndCorruption prevents caller mistakes from hiding invalid durable rows.
func TestStoreProjectSessionClassifiesAbsenceCorrelationAndCorruption(t *testing.T) {
	store, connection := newSessionStoreTestHarness(t)
	_, err := store.ActiveProjectSession(t.Context(), "project-missing")
	var missing *ProjectSessionNotFoundError
	if !errors.As(err, &missing) || missing.ProjectID != "project-missing" || missing.SessionID != "" {
		t.Fatalf("ActiveProjectSession(missing) error = %#v / %v", missing, err)
	}

	session := sessionStoreTestSession(t)
	seedSessionStoreTestRow(t, connection, session)
	_, err = store.ProjectSession(t.Context(), session.ProjectID, "session-other")
	if !errors.As(err, &missing) || missing.ProjectID != session.ProjectID || missing.SessionID != "session-other" {
		t.Fatalf("ProjectSession(wrong correlation) error = %#v / %v", missing, err)
	}

	duplicate := session
	duplicate.ID = "session-duplicate"
	seedSessionStoreTestRow(t, connection, duplicate)
	_, err = store.ActiveProjectSession(t.Context(), session.ProjectID)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "multiple active sessions") {
		t.Fatalf("ActiveProjectSession(duplicate) error = %v, want typed corruption", err)
	}
}

// TestStoreProjectSessionValidatesInputAndCancellation keeps invalid reads away from the database boundary.
func TestStoreProjectSessionValidatesInputAndCancellation(t *testing.T) {
	store, _ := newSessionStoreTestHarness(t)
	if _, err := store.ActiveProjectSession(t.Context(), " project"); err == nil || !strings.Contains(err.Error(), "project ID") {
		t.Fatalf("ActiveProjectSession(invalid project) error = %v", err)
	}
	if _, err := store.ProjectSession(t.Context(), "project-one", " session"); err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Fatalf("ProjectSession(invalid session) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ActiveProjectSession(ctx, "project-one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ActiveProjectSession(cancelled) error = %v", err)
	}
}

// TestStoreProjectSessionReportsQueryFailures keeps storage failures distinct from absence and corrupt rows.
func TestStoreProjectSessionReportsQueryFailures(t *testing.T) {
	store, connection := newSessionStoreTestHarness(t)
	if err := connection.Migrator().DropTable("project_sessions"); err != nil {
		t.Fatalf("drop project session table: %v", err)
	}
	_, err := store.ActiveProjectSession(t.Context(), "project-one")
	var missing *ProjectSessionNotFoundError
	var corrupt *CorruptStateError
	if err == nil || errors.As(err, &missing) || errors.As(err, &corrupt) || !strings.Contains(err.Error(), "read project session row") {
		t.Fatalf("ActiveProjectSession(query failure) error = %v", err)
	}
}

// TestNewStoreRetainsGeneratedProjectSessionRepository guards the dependency required by durable lifecycle reads.
func TestNewStoreRetainsGeneratedProjectSessionRepository(t *testing.T) {
	repository := new(models.ProjectSessionRepo)
	store := NewStore(nil, nil, repository, nil, nil)
	if store == nil || store.sessions != repository {
		t.Fatalf("NewStore() sessions = %p, want %p", store.sessions, repository)
	}
}

// newSessionStoreTestHarness opens an isolated named database with a deliberately weakenable read schema.
func newSessionStoreTestHarness(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_DRIVER", "unsupported")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=foreign_keys(1)")
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close project session database: %v", err)
		}
	})
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open project session database: %v", err)
	}
	if err := connection.Exec(`CREATE TABLE project_sessions (
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		owner TEXT NOT NULL,
		state TEXT NOT NULL,
		descriptor_digest TEXT NOT NULL,
		credential_digest TEXT NOT NULL,
		generation INTEGER NOT NULL,
		pid INTEGER,
		birth_token TEXT,
		executable_identity TEXT,
		argument_digest TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create project session test schema: %v", err)
	}
	repository := models.NewProjectSessionRepo(connections)
	return NewStore(nil, nil, repository, nil, nil), connection
}

// seedSessionStoreTestRow writes one validated session through the same generated model shape used in production.
func seedSessionStoreTestRow(t *testing.T, connection *gorm.DB, session domain.ProjectSession) {
	t.Helper()
	row, err := projectSessionModelFromDomain(session)
	if err != nil {
		t.Fatalf("convert project session fixture: %v", err)
	}
	if err := connection.Create(&row).Error; err != nil {
		t.Fatalf("seed project session fixture: %v", err)
	}
}

// sessionStoreTestSession returns one canonical attached session on the current operating system.
func sessionStoreTestSession(t *testing.T) domain.ProjectSession {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	at := time.Date(2026, 7, 19, 5, 45, 0, 0, time.UTC)
	return domain.ProjectSession{
		ID:               "session-orders",
		ProjectID:        "project-orders",
		Owner:            domain.SessionOwnerHarbor,
		State:            domain.SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64),
		CredentialDigest: strings.Repeat("b", 64),
		Generation:       7,
		Process: &domain.ProcessEvidence{
			PID:                4102,
			BirthToken:         "process-birth-4102",
			ExecutableIdentity: filepath.Clean(executable),
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: at,
		UpdatedAt: at.Add(time.Second),
	}
}
