package state

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const networkDataPlaneSetupStageMigrationName = "2026_07_22_010000_limit_active_network_dataplane_setup"

// networkDataPlaneSetupStageFixture owns one resolver predecessor and its journal staging request.
type networkDataPlaneSetupStageFixture struct {
	state    *networkDataPlaneSetupProjectionFixture
	journal  *OperationJournal
	request  StageNetworkDataPlaneSetupRequest
	database *gorm.DB
}

// TestStageNetworkDataPlaneSetupRequestRejectsIncompleteAuthority covers every caller-controlled admission branch.
func TestStageNetworkDataPlaneSetupRequestRejectsIncompleteAuthority(t *testing.T) {
	fixture := newNetworkDataPlaneSetupStageFixture(t)
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *StageNetworkDataPlaneSetupRequest)
	}{
		{name: "operation", want: "operation ID", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.ID = ""
		}},
		{name: "kind", want: "operation kind", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.Kind = domain.OperationKindNetworkResolverSetup
		}},
		{name: "project", want: "must not identify a project", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.ProjectID = "project-alpha"
		}},
		{name: "state", want: "operation", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.State = domain.OperationRunning
		}},
		{name: "phase", want: "queued phase", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.Phase = "waiting"
		}},
		{name: "projection stage", want: "requires \"resolver\" predecessor", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.Stage = NetworkStageFull
		}},
		{name: "projection revision", want: "revision must be positive", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.NetworkRevision = 0
		}},
		{name: "resolver proof", want: "generation must be positive", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.ResolverProof.Generation = 0
		}},
		{name: "ownership", want: "requires confirmed authority", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.ConfirmedOwnership = ownership.Observation{}
		}},
		{name: "policy", want: "network data-plane setup policy", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Policy.Suffix = ".invalid"
		}},
		{name: "policy fingerprint", want: "does not match confirmed ownership", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Policy.AuthorityFingerprint = strings.Repeat("d", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := fixture.request
			test.mutate(t, &request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	requestType := reflect.TypeOf(StageNetworkDataPlaneSetupRequest{})
	fields := make([]string, 0, requestType.NumField())
	for index := 0; index < requestType.NumField(); index++ {
		fields = append(fields, requestType.Field(index).Name)
	}
	wantFields := []string{"Operation", "Projection", "Policy"}
	if !slices.Equal(fields, wantFields) {
		t.Fatalf("StageNetworkDataPlaneSetupRequest fields = %v, want narrow surface %v", fields, wantFields)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.journal.StageNetworkDataPlaneSetup(ctx, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("StageNetworkDataPlaneSetup(cancelled) error = %v, want context.Canceled", err)
	}
	assertNetworkDataPlaneSetupStageCounts(t, fixture, 2, 0, 0)

	<-fixture.journal.mutations.permit
	released := false
	t.Cleanup(func() {
		if !released {
			fixture.journal.mutations.permit <- struct{}{}
		}
	})
	base, cancelQueued := context.WithCancel(context.Background())
	queuedContext := &networkInitializeSignalContext{Context: base, reached: make(chan struct{})}
	queuedResult := make(chan error, 1)
	go func() {
		_, err := fixture.journal.StageNetworkDataPlaneSetup(queuedContext, fixture.request)
		queuedResult <- err
	}()
	<-queuedContext.reached
	cancelQueued()
	if err := <-queuedResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued StageNetworkDataPlaneSetup() error = %v, want context.Canceled", err)
	}
	fixture.journal.mutations.permit <- struct{}{}
	released = true
	assertNetworkDataPlaneSetupStageCounts(t, fixture, 2, 0, 0)
}

// TestOperationJournalStagesAndReplaysExactNetworkDataPlaneSetup proves the projection itself is durable authority.
func TestOperationJournalStagesAndReplaysExactNetworkDataPlaneSetup(t *testing.T) {
	fixture := newNetworkDataPlaneSetupStageFixture(t)
	beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
	beforeOwnership := networkDataPlaneActivationTestProjection(t, fixture.database)

	staged, err := fixture.journal.StageNetworkDataPlaneSetup(nil, fixture.request)
	if err != nil {
		t.Fatalf("StageNetworkDataPlaneSetup(nil) error = %v", err)
	}
	if staged.Revision != 5 || staged.Operation.State != domain.OperationRequiresApproval ||
		staged.Operation.Phase != networkDataPlaneSetupApprovalPhase || staged.Operation.StartedAt == nil ||
		!staged.Operation.StartedAt.Equal(fixture.request.Operation.RequestedAt) ||
		staged.Operation.FinishedAt != nil || staged.Operation.Problem != nil {
		t.Fatalf("StageNetworkDataPlaneSetup(nil) = %#v, want revision-five approval", staged)
	}
	history, err := fixture.journal.Transitions(context.Background(), staged.Operation.ID)
	if err != nil {
		t.Fatalf("Transitions() error = %v", err)
	}
	wantStates := []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
	}
	wantPhases := []string{
		networkDataPlaneSetupQueuedPhase,
		networkDataPlaneSetupRunningPhase,
		networkDataPlaneSetupApprovalPhase,
	}
	for index := range history {
		if history[index].State != wantStates[index] || history[index].Phase != wantPhases[index] ||
			history[index].Sequence != domain.Sequence(index+3) ||
			!history[index].OccurredAt.Equal(fixture.request.Operation.RequestedAt) {
			t.Fatalf("transition %d = %#v", index+1, history[index])
		}
	}
	if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatal("network data-plane setup staging mutated network authority")
	}
	if afterOwnership := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterOwnership, beforeOwnership) {
		t.Fatal("network data-plane setup staging mutated confirmed ownership")
	}
	assertNetworkDataPlaneSetupHasNoPlanTable(t, fixture.database)

	fixture.journal = newNetworkSetupStageJournal(fixture.state.store.mutations.connections)
	replayed, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("replayed StageNetworkDataPlaneSetup() error = %v", err)
	}
	if !reflect.DeepEqual(replayed, staged) {
		t.Fatalf("replayed operation = %#v, want %#v", replayed, staged)
	}
	assertNetworkDataPlaneSetupStageCounts(t, fixture, 5, 1, 3)
}

