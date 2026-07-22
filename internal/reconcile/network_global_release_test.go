package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
)

// TestGlobalNetworkReleaseRecoverResumesOnlyRuntimeCheckpoints proves recovery does not rebuild authority or run later phases.
func TestGlobalNetworkReleaseRecoverResumesOnlyRuntimeCheckpoints(t *testing.T) {
	for _, test := range []struct {
		name  string
		found bool
		phase state.GlobalNetworkReleasePlanPhase
		calls int
	}{
		{
			name:  "absent plan",
			calls: 0,
		},
		{
			name:  "runtime release",
			found: true,
			phase: state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
			calls: 1,
		},
		{
			name:  "low ports",
			found: true,
			phase: state.GlobalNetworkReleasePlanPhaseLowPorts,
			calls: 1,
		},
		{
			name:  "later phase",
			found: true,
			phase: state.GlobalNetworkReleasePlanPhaseResolver,
			calls: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := testGlobalNetworkReleaseOperation(t)
			journal := &testGlobalNetworkReleaseJournal{
				found: test.found,
				plan: state.GlobalNetworkReleasePlanRecord{
					Operation: operation,
					Phase:     test.phase,
				},
			}
			runtime := &testGlobalNetworkReleaseRuntime{}
			coordinator := &GlobalNetworkReleaseCoordinator{
				journal: journal,
				runtime: runtime,
			}
			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if runtime.calls != test.calls {
				t.Fatalf("ReleaseNetworkRuntime() calls = %d, want %d", runtime.calls, test.calls)
			}
		})
	}
}

// TestGlobalNetworkReleaseRecoverFailsClosed proves durable-read and runtime failures are returned unchanged through recovery.
func TestGlobalNetworkReleaseRecoverFailsClosed(t *testing.T) {
	want := errors.New("runtime failed")
	operation := testGlobalNetworkReleaseOperation(t)
	journal := &testGlobalNetworkReleaseJournal{
		found: true,
		plan: state.GlobalNetworkReleasePlanRecord{
			Operation: operation,
			Phase:     state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
		},
	}
	runtime := &testGlobalNetworkReleaseRuntime{err: want}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		runtime: runtime,
	}
	if err := coordinator.Recover(t.Context()); !errors.Is(err, want) {
		t.Fatalf("Recover() error = %v, want %v", err, want)
	}
}

// TestGlobalNetworkReleaseRecoverRejectsMalformedActivePlans proves recovery never mutates runtime through an invalid durable owner or checkpoint.
func TestGlobalNetworkReleaseRecoverRejectsMalformedActivePlans(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "invalid plan phase",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Phase = "invalid"
			},
		},
		{
			name: "queued operation",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.State = domain.OperationQueued
				plan.Operation.Operation.Phase = string(domain.OperationQueued)
				plan.Operation.Operation.StartedAt = nil
			},
		},
		{
			name: "wrong operation phase",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.Phase = "wrong phase"
			},
		},
		{
			name: "zero operation revision",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Revision = 0
			},
		},
		{
			name: "operation revision overflow",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Revision = domain.MaximumSequence + 1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := state.GlobalNetworkReleasePlanRecord{
				Operation: testGlobalNetworkReleaseOperation(t),
				Phase:     state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
			}
			test.mutate(&plan)
			journal := &testGlobalNetworkReleaseJournal{
				found: true,
				plan:  plan,
			}
			runtime := &testGlobalNetworkReleaseRuntime{}
			coordinator := &GlobalNetworkReleaseCoordinator{
				journal: journal,
				runtime: runtime,
			}
			if err := coordinator.Recover(t.Context()); err == nil {
				t.Fatal("Recover() error = nil")
			}
			if runtime.calls != 0 {
				t.Fatalf("ReleaseNetworkRuntime() calls = %d, want zero", runtime.calls)
			}
		})
	}
}

// TestGlobalNetworkReleaseRecoverHonorsCancellation proves cancellation prevents a durable read or runtime mutation.
func TestGlobalNetworkReleaseRecoverHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	journal := &testGlobalNetworkReleaseJournal{}
	runtime := &testGlobalNetworkReleaseRuntime{}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		runtime: runtime,
	}
	if err := coordinator.Recover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recover() error = %v, want context cancellation", err)
	}
	if journal.reads != 0 || runtime.calls != 0 {
		t.Fatalf("reads = %d, calls = %d, want zero", journal.reads, runtime.calls)
	}
}

