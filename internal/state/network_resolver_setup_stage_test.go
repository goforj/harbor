package state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// networkResolverSetupFixture owns one identity-stage network and its confirmed schema-one machine projection.
type networkResolverSetupFixture struct {
	store    *Store
	database *gorm.DB
	journal  *OperationJournal
	source   *NetworkResolverSetupPlanSource
	request  StageNetworkResolverSetupRequest
}

// TestStageNetworkResolverSetupRequestRejectsIncompleteAuthority covers every independent admission boundary.
func TestStageNetworkResolverSetupRequestRejectsIncompleteAuthority(t *testing.T) {
	fixture := newNetworkResolverSetupFixture(t)
	tests := []struct {
		name   string
		want   string
		mutate func(*StageNetworkResolverSetupRequest)
	}{
		{name: "operation", want: "operation ID", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Operation.ID = ""
		}},
		{name: "kind", want: "operation kind", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Operation.Kind = domain.OperationKindNetworkSetup
		}},
		{name: "project", want: "must not identify a project", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Operation.ProjectID = "project-alpha"
		}},
		{name: "state", want: "must contain a start time", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Operation.State = domain.OperationRunning
		}},
		{name: "phase", want: "queued phase", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Operation.Phase = "waiting"
		}},
		{name: "network revision", want: "must be positive", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.ExpectedNetworkRevision = 0
		}},
		{name: "target schema", want: "target ownership schema", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.TargetOwnership.SchemaVersion = ownership.IdentitySchemaVersion
		}},
		{name: "target", want: "target ownership", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.TargetOwnership.TicketVerifierKey = ""
		}},
		{name: "policy", want: "network resolver setup policy", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.Policy.Suffix = ".invalid"
		}},
		{name: "policy fingerprint", want: "does not match target", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.TargetOwnership.NetworkPolicyFingerprint = strings.Repeat("d", 64)
		}},
		{name: "source fingerprint", want: "source ownership fingerprint", mutate: func(request *StageNetworkResolverSetupRequest) {
			request.ExpectedSourceOwnershipFingerprint = strings.Repeat("e", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StageNetworkResolverSetupRequest.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.journal.StageNetworkResolverSetup(ctx, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled StageNetworkResolverSetup() error = %v, want context.Canceled", err)
	}
	assertNetworkResolverSetupCounts(t, fixture, 1, 0, 0, 0)
}

// TestOperationJournalStagesAndReplaysExactNetworkResolverSetup proves all authority shares one approval revision.
func TestOperationJournalStagesAndReplaysExactNetworkResolverSetup(t *testing.T) {
	fixture := newNetworkResolverSetupFixture(t)
	staged, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("StageNetworkResolverSetup() error = %v", err)
	}
	if staged.Revision != 4 || staged.Operation.State != domain.OperationRequiresApproval ||
		staged.Operation.Phase != networkResolverSetupApprovalPhase || staged.Operation.StartedAt == nil ||
		!staged.Operation.StartedAt.Equal(fixture.request.Operation.RequestedAt) {
		t.Fatalf("StageNetworkResolverSetup() = %#v, want revision-four approval", staged)
	}
	history, err := fixture.journal.Transitions(context.Background(), staged.Operation.ID)
	if err != nil {
		t.Fatalf("Transitions() error = %v", err)
	}
	wantStates := []domain.OperationState{domain.OperationQueued, domain.OperationRunning, domain.OperationRequiresApproval}
	wantPhases := []string{
		networkResolverSetupQueuedPhase,
		networkResolverSetupRunningPhase,
		networkResolverSetupApprovalPhase,
	}
	for index := range history {
		if history[index].State != wantStates[index] || history[index].Phase != wantPhases[index] ||
			history[index].Sequence != domain.Sequence(index+2) ||
			!history[index].OccurredAt.Equal(fixture.request.Operation.RequestedAt) {
			t.Fatalf("transition %d = %#v", index+1, history[index])
		}
	}
	var row models.NetworkResolverSetupPlan
	if err := fixture.database.First(&row, networkResolverSetupPlanSingletonID).Error; err != nil {
		t.Fatalf("read staged resolver plan: %v", err)
	}
	plan, networkRevision, err := networkResolverSetupPlanFromModel(row, staged)
	if err != nil {
		t.Fatalf("networkResolverSetupPlanFromModel() error = %v", err)
	}
	if networkRevision != fixture.request.ExpectedNetworkRevision ||
		plan.ExpectedSourceOwnershipFingerprint != fixture.request.ExpectedSourceOwnershipFingerprint ||
		plan.TargetOwnership != fixture.request.TargetOwnership || plan.Policy != fixture.request.Policy {
		t.Fatalf("staged plan = %#v at network revision %d", plan, networkRevision)
	}

	fixture.restart()
	replayed, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("replayed StageNetworkResolverSetup() error = %v", err)
	}
	if !reflect.DeepEqual(replayed, staged) {
		t.Fatalf("replayed operation = %#v, want %#v", replayed, staged)
	}
	assertNetworkResolverSetupCounts(t, fixture, 4, 1, 3, 1)
}