// TestOperationJournalRejectsNonExactNetworkDataPlaneSetupReplay covers every immutable authority dimension.
func TestOperationJournalRejectsNonExactNetworkDataPlaneSetupReplay(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *StageNetworkDataPlaneSetupRequest)
	}{
		{name: "operation ID", want: "exact staged operation", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.ID = "operation-data-plane-proposed"
		}},
		{name: "request time", want: "exact staged operation", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Operation.RequestedAt = request.Operation.RequestedAt.Add(time.Minute)
		}},
		{name: "network revision", want: "network revision", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.NetworkRevision++
		}},
		{name: "resolver proof", want: "resolver proof differs", mutate: func(_ *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.ResolverProof.Evidence = "different valid resolver proof"
		}},
		{name: "confirmed ownership", want: "confirmed ownership differs", mutate: func(t *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Projection.ConfirmedOwnership.Record.OwnerIdentity = "502"
			setNetworkDataPlaneSetupStageOwnershipFingerprint(t, request)
		}},
		{name: "policy", want: "policy fingerprint does not match confirmed ownership", mutate: func(t *testing.T, request *StageNetworkDataPlaneSetupRequest) {
			request.Policy.AuthorityFingerprint = strings.Repeat("d", 64)
			bindNetworkDataPlaneSetupStagePolicy(t, request)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkDataPlaneSetupStageFixture(t)
			if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err != nil {
				t.Fatalf("initial StageNetworkDataPlaneSetup() error = %v", err)
			}
			candidate := fixture.request
			test.mutate(t, &candidate)
			if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), candidate); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("non-exact replay error = %v, want containing %q", err, test.want)
			}
			assertNetworkDataPlaneSetupStageCounts(t, fixture, 5, 1, 3)
		})
	}
}

