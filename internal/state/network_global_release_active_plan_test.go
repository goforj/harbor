package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestReadActiveGlobalNetworkReleasePlanFindsAndClones verifies discovery returns the validated singleton without altering durable state.
func TestReadActiveGlobalNetworkReleasePlanFindsAndClones(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	expected, expectedFound, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
	if err != nil || !expectedFound {
		t.Fatalf("read staged global network release plan: %#v, %t, %v", expected, expectedFound, err)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	plan, found, err := journal.ReadActiveGlobalNetworkReleasePlan(context.Background())
	if err != nil || !found {
		t.Fatalf("ReadActiveGlobalNetworkReleasePlan() = %#v, %t, %v", plan, found, err)
	}
	if !reflect.DeepEqual(plan.Operation, expected.Operation) || plan.Phase != GlobalNetworkReleasePlanPhaseRuntimeRelease || plan.CheckpointRevision != staged.Revision || !reflect.DeepEqual(plan.Authority, expected.Authority) {
		t.Fatalf("active global network release plan = %#v", plan)
	}
	plan.Authority.Root.CertificatePEM[0] ^= 1
	plan.Authority.LoopbackTargets[0].ObservationFingerprint = ""
	replayed, replayedFound, err := journal.ReadActiveGlobalNetworkReleasePlan(context.Background())
	if err != nil || !replayedFound || !reflect.DeepEqual(replayed.Authority, request.Authority) {
		t.Fatalf("replayed active plan = %#v, %t, %v", replayed, replayedFound, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestReadActiveGlobalNetworkReleasePlanReturnsAbsentOnlyWithoutOwnerOrPlan verifies the empty state is the sole benign absence.
func TestReadActiveGlobalNetworkReleasePlanReturnsAbsentOnlyWithoutOwnerOrPlan(t *testing.T) {
	journal, connection, _ := newGlobalNetworkReleaseStageFixture(t)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	plan, found, err := journal.ReadActiveGlobalNetworkReleasePlan(context.Background())
	if err != nil || found || !reflect.DeepEqual(plan, GlobalNetworkReleasePlanRecord{}) {
		t.Fatalf("ReadActiveGlobalNetworkReleasePlan() = %#v, %t, %v", plan, found, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestReadActiveGlobalNetworkReleasePlanRejectsInvalidBoundaries verifies discovery refuses incomplete or drifted release authority.
func TestReadActiveGlobalNetworkReleasePlanRejectsInvalidBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest)
	}{
		{
			name: "active owner missing plan",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "DELETE FROM network_global_release_plans WHERE id = 1")
			},
		},
		{
			name: "plan without active owner",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE operations SET state = 'succeeded', phase = 'completed' WHERE id = ?", request.Operation.ID)
			},
		},
		{
			name: "queued owner with plan",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE operations SET state = 'queued', phase = 'queued' WHERE id = ?", request.Operation.ID)
			},
		},
		{
			name: "approval owner with plan",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE operations SET state = 'requires_approval', phase = 'awaiting approval' WHERE id = ?", request.Operation.ID)
			},
		},
		{
			name: "wrong operation kind with plan",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE operations SET kind = 'network.setup' WHERE id = ?", request.Operation.ID)
			},
		},
		{
			name: "project-owned operation with plan",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE operations SET project_id = 'project-foreign' WHERE id = ?", request.Operation.ID)
			},
		},
		{
			name: "foreign plan owner",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "PRAGMA foreign_keys = OFF")
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET operation_id = 'operation-foreign' WHERE id = 1")
			},
		},
		{
			name: "authority digest",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET authority_digest = ? WHERE id = 1", strings.Repeat("a", 64))
			},
		},
		{
			name: "current network authority",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_state SET stage = 'resolver' WHERE id = 1")
			},
		},
		{
			name: "checkpoint",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET checkpoint_revision = 999 WHERE id = 1")
			},
		},
		{
			name: "retained high-water bound",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE harbor_state SET sequence = sequence - 1 WHERE id = 1")
			},
		},
		{
			name: "project quiescence",
			mutate: func(t *testing.T, connection *gorm.DB, request StageGlobalNetworkReleaseRequest) {
				globalNetworkReleasePlanInsertSequenceProject(t, connection, "project-active-boundary", 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
			if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			test.mutate(t, connection, request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			_, found, err := journal.ReadActiveGlobalNetworkReleasePlan(context.Background())
			var corrupt *CorruptStateError
			if found || !errors.As(err, &corrupt) {
				t.Fatalf("ReadActiveGlobalNetworkReleasePlan() = found %t, error %v", found, err)
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestReadActiveGlobalNetworkReleasePlanCancellation verifies canceled reads cannot observe or alter release authority.
func TestReadActiveGlobalNetworkReleasePlanCancellation(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, found, err := journal.ReadActiveGlobalNetworkReleasePlan(ctx)
	if found || !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadActiveGlobalNetworkReleasePlan() = found %t, error %v", found, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}