// TestGlobalNetworkReleaseResumeRejectsDifferentRequester proves an intent cannot transfer release authority between users.
func TestGlobalNetworkReleaseResumeRejectsDifferentRequester(t *testing.T) {
	operation := testGlobalNetworkReleaseOperation(t)
	journal := &testGlobalNetworkReleaseJournal{
		found: true,
		plan: state.GlobalNetworkReleasePlanRecord{
			Operation: operation,
			Phase:     state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
		},
	}
	runtime := &testGlobalNetworkReleaseRuntime{}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		runtime: runtime,
	}
	if _, err := coordinator.resume(t.Context(), operation.Operation.ID, "different-user"); err == nil {
		t.Fatal("resume() unexpectedly accepted a different requester")
	}
	if runtime.calls != 0 {
		t.Fatalf("ReleaseNetworkRuntime() calls = %d, want zero", runtime.calls)
	}
}

// TestGlobalNetworkReleaseStartRejectsInvalidAndCanceledRequestsBeforeAnyDependency proves admission has no destructive side effects.
func TestGlobalNetworkReleaseStartRejectsInvalidAndCanceledRequestsBeforeAnyDependency(t *testing.T) {
	journal := &testGlobalNetworkReleaseJournal{}
	runtime := &testGlobalNetworkReleaseRuntime{}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		runtime: runtime,
	}
	if _, err := coordinator.Start(t.Context(), GlobalNetworkReleaseStartRequest{}); err == nil {
		t.Fatal("Start() unexpectedly accepted an empty request")
	}
	if journal.reads != 0 || runtime.calls != 0 {
		t.Fatalf("reads = %d, runtime calls = %d, want zero", journal.reads, runtime.calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := GlobalNetworkReleaseStartRequest{
		OperationID:       "operation-global-release",
		IntentID:          "intent-global-release",
		RequesterIdentity: "501",
	}
	if _, err := coordinator.Start(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context cancellation", err)
	}
	if journal.reads != 0 || runtime.calls != 0 {
		t.Fatalf("after cancellation reads = %d, runtime calls = %d, want zero", journal.reads, runtime.calls)
	}
}

// TestGlobalNetworkReleaseStartRejectsMismatchedIntentReplayBeforeHostObservation proves an intent cannot select another operation.
func TestGlobalNetworkReleaseStartRejectsMismatchedIntentReplayBeforeHostObservation(t *testing.T) {
	operation := testGlobalNetworkReleaseOperation(t)
	operation.Operation.Kind = domain.OperationKindProjectStart
	journal := &testGlobalNetworkReleaseJournal{}
	journal.intent = operation
	journal.intentErr = nil
	runtime := &testGlobalNetworkReleaseRuntime{}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		runtime: runtime,
	}
	request := GlobalNetworkReleaseStartRequest{
		OperationID:       "operation-global-release",
		IntentID:          "intent-global-release",
		RequesterIdentity: "501",
	}
	if _, err := coordinator.Start(t.Context(), request); err == nil {
		t.Fatal("Start() unexpectedly accepted a non-release replay")
	}
	if runtime.calls != 0 {
		t.Fatalf("ReleaseNetworkRuntime() calls = %d, want zero", runtime.calls)
	}
}

// testGlobalNetworkReleaseOperation returns a valid globally-scoped durable operation record.
func testGlobalNetworkReleaseOperation(t *testing.T) state.OperationRecord {
	t.Helper()
	operation, err := domain.NewOperation("operation-global-release", "intent-global-release", domain.OperationKindNetworkRelease, "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	started := operation.RequestedAt
	operation.State = domain.OperationRunning
	operation.Phase = globalNetworkReleaseRuntimeOperationPhase
	operation.StartedAt = &started
	return state.OperationRecord{
		Operation: operation,
		Revision:  1,
	}
}

// testGlobalNetworkReleaseJournal records the recovery plan read without accepting staging.
type testGlobalNetworkReleaseJournal struct {
	mu        sync.Mutex
	found     bool
	plan      state.GlobalNetworkReleasePlanRecord
	err       error
	reads     int
	intent    state.OperationRecord
	intentErr error
}

// OperationByIntent is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	if journal.intentErr != nil {
		return state.OperationRecord{}, journal.intentErr
	}
	if journal.intent.Operation.ID != "" {
		return journal.intent, nil
	}
	return state.OperationRecord{}, &state.OperationIntentNotFoundError{}
}

// StageGlobalNetworkRelease is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected stage")
}

// ReadActiveGlobalNetworkReleasePlan returns the configured recovery receipt.
func (journal *testGlobalNetworkReleaseJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.reads++
	return journal.plan, journal.found, journal.err
}

// ReadGlobalNetworkReleasePlan is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	return journal.plan, journal.found, journal.err
}

// AdvanceGlobalNetworkReleaseLowPorts is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected low-port advance")
}

// AdvanceGlobalNetworkReleaseResolver is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected resolver advance")
}

// AdvanceGlobalNetworkReleaseTrust is not exercised by recovery tests.
func (journal *testGlobalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected trust advance")
}

// testGlobalNetworkReleaseRuntime records runtime-release requests.
type testGlobalNetworkReleaseRuntime struct {
	calls int
	err   error
}

