package migrations

import (
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestProjectProjectionMigrationCreatesFinalSchema verifies Harbor's embedded projection migration owns the complete normalized shape.
func TestProjectProjectionMigrationCreatesFinalSchema(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	if err := databaseConnection.Exec("UPDATE operation_journal_state SET sequence = 41 WHERE id = 1").Error; err != nil {
		t.Fatalf("advance journal sequence: %v", err)
	}
	if err := projectProjectionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply project projection migration: %v", err)
	}

	for _, table := range []string{"harbor_state", "projects", "project_apps", "project_services", "project_resources", "recent_resources"} {
		if !databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("migration did not create %s", table)
		}
	}
	if databaseConnection.Migrator().HasTable("operation_journal_state") {
		t.Fatal("migration retained the superseded operation_journal_state table")
	}
	for table, indexes := range map[string][]string{
		"projects":          {"projects_presentation_order_idx"},
		"project_apps":      {"project_apps_canonical_order_idx"},
		"project_services":  {"project_services_canonical_order_idx", "project_services_selection_idx"},
		"project_resources": {"project_resources_canonical_order_idx", "project_resources_kind_idx"},
		"recent_resources":  {"recent_resources_canonical_order_idx"},
	} {
		for _, index := range indexes {
			if !databaseConnection.Migrator().HasIndex(table, index) {
				t.Fatalf("migration did not create %s on %s", index, table)
			}
		}
	}

	var sequence int
	if err := databaseConnection.Raw("SELECT sequence FROM harbor_state WHERE id = 1").Scan(&sequence).Error; err != nil {
		t.Fatalf("read global sequence: %v", err)
	}
	if sequence != 41 {
		t.Fatalf("global sequence = %d, want preserved value 41", sequence)
	}
	assertMigrationStatementFails(t, databaseConnection, "INSERT INTO harbor_state (id, sequence) VALUES (2, 42)")
	assertMigrationStatementFails(t, databaseConnection, "UPDATE harbor_state SET id = 2 WHERE id = 1")
}

