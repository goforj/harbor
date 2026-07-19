package migrations

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const machineOwnershipProjectionMigrationName = "2026_07_19_140000_create_machine_ownership_projections"

const machineOwnershipProjectionVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// machineOwnershipProjectionRecord mirrors the exact non-authoritative confirmation persisted after helper success.
type machineOwnershipProjectionRecord struct {
	ID                     int
	NetworkStateID         int
	OwnershipSchemaVersion int
	InstallationID         string
	OwnerIdentity          string
	OwnershipGeneration    int64
	LoopbackPoolPrefix     string
	TicketVerifierKey      string
	RecordFingerprint      string
	ConfirmedAt            any
}

// TestMachineOwnershipProjectionMigrationCreatesBoundedSchema verifies the daemon projection cannot outlive its network root.
func TestMachineOwnershipProjectionMigrationCreatesBoundedSchema(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)

	if !databaseConnection.Migrator().HasTable("machine_ownership_projections") {
		t.Fatal("migration did not create machine_ownership_projections")
	}
	for _, column := range []string{
		"id",
		"network_state_id",
		"ownership_schema_version",
		"installation_id",
		"owner_identity",
		"ownership_generation",
		"loopback_pool_prefix",
		"ticket_verifier_key",
		"record_fingerprint",
		"confirmed_at",
	} {
		if !databaseConnection.Migrator().HasColumn("machine_ownership_projections", column) {
			t.Fatalf("migration did not create machine_ownership_projections.%s", column)
		}
	}
	for _, forbidden := range []string{"operation_id", "operation_revision", "project_id", "ticket_reference"} {
		if databaseConnection.Migrator().HasColumn("machine_ownership_projections", forbidden) {
			t.Fatalf("machine ownership projection retained transient %s authority", forbidden)
		}
	}
	assertMachineOwnershipProjectionForeignKey(t, databaseConnection)
}

