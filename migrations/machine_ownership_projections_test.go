package migrations

import (
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/logger"
	"gorm.io/gorm"
)

const machineOwnershipProjectionMigrationName = "2026_07_19_115139_create_machine_ownership_projections"

const machineOwnershipPolicyMigrationName = "2026_07_19_150000_add_machine_ownership_network_policy_fingerprint"

const machineOwnershipProjectionVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

const machineOwnershipNetworkPolicyFingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// machineOwnershipProjectionRecord mirrors the exact non-authoritative confirmation persisted after helper success.
type machineOwnershipProjectionRecord struct {
	ID                       int
	NetworkStateID           int
	OwnershipSchemaVersion   int
	InstallationID           string
	OwnerIdentity            string
	OwnershipGeneration      int64
	LoopbackPoolPrefix       string
	NetworkPolicyFingerprint *string
	TicketVerifierKey        string
	RecordFingerprint        string
	ConfirmedAt              any
}

// machineOwnershipProjectionStorageSnapshot captures SQLite storage classes and values across table rebuilds.
type machineOwnershipProjectionStorageSnapshot struct {
	ID                     string
	NetworkStateID         string
	OwnershipSchemaVersion string
	InstallationID         string
	OwnerIdentity          string
	OwnershipGeneration    string
	LoopbackPoolPrefix     string
	TicketVerifierKey      string
	RecordFingerprint      string
	ConfirmedAt            string
}

// TestMachineOwnershipProjectionMigrationKeepsHistoricalIdentity pins the ledger key already present in development databases.
func TestMachineOwnershipProjectionMigrationKeepsHistoricalIdentity(t *testing.T) {
	migration := machineOwnershipProjectionMigration(t)
	const historicalName = "2026_07_19_115139_create_machine_ownership_projections"
	const historicalPath = "harbord/default/2026_07_19_115139_create_machine_ownership_projections"
	if migration.Name() != historicalName || migration.SourcePath() != historicalPath {
		t.Fatalf(
			"machine ownership migration identity = (%q, %q), want historical (%q, %q)",
			migration.Name(),
			migration.SourcePath(),
			historicalName,
			historicalPath,
		)
	}
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
		"network_policy_fingerprint",
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
	assertProjectionCount(t, databaseConnection, "machine_ownership_projections", 0)
	assertMachineOwnershipProjectionForeignKey(t, databaseConnection)
}