// TestProjectProjectionMigrationAcceptsAggregateAndCascades verifies valid ownership can be stored and deleted without orphans.
func TestProjectProjectionMigrationAcceptsAggregateAndCascades(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)

	insertProjectionProject(t, databaseConnection, "project-01", "/work/orders", "orders", 1)
	if err := databaseConnection.Exec(`INSERT INTO project_apps
		(project_id, app_id, name, state, active, required)
		VALUES ('project-01', 'api', 'API', 'ready', 1, 1)`).Error; err != nil {
		t.Fatalf("insert project App: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_services
		(project_id, service_id, name, kind, state, owner, selection, required)
		VALUES ('project-01', 'mysql', 'MySQL', 'database', 'ready', 'compose', 'selected', 1)`).Error; err != nil {
		t.Fatalf("insert project service: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		VALUES ('project-01', 'api-reference', 'API Reference', 'api-reference', 'https://orders.test/docs', 'app', 'api')`).Error; err != nil {
		t.Fatalf("insert App-owned resource: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_service_id)
		VALUES ('project-01', 'database', 'Database', 'database', 'https://database.orders.test', 'service', 'mysql')`).Error; err != nil {
		t.Fatalf("insert service-owned resource: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO recent_resources
		(project_id, resource_id, accessed_at, sequence)
		VALUES ('project-01', 'api-reference', '2026-07-18T12:01:00Z', 2)`).Error; err != nil {
		t.Fatalf("insert recent resource: %v", err)
	}

	assertProjectionCount(t, databaseConnection, "projects", 1)
	assertProjectionCount(t, databaseConnection, "project_apps", 1)
	assertProjectionCount(t, databaseConnection, "project_services", 1)
	assertProjectionCount(t, databaseConnection, "project_resources", 2)
	assertProjectionCount(t, databaseConnection, "recent_resources", 1)

	if err := databaseConnection.Exec("DELETE FROM project_apps WHERE project_id = 'project-01' AND app_id = 'api'").Error; err != nil {
		t.Fatalf("delete resource owner: %v", err)
	}
	assertProjectionCount(t, databaseConnection, "project_resources", 1)
	assertProjectionCount(t, databaseConnection, "recent_resources", 0)

	if err := databaseConnection.Exec("DELETE FROM projects WHERE project_id = 'project-01'").Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	assertProjectionCount(t, databaseConnection, "projects", 0)
	assertProjectionCount(t, databaseConnection, "project_services", 0)
	assertProjectionCount(t, databaseConnection, "project_resources", 0)
}

// TestProjectProjectionMigrationRejectsInvalidEnums verifies persisted state cannot exceed the domain vocabulary.
func TestProjectProjectionMigrationRejectsInvalidEnums(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	seedProjectionOwners(t, databaseConnection)

	tests := []struct {
		name      string
		statement string
	}{
		{
			name: "project state",
			statement: `INSERT INTO projects
				(project_id, name, path, slug, state, favorite, updated_at, revision)
				VALUES ('invalid-project', 'Invalid', '/work/invalid', 'invalid', 'unknown', 0, '2026-07-18T12:00:00Z', 3)`,
		},
		{
			name: "App state",
			statement: `INSERT INTO project_apps
				(project_id, app_id, name, state, active, required)
				VALUES ('project-01', 'invalid-app', 'Invalid', 'unknown', 0, 0)`,
		},
		{
			name: "service state",
			statement: `INSERT INTO project_services
				(project_id, service_id, name, kind, state, owner, selection, required)
				VALUES ('project-01', 'invalid-state', 'Invalid', 'database', 'unknown', 'compose', 'selected', 0)`,
		},
		{
			name: "service owner",
			statement: `INSERT INTO project_services
				(project_id, service_id, name, kind, state, owner, selection, required)
				VALUES ('project-01', 'invalid-owner', 'Invalid', 'database', 'ready', 'harbor', 'selected', 0)`,
		},
		{
			name: "service selection",
			statement: `INSERT INTO project_services
				(project_id, service_id, name, kind, state, owner, selection, required)
				VALUES ('project-01', 'invalid-selection', 'Invalid', 'database', 'ready', 'compose', 'disabled', 0)`,
		},
		{
			name: "resource owner kind",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
				VALUES ('project-01', 'invalid-owner-kind', 'Invalid', 'http', 'https://invalid.test', 'project', 'api')`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMigrationStatementFails(t, databaseConnection, test.statement)
		})
	}
}

// TestProjectProjectionMigrationRejectsNonCanonicalSlugs verifies direct writers cannot bypass project DNS-label normalization.
func TestProjectProjectionMigrationRejectsNonCanonicalSlugs(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)

	invalid := []string{"Orders", "orders_api", "orders.api", "-orders", "orders-", "ordérs", strings.Repeat("a", 64)}
	for index, slug := range invalid {
		assertMigrationStatementFails(t, databaseConnection, `INSERT INTO projects
			(project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES (?, 'Invalid', ?, ?, 'stopped', 0, '2026-07-18T12:00:00Z', ?)`,
			fmt.Sprintf("invalid-project-%d", index),
			fmt.Sprintf("/work/invalid-%d", index),
			slug,
			index+1,
		)
	}
}

// TestProjectProjectionMigrationRejectsScalarConstraintViolations covers bounded text, flags, revisions, and scoped uniqueness.
func TestProjectProjectionMigrationRejectsScalarConstraintViolations(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	seedProjectionOwners(t, databaseConnection)
	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		VALUES ('project-01', 'docs', 'Docs', 'documentation', 'https://one.test/docs', 'app', 'api')`).Error; err != nil {
		t.Fatalf("insert resource for recency constraints: %v", err)
	}

	statements := []string{
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-favorite', 'Favorite', '/work/favorite', 'favorite', 'stopped', 2, '2026-07-18T12:00:00Z', 3)`,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-zero-revision', 'Revision', '/work/revision', 'revision', 'stopped', 0, '2026-07-18T12:00:00Z', 0)`,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES (' project-whitespace', 'Whitespace', '/work/whitespace', 'whitespace', 'stopped', 0, '2026-07-18T12:00:00Z', 3)`,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-name-space', ' Name', '/work/name-space', 'name-space', 'stopped', 0, '2026-07-18T12:00:00Z', 3)`,
		fmt.Sprintf(`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('%s', 'Long ID', '/work/long-id', 'long-id', 'stopped', 0, '2026-07-18T12:00:00Z', 3)`, strings.Repeat("a", 257)),
		fmt.Sprintf(`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-long-name', '%s', '/work/long-name', 'long-name', 'stopped', 0, '2026-07-18T12:00:00Z', 3)`, strings.Repeat("a", 513)),
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-duplicate-slug', 'Duplicate Slug', '/work/duplicate-slug', 'one', 'stopped', 0, '2026-07-18T12:00:00Z', 3)`,
		`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			VALUES ('project-duplicate-revision', 'Duplicate Revision', '/work/duplicate-revision', 'duplicate-revision', 'stopped', 0, '2026-07-18T12:00:00Z', 1)`,
		`INSERT INTO project_apps (project_id, app_id, name, state, active, required)
			VALUES ('project-01', 'bad-active', 'Bad Active', 'ready', 2, 0)`,
		`INSERT INTO project_apps (project_id, app_id, name, state, active, required)
			VALUES ('project-01', 'bad-required', 'Bad Required', 'ready', 1, 2)`,
		`INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required)
			VALUES ('project-01', 'bad-required', 'Bad Required', 'database', 'ready', 'compose', 'selected', 2)`,
		`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence)
			VALUES ('project-01', 'docs', '2026-07-18T12:01:00Z', 0)`,
	}
	for _, statement := range statements {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
}

// TestProjectProjectionMigrationRejectsBrokenOwnership verifies every resource belongs to exactly one entity in its own project.
func TestProjectProjectionMigrationRejectsBrokenOwnership(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	seedProjectionOwners(t, databaseConnection)

	tests := []struct {
		name      string
		statement string
	}{
		{
			name: "missing owner identity",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind)
				VALUES ('project-01', 'missing-owner', 'Missing Owner', 'http', 'https://missing.test', 'app')`,
		},
		{
			name: "two owner identities",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind, owner_app_id, owner_service_id)
				VALUES ('project-01', 'two-owners', 'Two Owners', 'http', 'https://two.test', 'app', 'api', 'mysql')`,
		},
		{
			name: "App identity for service owner",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
				VALUES ('project-01', 'wrong-owner-column', 'Wrong Owner', 'http', 'https://wrong.test', 'service', 'api')`,
		},
		{
			name: "cross-project App",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
				VALUES ('project-02', 'cross-App', 'Cross App', 'http', 'https://cross.test', 'app', 'worker')`,
		},
		{
			name: "cross-project service",
			statement: `INSERT INTO project_resources
				(project_id, resource_id, name, kind, url, owner_kind, owner_service_id)
				VALUES ('project-02', 'cross-service', 'Cross Service', 'http', 'https://cross.test', 'service', 'redis')`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMigrationStatementFails(t, databaseConnection, test.statement)
		})
	}
}

// TestProjectProjectionMigrationScopesStableIdentities verifies child identities are reusable only across project boundaries.
func TestProjectProjectionMigrationScopesStableIdentities(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	seedProjectionOwners(t, databaseConnection)

	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		VALUES ('project-01', 'docs', 'Docs', 'documentation', 'https://one.test/docs', 'app', 'api')`).Error; err != nil {
		t.Fatalf("insert first scoped resource: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		VALUES ('project-02', 'docs', 'Docs', 'documentation', 'https://two.test/docs', 'app', 'api')`).Error; err != nil {
		t.Fatalf("reuse resource identity in another project: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_service_id)
		VALUES ('project-01', 'admin', 'Admin', 'admin', 'https://one.test/admin', 'service', 'redis')`).Error; err != nil {
		t.Fatalf("insert project-only resource: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO recent_resources
		(project_id, resource_id, accessed_at, sequence)
		VALUES ('project-01', 'docs', '2026-07-18T12:01:00Z', 3)`).Error; err != nil {
		t.Fatalf("insert recent resource: %v", err)
	}

	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO project_apps
		(project_id, app_id, name, state, active, required)
		VALUES ('project-01', 'api', 'Duplicate', 'ready', 1, 1)`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO project_resources
		(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
		VALUES ('project-01', 'docs', 'Duplicate', 'documentation', 'https://duplicate.test', 'app', 'api')`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO projects
		(project_id, name, path, slug, state, favorite, updated_at, revision)
		VALUES ('project-03', 'Duplicate Path', '/work/one', 'three', 'ready', 0, '2026-07-18T12:00:00Z', 4)`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO recent_resources
		(project_id, resource_id, accessed_at, sequence)
		VALUES ('project-02', 'admin', '2026-07-18T12:02:00Z', 4)`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO recent_resources
		(project_id, resource_id, accessed_at, sequence)
		VALUES ('project-01', 'docs', '2026-07-18T12:02:00Z', 4)`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO recent_resources
		(project_id, resource_id, accessed_at, sequence)
		VALUES ('project-01', 'admin', '2026-07-18T12:02:00Z', 3)`)
}