// TestMachineOwnershipProjectionMigrationPersistsCompleteConfirmation verifies every field needed for later ticket admission survives restart.
func TestMachineOwnershipProjectionMigrationPersistsCompleteConfirmation(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.OwnerIdentity = "S-1-5-21-1000"
	insertMachineOwnershipProjection(t, databaseConnection, record)

	var read struct {
		ID                     int
		NetworkStateID         int
		OwnershipSchemaVersion int
		InstallationID         string
		OwnerIdentity          string
		OwnershipGeneration    int64
		LoopbackPoolPrefix     string
		TicketVerifierKey      string
		RecordFingerprint      string
		ConfirmedAt            time.Time
	}
	if err := databaseConnection.Raw(`SELECT id, network_state_id, ownership_schema_version,
		installation_id, owner_identity, ownership_generation, loopback_pool_prefix,
		ticket_verifier_key, record_fingerprint, confirmed_at
		FROM machine_ownership_projections WHERE id = 1`).Scan(&read).Error; err != nil {
		t.Fatalf("read machine ownership projection: %v", err)
	}
	if read.ID != record.ID || read.NetworkStateID != record.NetworkStateID ||
		read.OwnershipSchemaVersion != record.OwnershipSchemaVersion ||
		read.InstallationID != record.InstallationID || read.OwnerIdentity != record.OwnerIdentity ||
		read.OwnershipGeneration != record.OwnershipGeneration ||
		read.LoopbackPoolPrefix != record.LoopbackPoolPrefix ||
		read.TicketVerifierKey != record.TicketVerifierKey ||
		read.RecordFingerprint != record.RecordFingerprint ||
		!read.ConfirmedAt.Equal(record.ConfirmedAt.(time.Time)) {
		t.Fatalf("machine ownership projection = %#v, want %#v", read, record)
	}
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipProjectionMigrationRejectsInvalidConfirmation proves direct writers cannot broaden or forge projected authority.
func TestMachineOwnershipProjectionMigrationRejectsInvalidConfirmation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*machineOwnershipProjectionRecord)
	}{
		{name: "non-singleton ID", mutate: func(record *machineOwnershipProjectionRecord) { record.ID = 2 }},
		{name: "foreign network state", mutate: func(record *machineOwnershipProjectionRecord) { record.NetworkStateID = 2 }},
		{name: "ownership schema", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipSchemaVersion = 2 }},
		{name: "empty installation", mutate: func(record *machineOwnershipProjectionRecord) { record.InstallationID = " " }},
		{name: "unsafe installation", mutate: func(record *machineOwnershipProjectionRecord) { record.InstallationID = "-installation" }},
		{name: "long installation", mutate: func(record *machineOwnershipProjectionRecord) { record.InstallationID = strings.Repeat("a", 129) }},
		{name: "empty owner", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnerIdentity = "" }},
		{name: "spaced owner", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnerIdentity = " 501" }},
		{name: "unsafe owner", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnerIdentity = "501/502" }},
		{name: "zero generation", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipGeneration = 0 }},
		{name: "unsafe generation", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipGeneration = 9007199254740992 }},
		{name: "public pool", mutate: func(record *machineOwnershipProjectionRecord) { record.LoopbackPoolPrefix = "192.0.2.8/29" }},
		{name: "wrong pool width", mutate: func(record *machineOwnershipProjectionRecord) { record.LoopbackPoolPrefix = "127.77.0.0/24" }},
		{name: "unaligned pool", mutate: func(record *machineOwnershipProjectionRecord) { record.LoopbackPoolPrefix = "127.77.0.9/29" }},
		{name: "pool whitespace", mutate: func(record *machineOwnershipProjectionRecord) { record.LoopbackPoolPrefix = " 127.77.0.8/29" }},
		{name: "short verifier", mutate: func(record *machineOwnershipProjectionRecord) { record.TicketVerifierKey = strings.Repeat("A", 43) }},
		{name: "un-padded verifier", mutate: func(record *machineOwnershipProjectionRecord) { record.TicketVerifierKey = strings.Repeat("A", 44) }},
		{name: "unsafe verifier", mutate: func(record *machineOwnershipProjectionRecord) {
			record.TicketVerifierKey = strings.Repeat("A", 42) + "%="
		}},
		{name: "short fingerprint", mutate: func(record *machineOwnershipProjectionRecord) { record.RecordFingerprint = strings.Repeat("a", 63) }},
		{name: "uppercase fingerprint", mutate: func(record *machineOwnershipProjectionRecord) { record.RecordFingerprint = strings.Repeat("A", 64) }},
		{name: "missing confirmation time", mutate: func(record *machineOwnershipProjectionRecord) { record.ConfirmedAt = nil }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
			insertNetworkMigrationState(t, databaseConnection, 1)
			record := defaultMachineOwnershipProjectionRecord()
			test.mutate(&record)
			if err := executeMachineOwnershipProjection(databaseConnection, record); err == nil {
				t.Fatalf("invalid machine ownership projection unexpectedly succeeded: %#v", record)
			}
		})
	}

	t.Run("missing network state", func(t *testing.T) {
		databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
		if err := executeMachineOwnershipProjection(databaseConnection, defaultMachineOwnershipProjectionRecord()); err == nil {
			t.Fatal("machine ownership projection without a network root unexpectedly succeeded")
		}
	})

	t.Run("second singleton", func(t *testing.T) {
		databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
		insertNetworkMigrationState(t, databaseConnection, 1)
		insertMachineOwnershipProjection(t, databaseConnection, defaultMachineOwnershipProjectionRecord())
		if err := executeMachineOwnershipProjection(databaseConnection, defaultMachineOwnershipProjectionRecord()); err == nil {
			t.Fatal("second machine ownership projection unexpectedly succeeded")
		}
	})
}

// TestMachineOwnershipProjectionMigrationFollowsNetworkLifecycle verifies the projection is removed only with its derived network root.
func TestMachineOwnershipProjectionMigrationFollowsNetworkLifecycle(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	insertMachineOwnershipProjection(t, databaseConnection, defaultMachineOwnershipProjectionRecord())

	if err := databaseConnection.Exec("UPDATE network_state SET id = 2 WHERE id = 1").Error; err == nil {
		t.Fatal("network root update unexpectedly moved confirmed ownership")
	}
	if err := databaseConnection.Exec("DELETE FROM network_state WHERE id = 1").Error; err != nil {
		t.Fatalf("delete network root: %v", err)
	}
	assertProjectionCount(t, databaseConnection, "machine_ownership_projections", 0)
}