// TestNetworkResolverSetupPlanSourceResolvesAllAuthorityInOneTransaction proves restart reads are ordered and atomic.
func TestNetworkResolverSetupPlanSourceResolvesAllAuthorityInOneTransaction(t *testing.T) {
	fixture := newNetworkResolverSetupFixture(t)
	staged, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("StageNetworkResolverSetup() error = %v", err)
	}
	fixture.restart()

	tables := []string{"operations", "network_resolver_setup_plans", "network_state", "machine_ownership_projections"}
	readOrder := make([]string, 0, len(tables)+1)
	outsideTransaction := make(map[string]bool, len(tables))
	callback := "harbor:test_network_resolver_setup_source_transaction"
	if err := fixture.database.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if !slices.Contains(tables, tx.Statement.Table) {
			return
		}
		readOrder = append(readOrder, tx.Statement.Table)
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			outsideTransaction[tx.Statement.Table] = true
		}
	}); err != nil {
		t.Fatalf("register resolver source transaction observer: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Query().Remove(callback) })

	plan, err := fixture.source.Resolve(
		context.Background(),
		ticketissuer.ResolverRequest{OperationID: staged.Operation.ID},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Resolve().Validate() error = %v", err)
	}
	if plan.Purpose != ticketissuer.ResolverPlanPurposeSetup || !reflect.DeepEqual(plan.Operation, staged.Operation) ||
		plan.OperationRevision != staged.Revision || plan.CheckpointRevision != 0 ||
		plan.CheckpointPhase != ticketissuer.ResolverCheckpointPhaseSetupApproval ||
		plan.ExpectedSourceOwnershipFingerprint != fixture.request.ExpectedSourceOwnershipFingerprint ||
		plan.TargetOwnership != fixture.request.TargetOwnership || plan.Policy != fixture.request.Policy {
		t.Fatalf("Resolve() = %#v, want exact durable resolver authority", plan)
	}
	previous := -1
	for _, table := range tables {
		index := slices.Index(readOrder, table)
		if index < 0 || index <= previous {
			t.Fatalf("Resolve() authority read order = %#v, missing ordered %q", readOrder, table)
		}
		if outsideTransaction[table] {
			t.Fatalf("Resolve() read %s outside its transaction", table)
		}
		previous = index
	}
}

// TestOperationJournalRejectsNonExactNetworkResolverSetupReplay keeps one intent from selecting different authority.
func TestOperationJournalRejectsNonExactNetworkResolverSetupReplay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *StageNetworkResolverSetupRequest)
	}{
		{name: "operation ID", mutate: func(_ *testing.T, request *StageNetworkResolverSetupRequest) {
			request.Operation.ID = "operation-resolver-proposed"
		}},
		{name: "request time", mutate: func(_ *testing.T, request *StageNetworkResolverSetupRequest) {
			request.Operation.RequestedAt = request.Operation.RequestedAt.Add(time.Minute)
		}},
		{name: "network revision", mutate: func(_ *testing.T, request *StageNetworkResolverSetupRequest) {
			request.ExpectedNetworkRevision++
		}},
		{name: "target identity", mutate: func(t *testing.T, request *StageNetworkResolverSetupRequest) {
			request.TargetOwnership.OwnerIdentity = "502"
			setNetworkResolverSetupSourceFingerprint(t, request)
		}},
		{name: "policy", mutate: func(t *testing.T, request *StageNetworkResolverSetupRequest) {
			request.Policy = networkResolverSetupMacOSPolicy(t, request.Policy.AuthorityFingerprint)
			fingerprint, err := request.Policy.Fingerprint()
			if err != nil {
				t.Fatalf("fingerprint alternate policy: %v", err)
			}
			request.TargetOwnership.NetworkPolicyFingerprint = fingerprint
			setNetworkResolverSetupSourceFingerprint(t, request)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupFixture(t)
			if _, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request); err != nil {
				t.Fatalf("initial StageNetworkResolverSetup() error = %v", err)
			}
			candidate := fixture.request
			test.mutate(t, &candidate)
			if _, err := fixture.journal.StageNetworkResolverSetup(context.Background(), candidate); err == nil {
				t.Fatal("non-exact resolver setup replay unexpectedly succeeded")
			}
			assertNetworkResolverSetupCounts(t, fixture, 4, 1, 3, 1)
		})
	}
}