// TestProjectProjectionMigrationRollbackRestoresJournal verifies rollback removes only projection state and preserves the prior singleton.
func TestProjectProjectionMigrationRollbackRestoresJournal(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	if err := databaseConnection.Exec("UPDATE operation_journal_state SET sequence = 17 WHERE id = 1").Error; err != nil {
		t.Fatalf("advance operation journal sequence: %v", err)
	}
	migration := projectProjectionMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply project projection migration: %v", err)
	}
	insertProjectionProject(t, databaseConnection, "project-01", "/work/orders", "orders", 18)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback project projection migration: %v", err)
	}
	for _, table := range []string{"harbor_state", "projects", "project_apps", "project_services", "project_resources", "recent_resources"} {
		if databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("rollback retained %s", table)
		}
	}
	if !databaseConnection.Migrator().HasTable("operation_journal_state") {
		t.Fatal("rollback did not restore operation_journal_state")
	}
	if !databaseConnection.Migrator().HasTable("operations") || !databaseConnection.Migrator().HasTable("operation_transitions") {
		t.Fatal("rollback removed the earlier operation journal schema")
	}
	var sequence int
	if err := databaseConnection.Raw("SELECT sequence FROM operation_journal_state WHERE id = 1").Scan(&sequence).Error; err != nil {
		t.Fatalf("read restored operation journal sequence: %v", err)
	}
	if sequence != 17 {
		t.Fatalf("restored operation journal sequence = %d, want 17", sequence)
	}
}