// TestOperationJournalRejectsNetworkDataPlaneSetupOperationConflicts covers intent, ID, and active global ownership.
func TestOperationJournalRejectsNetworkDataPlaneSetupOperationConflicts(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		foreign := networkDataPlaneSetupStageOperation(
			t,
			"operation-data-plane-foreign",
			fixture.request.Operation.IntentID,
			domain.OperationKindNetworkResolverSetup,
		)
		if _, err := fixture.journal.Enqueue(context.Background(), foreign); err != nil {
			t.Fatalf("seed intent owner: %v", err)
		}
		_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
		var conflict *IntentConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("intent conflict error = %v", err)
		}
	})

	t.Run("operation ID", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		foreign := networkDataPlaneSetupStageOperation(
			t,
			fixture.request.Operation.ID,
			"intent-data-plane-foreign",
			domain.OperationKindNetworkResolverSetup,
		)
		if _, err := fixture.journal.Enqueue(context.Background(), foreign); err != nil {
			t.Fatalf("seed operation ID owner: %v", err)
		}
		_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
		var conflict *OperationIDConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("operation ID conflict error = %v", err)
		}
	})

	t.Run("active operation", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		active := networkDataPlaneSetupStageOperation(
			t,
			"operation-data-plane-active",
			"intent-data-plane-active",
			domain.OperationKindNetworkDataPlaneSetup,
		)
		if _, err := fixture.journal.Enqueue(context.Background(), active); err != nil {
			t.Fatalf("seed active data-plane operation: %v", err)
		}
		_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("active operation conflict error = %v", err)
		}
	})

	t.Run("multiple active operations", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		mustProjectStoreReadExec(
			t,
			fixture.database,
			"DROP INDEX operations_one_active_network_dataplane_setup_idx",
		)
		for index, identity := range []struct {
			operationID domain.OperationID
			intentID    domain.IntentID
		}{
			{operationID: "operation-data-plane-active-one", intentID: "intent-data-plane-active-one"},
			{operationID: "operation-data-plane-active-two", intentID: "intent-data-plane-active-two"},
		} {
			active := networkDataPlaneSetupStageOperation(
				t,
				identity.operationID,
				identity.intentID,
				domain.OperationKindNetworkDataPlaneSetup,
			)
			if _, err := fixture.journal.Enqueue(context.Background(), active); err != nil {
				t.Fatalf("seed active operation %d: %v", index, err)
			}
		}
		_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "found 2 active operations") {
			t.Fatalf("multiple active operation error = %v", err)
		}
	})
}

// TestOperationJournalNetworkDataPlaneSetupRejectsDurableDriftAndFullStage rechecks authority before replay or new staging.
func TestOperationJournalNetworkDataPlaneSetupRejectsDurableDriftAndFullStage(t *testing.T) {
	t.Run("proof drift", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err != nil {
			t.Fatalf("initial StageNetworkDataPlaneSetup() error = %v", err)
		}
		mustProjectStoreReadExec(
			t,
			fixture.database,
			"UPDATE network_setup_evidence SET evidence = 'durably changed proof' WHERE component = ?",
			NetworkSetupComponentResolver,
		)
		_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
		if err == nil || !strings.Contains(err.Error(), "resolver proof differs") {
			t.Fatalf("durable proof drift error = %v", err)
		}
		assertNetworkDataPlaneSetupStageCounts(t, fixture, 5, 1, 3)
	})

	t.Run("full stage replay and new intent", func(t *testing.T) {
		fixture := newNetworkDataPlaneSetupStageFixture(t)
		if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err != nil {
			t.Fatalf("initial StageNetworkDataPlaneSetup() error = %v", err)
		}
		fullRequest := networkDataPlaneActivationTestRequest(t, fixture.request.Projection.NetworkRevision)
		fullRequest.ConfirmedOwnership = fixture.request.Projection.ConfirmedOwnership
		fullRequest.Policy = fixture.request.Policy
		fullRequest.Setup[0] = fixture.request.Projection.ResolverProof
		fullRequest.At = fixture.request.Operation.RequestedAt.Add(time.Minute)
		fullRequest.Setup[1].VerifiedAt = fullRequest.At
		fullRequest.Listeners.DNS.VerifiedAt = fullRequest.At
		fullRequest.Listeners.HTTP.VerifiedAt = fullRequest.At
		fullRequest.Listeners.HTTPS.VerifiedAt = fullRequest.At
		if _, err := fixture.state.store.ActivateNetworkDataPlane(context.Background(), fullRequest); err != nil {
			t.Fatalf("ActivateNetworkDataPlane() error = %v", err)
		}

		if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err == nil ||
			!strings.Contains(err.Error(), `requires "resolver" stage, found "full"`) {
			t.Fatalf("full-stage replay error = %v", err)
		}
		fullProjection, err := fixture.state.source.Resolve(context.Background(), fixture.request.Policy)
		if err != nil {
			t.Fatalf("resolve full projection: %v", err)
		}
		newRequest := fixture.request
		newRequest.Operation = networkDataPlaneSetupStageOperation(
			t,
			"operation-data-plane-new",
			"intent-data-plane-new",
			domain.OperationKindNetworkDataPlaneSetup,
		)
		newRequest.Projection = fullProjection
		if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), newRequest); err == nil ||
			!strings.Contains(err.Error(), `requires "resolver" predecessor`) {
			t.Fatalf("full-stage new staging error = %v", err)
		}
		assertNetworkDataPlaneSetupStageCounts(t, fixture, 6, 1, 3)
	})
}

