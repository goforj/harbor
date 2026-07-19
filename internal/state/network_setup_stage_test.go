package state

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const networkSetupStageVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// networkSetupStageFixture owns one production-schema journal that can be reopened over the same database path.
type networkSetupStageFixture struct {
	connections *database.Connections
	database    *gorm.DB
	journal     *OperationJournal
}

// TestOperationJournalStagesNetworkSetup proves the operation history and singleton plan share the final approval revision.
func TestOperationJournalStagesNetworkSetup(t *testing.T) {
	fixture := newNetworkSetupStageFixture(t)
	request := networkSetupStageRequest(t, "operation-network-setup", "intent-network-setup")

	staged, err := fixture.journal.StageNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("StageNetworkSetup() error = %v", err)
	}
	if staged.Revision != 3 || staged.Operation.State != domain.OperationRequiresApproval ||
		staged.Operation.Phase != networkSetupApprovalPhase || staged.Operation.StartedAt == nil ||
		!staged.Operation.StartedAt.Equal(request.Operation.RequestedAt) {
		t.Fatalf("StageNetworkSetup() = %#v, want revision-three approval operation", staged)
	}

	history, err := fixture.journal.Transitions(context.Background(), request.Operation.ID)
	if err != nil {
		t.Fatalf("Transitions() error = %v", err)
	}
	wantStates := []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
	}
	wantPhases := []string{request.Operation.Phase, networkSetupRunningPhase, networkSetupApprovalPhase}
	if len(history) != len(wantStates) {
		t.Fatalf("transition count = %d, want %d", len(history), len(wantStates))
	}
	for index := range history {
		if history[index].State != wantStates[index] || history[index].Phase != wantPhases[index] ||
			history[index].Sequence != domain.Sequence(index+1) ||
			!history[index].OccurredAt.Equal(request.Operation.RequestedAt) {
			t.Fatalf("transition %d = %#v", index+1, history[index])
		}
	}

	var plan models.NetworkSetupPlan
	if err := fixture.database.First(&plan, 1).Error; err != nil {
		t.Fatalf("read staged network setup plan: %v", err)
	}
	if plan.OperationId != string(staged.Operation.ID) || plan.OperationRevision != int(staged.Revision) ||
		plan.OwnershipSchemaVersion != int(request.Ownership.SchemaVersion) ||
		plan.InstallationId != request.Ownership.InstallationID ||
		plan.OwnerIdentity != request.Ownership.OwnerIdentity ||
		plan.OwnershipGeneration != int(request.Ownership.Generation) ||
		plan.LoopbackPoolPrefix != request.Ownership.LoopbackPoolPrefix ||
		plan.TicketVerifierKey != request.Ownership.TicketVerifierKey {
		t.Fatalf("staged network setup plan = %#v, want exact revision-bound ownership", plan)
	}
	assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
}

// TestOperationJournalReplaysExactNetworkSetupAfterRestart proves retries do not consume sequence or duplicate authority.
func TestOperationJournalReplaysExactNetworkSetupAfterRestart(t *testing.T) {
	fixture := newNetworkSetupStageFixture(t)
	request := networkSetupStageRequest(t, "operation-network-replay", "intent-network-replay")
	first, err := fixture.journal.StageNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("first StageNetworkSetup() error = %v", err)
	}
	fixture.restart(t)
	proposal := networkSetupStageRequest(t, "operation-network-proposed", request.Operation.IntentID)
	proposal.Operation.RequestedAt = proposal.Operation.RequestedAt.Add(time.Hour)
	proposal.Ownership.InstallationID = "installation-proposed"
	proposal.Ownership.OwnerIdentity = "502"
	proposal.Ownership.LoopbackPoolPrefix = "127.89.0.16/29"

	replayed, err := fixture.journal.StageNetworkSetup(context.Background(), proposal)
	if err != nil {
		t.Fatalf("replayed StageNetworkSetup() error = %v", err)
	}
	if replayed.Revision != first.Revision || replayed.Operation.ID != first.Operation.ID ||
		replayed.Operation.IntentID != first.Operation.IntentID ||
		replayed.Operation.State != first.Operation.State || replayed.Operation.Phase != first.Operation.Phase ||
		!replayed.Operation.RequestedAt.Equal(first.Operation.RequestedAt) || replayed.Operation.StartedAt == nil ||
		first.Operation.StartedAt == nil || !replayed.Operation.StartedAt.Equal(*first.Operation.StartedAt) {
		t.Fatalf("replayed StageNetworkSetup() = %#v, want %#v", replayed, first)
	}
	var plan models.NetworkSetupPlan
	if err := fixture.database.First(&plan, 1).Error; err != nil {
		t.Fatalf("read replayed network setup plan: %v", err)
	}
	if plan.OperationId != string(request.Operation.ID) ||
		plan.InstallationId != request.Ownership.InstallationID ||
		plan.OwnerIdentity != request.Ownership.OwnerIdentity ||
		plan.LoopbackPoolPrefix != request.Ownership.LoopbackPoolPrefix {
		t.Fatalf("replayed plan = %#v, want original durable ownership", plan)
	}
	assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
}

