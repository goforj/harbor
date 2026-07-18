package state

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// TestStoreBeginProjectNetworkReleaseSuppressesRoutesAndRetainsRecoveryFacts verifies the first durable teardown boundary.
func TestStoreBeginProjectNetworkReleaseSuppressesRoutesAndRetainsRecoveryFacts(t *testing.T) {
	store, connection, _, _, request, initialization := newNetworkReleaseTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	projectBefore, err := store.Project(context.Background(), request.ProjectID)
	if err != nil {
		t.Fatalf("read project before Begin: %v", err)
	}
	operationBefore := networkReleaseTestOperation(t, store, request.OperationID)

	result, err := store.BeginProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("BeginProjectNetworkRelease() error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 11 || !result.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("BeginProjectNetworkRelease() result = %#v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ProjectNetworkReleaseMutationResult.Validate() error = %v", err)
	}
	if want := []domain.ProjectID{"project-alpha"}; !reflect.DeepEqual(result.Record.Reservations.SuppressedProjectIDs, want) {
		t.Fatalf("suppressed projects = %v, want %v", result.Record.Reservations.SuppressedProjectIDs, want)
	}
	if len(result.Record.Reservations.Endpoints) != 1 || result.Record.Reservations.Endpoints[0].Key.ProjectID != "project-beta" {
		t.Fatalf("public endpoints after Begin = %#v", result.Record.Reservations.Endpoints)
	}
	wantEnsures := slices.Clone(initialization.Ensures[:2])
	slices.SortFunc(wantEnsures, func(left, right NetworkLeaseEnsure) int {
		if networkLeaseLess(left.Lease, right.Lease) {
			return -1
		}
		if networkLeaseLess(right.Lease, left.Lease) {
			return 1
		}
		return 0
	})
	wantEndpoints := canonicalEndpointReservations([]EndpointReservation{
		initialization.Endpoints[0],
		initialization.Endpoints[2],
	})
	if !reflect.DeepEqual(result.Release.ActiveLeases, wantEnsures) || !reflect.DeepEqual(result.Release.Endpoints, wantEndpoints) {
		t.Fatalf("release recovery facts = leases %#v endpoints %#v", result.Release.ActiveLeases, result.Release.Endpoints)
	}
	if result.Release.ProjectID != request.ProjectID || result.Release.OperationID != request.OperationID ||
		result.Release.State != ProjectNetworkReleaseReleasing || result.Release.BeginGeneration != request.BeginGeneration ||
		!result.Release.BeganAt.Equal(request.At) || result.Release.Completion != nil {
		t.Fatalf("release record = %#v", result.Release)
	}

	after := networkReplaceTestRows(t, connection)
	if !reflect.DeepEqual(after.Leases, before.Leases) || !reflect.DeepEqual(after.Endpoints, before.Endpoints) {
		t.Fatal("Begin changed hidden lease or endpoint rows")
	}
	if len(after.Releases) != 1 || !projectNetworkReleaseBeginRowMatches(after.Releases[0], request) {
		t.Fatalf("release rows after Begin = %#v", after.Releases)
	}
	projectAfter, err := store.Project(context.Background(), request.ProjectID)
	if err != nil || !reflect.DeepEqual(projectAfter, projectBefore) {
		t.Fatalf("project after Begin = %#v, %v; want %#v", projectAfter, err, projectBefore)
	}
	if operationAfter := networkReleaseTestOperation(t, store, request.OperationID); !reflect.DeepEqual(operationAfter, operationBefore) {
		t.Fatalf("operation changed during Begin: got %#v want %#v", operationAfter, operationBefore)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 11 {
		t.Fatalf("Harbor sequence after Begin = %d, want 11", highWater)
	}

	read, found, err := store.ProjectNetworkRelease(context.Background(), request.OperationID)
	if err != nil || !found || !reflect.DeepEqual(read, result.Release) {
		t.Fatalf("ProjectNetworkRelease() = %#v, %t, %v; want %#v", read, found, err, result.Release)
	}
	network, initialized, err := store.Network(context.Background())
	if err != nil || !initialized || !reflect.DeepEqual(network, result.Record) {
		t.Fatalf("Network() = %#v, %t, %v; want Begin projection", network, initialized, err)
	}
	missing, found, err := store.ProjectNetworkRelease(context.Background(), "operation-unknown")
	if err != nil || found || !reflect.DeepEqual(missing, ProjectNetworkReleaseRecord{}) {
		t.Fatalf("missing ProjectNetworkRelease() = %#v, %t, %v", missing, found, err)
	}
}

// TestStoreBeginProjectNetworkReleaseReplaysOnlyExactForwardProgress verifies semantic retry ordering across three owners.
func TestStoreBeginProjectNetworkReleaseReplaysOnlyExactForwardProgress(t *testing.T) {
	t.Run("exact retry", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		first, err := store.BeginProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("seed Begin: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		second, err := store.BeginProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("replayed Begin: %v", err)
		}
		if !second.Replayed || !reflect.DeepEqual(second.Record, first.Record) || !reflect.DeepEqual(second.Release, first.Release) {
			t.Fatalf("replayed Begin = %#v, first %#v", second, first)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("exact replay changed network rows")
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 11 {
			t.Fatalf("Harbor sequence after exact replay = %d, want 11", highWater)
		}
	})

	t.Run("later operation revision", func(t *testing.T) {
		store, _, journal, running, request, _ := newNetworkReleaseTestHarness(t, 1)
		first, err := store.BeginProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("seed Begin: %v", err)
		}
		approvalAt := request.At.Add(time.Second)
		approval, err := journal.Transition(
			context.Background(),
			request.OperationID,
			running.Revision,
			domain.OperationRequiresApproval,
			"waiting for host approval",
			approvalAt,
			nil,
		)
		if err != nil || approval.Revision != 12 {
			t.Fatalf("approval transition = %#v, %v", approval, err)
		}

		replayed, err := store.BeginProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("replay after operation progress: %v", err)
		}
		if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, first.Record) || !reflect.DeepEqual(replayed.Release, first.Release) {
			t.Fatalf("replay after operation progress = %#v, first %#v", replayed, first)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 12 {
			t.Fatalf("Harbor sequence after operation progress = %d, want 12", highWater)
		}
	})

	t.Run("future expectations", func(t *testing.T) {
		store, _, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		if _, err := store.BeginProjectNetworkRelease(context.Background(), request); err != nil {
			t.Fatalf("seed Begin: %v", err)
		}
		for _, test := range []struct {
			name   string
			mutate func(*BeginProjectNetworkReleaseRequest)
			assert func(error) bool
		}{
			{name: "network", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedNetworkRevision = 12 }, assert: func(err error) bool {
				var conflict *NetworkRevisionConflictError
				return errors.As(err, &conflict) && conflict.Expected == 12 && conflict.Actual == 11
			}},
			{name: "project", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedProjectRevision = 12 }, assert: func(err error) bool {
				var conflict *ProjectRevisionConflictError
				return errors.As(err, &conflict) && conflict.Expected == 12 && conflict.Actual == 8
			}},
			{name: "operation", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedOperationRevision = 12 }, assert: func(err error) bool {
				var conflict *StaleRevisionError
				return errors.As(err, &conflict) && conflict.Expected == 12 && conflict.Actual == 10
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := request
				test.mutate(&candidate)
				_, err := store.BeginProjectNetworkRelease(context.Background(), candidate)
				if !test.assert(err) {
					t.Fatalf("future %s error = %v", test.name, err)
				}
			})
		}
	})

	t.Run("different semantic facts precede stale root", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		if _, err := store.BeginProjectNetworkRelease(context.Background(), request); err != nil {
			t.Fatalf("seed Begin: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		request.BeginGeneration++
		_, err := store.BeginProjectNetworkRelease(context.Background(), request)
		var conflict *ProjectNetworkReleaseConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "begin generation" {
			t.Fatalf("semantic conflict = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("semantic conflict changed rows")
		}
	})

}

// TestStoreBeginProjectNetworkReleaseRejectsOwnerAndRevisionConflicts verifies all pre-write authority boundaries.
func TestStoreBeginProjectNetworkReleaseRejectsOwnerAndRevisionConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Store, *gorm.DB, *OperationJournal, *OperationRecord, *BeginProjectNetworkReleaseRequest)
		assert func(error) bool
	}{
		{name: "network revision", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.ExpectedNetworkRevision = 6
		}, assert: func(err error) bool {
			var conflict *NetworkRevisionConflictError
			return errors.As(err, &conflict) && conflict.Expected == 6 && conflict.Actual == 7
		}},
		{name: "project revision", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.ExpectedProjectRevision = 5
		}, assert: func(err error) bool {
			var conflict *ProjectRevisionConflictError
			return errors.As(err, &conflict) && conflict.Expected == 5 && conflict.Actual == 8
		}},
		{name: "operation revision", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.ExpectedOperationRevision = 9
		}, assert: func(err error) bool {
			var conflict *StaleRevisionError
			return errors.As(err, &conflict) && conflict.Expected == 9 && conflict.Actual == 10
		}},
		{name: "operation state", mutate: func(_ *Store, _ *gorm.DB, journal *OperationJournal, running *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			approval, err := journal.Transition(context.Background(), request.OperationID, running.Revision, domain.OperationRequiresApproval, "approval", request.At, nil)
			if err != nil {
				panic(err)
			}
			request.ExpectedOperationRevision = approval.Revision
		}, assert: networkReleaseTestConflictLabel("operation state")},
		{name: "operation project", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.ProjectID = "project-beta"
			request.ExpectedProjectRevision = 6
		}, assert: networkReleaseTestConflictLabel("operation project")},
		{name: "network time", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.At = networkMutationTestTime().Add(-time.Second)
		}, assert: networkReleaseTestConflictLabel("begin time")},
		{name: "operation time", mutate: func(_ *Store, _ *gorm.DB, _ *OperationJournal, _ *OperationRecord, request *BeginProjectNetworkReleaseRequest) {
			request.At = request.At.Add(-2 * time.Second)
		}, assert: networkReleaseTestConflictLabel("begin time")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection, journal, running, request, _ := newNetworkReleaseTestHarness(t, 1)
			test.mutate(store, connection, journal, &running, &request)
			before := networkReplaceTestRows(t, connection)
			_, err := store.BeginProjectNetworkRelease(context.Background(), request)
			if !test.assert(err) {
				t.Fatalf("%s error = %v", test.name, err)
			}
			if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s conflict changed network rows", test.name)
			}
		})
	}

	t.Run("busy project", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		operation, err := domain.NewOperation(
			"operation-peer",
			"intent-peer",
			"project.inspect",
			request.ProjectID,
			request.At,
		)
		if err != nil {
			t.Fatalf("create peer operation: %v", err)
		}
		if _, err := projectStoreMutationJournal(store).Enqueue(context.Background(), operation); err != nil {
			t.Fatalf("enqueue peer operation: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		_, err = store.BeginProjectNetworkRelease(context.Background(), request)
		var busy *ProjectBusyError
		if !errors.As(err, &busy) || !reflect.DeepEqual(busy.OperationIDs, []domain.OperationID{"operation-peer"}) {
			t.Fatalf("busy project error = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("busy project rejection changed network rows")
		}
	})
}

// TestStoreBeginProjectNetworkReleaseRollsBackFailures verifies staging is one exact network transaction.
func TestStoreBeginProjectNetworkReleaseRollsBackFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		statement string
		want      string
	}{
		{
			name: "marker insertion",
			statement: `CREATE TRIGGER fail_network_release_begin_insert BEFORE INSERT ON network_project_releases
				BEGIN SELECT RAISE(ABORT, 'forced release marker failure'); END`,
			want: "forced release marker failure",
		},
		{
			name: "root update",
			statement: `CREATE TRIGGER fail_network_release_begin_root BEFORE UPDATE ON network_state
				BEGIN SELECT RAISE(ABORT, 'forced release root failure'); END`,
			want: "forced release root failure",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
			before := networkReplaceTestRows(t, connection)
			mustProjectStoreReadExec(t, connection, test.statement)
			_, err := store.BeginProjectNetworkRelease(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want %q", test.name, err, test.want)
			}
			assertNetworkReleaseBeginRollback(t, store, connection, before, 10)
		})
	}

	t.Run("tampered marker readback", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER corrupt_network_release_begin_marker
			AFTER UPDATE OF revision ON network_state
			BEGIN
				UPDATE network_project_releases SET begin_generation = begin_generation + 1
				WHERE operation_id = 'operation-release-alpha';
			END`)
		_, err := store.BeginProjectNetworkRelease(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "inserted row differs") {
			t.Fatalf("tampered marker error = %v", err)
		}
		assertNetworkReleaseBeginRollback(t, store, connection, before, 10)
	})

	t.Run("revision collision", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER collide_network_release_begin_revision
			AFTER UPDATE OF revision ON network_state
			BEGIN
				INSERT INTO operations
					(id, intent_id, kind, state, phase, requested_at, revision)
					VALUES ('operation-release-collision', 'intent-release-collision', 'maintenance.run', 'queued', 'queued', '2026-07-18T12:00:00Z', NEW.revision);
			END`)
		_, err := store.BeginProjectNetworkRelease(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision") {
			t.Fatalf("revision collision error = %v", err)
		}
		assertNetworkReleaseBeginRollback(t, store, connection, before, 10)
		if count := networkReleaseTestOperationCount(t, connection, "operation-release-collision"); count != 0 {
			t.Fatalf("collision operation survived rollback: count = %d", count)
		}
	})
}