// ReleaseNetworkRuntime records one requested durable runtime checkpoint.
func (runtime *testGlobalNetworkReleaseRuntime) ReleaseNetworkRuntime(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, error) {
	runtime.calls++
	return state.GlobalNetworkReleasePlanRecord{}, runtime.err
}

// TestGlobalNetworkReleaseStartStagesCompleteAuthority proves a fresh release
// binds every independently observed full-network fact before runtime teardown.
func TestGlobalNetworkReleaseStartStagesCompleteAuthority(t *testing.T) {
	fixture := newGlobalNetworkReleaseStartFixture(t)
	got, err := fixture.coordinator.Start(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got != fixture.staged {
		t.Fatalf("Start() = %#v, want %#v", got, fixture.staged)
	}
	if got := strings.Join(fixture.calls, ","); got != "intent,state,root,projection,ownership,low,resolver,trust,loopback,loopback,loopback,loopback,loopback,loopback,loopback,loopback,projects,stage,read,runtime" {
		t.Fatalf("call order = %s", got)
	}
	if fixture.stage.Authority.TrustDisposition != state.GlobalNetworkReleaseTrustOwned || len(fixture.stage.Authority.LoopbackTargets) != len(fixture.runtime.Network.Pool.Candidates()) || !reflect.DeepEqual(fixture.stage.Authority.ProjectRevisions, fixture.projects) {
		t.Fatalf("staged authority = %#v", fixture.stage.Authority)
	}
	if fixture.stage.Operation.RequestedAt.Before(fixture.projection.NetworkUpdatedAt) {
		t.Fatalf("requested at %s precedes network update %s", fixture.stage.Operation.RequestedAt, fixture.projection.NetworkUpdatedAt)
	}
	if fixture.low.request != fixture.low.observation.Request || fixture.resolver.request != fixture.resolver.observation.Request || !sameNetworkDataPlaneSetupTrustRequest(fixture.trust.request, fixture.trust.observedRequest) {
		t.Fatalf("observer requests = low %#v resolver %#v trust %#v", fixture.low.request, fixture.resolver.request, fixture.trust.observedRequest)
	}
	if !reflect.DeepEqual(fixture.loopback.addresses, fixture.runtime.Network.Pool.Candidates()) || fixture.source.sequence != fixture.runtime.Snapshot.Sequence || fixture.runtimeRelease.operationID != fixture.staged.Operation.ID {
		t.Fatalf("loopbacks=%v sequence=%d runtime=%q", fixture.loopback.addresses, fixture.source.sequence, fixture.runtimeRelease.operationID)
	}
	before := append([]byte(nil), fixture.stage.Authority.Root.CertificatePEM...)
	fixture.roots.root.CertificatePEM[0] ^= 0xff
	if !reflect.DeepEqual(fixture.stage.Authority.Root.CertificatePEM, before) {
		t.Fatal("staged root aliases observer bytes")
	}
}

// TestGlobalNetworkReleaseStartAcceptsIdenticalPreexistingTrust proves a matching unowned root is retained rather than claimed.
func TestGlobalNetworkReleaseStartAcceptsIdenticalPreexistingTrust(t *testing.T) {
	fixture := newGlobalNetworkReleaseStartFixture(t)
	fixture.trust.owned = false
	if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if fixture.stage.Authority.TrustDisposition != state.GlobalNetworkReleaseTrustPreexistingUnowned {
		t.Fatalf("trust disposition = %q", fixture.stage.Authority.TrustDisposition)
	}
}

// TestGlobalNetworkReleaseStartStopsBeforeStageOnAuthorityFailure proves every observer and source boundary is fail-closed.
func TestGlobalNetworkReleaseStartStopsBeforeStageOnAuthorityFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		stages int
		mutate func(*globalNetworkReleaseStartFixture)
	}{
		{
			name: "runtime state error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.source.err = errors.New("state")
			},
		},
		{
			name: "runtime state invalid",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.runtime.Snapshot.Sequence = 0
			},
		},
		{
			name: "network uninitialized",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.runtime.NetworkInitialized = false
			},
		},
		{
			name: "network non-full",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.runtime.Network.Stage = state.NetworkStageResolver
			},
		},
		{
			name: "project active",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.runtime.Snapshot.Projects[0].State = domain.ProjectReady
			},
		},
		{
			name: "root error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.roots.err = errors.New("root")
			},
		},
		{
			name: "projection error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.projections.err = errors.New("projection")
			},
		},
		{
			name: "projection non-full",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.projection.Stage = state.NetworkStageResolver
			},
		},
		{
			name: "projection requester mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.projection.ConfirmedOwnership.Record.OwnerIdentity = "502"
			},
		},
		{
			name: "ownership error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.ownership.err = errors.New("ownership")
			},
		},
		{
			name: "ownership mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.ownership.observation.Fingerprint = strings.Repeat("f", 64)
			},
		},
		{
			name: "low port error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.low.err = errors.New("low")
			},
		},
		{
			name: "low port request mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.low.observation.Request = lowport.Request{}
			},
		},
		{
			name: "low port nonexact",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.low.observation.Complete = false
			},
		},
		{
			name: "resolver error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.resolver.err = errors.New("resolver")
			},
		},
		{
			name: "resolver request mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.resolver.observation.Request = resolver.Request{}
			},
		},
		{
			name: "resolver nonexact",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.resolver.observation.Complete = false
			},
		},
		{
			name: "trust error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.trust.err = errors.New("trust")
			},
		},
		{
			name: "trust request mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.trust.requestMismatch = true
			},
		},
		{
			name: "trust unsafe foreign",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.trust.owned = false
				fixture.trust.nativeExact = false
			},
		},
		{
			name: "loopback error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.loopback.err = errors.New("loopback")
			},
		},
		{
			name: "loopback nonexact",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.loopback.state = loopback.StateAbsent
			},
		},
		{
			name: "loopback address mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.loopback.addressMismatch = true
			},
		},
		{
			name: "project revisions error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.source.projectsErr = errors.New("projects")
			},
		},
		{
			name: "project revision count drift",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.projects = nil
			},
		},
		{
			name:   "clock before update",
			stages: 1,
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.clock.now = fixture.projection.NetworkUpdatedAt.Add(-time.Second)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseStartFixture(t)
			test.mutate(fixture)
			if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err == nil {
				t.Fatal("Start() error = nil")
			}
			if fixture.stageCalls != test.stages || fixture.runtimeRelease.calls != 0 {
				t.Fatalf("stage/runtime calls = %d/%d, want %d/0", fixture.stageCalls, fixture.runtimeRelease.calls, test.stages)
			}
		})
	}
}