// TestOperationJournalRejectsNetworkResolverSetupAuthorityConflicts covers root, projection, ID, and active-owner conflicts.
func TestOperationJournalRejectsNetworkResolverSetupAuthorityConflicts(t *testing.T) {
	t.Run("operation ID", func(t *testing.T) {
		fixture := newNetworkResolverSetupFixture(t)
		foreign := fixture.request.Operation
		foreign.IntentID = "intent-resolver-foreign"
		if _, err := fixture.journal.Enqueue(context.Background(), foreign); err != nil {
			t.Fatalf("seed operation ID owner: %v", err)
		}
		_, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
		var conflict *OperationIDConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("operation ID conflict error = %v", err)
		}
	})

	t.Run("active operation", func(t *testing.T) {
		fixture := newNetworkResolverSetupFixture(t)
		active := networkResolverSetupOperation(t, "operation-resolver-active", "intent-resolver-active")
		if _, err := fixture.journal.Enqueue(context.Background(), active); err != nil {
			t.Fatalf("seed active resolver operation: %v", err)
		}
		_, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("active resolver conflict error = %v", err)
		}
	})

	t.Run("network revision", func(t *testing.T) {
		fixture := newNetworkResolverSetupFixture(t)
		request := fixture.request
		request.ExpectedNetworkRevision++
		_, err := fixture.journal.StageNetworkResolverSetup(context.Background(), request)
		var conflict *NetworkRevisionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("network revision conflict error = %v", err)
		}
	})

	t.Run("machine projection", func(t *testing.T) {
		fixture := newNetworkResolverSetupFixture(t)
		request := fixture.request
		request.TargetOwnership.OwnerIdentity = "502"
		setNetworkResolverSetupSourceFingerprint(t, &request)
		_, err := fixture.journal.StageNetworkResolverSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "confirmed machine projection") {
			t.Fatalf("projection conflict error = %v", err)
		}
	})
}