// TestStoreBeginProjectNetworkReleaseConcurrentRetriesAllocateOnce verifies equivalent owners converge under contention.
func TestStoreBeginProjectNetworkReleaseConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _, _, _, request, _ := newNetworkReleaseTestHarness(t, 4)
	start := make(chan struct{})
	results := make(chan struct {
		result ProjectNetworkReleaseMutationResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.BeginProjectNetworkRelease(context.Background(), request)
			results <- struct {
				result ProjectNetworkReleaseMutationResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent Begin errors = %v and %v", first.err, second.err)
	}
	if first.result.Replayed == second.result.Replayed ||
		!reflect.DeepEqual(first.result.Record, second.result.Record) ||
		!reflect.DeepEqual(first.result.Release, second.result.Release) {
		t.Fatalf("concurrent Begin results = %#v and %#v", first.result, second.result)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 11 {
		t.Fatalf("Harbor sequence after concurrent Begin = %d, want 11", highWater)
	}
}

// TestStoreBeginProjectNetworkReleaseValidatesAndCancelsBeforeWriting verifies the public writer boundary.
func TestStoreBeginProjectNetworkReleaseValidatesAndCancelsBeforeWriting(t *testing.T) {
	invalid := releaseContractTestBeginRequest()
	invalid.BeginGeneration = 0
	var absentStore *Store
	if _, err := absentStore.BeginProjectNetworkRelease(context.Background(), invalid); err == nil {
		t.Fatal("invalid Begin reached nil storage")
	}

	t.Run("pre-canceled", func(t *testing.T) {
		store, _, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.BeginProjectNetworkRelease(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 10 {
			t.Fatalf("Harbor sequence after pre-cancel = %d, want 10", highWater)
		}
	})

	t.Run("queued cancellation", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		base, cancel := context.WithCancel(context.Background())
		ctx := &networkReleaseSignalContext{Context: base, reached: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := store.BeginProjectNetworkRelease(ctx, request)
			result <- err
		}()
		<-ctx.reached
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error = %v", err)
		}
		store.mutations.permit <- struct{}{}
		released = true
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("queued cancellation changed rows")
		}
	})
}