// TestMachineOwnershipPolicyMigrationPreservesIdentityProjection verifies the rebuild copies schema-one storage without normalization.
func TestMachineOwnershipPolicyMigrationPreservesIdentityProjection(t *testing.T) {
	databaseConnection := newIdentityMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.RecordFingerprint = strings.Repeat("0123456789abcdef", 4)
	record.ConfirmedAt = "2026-07-19T14:00:00.123456789+05:30"
	insertIdentityMachineOwnershipProjection(t, databaseConnection, record)
	before := readMachineOwnershipProjectionStorage(t, databaseConnection)

	if err := machineOwnershipPolicyMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership policy migration: %v", err)
	}
	if !databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("upgraded projection is missing network_policy_fingerprint")
	}
	after := readMachineOwnershipProjectionStorage(t, databaseConnection)
	if after != before {
		t.Fatalf("upgraded schema-one storage = %#v, want %#v", after, before)
	}
	if policy := readMachineOwnershipNetworkPolicyStorage(t, databaseConnection); policy != "null:NULL" {
		t.Fatalf("upgraded schema-one network policy storage = %q, want NULL", policy)
	}
	assertMachineOwnershipProjectionForeignKey(t, databaseConnection)
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipProjectionMigrationPersistsCompleteConfirmation verifies every field needed for later ticket admission survives restart.
func TestMachineOwnershipProjectionMigrationPersistsCompleteConfirmation(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.OwnerIdentity = "S-1-5-21-1000"
	insertMachineOwnershipProjection(t, databaseConnection, record)

	var read struct {
		ID                       int
		NetworkStateID           int
		OwnershipSchemaVersion   int
		InstallationID           string
		OwnerIdentity            string
		OwnershipGeneration      int64
		LoopbackPoolPrefix       string
		NetworkPolicyFingerprint *string
		TicketVerifierKey        string
		RecordFingerprint        string
		ConfirmedAt              time.Time
	}
	if err := databaseConnection.Raw(`SELECT id, network_state_id, ownership_schema_version,
		installation_id, owner_identity, ownership_generation, loopback_pool_prefix,
		network_policy_fingerprint, ticket_verifier_key, record_fingerprint, confirmed_at
		FROM machine_ownership_projections WHERE id = 1`).Scan(&read).Error; err != nil {
		t.Fatalf("read machine ownership projection: %v", err)
	}
	if read.ID != record.ID || read.NetworkStateID != record.NetworkStateID ||
		read.OwnershipSchemaVersion != record.OwnershipSchemaVersion ||
		read.InstallationID != record.InstallationID || read.OwnerIdentity != record.OwnerIdentity ||
		read.OwnershipGeneration != record.OwnershipGeneration ||
		read.LoopbackPoolPrefix != record.LoopbackPoolPrefix ||
		read.NetworkPolicyFingerprint != record.NetworkPolicyFingerprint ||
		read.TicketVerifierKey != record.TicketVerifierKey ||
		read.RecordFingerprint != record.RecordFingerprint ||
		!read.ConfirmedAt.Equal(record.ConfirmedAt.(time.Time)) {
		t.Fatalf("machine ownership projection = %#v, want %#v", read, record)
	}
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipPolicyMigrationPersistsPolicyBoundConfirmation verifies schema two accepts one canonical network-policy digest.
func TestMachineOwnershipPolicyMigrationPersistsPolicyBoundConfirmation(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.OwnershipSchemaVersion = 2
	policyFingerprint := machineOwnershipNetworkPolicyFingerprint
	record.NetworkPolicyFingerprint = &policyFingerprint
	insertMachineOwnershipProjection(t, databaseConnection, record)

	var read struct {
		OwnershipSchemaVersion   int
		NetworkPolicyFingerprint string
	}
	if err := databaseConnection.Raw(`SELECT ownership_schema_version, network_policy_fingerprint
		FROM machine_ownership_projections WHERE id = 1`).Scan(&read).Error; err != nil {
		t.Fatalf("read policy-bound machine ownership projection: %v", err)
	}
	if read.OwnershipSchemaVersion != 2 || read.NetworkPolicyFingerprint != policyFingerprint {
		t.Fatalf("policy-bound machine ownership projection = %#v", read)
	}
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipProjectionMigrationRejectsInvalidConfirmation proves direct writers cannot broaden or forge projected authority.
func TestMachineOwnershipProjectionMigrationRejectsInvalidConfirmation(t *testing.T) {
	policyFingerprint := func(value string) *string { return &value }
	tests := []struct {
		name   string
		mutate func(*machineOwnershipProjectionRecord)
	}{
		{name: "non-singleton ID", mutate: func(record *machineOwnershipProjectionRecord) { record.ID = 2 }},
		{name: "foreign network state", mutate: func(record *machineOwnershipProjectionRecord) { record.NetworkStateID = 2 }},
		{name: "zero ownership schema", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipSchemaVersion = 0 }},
		{name: "unknown ownership schema", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipSchemaVersion = 3 }},
		{name: "identity schema with policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.NetworkPolicyFingerprint = policyFingerprint(machineOwnershipNetworkPolicyFingerprint)
		}},
		{name: "identity schema with empty policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.NetworkPolicyFingerprint = policyFingerprint("")
		}},
		{name: "policy schema without policy", mutate: func(record *machineOwnershipProjectionRecord) { record.OwnershipSchemaVersion = 2 }},
		{name: "policy schema with empty policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint("")
		}},
		{name: "policy schema with short policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint(strings.Repeat("a", 63))
		}},
		{name: "policy schema with long policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint(strings.Repeat("a", 65))
		}},
		{name: "policy schema with uppercase policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint(strings.Repeat("A", 64))
		}},
		{name: "policy schema with non-hex policy", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint(strings.Repeat("a", 63) + "g")
		}},
		{name: "policy schema with embedded null", mutate: func(record *machineOwnershipProjectionRecord) {
			record.OwnershipSchemaVersion = 2
			record.NetworkPolicyFingerprint = policyFingerprint(strings.Repeat("a", 32) + "\x00" + strings.Repeat("a", 31))
		}},
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