// TestOperationJournalRejectsNetworkSetupConflicts proves only the complete original operation and plan can replay.
func TestOperationJournalRejectsNetworkSetupConflicts(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		fixture := newNetworkSetupStageFixture(t)
		request := networkSetupStageRequest(t, "operation-requested", "intent-shared")
		existing := networkSetupStageOperation(t, "operation-existing", "intent-shared", domain.OperationKindProjectStart, "project-alpha")
		if _, err := fixture.journal.Enqueue(context.Background(), existing); err != nil {
			t.Fatalf("seed intent owner: %v", err)
		}

		_, err := fixture.journal.StageNetworkSetup(context.Background(), request)
		var conflict *IntentConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("intent conflict error = %v, want IntentConflictError", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 1, 1, 1, 0)
	})

	t.Run("operation ID", func(t *testing.T) {
		fixture := newNetworkSetupStageFixture(t)
		request := networkSetupStageRequest(t, "operation-shared", "intent-requested")
		existing := networkSetupStageOperation(t, "operation-shared", "intent-existing", domain.OperationKindProjectStart, "project-alpha")
		if _, err := fixture.journal.Enqueue(context.Background(), existing); err != nil {
			t.Fatalf("seed operation owner: %v", err)
		}

		_, err := fixture.journal.StageNetworkSetup(context.Background(), request)
		var conflict *OperationIDConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("operation conflict error = %v, want OperationIDConflictError", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 1, 1, 1, 0)
	})

	t.Run("foreign operation", func(t *testing.T) {
		fixture := newNetworkSetupStageFixture(t)
		request := networkSetupStageRequest(t, "operation-requested", "intent-requested")
		existing := networkSetupStageOperation(t, "operation-existing", "intent-existing", domain.OperationKindNetworkSetup, "")
		if _, err := fixture.journal.Enqueue(context.Background(), existing); err != nil {
			t.Fatalf("seed active setup operation: %v", err)
		}

		_, err := fixture.journal.StageNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("foreign operation error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 1, 1, 1, 0)
	})

	t.Run("foreign plan", func(t *testing.T) {
		fixture := newNetworkSetupStageFixture(t)
		foreign := networkSetupStageRequest(t, "operation-foreign", "intent-foreign")
		if _, err := fixture.journal.StageNetworkSetup(context.Background(), foreign); err != nil {
			t.Fatalf("stage foreign plan: %v", err)
		}
		request := networkSetupStageRequest(t, "operation-requested", "intent-requested")

		_, err := fixture.journal.StageNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "plan already belongs") {
			t.Fatalf("foreign plan error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("network state", func(t *testing.T) {
		fixture := newNetworkSetupStageFixture(t)
		at := networkSetupStageTime()
		networkSetupStageExec(t, fixture.database, `INSERT INTO network_state
			(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length,
			dns_suffix, created_at, updated_at, revision)
			VALUES (1, 'identity', 'installation-existing', 1, '127.88.0.8', 29, '.test', ?, ?, 1)`, at, at)

		_, err := fixture.journal.StageNetworkSetup(
			context.Background(),
			networkSetupStageRequest(t, "operation-requested", "intent-requested"),
		)
		if err == nil || !strings.Contains(err.Error(), "network state already exists") {
			t.Fatalf("network-state conflict error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 0, 0, 0, 0)
	})

}

// TestOperationJournalNetworkSetupStageRollsBackPlanFailure proves a late insert failure restores every sequence and history write.
func TestOperationJournalNetworkSetupStageRollsBackPlanFailure(t *testing.T) {
	fixture := newNetworkSetupStageFixture(t)
	cause := errors.New("network setup plan insert failed")
	callback := "harbor:test_network_setup_stage_plan_failure"
	if err := fixture.database.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "network_setup_plans" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatalf("register plan failure callback: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Create().Remove(callback) })

	_, err := fixture.journal.StageNetworkSetup(
		context.Background(),
		networkSetupStageRequest(t, "operation-network-rollback", "intent-network-rollback"),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("StageNetworkSetup() error = %v, want sentinel", err)
	}
	assertNetworkSetupStageDurableState(t, fixture, 0, 0, 0, 0)
}

// TestStageNetworkSetupRequestValidationAndCancellation covers strict admission before writer authority is consumed.
func TestStageNetworkSetupRequestValidationAndCancellation(t *testing.T) {
	valid := networkSetupStageRequest(t, "operation-network-validation", "intent-network-validation")
	tests := []struct {
		name   string
		mutate func(*StageNetworkSetupRequest)
		want   string
	}{
		{name: "operation", mutate: func(request *StageNetworkSetupRequest) {
			request.Operation = domain.Operation{}
		}, want: "operation ID"},
		{name: "kind", mutate: func(request *StageNetworkSetupRequest) {
			request.Operation.Kind = "host.setup"
		}, want: "kind must be"},
		{name: "project", mutate: func(request *StageNetworkSetupRequest) {
			request.Operation.ProjectID = "project-alpha"
		}, want: "must not identify a project"},
		{name: "state", mutate: func(request *StageNetworkSetupRequest) {
			at := request.Operation.RequestedAt
			request.Operation.State = domain.OperationRunning
			request.Operation.Phase = "running"
			request.Operation.StartedAt = &at
		}, want: "must be queued"},
		{name: "phase", mutate: func(request *StageNetworkSetupRequest) {
			request.Operation.Phase = "waiting"
		}, want: "queued phase"},
		{name: "schema", mutate: func(request *StageNetworkSetupRequest) {
			request.Ownership.SchemaVersion++
		}, want: "schema version"},
		{name: "generation", mutate: func(request *StageNetworkSetupRequest) {
			request.Ownership.Generation++
		}, want: "generation"},
		{name: "incomplete ownership", mutate: func(request *StageNetworkSetupRequest) {
			request.Ownership.InstallationID = ""
		}, want: "installation ID"},
		{name: "pool width", mutate: func(request *StageNetworkSetupRequest) {
			request.Ownership.LoopbackPoolPrefix = "127.88.0.0/24"
		}, want: "IPv4-loopback /29"},
		{name: "pool alignment", mutate: func(request *StageNetworkSetupRequest) {
			request.Ownership.LoopbackPoolPrefix = "127.88.0.9/29"
		}, want: "not canonical"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}

	fixture := newNetworkSetupStageFixture(t)
	invalid := valid
	invalid.Ownership.LoopbackPoolPrefix = "127.88.0.0/24"
	if _, err := fixture.journal.StageNetworkSetup(context.Background(), invalid); err == nil {
		t.Fatal("StageNetworkSetup() accepted an invalid ownership pool")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.journal.StageNetworkSetup(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled StageNetworkSetup() error = %v, want context.Canceled", err)
	}
	assertNetworkSetupStageDurableState(t, fixture, 0, 0, 0, 0)
}

// newNetworkSetupStageFixture applies production migrations and constructs the journal over one named SQLite writer.
func newNetworkSetupStageFixture(t *testing.T) *networkSetupStageFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")
	if _, err := ConfigureDatabase(); err != nil {
		t.Fatalf("configure network setup stage database: %v", err)
	}

	fixture := &networkSetupStageFixture{}
	fixture.connections = database.NewConnections(inspects.NewManager())
	databaseConnection, err := fixture.connections.GetHarbord()
	if err != nil {
		t.Fatalf("open network setup stage database: %v", err)
	}
	applyNetworkSetupPlanSourceMigrations(t, databaseConnection)
	fixture.database = databaseConnection
	fixture.journal = newNetworkSetupStageJournal(fixture.connections)
	t.Cleanup(func() {
		if err := fixture.connections.Close(context.Background()); err != nil {
			t.Errorf("close network setup stage database: %v", err)
		}
	})
	return fixture
}

// restart closes every process-local handle and reconstructs the journal over the same durable database.
func (fixture *networkSetupStageFixture) restart(t *testing.T) {
	t.Helper()
	if err := fixture.connections.Close(context.Background()); err != nil {
		t.Fatalf("close network setup stage database for restart: %v", err)
	}
	fixture.connections = database.NewConnections(inspects.NewManager())
	databaseConnection, err := fixture.connections.GetHarbord()
	if err != nil {
		t.Fatalf("reopen network setup stage database: %v", err)
	}
	fixture.database = databaseConnection
	fixture.journal = newNetworkSetupStageJournal(fixture.connections)
}

// newNetworkSetupStageJournal wires the existing generated journal repositories to one coordinator.
func newNetworkSetupStageJournal(connections *database.Connections) *OperationJournal {
	return NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		NewMutationCoordinator(connections),
	)
}

// networkSetupStageRequest creates one valid queued setup operation with complete generation-one ownership.
func networkSetupStageRequest(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
) StageNetworkSetupRequest {
	t.Helper()
	return StageNetworkSetupRequest{
		Operation: networkSetupStageOperation(
			t,
			operationID,
			intentID,
			domain.OperationKindNetworkSetup,
			"",
		),
		Ownership: ownership.Record{
			SchemaVersion:      ownership.CurrentSchemaVersion,
			InstallationID:     "installation-stage",
			OwnerIdentity:      "501",
			Generation:         1,
			LoopbackPoolPrefix: "127.88.0.8/29",
			TicketVerifierKey:  networkSetupStageVerifierKey,
		},
	}
}

// networkSetupStageOperation creates one valid queued operation for conflict and staging fixtures.
func networkSetupStageOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	kind domain.OperationKind,
	projectID domain.ProjectID,
) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(operationID, intentID, kind, projectID, networkSetupStageTime())
	if err != nil {
		t.Fatalf("create network setup stage operation: %v", err)
	}
	return operation
}

// networkSetupStageTime returns the stable UTC instant shared by every deterministic transition.
func networkSetupStageTime() time.Time {
	return time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
}

// networkSetupStageExec executes one fixture mutation or fails its test immediately.
func networkSetupStageExec(t *testing.T, databaseConnection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute network setup stage fixture mutation: %v", err)
	}
}

// assertNetworkSetupStageDurableState checks the high-water and every table owned by staging.
func assertNetworkSetupStageDurableState(
	t *testing.T,
	fixture *networkSetupStageFixture,
	wantSequence int64,
	wantOperations int64,
	wantTransitions int64,
	wantPlans int64,
) {
	t.Helper()
	var sequence int64
	if err := fixture.database.Raw("SELECT sequence FROM harbor_state WHERE id = 1").Scan(&sequence).Error; err != nil {
		t.Fatalf("read Harbor sequence: %v", err)
	}
	if sequence != wantSequence {
		t.Fatalf("Harbor sequence = %d, want %d", sequence, wantSequence)
	}
	for table, want := range map[string]int64{
		"operations":            wantOperations,
		"operation_transitions": wantTransitions,
		"network_setup_plans":   wantPlans,
	} {
		var count int64
		if err := fixture.database.Table(table).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	}
}