// TestOperationJournalNetworkResolverSetupRollsBackLatePlanFailure proves no sequence or lifecycle escapes a failed insert.
func TestOperationJournalNetworkResolverSetupRollsBackLatePlanFailure(t *testing.T) {
	fixture := newNetworkResolverSetupFixture(t)
	cause := errors.New("resolver setup plan insert failed")
	callback := "harbor:test_network_resolver_setup_plan_failure"
	if err := fixture.database.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "network_resolver_setup_plans" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatalf("register plan failure callback: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Create().Remove(callback) })

	_, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
	if !errors.Is(err, cause) {
		t.Fatalf("StageNetworkResolverSetup() error = %v, want sentinel", err)
	}
	assertNetworkResolverSetupCounts(t, fixture, 1, 0, 0, 0)
}

// TestNetworkResolverSetupPlanSourceRejectsDurableDrift covers operation, plan, root, and projection rereads.
func TestNetworkResolverSetupPlanSourceRejectsDurableDrift(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *networkResolverSetupFixture)
	}{
		{name: "approval phase", want: "operation phase", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			networkResolverSetupExec(t, fixture.database, "UPDATE operations SET phase = 'other' WHERE id = ?", fixture.request.Operation.ID)
		}},
		{name: "plan revision", want: "plan revision", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			weakenNetworkResolverSetupPlanSchema(t, fixture.database)
			networkResolverSetupExec(t, fixture.database, "UPDATE network_resolver_setup_plans SET operation_revision = operation_revision - 1")
		}},
		{name: "source fingerprint", want: "source ownership fingerprint", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			weakenNetworkResolverSetupPlanSchema(t, fixture.database)
			networkResolverSetupExec(t, fixture.database, "UPDATE network_resolver_setup_plans SET source_ownership_fingerprint = ?", strings.Repeat("a", 64))
		}},
		{name: "policy socket", want: "resolver setup policy", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			weakenNetworkResolverSetupPlanSchema(t, fixture.database)
			networkResolverSetupExec(t, fixture.database, "UPDATE network_resolver_setup_plans SET policy_http_bind_address = '127.0.0.2'")
		}},
		{name: "network stage", want: "network stage", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			networkResolverSetupExec(t, fixture.database, "UPDATE network_state SET stage = 'resolver'")
		}},
		{name: "network revision", want: "network revision", mutate: func(t *testing.T, fixture *networkResolverSetupFixture) {
			weakenNetworkResolverSetupPlanSchema(t, fixture.database)
			networkResolverSetupExec(t, fixture.database, "UPDATE network_state SET revision = revision + 1")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupFixture(t)
			if _, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request); err != nil {
				t.Fatalf("StageNetworkResolverSetup() error = %v", err)
			}
			test.mutate(t, fixture)
			_, err := fixture.source.Resolve(
				context.Background(),
				ticketissuer.ResolverRequest{OperationID: fixture.request.Operation.ID},
			)
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || corrupt.Entity != "network resolver setup plan" ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want resolver-plan corruption containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkResolverSetupPlanSourceAdmission covers request and context boundaries.
func TestNetworkResolverSetupPlanSourceAdmission(t *testing.T) {
	fixture := newNetworkResolverSetupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.source.Resolve(ctx, ticketissuer.ResolverRequest{OperationID: fixture.request.Operation.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Resolve() error = %v, want context.Canceled", err)
	}
	if _, err := fixture.source.Resolve(context.Background(), ticketissuer.ResolverRequest{}); err == nil {
		t.Fatal("Resolve() accepted an empty operation ID")
	}
}

// newNetworkResolverSetupFixture constructs resolver staging over the complete production schema.
func newNetworkResolverSetupFixture(t *testing.T) *networkResolverSetupFixture {
	t.Helper()
	store, databaseConnection := newNetworkInitializeTestHarness(t, false)
	_, identityResult := initializeNetworkDataPlaneActivationIdentity(t, store, databaseConnection)
	connections := store.mutations.connections
	fixture := &networkResolverSetupFixture{
		store:    store,
		database: databaseConnection,
		journal:  newNetworkSetupStageJournal(connections),
		source: NewNetworkResolverSetupPlanSource(
			models.NewNetworkResolverSetupPlanRepo(connections),
		),
	}
	policy := networkDataPlaneActivationTestPolicy(t)
	target := networkDataPlaneActivationTestOwnership(t, policy).Record
	operation := networkResolverSetupOperation(t, "operation-resolver-setup", "intent-resolver-setup")
	fixture.request = StageNetworkResolverSetupRequest{
		Operation:               operation,
		ExpectedNetworkRevision: identityResult.Record.Revision,
		TargetOwnership:         target,
		Policy:                  policy,
	}
	setNetworkResolverSetupSourceFingerprint(t, &fixture.request)
	return fixture
}

// restart reconstructs every resolver staging collaborator without changing durable state.
func (fixture *networkResolverSetupFixture) restart() {
	connections := fixture.store.mutations.connections
	fixture.journal = newNetworkSetupStageJournal(connections)
	fixture.source = NewNetworkResolverSetupPlanSource(models.NewNetworkResolverSetupPlanRepo(connections))
}

// networkResolverSetupOperation creates one valid queued global resolver operation.
func networkResolverSetupOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(
		operationID,
		intentID,
		domain.OperationKindNetworkResolverSetup,
		"",
		networkResolverSetupTime(),
	)
	if err != nil {
		t.Fatalf("create resolver setup operation: %v", err)
	}
	return operation
}

// networkResolverSetupTime returns the stable staging time used by resolver fixtures.
func networkResolverSetupTime() time.Time {
	return time.Date(2026, time.July, 20, 2, 0, 0, 0, time.UTC)
}

// setNetworkResolverSetupSourceFingerprint derives the only schema-one source permitted by the target.
func setNetworkResolverSetupSourceFingerprint(t *testing.T, request *StageNetworkResolverSetupRequest) {
	t.Helper()
	_, fingerprint, err := resolverSetupSourceOwnership(request.TargetOwnership)
	if err != nil {
		t.Fatalf("derive resolver setup source ownership: %v", err)
	}
	request.ExpectedSourceOwnershipFingerprint = fingerprint
}

// networkResolverSetupMacOSPolicy returns a distinct valid redirected profile for replay checks.
func networkResolverSetupMacOSPolicy(t *testing.T, authorityFingerprint string) networkpolicy.Policy {
	t.Helper()
	policy, err := networkpolicy.New(
		authorityFingerprint,
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{
			Advertised: networkDataPlaneActivationTestSocket("127.0.0.1:1054"),
			Bind:       networkDataPlaneActivationTestSocket("127.0.0.1:1054"),
		},
		networkpolicy.Listener{
			Advertised: networkDataPlaneActivationTestSocket("127.0.0.1:80"),
			Bind:       networkDataPlaneActivationTestSocket("127.0.0.1:18081"),
		},
		networkpolicy.Listener{
			Advertised: networkDataPlaneActivationTestSocket("127.0.0.1:443"),
			Bind:       networkDataPlaneActivationTestSocket("127.0.0.1:18444"),
		},
	)
	if err != nil {
		t.Fatalf("construct macOS resolver policy: %v", err)
	}
	return policy
}

// assertNetworkResolverSetupCounts checks the global sequence and every table owned by resolver staging.
func assertNetworkResolverSetupCounts(
	t *testing.T,
	fixture *networkResolverSetupFixture,
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
		"operations":                   wantOperations,
		"operation_transitions":        wantTransitions,
		"network_resolver_setup_plans": wantPlans,
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

// weakenNetworkResolverSetupPlanSchema permits focused read-time corruption probes after valid staging.
func weakenNetworkResolverSetupPlanSchema(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"PRAGMA foreign_keys = OFF",
		"ALTER TABLE network_resolver_setup_plans RENAME TO network_resolver_setup_plans_guarded",
		"CREATE TABLE network_resolver_setup_plans AS SELECT * FROM network_resolver_setup_plans_guarded",
		"DROP TABLE network_resolver_setup_plans_guarded",
		"PRAGMA foreign_keys = ON",
	} {
		networkResolverSetupExec(t, databaseConnection, statement)
	}
}

// networkResolverSetupExec executes one durable fixture mutation without hiding setup failures.
func networkResolverSetupExec(t *testing.T, databaseConnection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute network resolver setup fixture mutation: %v", err)
	}
}