// TestMachineOwnershipPolicyMigrationRollbackPreservesIdentityProjection verifies a reversible row returns to the exact schema-one shape.
func TestMachineOwnershipPolicyMigrationRollbackPreservesIdentityProjection(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)
	if err := machineOwnershipProjectionMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership projection migration: %v", err)
	}
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.RecordFingerprint = strings.Repeat("0123456789abcdef", 4)
	record.ConfirmedAt = "2026-07-19T14:00:00.123456789+05:30"
	insertIdentityMachineOwnershipProjection(t, databaseConnection, record)
	identitySchema := readMachineOwnershipProjectionSchema(t, databaseConnection)
	if err := machineOwnershipPolicyMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership policy migration: %v", err)
	}
	before := readMachineOwnershipProjectionStorage(t, databaseConnection)
	// A persistent namesake proves guard cleanup remains confined to SQLite's temporary schema.
	if err := databaseConnection.Exec(`CREATE TABLE machine_ownership_projection_policy_rollback_guard (
		marker TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create persistent rollback-guard namesake: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO machine_ownership_projection_policy_rollback_guard (marker)
		VALUES ('persistent')`).Error; err != nil {
		t.Fatalf("seed persistent rollback-guard namesake: %v", err)
	}

	if err := databaseConnection.Transaction(func(tx *gorm.DB) error {
		return machineOwnershipPolicyMigration(t).Down(tx)
	}); err != nil {
		t.Fatalf("rollback machine ownership policy migration: %v", err)
	}
	if !databaseConnection.Migrator().HasTable("machine_ownership_projections") {
		t.Fatal("rollback removed machine_ownership_projections")
	}
	if databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("rollback retained network_policy_fingerprint")
	}
	if restoredSchema := readMachineOwnershipProjectionSchema(t, databaseConnection); restoredSchema != identitySchema {
		t.Fatalf("restored schema-one table differs from its original definition:\n%s\nwant:\n%s", restoredSchema, identitySchema)
	}
	var guardMarker string
	if err := databaseConnection.Raw(`SELECT marker FROM main.machine_ownership_projection_policy_rollback_guard`).Scan(&guardMarker).Error; err != nil {
		t.Fatalf("read persistent rollback-guard namesake: %v", err)
	}
	if guardMarker != "persistent" {
		t.Fatalf("persistent rollback-guard namesake marker = %q, want persistent", guardMarker)
	}
	after := readMachineOwnershipProjectionStorage(t, databaseConnection)
	if after != before {
		t.Fatalf("rolled-back schema-one storage = %#v, want %#v", after, before)
	}
	invalidSchemaTwo := defaultMachineOwnershipProjectionRecord()
	invalidSchemaTwo.OwnershipSchemaVersion = 2
	if err := executeIdentityMachineOwnershipProjection(databaseConnection, invalidSchemaTwo); err == nil {
		t.Fatal("restored identity-only projection accepted schema two")
	}
	assertProjectionCount(t, databaseConnection, "network_state", 1)
	assertMachineOwnershipProjectionForeignKey(t, databaseConnection)
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipPolicyMigrationRefusesLossyRollback verifies schema-two authority and the upgraded schema remain intact on failure.
func TestMachineOwnershipPolicyMigrationRefusesLossyRollback(t *testing.T) {
	databaseConnection := newMachineOwnershipProjectionMigrationHarness(t)
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.OwnershipSchemaVersion = 2
	policyFingerprint := machineOwnershipNetworkPolicyFingerprint
	record.NetworkPolicyFingerprint = &policyFingerprint
	record.ConfirmedAt = "2026-07-19T15:00:00.987654321-04:00"
	insertMachineOwnershipProjection(t, databaseConnection, record)
	before := readMachineOwnershipProjectionStorage(t, databaseConnection)
	policyBefore := readMachineOwnershipNetworkPolicyStorage(t, databaseConnection)

	if err := databaseConnection.Transaction(func(tx *gorm.DB) error {
		return machineOwnershipPolicyMigration(t).Down(tx)
	}); err == nil {
		t.Fatal("machine ownership policy rollback accepted a schema-two projection")
	}
	if !databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("rejected rollback removed network_policy_fingerprint")
	}
	after := readMachineOwnershipProjectionStorage(t, databaseConnection)
	policyAfter := readMachineOwnershipNetworkPolicyStorage(t, databaseConnection)
	if after != before || policyAfter != policyBefore {
		t.Fatalf("projection after rejected rollback = %#v policy %q, want %#v policy %q", after, policyAfter, before, policyBefore)
	}
	assertProjectionCount(t, databaseConnection, "machine_ownership_projections", 1)
	assertMachineOwnershipProjectionForeignKey(t, databaseConnection)
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipPolicyMigrationRunsInEmbeddedStream verifies the all-migrations command reaches the policy-bound projection schema.
func TestMachineOwnershipPolicyMigrationRunsInEmbeddedStream(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	t.Setenv("FORJ_APP", "harbord")
	if err := NewMigrateCmd(logger.NewSilentLogger(), connections).Run(); err != nil {
		t.Fatalf("run embedded Harbor migrations: %v", err)
	}
	if !databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("embedded migration stream did not reach the policy-bound projection schema")
	}
	var applied int64
	if err := databaseConnection.Table("migrations").Where("name = ?", machineOwnershipPolicyMigrationName).Count(&applied).Error; err != nil {
		t.Fatalf("count machine ownership policy migration record: %v", err)
	}
	if applied != 1 {
		t.Fatalf("machine ownership policy migration records = %d, want 1", applied)
	}
	insertNetworkMigrationState(t, databaseConnection, 1)
	record := defaultMachineOwnershipProjectionRecord()
	record.OwnershipSchemaVersion = 2
	policyFingerprint := machineOwnershipNetworkPolicyFingerprint
	record.NetworkPolicyFingerprint = &policyFingerprint
	insertMachineOwnershipProjection(t, databaseConnection, record)
	assertMachineOwnershipProjectionForeignKeysClean(t, databaseConnection)
}