// TestGlobalNetworkReleaseStartReplaySkipsObservers proves a valid stored owner resumes without rebuilding host authority.
func TestGlobalNetworkReleaseStartReplaySkipsObservers(t *testing.T) {
	fixture := newGlobalNetworkReleaseStartFixture(t)
	authority := fixture.expectedAuthority()
	fixture.journal.intent = fixture.staged
	fixture.journal.plan = state.GlobalNetworkReleasePlanRecord{
		Operation: fixture.staged,
		Authority: authority,
		Phase:     state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
	}
	if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := strings.Join(fixture.calls, ","); got != "intent,read,runtime" {
		t.Fatalf("replay calls = %s", got)
	}
	if fixture.stageCalls != 0 {
		t.Fatalf("stage calls = %d, want zero", fixture.stageCalls)
	}
}

// TestGlobalNetworkReleaseStartReplayRejectsMalformedActivePlan proves replay never releases runtime through corrupt durable checkpoints.
func TestGlobalNetworkReleaseStartReplayRejectsMalformedActivePlan(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "invalid phase",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Phase = "invalid"
			},
		},
		{
			name: "queued operation",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.State = domain.OperationQueued
				plan.Operation.Operation.Phase = string(domain.OperationQueued)
				plan.Operation.Operation.StartedAt = nil
			},
		},
		{
			name: "wrong operation phase",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.Phase = "wrong phase"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseStartFixture(t)
			authority := fixture.expectedAuthority()
			fixture.journal.intent = fixture.staged
			fixture.journal.plan = state.GlobalNetworkReleasePlanRecord{
				Operation: fixture.staged,
				Authority: authority,
				Phase:     state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
			}
			test.mutate(&fixture.journal.plan)
			if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err == nil {
				t.Fatal("Start() error = nil")
			}
			if fixture.runtimeRelease.calls != 0 {
				t.Fatalf("runtime calls = %d, want zero", fixture.runtimeRelease.calls)
			}
		})
	}
}

// TestGlobalNetworkReleaseStartStopsAtPostAuthorityBoundary proves staging and checkpoint failures do not conceal later effects.
func TestGlobalNetworkReleaseStartStopsAtPostAuthorityBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseStartFixture)
		want   string
	}{
		{
			name: "stage error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.journal.stageErr = errors.New("stage")
			},
			want: "stage",
		},
		{
			name: "active plan read error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.journal.err = errors.New("read")
			},
			want: "read",
		},
		{
			name: "active plan missing",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.journal.missingAfterStage = true
			},
			want: "read",
		},
		{
			name: "active plan mismatch",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.journal.mismatchAfterStage = true
			},
			want: "read",
		},
		{
			name: "runtime error",
			mutate: func(fixture *globalNetworkReleaseStartFixture) {
				fixture.runtimeRelease.err = errors.New("runtime")
			},
			want: "runtime",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseStartFixture(t)
			test.mutate(fixture)
			if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err == nil {
				t.Fatal("Start() error = nil")
			}
			if test.want != "runtime" && fixture.runtimeRelease.calls != 0 {
				t.Fatalf("runtime calls = %d, want zero", fixture.runtimeRelease.calls)
			}
		})
	}
}