// applyProjectProjectionMigrations applies the production migration pair without creating the framework ledger.
func applyProjectProjectionMigrations(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	if err := projectProjectionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply project projection migration: %v", err)
	}
}

// projectProjectionMigration finds the production projection migration through Harbor's embedded registry.
func projectProjectionMigration(t *testing.T) Migration {
	t.Helper()
	migrations := selectMigrations("harbord", "default", "sqlite")
	for _, migration := range migrations {
		if migration.Name() == "2026_07_18_093810_create_project_projection" {
			return migration
		}
	}
	available := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		available = append(available, migration.Name())
	}
	t.Fatalf("project projection migration is not registered; available: %v", available)
	return nil
}

// insertProjectionProject inserts one valid project row with an independently chosen global revision.
func insertProjectionProject(t *testing.T, databaseConnection *gorm.DB, projectID, path, slug string, revision int) {
	t.Helper()
	if err := databaseConnection.Exec(`INSERT INTO projects
		(project_id, name, path, slug, state, favorite, updated_at, revision)
		VALUES (?, ?, ?, ?, 'ready', 0, '2026-07-18T12:00:00Z', ?)`, projectID, projectID, path, slug, revision).Error; err != nil {
		t.Fatalf("insert project %s: %v", projectID, err)
	}
}

// seedProjectionOwners creates two project scopes whose shared child IDs prove composite ownership behavior.
func seedProjectionOwners(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	insertProjectionProject(t, databaseConnection, "project-01", "/work/one", "one", 1)
	insertProjectionProject(t, databaseConnection, "project-02", "/work/two", "two", 2)
	for _, projectID := range []string{"project-01", "project-02"} {
		if err := databaseConnection.Exec(`INSERT INTO project_apps
			(project_id, app_id, name, state, active, required)
			VALUES (?, 'api', 'API', 'ready', 1, 1)`, projectID).Error; err != nil {
			t.Fatalf("insert %s App: %v", projectID, err)
		}
		if err := databaseConnection.Exec(`INSERT INTO project_services
			(project_id, service_id, name, kind, state, owner, selection, required)
			VALUES (?, 'mysql', 'MySQL', 'database', 'ready', 'compose', 'selected', 1)`, projectID).Error; err != nil {
			t.Fatalf("insert %s service: %v", projectID, err)
		}
	}
	if err := databaseConnection.Exec(`INSERT INTO project_apps
		(project_id, app_id, name, state, active, required)
		VALUES ('project-01', 'worker', 'Worker', 'stopped', 0, 0)`).Error; err != nil {
		t.Fatalf("insert project-only App: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO project_services
		(project_id, service_id, name, kind, state, owner, selection, required)
		VALUES ('project-01', 'redis', 'Redis', 'cache', 'stopped', 'compose', 'available', 0)`).Error; err != nil {
		t.Fatalf("insert project-only service: %v", err)
	}
}

// assertProjectionCount verifies cascades and failed statements leave exactly the expected number of rows.
func assertProjectionCount(t *testing.T, databaseConnection *gorm.DB, table string, expected int64) {
	t.Helper()
	var count int64
	if err := databaseConnection.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != expected {
		t.Fatalf("%s row count = %d, want %d", table, count, expected)
	}
}