// TestMachineOwnershipPolicyMigrationUpgradesHistoricalLedgerIdentity reproduces the durable database created before the later policy migration existed.
func TestMachineOwnershipPolicyMigrationUpgradesHistoricalLedgerIdentity(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() > machineOwnershipProjectionMigrationName {
			break
		}
		applied, err := applySQLiteMigration(databaseConnection, migration, migrationRecord{
			Name:       migration.Name(),
			App:        migration.App(),
			Connection: migration.Connection(),
			SourcePath: migration.SourcePath(),
			AppliedAt:  time.Date(2026, time.July, 19, 11, 51, 39, 0, time.UTC),
		})
		if err != nil || !applied {
			t.Fatalf("apply historical migration %s = (%t, %v), want (true, nil)", migration.Name(), applied, err)
		}
	}
	if databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("historical database unexpectedly contains the later policy column")
	}

	t.Setenv("FORJ_APP", "harbord")
	if err := NewMigrateCmd(logger.NewSilentLogger(), connections).Run(); err != nil {
		t.Fatalf("upgrade historical migration ledger: %v", err)
	}
	if !databaseConnection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("historical database did not reach the policy-bound schema")
	}
	for name, want := range map[string]int64{
		machineOwnershipProjectionMigrationName: 1,
		machineOwnershipPolicyMigrationName:     1,
		"2026_07_19_140000_create_machine_ownership_projections": 0,
	} {
		var count int64
		if err := databaseConnection.Table("migrations").Where("name = ?", name).Count(&count).Error; err != nil {
			t.Fatalf("count migration record %s: %v", name, err)
		}
		if count != want {
			t.Fatalf("migration record %s count = %d, want %d", name, count, want)
		}
	}
}

// newMachineOwnershipProjectionMigrationHarness applies the production network root and its daemon-owned confirmation projection.
func newMachineOwnershipProjectionMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	databaseConnection := newIdentityMachineOwnershipProjectionMigrationHarness(t)
	if err := machineOwnershipPolicyMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply machine ownership policy migration: %v", err)
	}
	return databaseConnection
}

// newIdentityMachineOwnershipProjectionMigrationHarness applies the production network root and identity-only confirmation projection.
func newIdentityMachineOwnershipProjectionMigrationHarness(t *testing.T) *gorm.DB {
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

// machineOwnershipPolicyMigration finds the embedded policy-binding migration by its stable identity.
func machineOwnershipPolicyMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == machineOwnershipPolicyMigrationName {
			return migration
		}
	}
	t.Fatalf("machine ownership policy migration %q is not registered", machineOwnershipPolicyMigrationName)
	return nil
}