// TestOperationJournalNetworkDataPlaneSetupRejectsCorruptReplayHistory validates the fixed three-edge contract.
func TestOperationJournalNetworkDataPlaneSetupRejectsCorruptReplayHistory(t *testing.T) {
	fixture := newNetworkDataPlaneSetupStageFixture(t)
	if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err != nil {
		t.Fatalf("initial StageNetworkDataPlaneSetup() error = %v", err)
	}
	mustProjectStoreReadExec(
		t,
		fixture.database,
		"UPDATE operation_transitions SET phase = 'different phase' WHERE operation_id = ? AND ordinal = 2",
		fixture.request.Operation.ID,
	)
	_, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "transition 2 differs") {
		t.Fatalf("corrupt replay history error = %v", err)
	}
	assertNetworkDataPlaneSetupStageCounts(t, fixture, 5, 1, 3)
}

// TestOperationJournalNetworkDataPlaneSetupRollsBackEveryWriteFault proves no partial lifecycle or sequence escapes.
func TestOperationJournalNetworkDataPlaneSetupRollsBackEveryWriteFault(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{name: "operation insert", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE INSERT ON operations
			WHEN NEW.kind = 'network.data-plane.setup' BEGIN SELECT RAISE(ABORT, 'forced operation insert'); END`},
		{name: "queued transition", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE INSERT ON operation_transitions
			WHEN NEW.phase = 'queued' BEGIN SELECT RAISE(ABORT, 'forced queued transition'); END`},
		{name: "running update", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE UPDATE ON operations
			WHEN NEW.kind = 'network.data-plane.setup' AND NEW.state = 'running' BEGIN SELECT RAISE(ABORT, 'forced running update'); END`},
		{name: "running transition", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE INSERT ON operation_transitions
			WHEN NEW.phase = 'preparing trusted ingress' BEGIN SELECT RAISE(ABORT, 'forced running transition'); END`},
		{name: "approval update", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE UPDATE ON operations
			WHEN NEW.kind = 'network.data-plane.setup' AND NEW.state = 'requires_approval' BEGIN SELECT RAISE(ABORT, 'forced approval update'); END`},
		{name: "approval transition", trigger: `CREATE TRIGGER fail_data_plane_stage BEFORE INSERT ON operation_transitions
			WHEN NEW.phase = 'awaiting trust approval' BEGIN SELECT RAISE(ABORT, 'forced approval transition'); END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkDataPlaneSetupStageFixture(t)
			mustProjectStoreReadExec(t, fixture.database, test.trigger)
			if _, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request); err == nil ||
				!strings.Contains(err.Error(), "forced") {
				t.Fatalf("StageNetworkDataPlaneSetup() write fault error = %v", err)
			}
			assertNetworkDataPlaneSetupStageCounts(t, fixture, 2, 0, 0)
		})
	}
}

