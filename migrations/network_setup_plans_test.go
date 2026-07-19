package migrations

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const networkSetupPlansMigrationName = "2026_07_19_130000_create_network_setup_plans"

const networkSetupTestVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestNetworkSetupPlansMigrationCreatesGlobalAuthoritySchema verifies the plan is singleton, operation-bound, and independent of project and network projections.
func TestNetworkSetupPlansMigrationCreatesGlobalAuthoritySchema(t *testing.T) {
	databaseConnection := newNetworkSetupMigrationHarness(t)

	if !databaseConnection.Migrator().HasTable("network_setup_plans") {
		t.Fatal("migration did not create network_setup_plans")
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"operation_revision",
		"ownership_schema_version",
		"installation_id",
		"owner_identity",
		"ownership_generation",
		"loopback_pool_prefix",
		"ticket_verifier_key",
	} {
		if !databaseConnection.Migrator().HasColumn("network_setup_plans", column) {
			t.Fatalf("migration did not create network_setup_plans.%s", column)
		}
	}
	for _, forbidden := range []string{"network_state_id", "project_id", "address"} {
		if databaseConnection.Migrator().HasColumn("network_setup_plans", forbidden) {
			t.Fatalf("network setup plan retained forbidden %s coupling", forbidden)
		}
	}
	if databaseConnection.Migrator().HasTable("network_state") {
		t.Fatal("network setup plan migration unexpectedly requires network persistence")
	}
	for _, index := range []string{
		"operations_network_setup_revision_idx",
		"operations_one_active_network_setup_idx",
	} {
		if !databaseConnection.Migrator().HasIndex("operations", index) {
			t.Fatalf("migration did not create operations index %s", index)
		}
	}

	assertNetworkSetupOperationForeignKey(t, databaseConnection)
}

// TestNetworkSetupPlansMigrationPersistsCompleteOwnershipRecord verifies restart recovery retains every machine ownership dimension exactly.
func TestNetworkSetupPlansMigrationPersistsCompleteOwnershipRecord(t *testing.T) {
	databaseConnection := newNetworkSetupMigrationHarness(t)
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-setup", "requires_approval", 1)
	plan := defaultNetworkSetupMigrationPlan("operation-setup", 1)
	plan.OwnerIdentity = "S-1-5-21-1000"
	insertNetworkSetupMigrationPlan(t, databaseConnection, plan)

	var read struct {
		ID                     int
		OperationID            string
		OperationRevision      int
		OwnershipSchemaVersion int
		InstallationID         string
		OwnerIdentity          string
		OwnershipGeneration    int
		LoopbackPoolPrefix     string
		TicketVerifierKey      string
	}
	if err := databaseConnection.Raw(`SELECT id, operation_id, operation_revision,
		ownership_schema_version, installation_id, owner_identity, ownership_generation,
		loopback_pool_prefix, ticket_verifier_key
		FROM network_setup_plans WHERE id = 1`).Scan(&read).Error; err != nil {
		t.Fatalf("read network setup plan: %v", err)
	}
	if read.ID != 1 || read.OperationID != "operation-setup" || read.OperationRevision != 1 ||
		read.OwnershipSchemaVersion != 1 || read.InstallationID != "installation-a" ||
		read.OwnerIdentity != "S-1-5-21-1000" || read.OwnershipGeneration != 1 ||
		read.LoopbackPoolPrefix != "127.77.0.8/29" || read.TicketVerifierKey != networkSetupTestVerifierKey {
		t.Fatalf("network setup plan = %#v, want exact ownership record", read)
	}
	assertNetworkSetupForeignKeysClean(t, databaseConnection)
}