// defaultMachineOwnershipProjectionRecord supplies one complete generation-one helper confirmation.
func defaultMachineOwnershipProjectionRecord() machineOwnershipProjectionRecord {
	return machineOwnershipProjectionRecord{
		ID:                       1,
		NetworkStateID:           1,
		OwnershipSchemaVersion:   1,
		InstallationID:           "installation-a",
		OwnerIdentity:            "501",
		OwnershipGeneration:      1,
		LoopbackPoolPrefix:       "127.77.0.8/29",
		NetworkPolicyFingerprint: nil,
		TicketVerifierKey:        machineOwnershipProjectionVerifierKey,
		RecordFingerprint:        strings.Repeat("a", 64),
		ConfirmedAt:              time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC),
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
		 ownership_generation, loopback_pool_prefix, network_policy_fingerprint,
		 ticket_verifier_key, record_fingerprint, confirmed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.NetworkStateID,
		record.OwnershipSchemaVersion,
		record.InstallationID,
		record.OwnerIdentity,
		record.OwnershipGeneration,
		record.LoopbackPoolPrefix,
		record.NetworkPolicyFingerprint,
		record.TicketVerifierKey,
		record.RecordFingerprint,
		record.ConfirmedAt,
	).Error
}

// insertIdentityMachineOwnershipProjection inserts one row into the pre-policy schema.
func insertIdentityMachineOwnershipProjection(t *testing.T, databaseConnection *gorm.DB, record machineOwnershipProjectionRecord) {
	t.Helper()
	if err := executeIdentityMachineOwnershipProjection(databaseConnection, record); err != nil {
		t.Fatalf("insert identity-only machine ownership projection: %v", err)
	}
}

// executeIdentityMachineOwnershipProjection writes through the original schema-one column list.
func executeIdentityMachineOwnershipProjection(databaseConnection *gorm.DB, record machineOwnershipProjectionRecord) error {
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

// readMachineOwnershipProjectionStorage returns exact SQLite representations shared by both schema versions.
func readMachineOwnershipProjectionStorage(t *testing.T, databaseConnection *gorm.DB) machineOwnershipProjectionStorageSnapshot {
	t.Helper()
	var snapshot machineOwnershipProjectionStorageSnapshot
	if err := databaseConnection.Raw(`SELECT
		typeof(id) || ':' || quote(id) AS id,
		typeof(network_state_id) || ':' || quote(network_state_id) AS network_state_id,
		typeof(ownership_schema_version) || ':' || quote(ownership_schema_version) AS ownership_schema_version,
		typeof(installation_id) || ':' || quote(installation_id) AS installation_id,
		typeof(owner_identity) || ':' || quote(owner_identity) AS owner_identity,
		typeof(ownership_generation) || ':' || quote(ownership_generation) AS ownership_generation,
		typeof(loopback_pool_prefix) || ':' || quote(loopback_pool_prefix) AS loopback_pool_prefix,
		typeof(ticket_verifier_key) || ':' || quote(ticket_verifier_key) AS ticket_verifier_key,
		typeof(record_fingerprint) || ':' || quote(record_fingerprint) AS record_fingerprint,
		typeof(confirmed_at) || ':' || quote(confirmed_at) AS confirmed_at
		FROM machine_ownership_projections WHERE id = 1`).Scan(&snapshot).Error; err != nil {
		t.Fatalf("read machine ownership projection storage: %v", err)
	}
	return snapshot
}

// readMachineOwnershipNetworkPolicyStorage returns the exact nullable policy-fingerprint representation.
func readMachineOwnershipNetworkPolicyStorage(t *testing.T, databaseConnection *gorm.DB) string {
	t.Helper()
	var storage string
	if err := databaseConnection.Raw(`SELECT typeof(network_policy_fingerprint) || ':' || quote(network_policy_fingerprint)
		FROM machine_ownership_projections WHERE id = 1`).Scan(&storage).Error; err != nil {
		t.Fatalf("read machine ownership network policy storage: %v", err)
	}
	return storage
}

// readMachineOwnershipProjectionSchema returns a stable table definition across SQLite rename quoting.
func readMachineOwnershipProjectionSchema(t *testing.T, databaseConnection *gorm.DB) string {
	t.Helper()
	var schema string
	if err := databaseConnection.Raw(`SELECT sql FROM sqlite_schema
		WHERE type = 'table' AND name = 'machine_ownership_projections'`).Scan(&schema).Error; err != nil {
		t.Fatalf("read machine ownership projection schema: %v", err)
	}
	return strings.Replace(schema, `CREATE TABLE "machine_ownership_projections"`, "CREATE TABLE machine_ownership_projections", 1)
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
