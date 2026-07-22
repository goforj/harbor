package state

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// TestAdvanceGlobalNetworkReleaseRuntimeRequestValidate rejects malformed caller fences before writer admission.
func TestAdvanceGlobalNetworkReleaseRuntimeRequestValidate(t *testing.T) {
	valid := AdvanceGlobalNetworkReleaseRuntimeRequest{
		OperationID:        "operation-global-release",
		CheckpointRevision: 1,
		NetworkRevision:    1,
	}
	tests := []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseRuntimeRequest)
	}{
		{
			name: "operation ID",
			mutate: func(request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.OperationID = ""
			},
		},
		{
			name: "zero checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.CheckpointRevision = 0
			},
		},
		{
			name: "overflow checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.CheckpointRevision = domain.Sequence(math.MaxUint64)
			},
		},
		{
			name: "zero network",
			mutate: func(request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.NetworkRevision = 0
			},
		},
		{
			name: "overflow network",
			mutate: func(request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.NetworkRevision = domain.Sequence(math.MaxUint64)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseRuntimeRequest.Validate() error = nil")
			}
		})
	}
}

// TestAdvanceGlobalNetworkReleaseRuntimeAdvancesAndReplays verifies one sequence is allocated and exact retries are read-only.
func TestAdvanceGlobalNetworkReleaseRuntimeAdvancesAndReplays(t *testing.T) {
	journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	stagedPlan, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), stage.Operation.ID)
	if err != nil || !found {
		t.Fatalf("read staged plan = %#v, %t, %v", stagedPlan, found, err)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	request := validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)
	advanced, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request)
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseRuntime() error = %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseLowPorts || advanced.CheckpointRevision != staged.Revision+1 {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if !reflect.DeepEqual(before["operations"], after["operations"]) || !reflect.DeepEqual(before["operation_transitions"], after["operation_transitions"]) {
		t.Fatalf("advance changed fixed operation history\nbefore: %#v\nafter: %#v", before, after)
	}
	globalNetworkReleaseRuntimeCheckpointAssertPlanOnlyChange(t, before, after, staged.Revision)
	advanced.Authority.Root.CertificatePEM[0] ^= 1
	replayed, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed.Authority, stagedPlan.Authority) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
}

// TestAdvanceGlobalNetworkReleaseRuntimeRejectsInvalidBoundariesWithoutMutation covers caller, phase, authority, and CAS fences.
func TestAdvanceGlobalNetworkReleaseRuntimeRejectsInvalidBoundariesWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest)
	}{
		{
			name: "operation ID",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.OperationID = "operation-foreign"
			},
		},
		{
			name: "checkpoint revision",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network revision",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				request.NetworkRevision++
			},
		},
		{
			name: "authority drift",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_state SET stage = 'resolver' WHERE id = 1")
			},
		},
		{
			name: "checkpoint corruption",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET checkpoint_revision = 999 WHERE id = 1")
			},
		},
		{
			name: "other plan phase",
			mutate: func(t *testing.T, connection *gorm.DB, request *AdvanceGlobalNetworkReleaseRuntimeRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE harbor_state SET sequence = sequence + 1 WHERE id = 1")
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET phase = 'resolver', checkpoint_revision = checkpoint_revision + 1 WHERE id = 1")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			request := validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)
			test.mutate(t, connection, &request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseRuntime() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseRuntimeRejectsStaleReplayAndCancellation verifies replay remains exact and canceled callers do not enter the writer.
func TestAdvanceGlobalNetworkReleaseRuntimeRejectsStaleReplayAndCancellation(t *testing.T) {
	journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)
	if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request); err != nil {
		t.Fatalf("advance: %v", err)
	}
	stale := request
	stale.CheckpointRevision++
	before := globalNetworkReleaseStageSnapshot(t, connection)
	if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), stale); err == nil {
		t.Fatal("stale replay error = nil")
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled advance error = %v", err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestAdvanceGlobalNetworkReleaseRuntimeReplayRevalidatesAuthorityAndQuiescence proves idempotency never bypasses current release safety checks.
func TestAdvanceGlobalNetworkReleaseRuntimeReplayRevalidatesAuthorityAndQuiescence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, connection *gorm.DB)
	}{
		{
			name: "current authority",
			mutate: func(t *testing.T, connection *gorm.DB) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_state SET stage = 'resolver' WHERE id = 1")
			},
		},
		{
			name: "project quiescence",
			mutate: func(t *testing.T, connection *gorm.DB) {
				globalNetworkReleasePlanInsertSequenceProject(t, connection, "project-replay-drift", 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			request := validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)
			if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request); err != nil {
				t.Fatalf("advance: %v", err)
			}
			test.mutate(t, connection)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseRuntime() replay error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseRuntimeConcurrentReplay proves competing exact callers allocate only one checkpoint sequence.
func TestAdvanceGlobalNetworkReleaseRuntimeConcurrentReplay(t *testing.T) {
	journal, _, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)
	const callers = 8
	results := make(chan GlobalNetworkReleasePlanRecord, callers)
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), request)
			results <- result
			errorsByCaller <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent advance error = %v", err)
		}
	}
	all := make([]GlobalNetworkReleasePlanRecord, 0, callers)
	for result := range results {
		if result.Phase != GlobalNetworkReleasePlanPhaseLowPorts || result.CheckpointRevision != staged.Revision+1 {
			t.Fatalf("concurrent result = %#v", result)
		}
		all = append(all, result)
	}
	all[0].Authority.Root.CertificatePEM[0] ^= 1
	for index := 1; index < len(all); index++ {
		if all[index].Authority.Root.CertificatePEM[0] == all[0].Authority.Root.CertificatePEM[0] {
			t.Fatal("concurrent advance results share authority certificate bytes")
		}
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != staged.Revision+1 {
		t.Fatalf("sequence after concurrent advance = %d, want %d", sequence, staged.Revision+1)
	}
}