// TestGlobalNetworkReleaseStartRejectsMalformedStagedOperation proves runtime release cannot follow a journal that returns a queued result.
func TestGlobalNetworkReleaseStartRejectsMalformedStagedOperation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseJournal)
	}{
		{
			name: "queued",
			mutate: func(journal *globalNetworkReleaseJournal) {
				journal.queuedResult = true
			},
		},
		{
			name: "wrong phase",
			mutate: func(journal *globalNetworkReleaseJournal) {
				journal.wrongPhaseResult = true
			},
		},
		{
			name: "zero revision",
			mutate: func(journal *globalNetworkReleaseJournal) {
				journal.zeroRevisionResult = true
			},
		},
		{
			name: "revision overflow",
			mutate: func(journal *globalNetworkReleaseJournal) {
				journal.overflowRevisionResult = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseStartFixture(t)
			test.mutate(fixture.journal)
			if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err == nil {
				t.Fatal("Start() error = nil")
			}
			if strings.Contains(strings.Join(fixture.calls, ","), "read") || fixture.runtimeRelease.calls != 0 {
				t.Fatalf("calls = %v; runtime calls = %d", fixture.calls, fixture.runtimeRelease.calls)
			}
		})
	}
}

// globalNetworkReleaseStartFixture provides a valid full authority graph whose individual observers can be rejected independently.
type globalNetworkReleaseStartFixture struct {
	coordinator    *GlobalNetworkReleaseCoordinator
	request        GlobalNetworkReleaseStartRequest
	journal        *globalNetworkReleaseJournal
	source         *globalNetworkReleaseState
	projections    *globalNetworkReleaseProjection
	roots          *globalNetworkReleaseRoots
	ownership      *globalNetworkReleaseOwnership
	low            *globalNetworkReleaseLowPorts
	resolver       *globalNetworkReleaseResolver
	trust          *globalNetworkReleaseTrust
	loopback       *globalNetworkReleaseLoopback
	runtimeRelease *globalNetworkReleaseRuntime
	clock          *globalNetworkReleaseClock
	runtime        state.RuntimeState
	projection     state.NetworkDataPlaneSetupProjection
	projects       []state.NetworkProjectRevision
	staged         state.OperationRecord
	stage          state.StageGlobalNetworkReleaseRequest
	stageCalls     int
	calls          []string
}