// TestOperationJournalConcurrentNetworkDataPlaneSetupRetriesAllocateOnce proves same-intent races converge exactly.
func TestOperationJournalConcurrentNetworkDataPlaneSetupRetriesAllocateOnce(t *testing.T) {
	fixture := newNetworkDataPlaneSetupStageFixture(t)
	start := make(chan struct{})
	results := make(chan struct {
		record OperationRecord
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			record, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.request)
			results <- struct {
				record OperationRecord
				err    error
			}{record: record, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent staging errors = %v and %v", first.err, second.err)
	}
	if !reflect.DeepEqual(first.record, second.record) || first.record.Revision != 5 {
		t.Fatalf("concurrent staging records = %#v and %#v", first.record, second.record)
	}
	assertNetworkDataPlaneSetupStageCounts(t, fixture, 5, 1, 3)
}

// newNetworkDataPlaneSetupStageFixture constructs one exact resolver predecessor over the production schema.
func newNetworkDataPlaneSetupStageFixture(t *testing.T) *networkDataPlaneSetupStageFixture {
	t.Helper()
	stateFixture := newNetworkDataPlaneSetupProjectionFixture(t)
	applyNetworkDataPlaneSetupStageMigration(t, stateFixture.database)
	projection, err := stateFixture.source.Resolve(context.Background(), stateFixture.request.Policy)
	if err != nil {
		t.Fatalf("resolve data-plane setup predecessor: %v", err)
	}
	connections := stateFixture.store.mutations.connections
	request := StageNetworkDataPlaneSetupRequest{
		Operation: networkDataPlaneSetupStageOperation(
			t,
			"operation-data-plane-setup",
			"intent-data-plane-setup",
			domain.OperationKindNetworkDataPlaneSetup,
		),
		Projection: projection,
		Policy:     stateFixture.request.Policy,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate data-plane setup stage fixture: %v", err)
	}
	return &networkDataPlaneSetupStageFixture{
		state:    stateFixture,
		journal:  newNetworkSetupStageJournal(connections),
		request:  request,
		database: stateFixture.database,
	}
}

// applyNetworkDataPlaneSetupStageMigration installs the production active-owner guard omitted by the older shared fixture.
func applyNetworkDataPlaneSetupStageMigration(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() != networkDataPlaneSetupStageMigrationName ||
			migration.App() != "harbord" ||
			migration.Connection() != "default" ||
			(migration.Driver() != "" && migration.Driver() != "sqlite") {
			continue
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply network data-plane setup stage migration: %v", err)
		}
		return
	}
	t.Fatalf("network data-plane setup stage migration %q was not registered", networkDataPlaneSetupStageMigrationName)
}

// networkDataPlaneSetupStageOperation constructs one stable queued global operation for staging tests.
func networkDataPlaneSetupStageOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	kind domain.OperationKind,
) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(
		operationID,
		intentID,
		kind,
		"",
		networkMutationTestTime().Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("create network data-plane setup operation: %v", err)
	}
	return operation
}

// bindNetworkDataPlaneSetupStagePolicy updates the request's ownership fingerprint for one alternate canonical policy.
func bindNetworkDataPlaneSetupStagePolicy(t *testing.T, request *StageNetworkDataPlaneSetupRequest) {
	t.Helper()
	policyFingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint network data-plane setup policy: %v", err)
	}
	request.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = policyFingerprint
	setNetworkDataPlaneSetupStageOwnershipFingerprint(t, request)
}

// setNetworkDataPlaneSetupStageOwnershipFingerprint keeps a mutated observation internally canonical.
func setNetworkDataPlaneSetupStageOwnershipFingerprint(t *testing.T, request *StageNetworkDataPlaneSetupRequest) {
	t.Helper()
	fingerprint, err := request.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint network data-plane setup ownership: %v", err)
	}
	request.Projection.ConfirmedOwnership.Fingerprint = fingerprint
}

// assertNetworkDataPlaneSetupStageCounts checks the high-water and the only two tables staging may change.
func assertNetworkDataPlaneSetupStageCounts(
	t *testing.T,
	fixture *networkDataPlaneSetupStageFixture,
	wantSequence int64,
	wantOperations int64,
	wantTransitions int64,
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

// assertNetworkDataPlaneSetupHasNoPlanTable proves staging did not introduce a second durable authority owner.
func assertNetworkDataPlaneSetupHasNoPlanTable(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var tables []string
	if err := databaseConnection.Raw(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND (name LIKE 'network_data_plane_setup_plan%' OR name LIKE 'network_dataplane_setup_plan%')`).
		Scan(&tables).Error; err != nil {
		t.Fatalf("inspect network data-plane setup plan tables: %v", err)
	}
	if len(tables) != 0 {
		t.Fatalf("network data-plane setup plan tables = %v, want none", tables)
	}
}