// TestNetworkSetupPlansMigrationRejectsInvalidAuthority verifies direct writers cannot broaden bootstrap ownership or create competing plans.
func TestNetworkSetupPlansMigrationRejectsInvalidAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkSetupMigrationPlan)
	}{
		{name: "non-singleton ID", mutate: func(plan *networkSetupMigrationPlan) { plan.ID = 2 }},
		{name: "zero operation revision", mutate: func(plan *networkSetupMigrationPlan) { plan.OperationRevision = 0 }},
		{name: "unsafe operation revision", mutate: func(plan *networkSetupMigrationPlan) { plan.OperationRevision = 9007199254740992 }},
		{name: "ownership schema", mutate: func(plan *networkSetupMigrationPlan) { plan.OwnershipSchemaVersion = 2 }},
		{name: "empty installation", mutate: func(plan *networkSetupMigrationPlan) { plan.InstallationID = " " }},
		{name: "unsafe installation", mutate: func(plan *networkSetupMigrationPlan) { plan.InstallationID = "-installation" }},
		{name: "long installation", mutate: func(plan *networkSetupMigrationPlan) { plan.InstallationID = strings.Repeat("a", 129) }},
		{name: "empty owner", mutate: func(plan *networkSetupMigrationPlan) { plan.OwnerIdentity = "" }},
		{name: "spaced owner", mutate: func(plan *networkSetupMigrationPlan) { plan.OwnerIdentity = " 501" }},
		{name: "unsafe owner", mutate: func(plan *networkSetupMigrationPlan) { plan.OwnerIdentity = "501/502" }},
		{name: "ownership generation", mutate: func(plan *networkSetupMigrationPlan) { plan.OwnershipGeneration = 2 }},
		{name: "public pool", mutate: func(plan *networkSetupMigrationPlan) { plan.LoopbackPoolPrefix = "192.0.2.8/29" }},
		{name: "wrong pool width", mutate: func(plan *networkSetupMigrationPlan) { plan.LoopbackPoolPrefix = "127.77.0.0/24" }},
		{name: "unaligned pool", mutate: func(plan *networkSetupMigrationPlan) { plan.LoopbackPoolPrefix = "127.77.0.9/29" }},
		{name: "pool whitespace", mutate: func(plan *networkSetupMigrationPlan) { plan.LoopbackPoolPrefix = " 127.77.0.8/29" }},
		{name: "short verifier", mutate: func(plan *networkSetupMigrationPlan) { plan.TicketVerifierKey = strings.Repeat("A", 43) }},
		{name: "un-padded verifier", mutate: func(plan *networkSetupMigrationPlan) { plan.TicketVerifierKey = strings.Repeat("A", 44) }},
		{name: "unsafe verifier", mutate: func(plan *networkSetupMigrationPlan) { plan.TicketVerifierKey = strings.Repeat("A", 42) + "%=" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newNetworkSetupMigrationHarness(t)
			insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-setup", "requires_approval", 1)
			plan := defaultNetworkSetupMigrationPlan("operation-setup", 1)
			test.mutate(&plan)
			if err := executeNetworkSetupMigrationPlan(databaseConnection, plan); err == nil {
				t.Fatalf("invalid network setup plan unexpectedly succeeded: %#v", plan)
			}
		})
	}

	t.Run("second singleton", func(t *testing.T) {
		databaseConnection := newNetworkSetupMigrationHarness(t)
		insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-one", "requires_approval", 1)
		insertNetworkSetupMigrationPlan(t, databaseConnection, defaultNetworkSetupMigrationPlan("operation-one", 1))
		if err := executeNetworkSetupMigrationPlan(databaseConnection, defaultNetworkSetupMigrationPlan("operation-one", 1)); err == nil {
			t.Fatal("second singleton network setup plan unexpectedly succeeded")
		}
	})

	t.Run("missing operation", func(t *testing.T) {
		databaseConnection := newNetworkSetupMigrationHarness(t)
		if err := executeNetworkSetupMigrationPlan(databaseConnection, defaultNetworkSetupMigrationPlan("operation-missing", 1)); err == nil {
			t.Fatal("network setup plan without an operation unexpectedly succeeded")
		}
	})

	t.Run("different operation revision", func(t *testing.T) {
		databaseConnection := newNetworkSetupMigrationHarness(t)
		insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-setup", "requires_approval", 1)
		if err := executeNetworkSetupMigrationPlan(databaseConnection, defaultNetworkSetupMigrationPlan("operation-setup", 2)); err == nil {
			t.Fatal("network setup plan bound to a different operation revision unexpectedly succeeded")
		}
	})
}

// TestNetworkSetupPlansMigrationSerializesActiveGlobalSetup verifies concurrent setup selection cannot reach durable approval independently.
func TestNetworkSetupPlansMigrationSerializesActiveGlobalSetup(t *testing.T) {
	databaseConnection := newNetworkSetupMigrationHarness(t)
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-first", "queued", 1)
	assertNetworkSetupOperationInsertFails(t, databaseConnection, "operation-racing", "running", 2)

	insertNetworkSetupMigrationOperationWithKind(t, databaseConnection, "operation-other", "host.cleanup", "running", 2)
	mustExecNetworkSetupMigration(t, databaseConnection, `UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?
		WHERE id = 'operation-first'`, networkSetupMigrationStartedAt(), networkSetupMigrationStartedAt().Add(time.Second))
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-next", "requires_approval", 3)
}