// TestStoreCompleteProjectNetworkReleaseCommitsExactTeardown verifies hidden routes, leases, and digest advance atomically.
func TestStoreCompleteProjectNetworkReleaseCommitsExactTeardown(t *testing.T) {
	store, connection, _, _, begin, initialization := newNetworkReleaseTestHarness(t, 1)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("stage release: %v", err)
	}
	request := networkReleaseTestCompleteRequest(begin, staged.Release)
	before := networkReplaceTestRows(t, connection)
	projectBefore, err := store.Project(context.Background(), request.ProjectID)
	if err != nil {
		t.Fatalf("read project before Complete: %v", err)
	}
	operationBefore := networkReleaseTestOperation(t, store, request.OperationID)

	result, err := store.CompleteProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteProjectNetworkRelease() error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 12 || !result.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("CompleteProjectNetworkRelease() result = %#v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("completed result Validate() error = %v", err)
	}
	if result.Release.State != ProjectNetworkReleaseCompleted || result.Release.Completion == nil ||
		result.Release.Completion.Generation != request.CompletionGeneration ||
		!result.Release.Completion.CompletedAt.Equal(request.At) ||
		result.Release.Completion.Evidence != request.ReleaseEvidence ||
		result.Release.Completion.ReleaseSetDigest != projectNetworkReleaseSetDigest(request.Releases) ||
		len(result.Release.ActiveLeases) != 0 || len(result.Release.Endpoints) != 0 {
		t.Fatalf("completed release = %#v", result.Release)
	}
	for _, lease := range result.Record.Leases {
		if lease.Key.ProjectID == request.ProjectID {
			t.Fatalf("completed projection retains target lease %#v", lease)
		}
	}
	if len(result.Record.Quarantines) != 2 {
		t.Fatalf("completed quarantines = %#v, want two", result.Record.Quarantines)
	}
	if len(result.Record.Reservations.Endpoints) != 1 || result.Record.Reservations.Endpoints[0] != initialization.Endpoints[1] {
		t.Fatalf("completed public endpoints = %#v", result.Record.Reservations.Endpoints)
	}

	after := networkReplaceTestRows(t, connection)
	for _, row := range after.Endpoints {
		if row.ProjectId == string(request.ProjectID) {
			t.Fatalf("completed release retained raw endpoint %#v", row)
		}
	}
	if len(after.Endpoints) != len(before.Endpoints)-2 {
		t.Fatalf("endpoint count after Complete = %d, want %d", len(after.Endpoints), len(before.Endpoints)-2)
	}
	for _, release := range request.Releases {
		prior := networkReplaceTestLeaseRow(t, before.Leases, string(request.ProjectID), release.Lease.Key.SecondaryID)
		persisted := networkReplaceTestLeaseRowByID(t, after.Leases, prior.Id)
		if !networkReplacementReleasedRowMatches(persisted, prior, release) {
			t.Fatalf("completed quarantine = %#v, prior %#v, release %#v", persisted, prior, release)
		}
	}
	marker := networkReleaseTestMarker(t, after.Releases, request.OperationID)
	beforeMarker := networkReleaseTestMarker(t, before.Releases, request.OperationID)
	if !projectNetworkReleaseCompletedRowMatches(
		marker,
		beforeMarker,
		request,
		projectNetworkReleaseSetDigest(request.Releases),
	) {
		t.Fatalf("completed marker = %#v", marker)
	}
	projectAfter, err := store.Project(context.Background(), request.ProjectID)
	if err != nil || !reflect.DeepEqual(projectAfter, projectBefore) {
		t.Fatalf("project after Complete = %#v, %v; want %#v", projectAfter, err, projectBefore)
	}
	if operationAfter := networkReleaseTestOperation(t, store, request.OperationID); !reflect.DeepEqual(operationAfter, operationBefore) {
		t.Fatalf("operation changed during Complete: got %#v want %#v", operationAfter, operationBefore)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 12 {
		t.Fatalf("Harbor sequence after Complete = %d, want 12", highWater)
	}
	read, found, err := store.ProjectNetworkRelease(context.Background(), request.OperationID)
	if err != nil || !found || !reflect.DeepEqual(read, result.Release) {
		t.Fatalf("ProjectNetworkRelease() after Complete = %#v, %t, %v", read, found, err)
	}
}

