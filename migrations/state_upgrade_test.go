package migrations

import (
	"context"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/state"
)

// TestStateUpgradePreservesOperationJournal proves the projection migration upgrades and rolls back a live journal without losing its durable history.
func TestStateUpgradePreservesOperationJournal(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}

	appliedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	journalMigration := operationJournalMigration(t)
	if err := applySQLiteMigration(databaseConnection, journalMigration, migrationRecord{
		Name:       journalMigration.Name(),
		App:        journalMigration.App(),
		Connection: journalMigration.Connection(),
		SourcePath: journalMigration.SourcePath(),
		AppliedAt:  appliedAt,
	}); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	var appliedMigrationCount int64
	if err := databaseConnection.Table("migrations").Count(&appliedMigrationCount).Error; err != nil {
		t.Fatalf("count legacy migration records: %v", err)
	}
	if appliedMigrationCount != 1 || databaseConnection.Migrator().HasTable("projects") {
		t.Fatalf("legacy database has %d migration records and projects table present = %t, want one journal migration only", appliedMigrationCount, databaseConnection.Migrator().HasTable("projects"))
	}

	requestedAt := appliedAt
	if err := databaseConnection.Exec("UPDATE operation_journal_state SET sequence = 7 WHERE id = 1").Error; err != nil {
		t.Fatalf("advance legacy journal sequence: %v", err)
	}
	if err := insertMigrationOperation(
		databaseConnection,
		"operation-before-upgrade",
		"intent-before-upgrade",
		"queued",
		requestedAt,
		nil,
		nil,
		nil,
		7,
	); err != nil {
		t.Fatalf("insert legacy operation: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO operation_transitions
		(operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
		VALUES ('operation-before-upgrade', 1, NULL, 'queued', 'queued', ?, 7)`, requestedAt).Error; err != nil {
		t.Fatalf("insert legacy operation transition: %v", err)
	}

	projectionMigration := projectProjectionMigration(t)
	if err := applySQLiteMigration(databaseConnection, projectionMigration, migrationRecord{
		Name:       projectionMigration.Name(),
		App:        projectionMigration.App(),
		Connection: projectionMigration.Connection(),
		SourcePath: projectionMigration.SourcePath(),
		AppliedAt:  appliedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("apply project projection migration: %v", err)
	}

	journal := state.NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		state.NewMutationCoordinator(connections),
	)
	sequence, err := journal.CurrentSequence(context.Background())
	if err != nil {
		t.Fatalf("read upgraded journal sequence: %v", err)
	}
	if sequence != 7 {
		t.Fatalf("upgraded journal sequence = %d, want 7", sequence)
	}
	preserved, err := journal.Operation(context.Background(), "operation-before-upgrade")
	if err != nil {
		t.Fatalf("read preserved operation: %v", err)
	}
	if preserved.Revision != 7 || preserved.Operation.IntentID != "intent-before-upgrade" {
		t.Fatalf("preserved operation = %#v, want revision 7 and original intent", preserved)
	}
	history, err := journal.Transitions(context.Background(), preserved.Operation.ID)
	if err != nil {
		t.Fatalf("read preserved operation history: %v", err)
	}
	if len(history) != 1 || history[0].State != domain.OperationQueued || history[0].Sequence != 7 {
		t.Fatalf("preserved operation history = %#v, want queued transition at sequence 7", history)
	}

	operation, err := domain.NewOperation(
		"operation-after-upgrade",
		"intent-after-upgrade",
		"host.setup",
		"",
		requestedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("create post-upgrade operation: %v", err)
	}
	appended, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("append post-upgrade mutation: %v", err)
	}
	if appended.Revision != 8 {
		t.Fatalf("post-upgrade mutation revision = %d, want 8", appended.Revision)
	}
	sequence, err = journal.CurrentSequence(context.Background())
	if err != nil {
		t.Fatalf("read advanced upgraded sequence: %v", err)
	}
	if sequence != 8 {
		t.Fatalf("advanced upgraded sequence = %d, want 8", sequence)
	}

	if err := rollbackSQLiteMigration(databaseConnection, projectionMigration); err != nil {
		t.Fatalf("rollback project projection migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("harbor_state") {
		t.Fatal("rollback retained the current Harbor singleton table")
	}
	if !databaseConnection.Migrator().HasTable("operation_journal_state") {
		t.Fatal("rollback did not restore the legacy journal singleton table")
	}
	var journalMigrationCount int64
	if err := databaseConnection.Table("migrations").Where("name = ?", journalMigration.Name()).Count(&journalMigrationCount).Error; err != nil {
		t.Fatalf("count operation journal migration record: %v", err)
	}
	if journalMigrationCount != 1 {
		t.Fatal("rollback removed the earlier operation journal migration record")
	}
	var projectionMigrationCount int64
	if err := databaseConnection.Table("migrations").Where("name = ?", projectionMigration.Name()).Count(&projectionMigrationCount).Error; err != nil {
		t.Fatalf("count project projection migration record: %v", err)
	}
	if projectionMigrationCount != 0 {
		t.Fatal("rollback retained the project projection migration record")
	}

	// The generated HarborState repository models the latest schema, so rollback checks the historical singleton by its legacy table name.
	var legacySequence int
	if err := databaseConnection.Raw("SELECT sequence FROM operation_journal_state WHERE id = 1").Scan(&legacySequence).Error; err != nil {
		t.Fatalf("read rolled-back journal sequence: %v", err)
	}
	if legacySequence != 8 {
		t.Fatalf("rolled-back journal sequence = %d, want 8", legacySequence)
	}
	var preservedRevision int
	if err := databaseConnection.Raw("SELECT revision FROM operations WHERE id = 'operation-before-upgrade'").Scan(&preservedRevision).Error; err != nil {
		t.Fatalf("read rolled-back legacy operation: %v", err)
	}
	if preservedRevision != 7 {
		t.Fatalf("rolled-back legacy operation revision = %d, want 7", preservedRevision)
	}
}
