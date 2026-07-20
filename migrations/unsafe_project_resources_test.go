package migrations

import "testing"

const unsafeProjectResourcesMigrationName = "2026_07_20_030000_remove_unsafe_project_resources"

// TestUnsafeProjectResourcesMigrationRetainsOnlyTheReadinessProof verifies older optional framework links cannot prevent safe daemon snapshots.
func TestUnsafeProjectResourcesMigrationRetainsOnlyTheReadinessProof(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	seedProjectionOwners(t, databaseConnection)
	for _, resource := range []struct {
		id  string
		url string
	}{
		{id: "app-http", url: "http://127.77.4.8:3000"},
		{id: "ipv6", url: "http://[::1]:3000"},
		{id: "routed", url: "https://orders.test/docs"},
		{id: "legacy-external", url: "https://dev.diclan.app"},
		{id: "legacy-localhost", url: "http://localhost:3000"},
	} {
		if err := databaseConnection.Exec(`INSERT INTO project_resources
			(project_id, resource_id, name, kind, url, owner_kind, owner_app_id)
			VALUES ('project-01', ?, ?, 'application', ?, 'app', 'api')`, resource.id, resource.id, resource.url).Error; err != nil {
			t.Fatalf("insert %s resource: %v", resource.id, err)
		}
	}

	migration := unsafeProjectResourcesMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply unsafe resource migration: %v", err)
	}
	var retained []string
	if err := databaseConnection.Table("project_resources").Where("project_id = ?", "project-01").Order("resource_id ASC").Pluck("resource_id", &retained).Error; err != nil {
		t.Fatalf("read retained resource IDs: %v", err)
	}
	want := []string{"app-http"}
	if len(retained) != len(want) {
		t.Fatalf("retained resources = %#v, want %#v", retained, want)
	}
	for index := range want {
		if retained[index] != want[index] {
			t.Fatalf("retained resources = %#v, want %#v", retained, want)
		}
	}

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback unsafe resource migration: %v", err)
	}
	var afterRollback []string
	if err := databaseConnection.Table("project_resources").Where("project_id = ?", "project-01").Order("resource_id ASC").Pluck("resource_id", &afterRollback).Error; err != nil {
		t.Fatalf("read retained resources after rollback: %v", err)
	}
	if len(afterRollback) != len(want) {
		t.Fatalf("rollback restored removed resource projections: %#v", afterRollback)
	}
}

// unsafeProjectResourcesMigration finds the embedded one-way repair through the production registry.
func unsafeProjectResourcesMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == unsafeProjectResourcesMigrationName {
			return migration
		}
	}
	t.Fatalf("unsafe resource migration %q is not registered", unsafeProjectResourcesMigrationName)
	return nil
}