// TestStoreCompleteProjectNetworkReleaseReplaysDigestAcrossLifecycleProgress verifies tombstone replay independence.
func TestStoreCompleteProjectNetworkReleaseReplaysDigestAcrossLifecycleProgress(t *testing.T) {
	t.Run("exact retry", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		first, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		second, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("replayed Complete: %v", err)
		}
		if !second.Replayed || !reflect.DeepEqual(second.Record, first.Record) || !reflect.DeepEqual(second.Release, first.Release) {
			t.Fatalf("replayed Complete = %#v, first %#v", second, first)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("exact Complete replay changed network rows")
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 12 {
			t.Fatalf("Harbor sequence after Complete replay = %d, want 12", highWater)
		}
	})

	t.Run("semantic mismatch precedes stale root", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		if _, err := store.CompleteProjectNetworkRelease(context.Background(), request); err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		request.Releases[0].ReleaseEvidence = "different verified release"
		_, err = store.CompleteProjectNetworkRelease(context.Background(), request)
		var conflict *ProjectNetworkReleaseConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "release set" {
			t.Fatalf("release-set replay conflict = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("release-set replay conflict changed rows")
		}
	})

	t.Run("later operation revision", func(t *testing.T) {
		store, _, journal, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		first, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
		approval, err := journal.Transition(
			context.Background(),
			request.OperationID,
			running.Revision,
			domain.OperationRequiresApproval,
			"waiting for final approval",
			request.At.Add(time.Second),
			nil,
		)
		if err != nil || approval.Revision != 13 {
			t.Fatalf("approval transition = %#v, %v", approval, err)
		}
		replayed, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("replay after operation progress: %v", err)
		}
		if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, first.Record) || !reflect.DeepEqual(replayed.Release, first.Release) {
			t.Fatalf("replay after operation progress = %#v, first %#v", replayed, first)
		}
	})

	t.Run("future expectations", func(t *testing.T) {
		store, _, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		if _, err := store.CompleteProjectNetworkRelease(context.Background(), request); err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
		for _, test := range []struct {
			name   string
			mutate func(*CompleteProjectNetworkReleaseRequest)
			assert func(error) bool
		}{
			{name: "network", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ExpectedNetworkRevision = 13 }, assert: func(err error) bool {
				var conflict *NetworkRevisionConflictError
				return errors.As(err, &conflict) && conflict.Expected == 13 && conflict.Actual == 12
			}},
			{name: "project", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ExpectedProjectRevision = 13 }, assert: func(err error) bool {
				var conflict *ProjectRevisionConflictError
				return errors.As(err, &conflict) && conflict.Expected == 13 && conflict.Actual == 8
			}},
			{name: "operation", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ExpectedOperationRevision = 13 }, assert: func(err error) bool {
				var conflict *StaleRevisionError
				return errors.As(err, &conflict) && conflict.Expected == 13 && conflict.Actual == 10
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := request
				test.mutate(&candidate)
				_, err := store.CompleteProjectNetworkRelease(context.Background(), candidate)
				if !test.assert(err) {
					t.Fatalf("future %s error = %v", test.name, err)
				}
			})
		}
	})

	t.Run("project deletion and lease reuse", func(t *testing.T) {
		store, connection, _, _, begin, initialization := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		first, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("seed Complete: %v", err)
		}
		completedOperation, err := store.CompleteProjectUnregister(
			context.Background(),
			request.ProjectID,
			request.OperationID,
			request.ExpectedOperationRevision,
			"completed project unregister",
			request.At.Add(time.Minute),
		)
		if err != nil || completedOperation.Revision != 13 || completedOperation.Operation.State != domain.OperationSucceeded {
			t.Fatalf("CompleteProjectUnregister() = %#v, %v", completedOperation, err)
		}

		reuseAt := request.At.Add(2 * time.Hour)
		secondaryIDs := []string{"cache", "metrics"}
		ensures := make([]NetworkLeaseEnsure, 0, len(request.Releases))
		for index, release := range request.Releases {
			lease := release.Lease
			lease.Key.ProjectID = "project-beta"
			lease.Key.SecondaryID = secondaryIDs[index]
			ensures = append(ensures, NetworkLeaseEnsure{
				Lease:          lease,
				Generation:     release.ReleaseGeneration + 1,
				EnsureEvidence: "verified reuse by another project",
				LeasedAt:       reuseAt.Add(-time.Minute),
			})
		}
		slices.SortFunc(ensures, func(left, right NetworkLeaseEnsure) int {
			if networkLeaseLess(left.Lease, right.Lease) {
				return -1
			}
			if networkLeaseLess(right.Lease, left.Lease) {
				return 1
			}
			return 0
		})
		reused, err := store.ReplaceProjectNetwork(context.Background(), ReplaceProjectNetworkRequest{
			ProjectID:               "project-beta",
			ExpectedNetworkRevision: first.Record.Revision,
			ExpectedProjectRevision: 6,
			Ensures:                 ensures,
			Releases:                []NetworkLeaseRelease{},
			Endpoints:               []EndpointReservation{initialization.Endpoints[1]},
			At:                      reuseAt,
		})
		if err != nil || reused.Replayed || reused.Record.Revision != 14 {
			t.Fatalf("reuse released leases = %#v, %v", reused, err)
		}
		for _, release := range request.Releases {
			row := networkReplaceTestLeaseRowByAddress(t, networkReplaceTestRows(t, connection).Leases, release.Lease.Address.String())
			if row.SourceProjectId != "project-beta" || row.State != "leased" || networkLeaseHasReleaseFields(row) {
				t.Fatalf("reused lease row = %#v", row)
			}
		}

		beforeReplay := networkReplaceTestRows(t, connection)
		replayed, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("Complete replay after deletion and reuse: %v", err)
		}
		if !replayed.Replayed || !reflect.DeepEqual(replayed.Release, first.Release) ||
			!reflect.DeepEqual(replayed.Record, reused.Record) {
			t.Fatalf("Complete replay after deletion and reuse = %#v", replayed)
		}
		if afterReplay := networkReplaceTestRows(t, connection); !reflect.DeepEqual(afterReplay, beforeReplay) {
			t.Fatal("Complete replay after deletion and reuse changed durable rows")
		}
		if _, err := store.BeginProjectNetworkRelease(context.Background(), begin); err == nil {
			t.Fatal("Begin replay after final project deletion succeeded")
		} else {
			var missing *ProjectNotFoundError
			if !errors.As(err, &missing) || missing.ProjectID != begin.ProjectID {
				t.Fatalf("Begin replay after project deletion error = %v", err)
			}
		}
	})
}

