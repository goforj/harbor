package migrations

import "testing"

// TestProjectOutputHistoryMigrationKeepsOnlyDiagnosticCorrelation covers upgrade, constraints, cleanup, and rollback.
func TestProjectOutputHistoryMigrationKeepsOnlyDiagnosticCorrelation(t *testing.T) {
	connections, db := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, db)
	var selected Migration
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == "2026_10_21_013011_create_project_output_history" {
			selected = migration
		}
	}
	if selected == nil {
		t.Fatal("history migration is not registered")
	}
	if err := selected.Up(db); err != nil {
		t.Fatal(err)
	}
	insertProjectionProject(t, db, "project-history", "/work/history", "history", 1)
	valid := `INSERT INTO project_output_history VALUES ('project-history', 'session-failed', 2, '2026-10-21T06:18:00Z')`
	for _, statement := range []string{
		`INSERT INTO project_output_history VALUES ('project-missing', 'session-failed', 2, '2026-10-21T06:18:00Z')`,
		`INSERT INTO project_output_history VALUES ('project-history', '', 2, '2026-10-21T06:18:00Z')`,
		`INSERT INTO project_output_history VALUES ('project-history', 'session-failed', 0, '2026-10-21T06:18:00Z')`,
		`INSERT INTO project_output_history VALUES ('project-history', 'session-failed', 9007199254740992, '2026-10-21T06:18:00Z')`,
	} {
		assertMigrationStatementFails(t, db, statement)
	}
	if err := db.Exec(valid).Error; err != nil {
		t.Fatal(err)
	}
	assertMigrationStatementFails(t, db, valid)
	if err := db.Exec(`DELETE FROM projects WHERE project_id = 'project-history'`).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("project_output_history").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("orphaned diagnostics = %d, %v", count, err)
	}
	if err := selected.Down(db); err != nil {
		t.Fatal(err)
	}
	if err := selected.Up(db); err != nil {
		t.Fatal(err)
	}
}