// TestAdvanceGlobalNetworkReleaseRuntimeRollsBackLateFailures proves sequence allocation and plan updates remain atomic through post-validation.
func TestAdvanceGlobalNetworkReleaseRuntimeRollsBackLateFailures(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name: "sequence allocation",
			trigger: `CREATE TRIGGER fail_global_release_runtime_sequence
				BEFORE UPDATE OF sequence ON harbor_state
				BEGIN SELECT RAISE(ABORT, 'forced sequence allocation failure'); END`,
		},
		{
			name: "plan update",
			trigger: `CREATE TRIGGER fail_global_release_runtime_update
				BEFORE UPDATE OF phase ON network_global_release_plans
				WHEN NEW.phase = 'low_ports'
				BEGIN SELECT RAISE(ABORT, 'forced runtime advance update failure'); END`,
		},
		{
			name: "post validation",
			trigger: `CREATE TRIGGER corrupt_global_release_runtime_post_validation
				AFTER UPDATE OF phase ON network_global_release_plans
				WHEN NEW.phase = 'low_ports'
				BEGIN UPDATE network_global_release_plans SET phase = 'resolver' WHERE id = NEW.id; END`,
		},
		{
			name: "checkpoint sequence owner collision",
			trigger: `CREATE TRIGGER collide_global_release_runtime_checkpoint
				AFTER UPDATE OF phase ON network_global_release_plans
				WHEN NEW.phase = 'low_ports'
				BEGIN
					INSERT INTO operations (
						id,
						intent_id,
						kind,
						state,
						phase,
						requested_at,
						revision
					) VALUES (
						'operation-runtime-checkpoint-collision',
						'intent-runtime-checkpoint-collision',
						'maintenance.run',
						'queued',
						'queued',
						'2026-07-22T00:00:00Z',
						NEW.checkpoint_revision
					);
				END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			globalNetworkReleaseStageExec(t, connection, test.trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision)); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseRuntime() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseRuntimeRequest returns the exact staging fence consumed by the runtime owner.
func validAdvanceGlobalNetworkReleaseRuntimeRequest(stage StageGlobalNetworkReleaseRequest, checkpoint domain.Sequence) AdvanceGlobalNetworkReleaseRuntimeRequest {
	return AdvanceGlobalNetworkReleaseRuntimeRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: checkpoint,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
	}
}

// globalNetworkReleaseRuntimeCheckpointAssertPlanOnlyChange proves the runtime checkpoint alters only its phase and checkpoint revision.
func globalNetworkReleaseRuntimeCheckpointAssertPlanOnlyChange(t *testing.T, before map[string][]map[string]any, after map[string][]map[string]any, prior domain.Sequence) {
	t.Helper()
	if len(before["network_global_release_plans"]) != 1 || len(after["network_global_release_plans"]) != 1 {
		t.Fatalf("global release plan rows changed unexpectedly\nbefore: %#v\nafter: %#v", before, after)
	}
	for key, value := range before["network_global_release_plans"][0] {
		if key == "phase" || key == "checkpoint_revision" {
			continue
		}
		if !reflect.DeepEqual(value, after["network_global_release_plans"][0][key]) {
			t.Fatalf("global release plan column %q changed from %#v to %#v", key, value, after["network_global_release_plans"][0][key])
		}
	}
	if after["network_global_release_plans"][0]["phase"] != string(GlobalNetworkReleasePlanPhaseLowPorts) || after["network_global_release_plans"][0]["checkpoint_revision"] != int64(prior+1) {
		t.Fatalf("checkpoint plan row = %#v", after["network_global_release_plans"][0])
	}
}