// TestStoreCompleteProjectNetworkReleaseRejectsIncompleteOrChangedAuthority verifies all first-completion gates.
func TestStoreCompleteProjectNetworkReleaseRejectsIncompleteOrChangedAuthority(t *testing.T) {
	t.Run("not started", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		request := networkReleaseTestCompleteRequest(begin, ProjectNetworkReleaseRecord{
			ProjectID: begin.ProjectID, OperationID: begin.OperationID, BeginGeneration: begin.BeginGeneration, BeganAt: begin.At,
			ActiveLeases: []NetworkLeaseEnsure{}, Endpoints: []EndpointReservation{},
		})
		request.Releases = releaseContractTestCompleteRequest().Releases
		for index := range request.Releases {
			request.Releases[index].Lease.Key.ProjectID = begin.ProjectID
		}
		before := networkReplaceTestRows(t, connection)
		_, err := store.CompleteProjectNetworkRelease(context.Background(), request)
		var missing *ProjectNetworkReleaseNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("not-started error = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("not-started rejection changed rows")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*Store, *OperationJournal, OperationRecord, *CompleteProjectNetworkReleaseRequest)
		assert func(error) bool
	}{
		{name: "network revision", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.ExpectedNetworkRevision = 9
		}, assert: func(err error) bool {
			var conflict *NetworkRevisionConflictError
			return errors.As(err, &conflict) && conflict.Expected == 9 && conflict.Actual == 11
		}},
		{name: "project revision", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.ExpectedProjectRevision = 7
		}, assert: func(err error) bool {
			var conflict *ProjectRevisionConflictError
			return errors.As(err, &conflict) && conflict.Expected == 7 && conflict.Actual == 8
		}},
		{name: "operation revision", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.ExpectedOperationRevision = 9
		}, assert: func(err error) bool {
			var conflict *StaleRevisionError
			return errors.As(err, &conflict) && conflict.Expected == 9 && conflict.Actual == 10
		}},
		{name: "begin generation", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.ExpectedBeginGeneration++
			request.CompletionGeneration++
		}, assert: networkReleaseTestConflictLabel("begin generation")},
		{name: "operation state", mutate: func(_ *Store, journal *OperationJournal, running OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			approval, err := journal.Transition(context.Background(), request.OperationID, running.Revision, domain.OperationRequiresApproval, "approval", request.At, nil)
			if err != nil {
				panic(err)
			}
			request.ExpectedOperationRevision = approval.Revision
		}, assert: networkReleaseTestConflictLabel("operation state")},
		{name: "completion time", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.At = request.At.Add(-6 * time.Minute)
			for index := range request.Releases {
				request.Releases[index].ReleasedAt = request.At
				request.Releases[index].QuarantinedAt = request.At
				request.Releases[index].ReuseAfter = request.At.Add(time.Hour)
			}
		}, assert: networkReleaseTestConflictLabel("completion time")},
		{name: "missing secondary", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.Releases = request.Releases[:1]
		}, assert: networkReleaseTestConflictLabel("release set")},
		{name: "address", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.Releases[0].Lease.Address = request.Releases[1].Lease.Address
		}, assert: func(err error) bool { return err != nil }},
		{name: "generation", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.Releases[0].ReleaseGeneration = 1
		}, assert: networkReleaseTestConflictLabel("release set")},
		{name: "before staged begin", mutate: func(_ *Store, _ *OperationJournal, _ OperationRecord, request *CompleteProjectNetworkReleaseRequest) {
			request.Releases[0].ReleasedAt = request.At.Add(-10 * time.Minute)
			request.Releases[0].QuarantinedAt = request.Releases[0].ReleasedAt
			request.Releases[0].ReuseAfter = request.At.Add(time.Hour)
		}, assert: networkReleaseTestConflictLabel("release set")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection, journal, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
			staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
			if err != nil {
				t.Fatalf("stage release: %v", err)
			}
			request := networkReleaseTestCompleteRequest(begin, staged.Release)
			test.mutate(store, journal, running, &request)
			before := networkReplaceTestRows(t, connection)
			_, err = store.CompleteProjectNetworkRelease(context.Background(), request)
			if !test.assert(err) {
				t.Fatalf("%s error = %v", test.name, err)
			}
			if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s rejection changed network rows", test.name)
			}
		})
	}
}