// newGlobalNetworkReleaseStartFixture constructs full, stopped, canonical release authority using existing data-plane fixtures.
func newGlobalNetworkReleaseStartFixture(t *testing.T) *globalNetworkReleaseStartFixture {
	t.Helper()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	plan, _ := networkDataPlaneSetupTestTrustPlan(t)
	pool := networkResolverSetupTestPool(t, "127.91.0.8/29")
	policy, err := networkplan.Build(networkplan.Request{
		Platform:             networkplan.PlatformMacOS,
		InstallationID:       identity.InstallationID(plan.TargetOwnership.InstallationID),
		Pool:                 pool,
		AuthorityFingerprint: plan.Root.Fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	target := plan.TargetOwnership
	target.LoopbackPoolPrefix = pool.Prefix().String()
	target.NetworkPolicyFingerprint = policyFingerprint
	trustRequest, err := trust.NewRequestForRequester(target.InstallationID, target.OwnerIdentity, policy.Mechanisms.Trust, plan.Root)
	if err != nil {
		t.Fatal(err)
	}
	owner := networkResolverSetupTestOwnershipObservation(t, target)
	projection := state.NetworkDataPlaneSetupProjection{
		Stage:            state.NetworkStageFull,
		NetworkRevision:  9,
		NetworkUpdatedAt: now,
		ResolverProof: state.NetworkSetupProof{
			Component:  state.NetworkSetupComponentResolver,
			Evidence:   "resolver proof",
			Generation: target.Generation,
			VerifiedAt: now,
		},
		LowPortProof: state.NetworkSetupProof{
			Component:  state.NetworkSetupComponentLowPorts,
			Evidence:   "low port proof",
			Generation: target.Generation,
			VerifiedAt: now,
		},
		Listeners:          networkResolverSetupTestListeners(policy, now),
		ConfirmedOwnership: owner,
	}
	network := networkDataPlaneSetupLifecycleFullRecord(t, networkDataPlaneSetupTestLowPortPlan(t), now, 9)
	network.Pool = pool
	project := domain.ProjectSnapshot{
		ID:        "project-release",
		Name:      "Release",
		Path:      "/tmp/release",
		Slug:      "release",
		State:     domain.ProjectStopped,
		UpdatedAt: now,
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	runtime := state.RuntimeState{
		NetworkInitialized: true,
		Network:            network,
		Snapshot: domain.Snapshot{
			SchemaVersion:     domain.SnapshotSchemaVersion,
			Sequence:          10,
			CapturedAt:        now,
			Projects:          []domain.ProjectSnapshot{project},
			Operations:        []domain.Operation{},
			RecentResourceIDs: []domain.ResourceRef{},
		},
	}
	projects := []state.NetworkProjectRevision{{
		ProjectID: project.ID,
		Revision:  10,
	}}
	fixture := &globalNetworkReleaseStartFixture{
		request: GlobalNetworkReleaseStartRequest{
			OperationID:       "operation-global-release",
			IntentID:          "intent-global-release",
			RequesterIdentity: "501",
		},
		runtime:    runtime,
		projection: projection,
		projects:   projects,
	}
	fixture.journal = &globalNetworkReleaseJournal{
		call:    fixture.call,
		fixture: fixture,
	}
	fixture.source = &globalNetworkReleaseState{
		fixture: fixture,
	}
	fixture.projections = &globalNetworkReleaseProjection{
		fixture: fixture,
	}
	fixture.roots = &globalNetworkReleaseRoots{
		root:    plan.Root,
		fixture: fixture,
	}
	fixture.ownership = &globalNetworkReleaseOwnership{
		observation: owner,
		fixture:     fixture,
	}
	lowRequest, err := lowport.NewRequest(target, policy)
	if err != nil {
		t.Fatal(err)
	}
	fixture.low = &globalNetworkReleaseLowPorts{
		observation: networkDataPlaneSetupLowPortObservation(lowRequest),
		fixture:     fixture,
	}
	fixture.resolver = &globalNetworkReleaseResolver{
		observation: networkResolverSetupTestExactObservation(t, target, policy),
		fixture:     fixture,
	}
	fixture.trust = &globalNetworkReleaseTrust{
		request:     trustRequest,
		fixture:     fixture,
		owned:       true,
		nativeExact: true,
	}
	fixture.loopback = &globalNetworkReleaseLoopback{
		fixture: fixture,
		state:   loopback.StateExact,
	}
	fixture.runtimeRelease = &globalNetworkReleaseRuntime{
		fixture: fixture,
	}
	fixture.clock = &globalNetworkReleaseClock{
		now: now,
	}
	fixture.coordinator = NewGlobalNetworkReleaseCoordinator(
		fixture.journal,
		fixture.source,
		fixture.projections,
		fixture.roots,
		fixture.ownership,
		fixture.low,
		globalNetworkReleaseUnavailableLowPortPlans{},
		func() (GlobalNetworkReleaseLowPortIssuer, error) {
			return nil, errors.New("unexpected release low-port issuer")
		},
		globalNetworkReleaseUnavailableResolverPlans{},
		func() (GlobalNetworkReleaseResolverIssuer, error) {
			return nil, errors.New("unexpected release resolver issuer")
		},
		globalNetworkReleaseUnavailableTrustPlans{},
		func() (GlobalNetworkReleaseTrustIssuer, error) {
			return nil, errors.New("unexpected release trust issuer")
		},
		fixture.resolver,
		fixture.trust,
		fixture.loopback,
		fixture.runtimeRelease,
		networkplan.PlatformMacOS,
		fixture.clock,
	)
	return fixture
}

// globalNetworkReleaseUnavailableLowPortPlans prevents start tests from opening release approval authority.
type globalNetworkReleaseUnavailableLowPortPlans struct{}

// Resolve rejects a capability read that start tests do not exercise.
func (globalNetworkReleaseUnavailableLowPortPlans) Resolve(context.Context, ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	return ticketissuer.LowPortPlan{}, errors.New("unexpected release low-port plan")
}

// globalNetworkReleaseUnavailableResolverPlans prevents start tests from opening resolver approval authority.
type globalNetworkReleaseUnavailableResolverPlans struct{}

// Resolve rejects a capability read that start tests do not exercise.
func (globalNetworkReleaseUnavailableResolverPlans) Resolve(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
	return ticketissuer.ResolverPlan{}, errors.New("unexpected release resolver plan")
}

// globalNetworkReleaseUnavailableTrustPlans prevents start tests from opening release trust authority.
type globalNetworkReleaseUnavailableTrustPlans struct{}

// Resolve rejects a capability read that start tests do not exercise.
func (globalNetworkReleaseUnavailableTrustPlans) Resolve(context.Context, ticketissuer.TrustRequest) (ticketissuer.TrustPlan, error) {
	return ticketissuer.TrustPlan{}, errors.New("unexpected release trust plan")
}

// call appends an observable coordinator boundary.
func (fixture *globalNetworkReleaseStartFixture) call(name string) {
	fixture.calls = append(fixture.calls, name)
}

// expectedAuthority rebuilds the known-valid stored authority for replay tests.
func (fixture *globalNetworkReleaseStartFixture) expectedAuthority() state.GlobalNetworkReleaseAuthority {
	fixture.tStageAuthority()
	return fixture.stage.Authority
}

// tStageAuthority stages once through the normal fixture so the replay record is contract-valid.
func (fixture *globalNetworkReleaseStartFixture) tStageAuthority() {
	if fixture.stage.Operation.ID != "" {
		return
	}
	_, err := fixture.coordinator.Start(context.Background(), fixture.request)
	if err != nil {
		panic(err)
	}
	fixture.calls = nil
	fixture.runtimeRelease.calls = 0
	fixture.stageCalls = 0
}

// globalNetworkReleaseJournal scripts intent, staging, and active-plan reads.
type globalNetworkReleaseJournal struct {
	call                   func(string)
	intent                 state.OperationRecord
	plan                   state.GlobalNetworkReleasePlanRecord
	found                  bool
	err                    error
	stageErr               error
	queuedResult           bool
	wrongPhaseResult       bool
	zeroRevisionResult     bool
	overflowRevisionResult bool
	missingAfterStage      bool
	mismatchAfterStage     bool
	fixture                *globalNetworkReleaseStartFixture
}

// OperationByIntent returns the scripted idempotency record.
func (journal *globalNetworkReleaseJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	journal.call("intent")
	if journal.intent.Operation.ID != "" {
		return journal.intent, nil
	}
	return state.OperationRecord{}, &state.OperationIntentNotFoundError{}
}

// StageGlobalNetworkRelease retains the request and returns a valid staged record.
func (journal *globalNetworkReleaseJournal) StageGlobalNetworkRelease(_ context.Context, request state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	journal.call("stage")
	journal.fixture.stageCalls++
	journal.fixture.stage = request
	if journal.stageErr != nil {
		return state.OperationRecord{}, journal.stageErr
	}
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, err
	}
	if journal.queuedResult {
		return state.OperationRecord{
			Operation: request.Operation,
			Revision:  11,
		}, nil
	}
	running := request.Operation
	started := running.RequestedAt
	running.State = domain.OperationRunning
	running.Phase = globalNetworkReleaseRuntimeOperationPhase
	if journal.wrongPhaseResult {
		running.Phase = "wrong phase"
	}
	running.StartedAt = &started
	journal.fixture.staged = state.OperationRecord{
		Operation: running,
		Revision:  11,
	}
	if journal.zeroRevisionResult {
		journal.fixture.staged.Revision = 0
	}
	if journal.overflowRevisionResult {
		journal.fixture.staged.Revision = domain.MaximumSequence + 1
	}
	journal.plan = state.GlobalNetworkReleasePlanRecord{
		Operation:          journal.fixture.staged,
		Authority:          request.Authority,
		Phase:              state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
		CheckpointRevision: journal.fixture.staged.Revision,
		NetworkRevision:    request.Authority.Projection.NetworkRevision,
		NetworkUpdatedAt:   request.Authority.Projection.NetworkUpdatedAt,
	}
	journal.found = true
	if journal.missingAfterStage {
		journal.found = false
	}
	if journal.mismatchAfterStage {
		journal.plan.Operation.Operation.ID = "other-operation"
	}
	return journal.fixture.staged, nil
}

// ReadActiveGlobalNetworkReleasePlan returns the staged checkpoint.
func (journal *globalNetworkReleaseJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	journal.call("read")
	return journal.plan, journal.found, journal.err
}

// ReadGlobalNetworkReleasePlan returns the fixture plan for the selected operation.
func (journal *globalNetworkReleaseJournal) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	journal.call("read operation")
	if journal.plan.Operation.Operation.ID != operationID {
		return state.GlobalNetworkReleasePlanRecord{}, false, journal.err
	}
	return journal.plan, journal.found, journal.err
}

// AdvanceGlobalNetworkReleaseLowPorts is not exercised by start tests.
func (journal *globalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected low-port advance")
}

// AdvanceGlobalNetworkReleaseResolver is not exercised by start tests.
func (journal *globalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected resolver advance")
}

// AdvanceGlobalNetworkReleaseTrust is not exercised by start tests.
func (journal *globalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected trust advance")
}

// globalNetworkReleaseState scripts coherent durable state and revision reads.
type globalNetworkReleaseState struct {
	fixture     *globalNetworkReleaseStartFixture
	err         error
	projectsErr error
	sequence    domain.Sequence
}

// RuntimeState returns the configured runtime snapshot.
func (source *globalNetworkReleaseState) RuntimeState(context.Context) (state.RuntimeState, error) {
	source.fixture.call("state")
	return source.fixture.runtime, source.err
}

// GlobalNetworkReleaseProjectRevisions returns the configured canonical revision set.
func (source *globalNetworkReleaseState) GlobalNetworkReleaseProjectRevisions(_ context.Context, sequence domain.Sequence) ([]state.NetworkProjectRevision, error) {
	source.fixture.call("projects")
	source.sequence = sequence
	return source.fixture.projects, source.projectsErr
}

// globalNetworkReleaseProjection scripts full policy-bound authority reads.
type globalNetworkReleaseProjection struct {
	fixture *globalNetworkReleaseStartFixture
	err     error
}

// Resolve returns the configured projection.
func (source *globalNetworkReleaseProjection) Resolve(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
	source.fixture.call("projection")
	return source.fixture.projection, source.err
}

// globalNetworkReleaseRoots scripts public-root reads.
type globalNetworkReleaseRoots struct {
	fixture *globalNetworkReleaseStartFixture
	root    certificates.Root
	err     error
}

// PublicRoot returns the configured public root.
func (source *globalNetworkReleaseRoots) PublicRoot() (certificates.Root, error) {
	source.fixture.call("root")
	return source.root, source.err
}

// globalNetworkReleaseOwnership scripts protected ownership reads.
type globalNetworkReleaseOwnership struct {
	fixture     *globalNetworkReleaseStartFixture
	observation ownership.Observation
	err         error
}

// Observe returns the configured ownership observation.
func (source *globalNetworkReleaseOwnership) Observe(context.Context) (ownership.Observation, error) {
	source.fixture.call("ownership")
	return source.observation, source.err
}

// globalNetworkReleaseLowPorts scripts low-port observations.
type globalNetworkReleaseLowPorts struct {
	fixture     *globalNetworkReleaseStartFixture
	observation lowport.Observation
	request     lowport.Request
	err         error
}

// Observe returns the configured low-port observation.
func (source *globalNetworkReleaseLowPorts) Observe(_ context.Context, request lowport.Request) (lowport.Observation, error) {
	source.fixture.call("low")
	source.request = request
	return source.observation, source.err
}

// globalNetworkReleaseResolver scripts resolver observations.
type globalNetworkReleaseResolver struct {
	fixture     *globalNetworkReleaseStartFixture
	observation resolver.Observation
	request     resolver.Request
	err         error
}

// Observe returns the configured resolver observation.
func (source *globalNetworkReleaseResolver) Observe(_ context.Context, request resolver.Request) (resolver.Observation, error) {
	source.fixture.call("resolver")
	source.request = request
	return source.observation, source.err
}

// globalNetworkReleaseTrust scripts exact owned or identical unowned trust observations.
type globalNetworkReleaseTrust struct {
	fixture         *globalNetworkReleaseStartFixture
	request         trust.Request
	owned           bool
	nativeExact     bool
	requestMismatch bool
	observedRequest trust.Request
	err             error
}

// Observe returns an observation bound to the coordinator's freshly-built request.
func (source *globalNetworkReleaseTrust) Observe(_ context.Context, request trust.Request) (trust.Observation, error) {
	source.fixture.call("trust")
	source.observedRequest = request
	entry := trust.Entry{
		Mechanism:              request.Mechanism(),
		NativeID:               "entry",
		CertificateFingerprint: request.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("c", 64),
	}
	if source.owned {
		owner := request.OwnerMarker()
		entry.Owner = &owner
	}
	observation := trust.Observation{
		Request:  request,
		Complete: true,
		Entries:  []trust.Entry{entry},
	}
	if source.requestMismatch {
		observation.Request = trust.Request{}
	}
	observation.Entries[0].NativeExact = source.nativeExact
	return observation, source.err
}

// globalNetworkReleaseLoopback scripts every canonical pool candidate observation.
type globalNetworkReleaseLoopback struct {
	fixture         *globalNetworkReleaseStartFixture
	state           loopback.State
	addressMismatch bool
	addresses       []netip.Addr
	err             error
}

// Observe returns the requested candidate's valid exact fact unless scripted otherwise.
func (source *globalNetworkReleaseLoopback) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	source.fixture.call("loopback")
	source.addresses = append(source.addresses, address)
	observation := networkSetupTestObservation(address)
	if source.addressMismatch {
		observation.Address = address.Next()
	}
	observation.State = source.state
	return observation, source.err
}

// globalNetworkReleaseRuntime records release calls after durable staging.
type globalNetworkReleaseRuntime struct {
	fixture     *globalNetworkReleaseStartFixture
	calls       int
	err         error
	operationID domain.OperationID
}

// ReleaseNetworkRuntime records the runtime boundary.
func (runtime *globalNetworkReleaseRuntime) ReleaseNetworkRuntime(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, error) {
	runtime.fixture.call("runtime")
	runtime.calls++
	runtime.operationID = operationID
	return state.GlobalNetworkReleasePlanRecord{}, runtime.err
}

// globalNetworkReleaseClock supplies mutable deterministic staging time.
type globalNetworkReleaseClock struct{ now time.Time }

// Now returns the configured coordinator instant.
func (clock *globalNetworkReleaseClock) Now() time.Time { return clock.now }