// TestMachineOwnershipProjectionMigrationRollbackPreservesNetworkState verifies reversal removes only the derived confirmation table.
func TestMachineOwnershipProjectionMigrationRollbackPreservesNetworkState(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)
	migration := machineOwnershipProjectionMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership projection migration: %v", err)
	}
	insertNetworkMigrationState(t, databaseConnection, 1)
	insertMachineOwnershipProjection(t, databaseConnection, defaultMachineOwnershipProjectionRecord())

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback machine ownership projection migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("machine_ownership_projections") {
		t.Fatal("rollback retained machine_ownership_projections")
	}
	assertProjectionCount(t, databaseConnection, "network_state", 1)
}

// newMachineOwnershipProjectionMigrationHarness applies the production network root and its daemon-owned confirmation projection.
func newMachineOwnershipProjectionMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	applyNetworkPersistenceMigrations(t, databaseConnection)
	if err := machineOwnershipProjectionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership projection migration: %v", err)
	}
	return databaseConnection
}

// machineOwnershipProjectionMigration finds the embedded production migration by its stable identity.
func machineOwnershipProjectionMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == machineOwnershipProjectionMigrationName {
			return migration
		}
	}
	t.Fatalf("machine ownership projection migration %q is not registered", machineOwnershipProjectionMigrationName)
	return nil
}

// defaultMachineOwnershipProjectionRecord supplies one complete generation-one helper confirmation.
func defaultMachineOwnershipProjectionRecord() machineOwnershipProjectionRecord {
	return machineOwnershipProjectionRecord{
		ID:                     1,
		NetworkStateID:         1,
		OwnershipSchemaVersion: 1,
		InstallationID:         "installation-a",
		OwnerIdentity:          "501",
		OwnershipGeneration:    1,
		LoopbackPoolPrefix:     "127.77.0.8/29",
		TicketVerifierKey:      machineOwnershipProjectionVerifierKey,
		RecordFingerprint:      strings.Repeat("a", 64),
		ConfirmedAt:            time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC),
	}
}

// insertMachineOwnershipProjection inserts one record expected to satisfy every schema invariant.
func insertMachineOwnershipProjection(t *testing.T, databaseConnection *gorm.DB, record machineOwnershipProjectionRecord) {
	t.Helper()
	if err := executeMachineOwnershipProjection(databaseConnection, record); err != nil {
		t.Fatalf("insert machine ownership projection: %v", err)
	}
}

// executeMachineOwnershipProjection keeps valid and invalid records on the same SQL insert path.
func executeMachineOwnershipProjection(databaseConnection *gorm.DB, record machineOwnershipProjectionRecord) error {
	return databaseConnection.Exec(`INSERT INTO machine_ownership_projections
		(id, network_state_id, ownership_schema_version, installation_id, owner_identity,
		 ownership_generation, loopback_pool_prefix, ticket_verifier_key, record_fingerprint, confirmed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.NetworkStateID,
		record.OwnershipSchemaVersion,
		record.InstallationID,
		record.OwnerIdentity,
		record.OwnershipGeneration,
		record.LoopbackPoolPrefix,
		record.TicketVerifierKey,
		record.RecordFingerprint,
		record.ConfirmedAt,
	).Error
}

// assertMachineOwnershipProjectionForeignKey proves the confirmation has only one derived network owner.
func assertMachineOwnershipProjectionForeignKey(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var rows []struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_list(machine_ownership_projections)").Scan(&rows).Error; err != nil {
		t.Fatalf("inspect machine ownership projection foreign key: %v", err)
	}
	if len(rows) != 1 || rows[0].Table != "network_state" || rows[0].From != "network_state_id" ||
		rows[0].To != "id" || rows[0].OnUpdate != "RESTRICT" || rows[0].OnDelete != "CASCADE" {
		t.Fatalf("machine ownership projection foreign key = %#v", rows)
	}
}

// assertMachineOwnershipProjectionForeignKeysClean verifies an accepted projection leaves no deferred ownership mismatch.
func assertMachineOwnershipProjectionForeignKeysClean(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var violations []struct {
		Table  string
		RowID  int
		Parent string
		FKID   int
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("check machine ownership projection foreign keys: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("machine ownership projection foreign key violations = %#v", violations)
	}
}
