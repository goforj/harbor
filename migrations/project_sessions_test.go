package migrations

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

const projectSessionMigrationName = "2026_07_19_054145_create_project_sessions"

// TestProjectSessionMigrationCreatesRestrictiveLifecycleSchema verifies direct writers cannot weaken session or process correlation.
func TestProjectSessionMigrationCreatesRestrictiveLifecycleSchema(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectSessionMigrations(t, databaseConnection)

	if !databaseConnection.Migrator().HasTable("project_sessions") {
		t.Fatal("project session migration did not create project_sessions")
	}
	if !databaseConnection.Migrator().HasIndex("project_sessions", "project_sessions_state_idx") {
		t.Fatal("project session migration did not create project_sessions_state_idx")
	}
	columns := projectSessionMigrationColumns(t, databaseConnection)
	for _, name := range []string{"session_id", "project_id", "owner", "state", "descriptor_digest", "credential_digest", "generation", "pid", "birth_token", "executable_identity", "argument_digest", "created_at", "updated_at"} {
		if _, exists := columns[name]; !exists {
			t.Fatalf("project_sessions is missing %s", name)
		}
	}
	for _, forbidden := range []string{"credential", "credential_value", "credential_secret", "arguments"} {
		if _, exists := columns[forbidden]; exists {
			t.Fatalf("project_sessions persists forbidden raw material column %q", forbidden)
		}
	}

	insertProjectionProject(t, databaseConnection, "project-one", "/work/one", "one", 1)
	insertProjectionProject(t, databaseConnection, "project-two", "/work/two", "two", 2)
	insertProjectionProject(t, databaseConnection, "project-three", "/work/three", "three", 3)
	insertProjectSessionMigrationRow(t, databaseConnection, "session-one", "project-one", "planned", false)
	insertProjectSessionMigrationRow(t, databaseConnection, "session-two", "project-two", "attached", true)

	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM projects WHERE project_id = 'project-one'")
	assertMigrationStatementFails(t, databaseConnection, projectSessionMigrationInsert("session-one", "project-three", "planned", false))
	assertMigrationStatementFails(t, databaseConnection, projectSessionMigrationInsert("session-three", "project-one", "planned", false))
	assertMigrationStatementFails(t, databaseConnection, projectSessionMigrationInsert("session-missing", "project-missing", "planned", false))

	invalidStatements := []string{
		projectSessionMigrationInsert(" session-three", "project-three", "planned", false),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "planned", false), "'harbor'", "'desktop'", 1),
		projectSessionMigrationInsert("session-three", "project-three", "ready", false),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "planned", false), projectSessionMigrationDigest("a"), "'abc'", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "planned", false), projectSessionMigrationDigest("a"), projectSessionMigrationDigest("A"), 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "planned", false), projectSessionMigrationDigest("b"), projectSessionMigrationDigest("g"), 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "planned", false), ", 1, NULL", ", 0, NULL", 1),
		projectSessionMigrationInsert("session-three", "project-three", "planned", true),
		projectSessionMigrationInsert("session-three", "project-three", "attached", false),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "4102, 'birth-4102'", "0, 'birth-4102'", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'birth-4102'", "''", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'birth-4102'", "'"+strings.Repeat("b", 513)+"'", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'birth-4102'", "NULL", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'/usr/local/bin/forj'", "'/"+strings.Repeat("e", 4096)+"'", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'/usr/local/bin/forj'", "NULL", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), projectSessionMigrationDigest("c"), "NULL", 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), projectSessionMigrationDigest("c"), projectSessionMigrationDigest("C"), 1),
		strings.Replace(projectSessionMigrationInsert("session-three", "project-three", "attached", true), "'2026-07-19T05:45:01Z'", "'2026-07-19T05:44:59Z'", 1),
	}
	for _, statement := range invalidStatements {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
}

// TestProjectSessionMigrationDownAndReapplyIsReversible proves rollback removes only session state and leaves registration reusable.
func TestProjectSessionMigrationDownAndReapplyIsReversible(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	migration := projectSessionMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply project session migration: %v", err)
	}
	insertProjectionProject(t, databaseConnection, "project-reapply", "/work/reapply", "reapply", 1)
	insertProjectSessionMigrationRow(t, databaseConnection, "session-reapply", "project-reapply", "planned", false)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback project session migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("project_sessions") {
		t.Fatal("project session rollback retained project_sessions")
	}
	if !databaseConnection.Migrator().HasTable("projects") {
		t.Fatal("project session rollback removed registered projects")
	}
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("reapply project session migration: %v", err)
	}
	insertProjectSessionMigrationRow(t, databaseConnection, "session-reapply", "project-reapply", "planned", false)
	var count int64
	if err := databaseConnection.Table("project_sessions").Count(&count).Error; err != nil {
		t.Fatalf("count reapplied project sessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("reapplied project session count = %d, want 1", count)
	}
}

// applyProjectSessionMigrations applies the registration projection and production session schema without a migration ledger.
func applyProjectSessionMigrations(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := projectSessionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply project session migration: %v", err)
	}
}

// projectSessionMigration finds the embedded production session migration by stable identity.
func projectSessionMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == projectSessionMigrationName {
			return migration
		}
	}
	t.Fatalf("project session migration %q is not registered", projectSessionMigrationName)
	return nil
}

// insertProjectSessionMigrationRow writes one valid planned or process-backed lifecycle fixture.
func insertProjectSessionMigrationRow(t *testing.T, databaseConnection *gorm.DB, sessionID, projectID, state string, process bool) {
	t.Helper()
	if err := databaseConnection.Exec(projectSessionMigrationInsert(sessionID, projectID, state, process)).Error; err != nil {
		t.Fatalf("insert project session %s: %v", sessionID, err)
	}
}

// projectSessionMigrationInsert renders fixed test-only SQL without accepting untrusted runtime values.
func projectSessionMigrationInsert(sessionID, projectID, state string, process bool) string {
	pid := "NULL"
	birthToken := "NULL"
	executableIdentity := "NULL"
	argumentDigest := "NULL"
	if process {
		pid = "4102"
		birthToken = "'birth-4102'"
		executableIdentity = "'/usr/local/bin/forj'"
		argumentDigest = projectSessionMigrationDigest("c")
	}
	return `INSERT INTO project_sessions
		(session_id, project_id, owner, state, descriptor_digest, credential_digest, generation, pid, birth_token, executable_identity, argument_digest, created_at, updated_at)
		VALUES ('` + sessionID + `', '` + projectID + `', 'harbor', '` + state + `', ` + projectSessionMigrationDigest("a") + `, ` + projectSessionMigrationDigest("b") + `, 1, ` + pid + `, ` + birthToken + `, ` + executableIdentity + `, ` + argumentDigest + `, '2026-07-19T05:45:00Z', '2026-07-19T05:45:01Z')`
}

// projectSessionMigrationDigest creates one fixed lowercase hexadecimal digest literal for schema tests.
func projectSessionMigrationDigest(character string) string {
	return "'" + strings.Repeat(character, 64) + "'"
}

// projectSessionMigrationColumns returns the exact persisted column vocabulary for secret-surface assertions.
func projectSessionMigrationColumns(t *testing.T, databaseConnection *gorm.DB) map[string]struct{} {
	t.Helper()
	var rows []struct {
		Name string
	}
	if err := databaseConnection.Raw("PRAGMA table_info(project_sessions)").Scan(&rows).Error; err != nil {
		t.Fatalf("inspect project_sessions columns: %v", err)
	}
	columns := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		columns[row.Name] = struct{}{}
	}
	return columns
}