// TestNetworkSetupPlansMigrationLocksOperationRevision verifies authority must retire before its owner can advance.
func TestNetworkSetupPlansMigrationLocksOperationRevision(t *testing.T) {
	databaseConnection := newNetworkSetupMigrationHarness(t)
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-setup", "requires_approval", 1)
	insertNetworkSetupMigrationPlan(t, databaseConnection, defaultNetworkSetupMigrationPlan("operation-setup", 1))

	assertMigrationStatementFails(t, databaseConnection,
		"UPDATE operations SET revision = 2 WHERE id = 'operation-setup'")
	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM network_setup_plans WHERE id = 1")
	mustExecNetworkSetupMigration(t, databaseConnection,
		"UPDATE operations SET revision = 2 WHERE id = 'operation-setup'")

	insertNetworkSetupMigrationPlan(t, databaseConnection, defaultNetworkSetupMigrationPlan("operation-setup", 2))
	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-setup'")
	assertProjectionCount(t, databaseConnection, "network_setup_plans", 0)
}

// TestNetworkSetupPlansMigrationRollbackPreservesOperationJournal verifies reversal removes only the setup scaffold and its indexes.
func TestNetworkSetupPlansMigrationRollbackPreservesOperationJournal(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	migration := networkSetupPlansMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply network setup plans migration: %v", err)
	}
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-setup", "requires_approval", 1)
	insertNetworkSetupMigrationPlan(t, databaseConnection, defaultNetworkSetupMigrationPlan("operation-setup", 1))

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback network setup plans migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_setup_plans") {
		t.Fatal("rollback retained network_setup_plans")
	}
	for _, index := range []string{"operations_network_setup_revision_idx", "operations_one_active_network_setup_idx"} {
		if databaseConnection.Migrator().HasIndex("operations", index) {
			t.Fatalf("rollback retained operations index %s", index)
		}
	}
	assertProjectionCount(t, databaseConnection, "operations", 1)
}

// networkSetupMigrationPlan mirrors the exact insert surface used by valid and malformed schema probes.
type networkSetupMigrationPlan struct {
	ID                     int
	OperationID            string
	OperationRevision      int64
	OwnershipSchemaVersion int
	InstallationID         string
	OwnerIdentity          string
	OwnershipGeneration    int
	LoopbackPoolPrefix     string
	TicketVerifierKey      string
}

// newNetworkSetupMigrationHarness opens the operation journal and applies only the independent setup-plan scaffold.
func newNetworkSetupMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() {
		closeOperationMigrationDatabase(t, connections)
	})
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	if err := networkSetupPlansMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network setup plans migration: %v", err)
	}
	return databaseConnection
}

// networkSetupPlansMigration finds the embedded production migration by its stable identity.
func networkSetupPlansMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkSetupPlansMigrationName {
			return migration
		}
	}
	t.Fatalf("network setup plans migration %q is not registered", networkSetupPlansMigrationName)
	return nil
}

// defaultNetworkSetupMigrationPlan supplies one complete generation-one ownership record.
func defaultNetworkSetupMigrationPlan(operationID string, revision int64) networkSetupMigrationPlan {
	return networkSetupMigrationPlan{
		ID:                     1,
		OperationID:            operationID,
		OperationRevision:      revision,
		OwnershipSchemaVersion: 1,
		InstallationID:         "installation-a",
		OwnerIdentity:          "501",
		OwnershipGeneration:    1,
		LoopbackPoolPrefix:     "127.77.0.8/29",
		TicketVerifierKey:      networkSetupTestVerifierKey,
	}
}

// insertNetworkSetupMigrationPlan inserts one plan expected to satisfy every schema invariant.
func insertNetworkSetupMigrationPlan(t *testing.T, databaseConnection *gorm.DB, plan networkSetupMigrationPlan) {
	t.Helper()
	if err := executeNetworkSetupMigrationPlan(databaseConnection, plan); err != nil {
		t.Fatalf("insert network setup plan: %v", err)
	}
}