// TestStoreCompleteProjectNetworkReleaseRollsBackEveryWrite verifies no partial teardown or digest can commit.
func TestStoreCompleteProjectNetworkReleaseRollsBackEveryWrite(t *testing.T) {
	for _, test := range []struct {
		name      string
		statement string
		want      string
	}{
		{
			name: "endpoint deletion",
			statement: `CREATE TRIGGER fail_network_release_complete_delete BEFORE DELETE ON public_endpoint_leases
				BEGIN SELECT RAISE(ABORT, 'forced complete endpoint failure'); END`,
			want: "forced complete endpoint failure",
		},
		{
			name: "lease quarantine",
			statement: `CREATE TRIGGER fail_network_release_complete_lease BEFORE UPDATE ON loopback_address_leases
				WHEN NEW.state = 'quarantined'
				BEGIN SELECT RAISE(ABORT, 'forced complete lease failure'); END`,
			want: "forced complete lease failure",
		},
		{
			name: "marker completion",
			statement: `CREATE TRIGGER fail_network_release_complete_marker BEFORE UPDATE ON network_project_releases
				BEGIN SELECT RAISE(ABORT, 'forced complete marker failure'); END`,
			want: "forced complete marker failure",
		},
		{
			name: "root update",
			statement: `CREATE TRIGGER fail_network_release_complete_root BEFORE UPDATE ON network_state
				BEGIN SELECT RAISE(ABORT, 'forced complete root failure'); END`,
			want: "forced complete root failure",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
			staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
			if err != nil {
				t.Fatalf("stage release: %v", err)
			}
			request := networkReleaseTestCompleteRequest(begin, staged.Release)
			before := networkReplaceTestRows(t, connection)
			mustProjectStoreReadExec(t, connection, test.statement)
			_, err = store.CompleteProjectNetworkRelease(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want %q", test.name, err, test.want)
			}
			assertNetworkReleaseBeginRollback(t, store, connection, before, 11)
		})
	}

	t.Run("tampered hidden history", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		before := networkReplaceTestRows(t, connection)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER corrupt_network_release_complete_history
			AFTER UPDATE OF revision ON network_state
			BEGIN
				UPDATE loopback_address_leases SET ensure_evidence = 'tampered history'
				WHERE source_project_id = 'project-alpha';
			END`)
		_, err = store.CompleteProjectNetworkRelease(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "completed quarantine differs") {
			t.Fatalf("tampered history error = %v", err)
		}
		assertNetworkReleaseBeginRollback(t, store, connection, before, 11)
	})
}

// TestStoreCompleteProjectNetworkReleaseConcurrentRetriesAllocateOnce verifies one completion sequence under contention.
func TestStoreCompleteProjectNetworkReleaseConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _, _, _, begin, _ := newNetworkReleaseTestHarness(t, 4)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("stage release: %v", err)
	}
	request := networkReleaseTestCompleteRequest(begin, staged.Release)
	start := make(chan struct{})
	results := make(chan struct {
		result ProjectNetworkReleaseMutationResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.CompleteProjectNetworkRelease(context.Background(), request)
			results <- struct {
				result ProjectNetworkReleaseMutationResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent Complete errors = %v and %v", first.err, second.err)
	}
	if first.result.Replayed == second.result.Replayed ||
		!reflect.DeepEqual(first.result.Record, second.result.Record) ||
		!reflect.DeepEqual(first.result.Release, second.result.Release) {
		t.Fatalf("concurrent Complete results = %#v and %#v", first.result, second.result)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 12 {
		t.Fatalf("Harbor sequence after concurrent Complete = %d, want 12", highWater)
	}
}

// TestStoreCompleteProjectNetworkReleaseValidatesClonesAndCancelsBeforeWriting verifies the public completion boundary.
func TestStoreCompleteProjectNetworkReleaseValidatesClonesAndCancelsBeforeWriting(t *testing.T) {
	invalid := releaseContractTestCompleteRequest()
	invalid.CompletionGeneration = 0
	var absentStore *Store
	if _, err := absentStore.CompleteProjectNetworkRelease(context.Background(), invalid); err == nil {
		t.Fatal("invalid Complete reached nil storage")
	}

	t.Run("pre-canceled", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		before := networkReplaceTestRows(t, connection)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = store.CompleteProjectNetworkRelease(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled error = %v", err)
		}
		assertNetworkReleaseBeginRollback(t, store, connection, before, 11)
	})

	t.Run("queued cancellation", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		before := networkReplaceTestRows(t, connection)
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		base, cancel := context.WithCancel(context.Background())
		ctx := &networkReleaseSignalContext{Context: base, reached: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := store.CompleteProjectNetworkRelease(ctx, request)
			result <- err
		}()
		<-ctx.reached
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error = %v", err)
		}
		store.mutations.permit <- struct{}{}
		released = true
		assertNetworkReleaseBeginRollback(t, store, connection, before, 11)
	})

	t.Run("queued caller mutation", func(t *testing.T) {
		store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
		if err != nil {
			t.Fatalf("stage release: %v", err)
		}
		request := networkReleaseTestCompleteRequest(begin, staged.Release)
		expected := cloneCompleteProjectNetworkReleaseRequest(request)
		expectedDigest := projectNetworkReleaseSetDigest(expected.Releases)
		beforeMarker := networkReleaseTestMarker(t, networkReplaceTestRows(t, connection).Releases, expected.OperationID)
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		ctx := &networkReleaseSignalContext{Context: context.Background(), reached: make(chan struct{})}
		result := make(chan struct {
			value ProjectNetworkReleaseMutationResult
			err   error
		}, 1)
		go func() {
			value, err := store.CompleteProjectNetworkRelease(ctx, request)
			result <- struct {
				value ProjectNetworkReleaseMutationResult
				err   error
			}{value: value, err: err}
		}()
		<-ctx.reached
		request.ReleaseEvidence = "caller-mutated completion evidence"
		request.Releases[0].ReleaseEvidence = "caller-mutated release evidence"
		request.Releases[0].Lease.Key.SecondaryID = "caller-mutated"
		store.mutations.permit <- struct{}{}
		released = true
		completed := <-result
		if completed.err != nil {
			t.Fatalf("Complete after queued caller mutation: %v", completed.err)
		}
		if completed.value.Release.Completion == nil ||
			completed.value.Release.Completion.Evidence != expected.ReleaseEvidence ||
			completed.value.Release.Completion.ReleaseSetDigest != expectedDigest {
			t.Fatalf("Complete persisted caller mutation = %#v", completed.value.Release)
		}
		marker := networkReleaseTestMarker(t, networkReplaceTestRows(t, connection).Releases, expected.OperationID)
		if !projectNetworkReleaseCompletedRowMatches(marker, beforeMarker, expected, expectedDigest) {
			t.Fatalf("queued Complete marker = %#v", marker)
		}
	})
}

// networkReleaseSignalContext exposes the post-clone cancellation check as a deterministic test barrier.
type networkReleaseSignalContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

// Err signals that request canonicalization completed before reporting the embedded context state.
func (ctx *networkReleaseSignalContext) Err() error {
	ctx.once.Do(func() { close(ctx.reached) })
	return ctx.Context.Err()
}

// newNetworkReleaseTestHarness prepares initialized network state and one running unregister owner.
func newNetworkReleaseTestHarness(
	t *testing.T,
	maximumConnections int,
) (*Store, *gorm.DB, *OperationJournal, OperationRecord, BeginProjectNetworkReleaseRequest, InitializeNetworkRequest) {
	t.Helper()
	store, connection, _, initialization := newNetworkReplaceTestHarness(t, maximumConnections)
	project, err := store.Project(context.Background(), "project-alpha")
	if err != nil {
		t.Fatalf("read release project: %v", err)
	}
	journal, running, at := projectStoreMutationRunningUnregister(
		t,
		store,
		project.Project,
		"operation-release-alpha",
	)
	updatedProject, err := store.Project(context.Background(), project.Project.ID)
	if err != nil {
		t.Fatalf("read updated release project: %v", err)
	}
	request := BeginProjectNetworkReleaseRequest{
		ProjectID:                 project.Project.ID,
		OperationID:               running.Operation.ID,
		ExpectedNetworkRevision:   7,
		ExpectedProjectRevision:   updatedProject.Revision,
		ExpectedOperationRevision: running.Revision,
		BeginGeneration:           100,
		At:                        at,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Begin fixture Validate() error = %v", err)
	}
	return store, connection, journal, running, request, initialization
}

// networkReleaseTestOperation reads one operation and its revision through the journal authority.
func networkReleaseTestOperation(t *testing.T, store *Store, operationID domain.OperationID) OperationRecord {
	t.Helper()
	record, err := projectStoreMutationJournal(store).Operation(context.Background(), operationID)
	if err != nil {
		t.Fatalf("read operation %q: %v", operationID, err)
	}
	return record
}

// networkReleaseTestCompleteRequest returns exact release facts for the durable leases captured by Begin.
func networkReleaseTestCompleteRequest(
	begin BeginProjectNetworkReleaseRequest,
	release ProjectNetworkReleaseRecord,
) CompleteProjectNetworkReleaseRequest {
	completedAt := begin.At.Add(5 * time.Minute)
	releases := make([]NetworkLeaseRelease, 0, len(release.ActiveLeases))
	for index, ensure := range release.ActiveLeases {
		releasedAt := begin.At.Add(time.Duration(index+1) * time.Minute)
		releases = append(releases, NetworkLeaseRelease{
			Lease:             ensure.Lease,
			ReleaseGeneration: ensure.Generation + uint64(index) + 100,
			ReleaseEvidence:   "verified address release",
			ReleasedAt:        releasedAt,
			QuarantinedAt:     releasedAt,
			ReuseAfter:        completedAt.Add(time.Hour),
			QuarantineReason:  "project unregister pending safe reuse",
		})
	}
	slices.SortFunc(releases, func(left, right NetworkLeaseRelease) int {
		if networkLeaseLess(left.Lease, right.Lease) {
			return -1
		}
		if networkLeaseLess(right.Lease, left.Lease) {
			return 1
		}
		return 0
	})
	return CompleteProjectNetworkReleaseRequest{
		ProjectID:                 begin.ProjectID,
		OperationID:               begin.OperationID,
		ExpectedNetworkRevision:   11,
		ExpectedProjectRevision:   begin.ExpectedProjectRevision,
		ExpectedOperationRevision: begin.ExpectedOperationRevision,
		ExpectedBeginGeneration:   begin.BeginGeneration,
		CompletionGeneration:      begin.BeginGeneration + 1,
		Releases:                  releases,
		ReleaseEvidence:           "verified route withdrawal and host teardown",
		At:                        completedAt,
	}
}

// networkReleaseTestMarker returns the one durable marker owned by an operation.
func networkReleaseTestMarker(
	t *testing.T,
	rows []models.NetworkProjectRelease,
	operationID domain.OperationID,
) models.NetworkProjectRelease {
	t.Helper()
	for _, row := range rows {
		if row.OperationId == string(operationID) {
			return row
		}
	}
	t.Fatalf("network release marker %q is missing", operationID)
	return models.NetworkProjectRelease{}
}

// networkReleaseTestConflictLabel requires the typed release conflict and one fixed difference label.
func networkReleaseTestConflictLabel(label string) func(error) bool {
	return func(err error) bool {
		var conflict *ProjectNetworkReleaseConflictError
		return errors.As(err, &conflict) && conflict.Difference == label
	}
}

// assertNetworkReleaseBeginRollback proves a failed Begin restores exact rows and its pre-call global sequence.
func assertNetworkReleaseBeginRollback(
	t *testing.T,
	store *Store,
	connection *gorm.DB,
	before networkModelRows,
	wantSequence domain.Sequence,
) {
	t.Helper()
	if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("failed Begin changed durable network rows")
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != wantSequence {
		t.Fatalf("Harbor sequence after failed Begin = %d, want %d", highWater, wantSequence)
	}
}

// networkReleaseTestOperationCount returns one exact operation identity count after rollback assertions.
func networkReleaseTestOperationCount(t *testing.T, connection *gorm.DB, operationID domain.OperationID) int64 {
	t.Helper()
	var count int64
	if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Count(&count).Error; err != nil {
		t.Fatalf("count operation %q: %v", operationID, err)
	}
	return count
}
