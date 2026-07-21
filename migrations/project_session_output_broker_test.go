package migrations

import (
	"strings"
	"testing"
)

const projectSessionOutputBrokerMigrationName = "2026_07_21_060000_add_project_session_output_broker"

// TestProjectSessionOutputBrokerMigrationAddsAllOrNothingEvidence proves the new broker columns cannot retain partial authority.
func TestProjectSessionOutputBrokerMigrationAddsAllOrNothingEvidence(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := projectSessionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply project session migration: %v", err)
	}
	if err := projectSessionOutputBrokerMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply output broker migration: %v", err)
	}

	columns := projectSessionMigrationColumns(t, databaseConnection)
	for _, name := range []string{
		"output_broker_endpoint_reference",
		"output_broker_ticket_digest",
		"output_broker_manifest_path",
		"output_broker_pid",
		"output_broker_birth_token",
		"output_broker_executable_identity",
		"output_broker_argument_digest",
	} {
		if _, exists := columns[name]; !exists {
			t.Fatalf("project_sessions is missing %s", name)
		}
	}

	insertProjectionProject(t, databaseConnection, "project-broker", "/work/broker", "broker", 1)
	insertProjectSessionMigrationRow(t, databaseConnection, "session-broker", "project-broker", "attached", true)
	valid := outputBrokerSessionUpdate("session-broker")
	if err := databaseConnection.Exec(valid).Error; err != nil {
		t.Fatalf("insert complete output broker evidence: %v", err)
	}
	invalid := []string{
		strings.Replace(valid, "'/tmp/harbor-output-broker.json'", "NULL", 1),
		strings.Replace(valid, "'"+strings.Repeat("d", 64)+"'", "'"+strings.Repeat("D", 64)+"'", 1),
		strings.Replace(valid, "4103", "0", 1),
	}
	for _, statement := range invalid {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
}

// TestProjectSessionOutputBrokerMigrationRollbackPreservesLegacyAuthority proves rollback removes only additive broker fields.
func TestProjectSessionOutputBrokerMigrationRollbackPreservesLegacyAuthority(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := projectSessionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply project session migration: %v", err)
	}
	if err := projectSessionOutputBrokerMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply output broker migration: %v", err)
	}
	insertProjectionProject(t, databaseConnection, "project-rollback", "/work/rollback", "rollback", 1)
	insertProjectSessionMigrationRow(t, databaseConnection, "session-rollback", "project-rollback", "planned", false)
	if err := projectSessionOutputBrokerMigration(t).Down(databaseConnection); err != nil {
		t.Fatalf("rollback output broker migration: %v", err)
	}
	columns := projectSessionMigrationColumns(t, databaseConnection)
	if _, exists := columns["output_broker_endpoint_reference"]; exists {
		t.Fatal("rollback retained output broker columns")
	}
	var count int64
	if err := databaseConnection.Table("project_sessions").Count(&count).Error; err != nil {
		t.Fatalf("count legacy sessions after rollback: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy session count after rollback = %d, want 1", count)
	}
}

// projectSessionOutputBrokerMigration finds the additive broker schema migration by stable identity.
func projectSessionOutputBrokerMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == projectSessionOutputBrokerMigrationName {
			return migration
		}
	}
	t.Fatalf("output broker migration %q is not registered", projectSessionOutputBrokerMigrationName)
	return nil
}

// outputBrokerSessionUpdate renders one complete broker evidence update for migration constraints.
func outputBrokerSessionUpdate(sessionID string) string {
	return `UPDATE project_sessions SET
		output_broker_endpoint_reference = '/tmp/harbor-output-broker.sock',
		output_broker_ticket_digest = '` + strings.Repeat("d", 64) + `',
		output_broker_manifest_path = '/tmp/harbor-output-broker.json',
		output_broker_pid = 4103,
		output_broker_birth_token = 'broker-birth-4103',
		output_broker_executable_identity = '/usr/local/bin/outputbroker',
		output_broker_argument_digest = '` + strings.Repeat("e", 64) + `'
		WHERE session_id = '` + sessionID + `'`
}