// executeNetworkSetupMigrationPlan keeps valid and invalid records on the same SQL insert path.
func executeNetworkSetupMigrationPlan(databaseConnection *gorm.DB, plan networkSetupMigrationPlan) error {
	return databaseConnection.Exec(`INSERT INTO network_setup_plans
		(id, operation_id, operation_revision, ownership_schema_version, installation_id,
		 owner_identity, ownership_generation, loopback_pool_prefix, ticket_verifier_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID,
		plan.OperationID,
		plan.OperationRevision,
		plan.OwnershipSchemaVersion,
		plan.InstallationID,
		plan.OwnerIdentity,
		plan.OwnershipGeneration,
		plan.LoopbackPoolPrefix,
		plan.TicketVerifierKey,
	).Error
}

// insertNetworkSetupMigrationOperation writes one global setup operation with a valid lifecycle shape.
func insertNetworkSetupMigrationOperation(t *testing.T, databaseConnection *gorm.DB, operationID string, state string, revision int) {
	t.Helper()
	insertNetworkSetupMigrationOperationWithKind(t, databaseConnection, operationID, "network.setup", state, revision)
}

// insertNetworkSetupMigrationOperationWithKind writes one operation without coupling the active-setup index test to unrelated kinds.
func insertNetworkSetupMigrationOperationWithKind(t *testing.T, databaseConnection *gorm.DB, operationID string, kind string, state string, revision int) {
	t.Helper()
	requestedAt := networkSetupMigrationRequestedAt()
	var startedAt *time.Time
	if state == "running" || state == "requires_approval" {
		started := networkSetupMigrationStartedAt()
		startedAt = &started
	}
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)`,
		operationID,
		"intent-"+operationID,
		kind,
		state,
		state,
		requestedAt,
		startedAt,
		revision,
	).Error; err != nil {
		t.Fatalf("insert %s operation %q: %v", kind, operationID, err)
	}
}

// assertNetworkSetupOperationInsertFails verifies a competing active setup cannot enter operations.
func assertNetworkSetupOperationInsertFails(t *testing.T, databaseConnection *gorm.DB, operationID string, state string, revision int) {
	t.Helper()
	requestedAt := networkSetupMigrationRequestedAt()
	startedAt := networkSetupMigrationStartedAt()
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES (?, ?, 'network.setup', NULL, ?, ?, ?, ?, ?)`,
		operationID, "intent-"+operationID, state, state, requestedAt, startedAt, revision)
}

// networkSetupMigrationRequestedAt returns the stable UTC request time shared by migration fixtures.
func networkSetupMigrationRequestedAt() time.Time {
	return time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC)
}

// networkSetupMigrationStartedAt returns the stable UTC activation time shared by active fixtures.
func networkSetupMigrationStartedAt() time.Time {
	return networkSetupMigrationRequestedAt().Add(time.Second)
}

// mustExecNetworkSetupMigration executes one schema probe that must succeed.
func mustExecNetworkSetupMigration(t *testing.T, databaseConnection *gorm.DB, statement string, arguments ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, arguments...).Error; err != nil {
		t.Fatalf("execute network setup migration statement: %v", err)
	}
}

// assertNetworkSetupOperationForeignKey proves the plan has only the exact composite operation owner.
func assertNetworkSetupOperationForeignKey(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var rows []struct {
		Table    string
		From     string
		To       string
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_list('network_setup_plans')").Scan(&rows).Error; err != nil {
		t.Fatalf("read network setup plan foreign keys: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("network setup plan foreign key rows = %#v, want exact operation ID and revision", rows)
	}
	want := map[string]string{"operation_id": "id", "operation_revision": "revision"}
	for _, row := range rows {
		if row.Table != "operations" || want[row.From] != row.To || row.OnUpdate != "RESTRICT" || row.OnDelete != "CASCADE" {
			t.Fatalf("network setup plan foreign key row = %#v, want exact operations owner", row)
		}
		delete(want, row.From)
	}
	if len(want) != 0 {
		t.Fatalf("network setup plan foreign key is missing mappings %v", want)
	}
}

// assertNetworkSetupForeignKeysClean verifies accepted rows leave no deferred ownership mismatch.
func assertNetworkSetupForeignKeysClean(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var violations []struct {
		Table  string
		RowID  int
		Parent string
		FKID   int
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("check network setup plan foreign keys: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("network setup plan foreign key violations = %#v", violations)
	}
}
