package harbordruntime

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
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// TestControllerArmNetworkReleaseStartsOnlyAnchor proves recovery arming prevents durable full listeners from binding.
func TestControllerArmNetworkReleaseStartsOnlyAnchor(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	anchor := newTestDataPlane(nil)
	dependencies := testControllerDependencies(material, authority, anchor)
	dependencies.globalNetworkReleasePlans = &testGlobalNetworkReleasePlanStore{
		found: true,
		plan: state.GlobalNetworkReleasePlanRecord{
			Operation: state.OperationRecord{
				Operation: domain.Operation{
					ID: "operation-global-release",
				},
			},
			Phase:              state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
			CheckpointRevision: 1,
			NetworkRevision:    1,
		},
	}
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		if got := config.Desired.ListenerPlan(); got != (dataplane.ListenerPlan{}) {
			t.Fatalf("armed listener plan = %#v, want zero anchor", got)
		}
		return anchor, nil
	}
	controller := newFakeController(t, source, dependencies)
	if armed, err := controller.ArmNetworkRelease(context.Background()); err != nil || !armed {
		t.Fatalf("ArmNetworkRelease() = %t, %v", armed, err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if controller.runtimeDone == nil {
		t.Fatal("cold release Start() did not retain anchor Done")
	}
	if anchor.starts.Load() != 1 {
		t.Fatalf("anchor starts = %d, want 1", anchor.starts.Load())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerArmNetworkReleaseRejectsInvalidAndUnavailablePlans keeps recovery arming side-effect free until one durable plan exists.
func TestControllerArmNetworkReleaseRejectsInvalidAndUnavailablePlans(t *testing.T) {
	if armed, err := (*Controller)(nil).ArmNetworkRelease(context.Background()); armed || !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("nil ArmNetworkRelease() = %t, %v", armed, err)
	}
	if armed, err := (&Controller{}).ArmNetworkRelease(context.Background()); armed || !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero ArmNetworkRelease() = %t, %v", armed, err)
	}

	for _, test := range []struct {
		name  string
		setup func(*Controller, *testGlobalNetworkReleasePlanStore)
		ctx   func() context.Context
		want  error
	}{
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: context.Canceled,
		},
		{
			name: "plan read error",
			setup: func(_ *Controller, store *testGlobalNetworkReleasePlanStore) {
				store.err = errors.New("read release plan")
			},
			ctx:  context.Background,
			want: errors.New("read release plan"),
		},
		{
			name: "absent plan",
			ctx:  context.Background,
		},
		{
			name: "started",
			setup: func(controller *Controller, _ *testGlobalNetworkReleasePlanStore) {
				controller.state = controllerStateReady
			},
			ctx:  context.Background,
			want: errors.New("controller has already started"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &testGlobalNetworkReleasePlanStore{}
			controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil)))
			controller.dependencies.globalNetworkReleasePlans = store
			if test.setup != nil {
				test.setup(controller, store)
			}

			armed, err := controller.ArmNetworkRelease(test.ctx())
			if test.want == nil {
				if err != nil || armed || controller.releaseMode {
					t.Fatalf("ArmNetworkRelease() = %t, %v, mode %t", armed, err, controller.releaseMode)
				}
				return
			}
			if err == nil || armed || controller.releaseMode {
				t.Fatalf("ArmNetworkRelease() = %t, %v, mode %t", armed, err, controller.releaseMode)
			}
			if test.name == "canceled" && !errors.Is(err, test.want) {
				t.Fatalf("ArmNetworkRelease() error = %v, want %v", err, test.want)
			}
			if test.name == "plan read error" && !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("ArmNetworkRelease() error = %v, want %v", err, test.want)
			}
			if test.name == "started" && !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("ArmNetworkRelease() error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestControllerReleaseNetworkRuntimeRejectsInvalidAndUnownedPlans keeps checkpoint advancement behind one exact active owner.
func TestControllerReleaseNetworkRuntimeRejectsInvalidAndUnownedPlans(t *testing.T) {
	if _, err := (*Controller)(nil).ReleaseNetworkRuntime(context.Background(), "operation-global-release"); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("nil ReleaseNetworkRuntime() error = %v", err)
	}

	for _, test := range []struct {
		name  string
		id    domain.OperationID
		setup func(*Controller, *testGlobalNetworkReleasePlanStore)
		ctx   func() context.Context
		want  string
	}{
		{
			name: "invalid ID",
			id:   "",
			ctx:  context.Background,
			want: "operation ID",
		},
		{
			name: "canceled",
			id:   "operation-global-release",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: context.Canceled.Error(),
		},
		{
			name: "read error",
			id:   "operation-global-release",
			setup: func(_ *Controller, store *testGlobalNetworkReleasePlanStore) {
				store.err = errors.New("read release plan")
			},
			ctx:  context.Background,
			want: "read release plan",
		},
		{
			name: "absent plan",
			id:   "operation-global-release",
			ctx:  context.Background,
			want: "owner does not match",
		},
		{
			name: "wrong owner",
			id:   "operation-global-release",
			setup: func(_ *Controller, store *testGlobalNetworkReleasePlanStore) {
				store.found = true
				store.plan.Operation.Operation.ID = "operation-other-release"
			},
			ctx:  context.Background,
			want: "owner does not match",
		},
		{
			name: "unsupported phase",
			id:   "operation-global-release",
			setup: func(_ *Controller, store *testGlobalNetworkReleasePlanStore) {
				store.found = true
				store.plan.Operation.Operation.ID = "operation-global-release"
				store.plan.Phase = state.GlobalNetworkReleasePlanPhaseResolver
			},
			ctx:  context.Background,
			want: "active plan phase",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, store, previous, anchor := readyReleaseController(t)
			store.found = false
			if test.setup != nil {
				test.setup(controller, store)
			}
			if _, err := controller.ReleaseNetworkRuntime(test.ctx(), test.id); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReleaseNetworkRuntime() error = %v, want containing %q", err, test.want)
			}
			if previous.closes.Load() != 0 || anchor.starts.Load() != 0 {
				t.Fatalf("invalid release mutated runtimes: previous closes %d, anchor starts %d", previous.closes.Load(), anchor.starts.Load())
			}
		})
	}
}

// TestControllerEnsureReleaseAnchorRejectsRetainedModeDrift proves a replay cannot bypass the retired-anchor proof.
func TestControllerEnsureReleaseAnchorRejectsRetainedModeDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Controller, *testGlobalNetworkReleasePlanStore, *testDataPlane)
		want   string
	}{
		{
			name: "fence mismatch",
			mutate: func(controller *Controller, _ *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				controller.releaseFence.networkRevision++
			},
			want: "retained release fence",
		},
		{
			name: "retirement unverified",
			mutate: func(controller *Controller, _ *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				controller.releaseRuntimeRetired = false
			},
			want: "retirement is not verified",
		},
		{
			name: "closed anchor",
			mutate: func(_ *Controller, _ *testGlobalNetworkReleasePlanStore, anchor *testDataPlane) {
				close(anchor.done)
			},
			want: "anchor is not alive",
		},
		{
			name: "configured anchor",
			mutate: func(_ *Controller, _ *testGlobalNetworkReleasePlanStore, anchor *testDataPlane) {
				anchor.snapshot.DNS = dataplane.DNSStatus{
					Configured: true,
					Address:    netip.MustParseAddrPort("127.0.0.1:1053"),
					Running:    true,
				}
			},
			want: "ready zero-listener anchor",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, store, _, anchor := readyReleaseController(t)
			anchor.snapshot = dataplane.Snapshot{
				State:  dataplane.StateReady,
				Relays: []dataplane.RelayStatus{},
			}
			controller.dataPlane = anchor
			controller.runtimeDone = anchor.done
			controller.releaseMode = true
			controller.releaseRuntimeRetired = true
			controller.releaseFence = releaseFenceFromPlan(store.plan)
			test.mutate(controller, store, anchor)

			if err := controller.ensureReleaseAnchor(context.Background(), store.plan); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ensureReleaseAnchor() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestControllerReleaseNetworkRuntimeAdvancesAfterRetiringTheExactFullGeneration proves the controller owns the sole runtime checkpoint advance.
func TestControllerReleaseNetworkRuntimeAdvancesAfterRetiringTheExactFullGeneration(t *testing.T) {
	controller, store, previous, anchor := readyReleaseController(t)
	result, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release")
	if err != nil {
		t.Fatalf("ReleaseNetworkRuntime() error = %v", err)
	}
	if result.Phase != state.GlobalNetworkReleasePlanPhaseLowPorts || previous.closes.Load() != 1 || anchor.starts.Load() != 1 {
		t.Fatalf("release result = %#v, previous closes = %d, anchor starts = %d", result, previous.closes.Load(), anchor.starts.Load())
	}
	store.mutex.Lock()
	requests := append([]state.AdvanceGlobalNetworkReleaseRuntimeRequest(nil), store.advanceCalls...)
	store.mutex.Unlock()
	expected := state.AdvanceGlobalNetworkReleaseRuntimeRequest{
		OperationID:        "operation-global-release",
		CheckpointRevision: 11,
		NetworkRevision:    7,
	}
	if len(requests) != 1 || requests[0] != expected {
		t.Fatalf("advance requests = %#v", requests)
	}
}

// TestControllerReleaseNetworkRuntimeRetainsAnchorAcrossAdvanceFailure proves a lost durable response does not rebind listeners.
func TestControllerReleaseNetworkRuntimeRetainsAnchorAcrossAdvanceFailure(t *testing.T) {
	controller, store, previous, anchor := readyReleaseController(t)
	store.advance = func(state.AdvanceGlobalNetworkReleaseRuntimeRequest) (state.GlobalNetworkReleasePlanRecord, error) {
		return state.GlobalNetworkReleasePlanRecord{}, errors.New("compare-and-swap did not match")
	}
	if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err == nil {
		t.Fatal("first ReleaseNetworkRuntime() succeeded")
	}
	store.mutex.Lock()
	store.plan.Phase = state.GlobalNetworkReleasePlanPhaseLowPorts
	store.plan.CheckpointRevision = 12
	store.advance = nil
	store.mutex.Unlock()
	if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err != nil {
		t.Fatalf("retry ReleaseNetworkRuntime() error = %v", err)
	}
	if previous.closes.Load() != 1 || anchor.starts.Load() != 1 {
		t.Fatalf("retry rebinding: old closes = %d, anchor starts = %d", previous.closes.Load(), anchor.starts.Load())
	}
}

// TestControllerReleaseNetworkRuntimeRefusesUnverifiedRetirement proves an anchor cannot authorize a checkpoint after old-generation cleanup fails.
func TestControllerReleaseNetworkRuntimeRefusesUnverifiedRetirement(t *testing.T) {
	controller, store, previous, _ := readyReleaseController(t)
	previous.closeErr = errors.New("retire full generation failed")
	if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err == nil {
		t.Fatal("ReleaseNetworkRuntime() succeeded with an unverified retirement")
	}
	if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err == nil {
		t.Fatal("retry ReleaseNetworkRuntime() advanced an unverified retirement")
	}
	store.mutex.Lock()
	advanceCalls := len(store.advanceCalls)
	store.mutex.Unlock()
	if advanceCalls != 0 {
		t.Fatalf("advance calls = %d, want 0", advanceCalls)
	}
}

// TestControllerReleaseNetworkRuntimeRejectsDriftedFullProof proves durable and process-local authority must agree before an anchor can start.
func TestControllerReleaseNetworkRuntimeRejectsDriftedFullProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Controller, *testGlobalNetworkReleasePlanStore, *testDataPlane)
	}{
		{
			name: "wrong owner",
			mutate: func(_ *Controller, store *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				store.plan.Operation.Operation.ID = "operation-other-release"
			},
		},
		{
			name: "wrong phase",
			mutate: func(_ *Controller, store *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				store.plan.Phase = state.GlobalNetworkReleasePlanPhaseResolver
			},
		},
		{
			name: "wrong root",
			mutate: func(_ *Controller, store *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				store.plan.Authority.Root.Fingerprint = "different"
			},
		},
		{
			name: "wrong listeners",
			mutate: func(_ *Controller, store *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				store.plan.Authority.Projection.Listeners.HTTP.Bind = netip.MustParseAddrPort("127.0.0.1:2080")
			},
		},
		{
			name: "published routes",
			mutate: func(controller *Controller, _ *testGlobalNetworkReleasePlanStore, _ *testDataPlane) {
				controller.publishedHTTPRoutes = []dataplane.HTTPRoute{{}}
			},
		},
		{
			name: "invalid snapshot",
			mutate: func(_ *Controller, _ *testGlobalNetworkReleasePlanStore, previous *testDataPlane) {
				previous.snapshot.State = "invalid"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, store, previous, anchor := readyReleaseController(t)
			store.mutex.Lock()
			test.mutate(controller, store, previous)
			store.mutex.Unlock()
			if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err == nil {
				t.Fatal("ReleaseNetworkRuntime() succeeded")
			}
			if anchor.starts.Load() != 0 || previous.closes.Load() != 0 {
				t.Fatalf("drift started anchor %d times and closed full runtime %d times", anchor.starts.Load(), previous.closes.Load())
			}
		})
	}
}

// TestControllerReleaseNetworkRuntimeAdmitsAdvancedLifecycleRevision proves a lifecycle-only durable revision advance does not require rebuilding the immutable full listener generation.
func TestControllerReleaseNetworkRuntimeAdmitsAdvancedLifecycleRevision(t *testing.T) {
	controller, store, previous, anchor := readyReleaseController(t)
	runtimeRevision := controller.runtimeNetworkRevision
	rootFingerprint := store.plan.Authority.Root.Fingerprint
	listeners := store.plan.Authority.Projection.Listeners
	store.mutex.Lock()
	store.plan.NetworkRevision++
	store.plan.NetworkUpdatedAt = store.plan.NetworkUpdatedAt.Add(time.Second)
	store.plan.Authority.Projection.NetworkRevision = store.plan.NetworkRevision
	store.plan.Authority.Projection.NetworkUpdatedAt = store.plan.NetworkUpdatedAt
	stagedRevision := store.plan.NetworkRevision
	store.mutex.Unlock()
	if runtimeRevision >= stagedRevision {
		t.Fatalf("runtime network revision = %d, want less than staged revision %d", runtimeRevision, stagedRevision)
	}
	if store.plan.Authority.Projection.NetworkRevision != store.plan.NetworkRevision ||
		!store.plan.Authority.Projection.NetworkUpdatedAt.Equal(store.plan.NetworkUpdatedAt) {
		t.Fatalf("staged authority projection does not match durable network boundary")
	}
	if store.plan.Authority.Root.Fingerprint != rootFingerprint {
		t.Fatalf("staged root fingerprint = %q, want %q", store.plan.Authority.Root.Fingerprint, rootFingerprint)
	}
	if store.plan.Authority.Projection.Listeners != listeners {
		t.Fatalf("staged listeners = %#v, want %#v", store.plan.Authority.Projection.Listeners, listeners)
	}

	result, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release")
	if err != nil {
		t.Fatalf("ReleaseNetworkRuntime() error = %v", err)
	}
	if anchor.starts.Load() != 1 {
		t.Fatalf("anchor starts = %d, want 1", anchor.starts.Load())
	}
	if previous.closes.Load() != 1 {
		t.Fatalf("full runtime closes = %d, want 1", previous.closes.Load())
	}
	if result.NetworkRevision != stagedRevision {
		t.Fatalf("durable network revision = %d, want %d", result.NetworkRevision, stagedRevision)
	}
	store.mutex.Lock()
	requests := append([]state.AdvanceGlobalNetworkReleaseRuntimeRequest(nil), store.advanceCalls...)
	store.mutex.Unlock()
	expected := state.AdvanceGlobalNetworkReleaseRuntimeRequest{
		OperationID:        "operation-global-release",
		CheckpointRevision: 11,
		NetworkRevision:    stagedRevision,
	}
	if len(requests) != 1 || requests[0] != expected {
		t.Fatalf("advance requests = %#v, want %#v", requests, expected)
	}
	if controller.releaseFence.networkRevision != stagedRevision {
		t.Fatalf("release fence network revision = %d, want %d", controller.releaseFence.networkRevision, stagedRevision)
	}
}

// TestControllerReleaseNetworkRuntimeCleansRejectedAnchors proves no failed anchor can retire the current generation or advance durable state.
func TestControllerReleaseNetworkRuntimeCleansRejectedAnchors(t *testing.T) {
	tests := []struct {
		name    string
		factory func(*testDataPlane) dataPlaneFactory
		closes  int64
	}{
		{
			name: "factory error",
			factory: func(*testDataPlane) dataPlaneFactory {
				return func(dataplane.Config) (dataPlane, error) {
					return nil, errors.New("anchor factory failed")
				}
			},
		},
		{
			name: "typed nil",
			factory: func(*testDataPlane) dataPlaneFactory {
				return func(dataplane.Config) (dataPlane, error) {
					var runtime *testDataPlane
					return runtime, nil
				}
			},
		},
		{
			name: "nil done",
			factory: func(anchor *testDataPlane) dataPlaneFactory {
				anchor.doneFunc = func() <-chan struct{} { return nil }
				return func(dataplane.Config) (dataPlane, error) { return anchor, nil }
			},
			closes: 1,
		},
		{
			name: "start error",
			factory: func(anchor *testDataPlane) dataPlaneFactory {
				anchor.startErr = errors.New("anchor start failed")
				return func(dataplane.Config) (dataPlane, error) { return anchor, nil }
			},
			closes: 1,
		},
		{
			name: "already closed done",
			factory: func(anchor *testDataPlane) dataPlaneFactory {
				close(anchor.done)
				return func(dataplane.Config) (dataPlane, error) { return anchor, nil }
			},
			closes: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, store, previous, anchor := readyReleaseController(t)
			controller.dependencies.newDataPlane = test.factory(anchor)
			if _, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release"); err == nil {
				t.Fatal("ReleaseNetworkRuntime() succeeded")
			}
			if anchor.closes.Load() != test.closes || previous.closes.Load() != 0 {
				t.Fatalf("anchor closes = %d, old closes = %d", anchor.closes.Load(), previous.closes.Load())
			}
			store.mutex.Lock()
			advanceCalls := len(store.advanceCalls)
			store.mutex.Unlock()
			if advanceCalls != 0 {
				t.Fatalf("advance calls = %d, want 0", advanceCalls)
			}
		})
	}
}

// TestControllerReleaseNetworkRuntimeCancellationCleansAnchor proves caller cancellation cannot leave an unowned candidate alive.
func TestControllerReleaseNetworkRuntimeCancellationCleansAnchor(t *testing.T) {
	controller, store, previous, anchor := readyReleaseController(t)
	anchor.startEntered = make(chan struct{})
	anchor.blockStart = true
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := controller.ReleaseNetworkRuntime(ctx, "operation-global-release")
		result <- err
	}()
	waitControllerSignal(t, anchor.startEntered, "anchor startup")
	cancel()
	if err := <-result; err == nil {
		t.Fatal("ReleaseNetworkRuntime() succeeded after cancellation")
	}
	if anchor.closes.Load() != 1 || previous.closes.Load() != 0 {
		t.Fatalf("anchor closes = %d, old closes = %d", anchor.closes.Load(), previous.closes.Load())
	}
	store.mutex.Lock()
	advanceCalls := len(store.advanceCalls)
	store.mutex.Unlock()
	if advanceCalls != 0 {
		t.Fatalf("advance calls = %d, want 0", advanceCalls)
	}
}

// TestControllerReleaseNetworkRuntimeCloseInterruptsAnchorPublication proves shutdown joins both candidates without advancing durable release state.
func TestControllerReleaseNetworkRuntimeCloseInterruptsAnchorPublication(t *testing.T) {
	controller, store, previous, anchor := readyReleaseController(t)
	anchor.startEntered = make(chan struct{})
	anchor.afterStart = func() { <-anchor.closeRelease }
	anchor.closeRelease = make(chan struct{})
	go controller.monitor(&testMaterialStore{})

	releaseResult := make(chan error, 1)
	go func() {
		_, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release")
		releaseResult <- err
	}()
	waitControllerSignal(t, anchor.startEntered, "anchor startup")

	closeResult := make(chan error, 1)
	go func() { closeResult <- controller.Close(context.Background()) }()
	waitControllerSignal(t, controller.stop, "shutdown claim")
	select {
	case err := <-closeResult:
		t.Fatalf("Close() returned during anchor publication: %v", err)
	default:
	}

	close(anchor.closeRelease)
	if err := <-releaseResult; err == nil {
		t.Fatal("ReleaseNetworkRuntime() succeeded after Close() interrupted anchor publication")
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if previous.closes.Load() != 1 || anchor.closes.Load() != 1 {
		t.Fatalf("generation closes = old:%d anchor:%d, want one each", previous.closes.Load(), anchor.closes.Load())
	}
	store.mutex.Lock()
	advanceCalls := len(store.advanceCalls)
	store.mutex.Unlock()
	if advanceCalls != 0 {
		t.Fatalf("advance calls = %d, want none after interrupted publication", advanceCalls)
	}
}

// TestControllerReconcileWaitsForReleaseAnchorAdmission proves a route reconciler cannot publish through an in-flight release transition.
func TestControllerReconcileWaitsForReleaseAnchorAdmission(t *testing.T) {
	controller, _, _, anchor := readyReleaseController(t)
	anchor.startEntered = make(chan struct{})
	anchor.afterStart = func() { <-anchor.closeRelease }
	anchor.closeRelease = make(chan struct{})
	releaseResult := make(chan error, 1)
	go func() {
		_, err := controller.ReleaseNetworkRuntime(context.Background(), "operation-global-release")
		releaseResult <- err
	}()
	waitControllerSignal(t, anchor.startEntered, "anchor startup")
	reconcileResult := make(chan error, 1)
	go func() { reconcileResult <- controller.Reconcile(context.Background()) }()
	select {
	case err := <-reconcileResult:
		t.Fatalf("Reconcile() returned during anchor admission: %v", err)
	default:
	}
	close(anchor.closeRelease)
	if err := <-releaseResult; err != nil {
		t.Fatalf("ReleaseNetworkRuntime() error = %v", err)
	}
	if err := <-reconcileResult; err == nil {
		t.Fatal("Reconcile() succeeded after release mode published")
	}
	if len(anchor.replacements) != 0 {
		t.Fatalf("anchor route replacements = %#v", anchor.replacements)
	}
}

// TestControllerCloseWaitsForReleaseSerialization proves shutdown cannot complete while release admission owns a candidate generation.
func TestControllerCloseWaitsForReleaseSerialization(t *testing.T) {
	anchor := newTestDataPlane(nil)
	dependencies := testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, anchor)
	dependencies.globalNetworkReleasePlans = releaseTestPlanStore(state.GlobalNetworkReleasePlanPhaseLowPorts)
	controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if controller.runtimeDone == nil {
		t.Fatal("cold release Start() did not retain anchor Done")
	}
	controller.reconcileMutex.Lock()
	result := make(chan error, 1)
	go func() { result <- controller.Close(context.Background()) }()
	select {
	case err := <-result:
		t.Fatalf("Close() returned before release serialization ended: %v", err)
	case <-time.After(controllerTestWait):
	}
	controller.reconcileMutex.Unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(controllerTestWait):
		t.Fatal("Close() did not return after release serialization ended")
	}
}

// TestControllerReleaseModeGuardsMutationEntrypoints proves release ownership rejects competing topology writers before durable reads.
func TestControllerReleaseModeGuardsMutationEntrypoints(t *testing.T) {
	controller, _, _, anchor := readyReleaseController(t)
	controller.releaseMode = true
	source := controller.source.(*testRuntimeStateSource)
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "activate full",
			call: func() error { return controller.ActivateNetwork(context.Background(), 7) },
		},
		{
			name: "activate resolver",
			call: func() error { return controller.ActivateResolver(context.Background(), 7) },
		},
		{
			name: "replace native routes",
			call: func() error { return controller.ReplaceManagedNativeRoutes(context.Background(), nil) },
		},
		{
			name: "observe native routes",
			call: func() error { return controller.ManagedNativeRoutesLive(context.Background(), nil) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeCalls := source.calls.Load()
			if err := test.call(); err == nil {
				t.Fatal("mutation entrypoint succeeded")
			}
			if source.calls.Load() != beforeCalls || len(anchor.replacements) != 0 {
				t.Fatalf("source calls = %d, anchor replacements = %#v", source.calls.Load(), anchor.replacements)
			}
		})
	}
}

// TestControllerVerifyNetworkRuntimeReleasedBindsStableAnchorProof proves the durable effects checkpoint, rather than process-local generation, determines the proof.
func TestControllerVerifyNetworkRuntimeReleasedBindsStableAnchorProof(t *testing.T) {
	controller, store, anchor := readyReleasedVerificationController(t)
	before := captureReleaseVerificationState(controller, anchor)
	first, err := controller.VerifyNetworkRuntimeReleased(
		context.Background(),
		"operation-global-release",
		11,
		7,
	)
	if err != nil {
		t.Fatalf("VerifyNetworkRuntimeReleased() error = %v", err)
	}
	second, err := controller.VerifyNetworkRuntimeReleased(
		context.Background(),
		"operation-global-release",
		11,
		7,
	)
	if err != nil {
		t.Fatalf("second VerifyNetworkRuntimeReleased() error = %v", err)
	}
	const want = "fcf44778ba8dbb8344bd63eb3201c1fd72fad36d4421f165b700053f261aa4c7"
	if first != want || second != want {
		t.Fatalf("release proofs = %q, %q, want stable %q", first, second, want)
	}
	if first == releaseRuntimeObservationDigest("operation-other-release", 11, 7) ||
		first == releaseRuntimeObservationDigest("operation-global-release", 12, 7) ||
		first == releaseRuntimeObservationDigest("operation-global-release", 11, 8) {
		t.Fatalf("release proof %q is not bound to operation, checkpoint, and network", first)
	}
	independent, independentStore, independentAnchor := readyReleasedVerificationController(t)
	independent.runtimeGeneration = 99
	independentBefore := captureReleaseVerificationState(independent, independentAnchor)
	proof, err := independent.VerifyNetworkRuntimeReleased(
		context.Background(),
		"operation-global-release",
		11,
		7,
	)
	if err != nil || proof != first {
		t.Fatalf("independent release proof = %q, %v; want %q, nil", proof, err, first)
	}
	assertReleaseVerificationUnchanged(t, before, controller, store, anchor)
	assertReleaseVerificationUnchanged(
		t,
		independentBefore,
		independent,
		independentStore,
		independentAnchor,
	)
}

// TestControllerVerifyNetworkRuntimeReleasedRejectsUnprovenStateWithoutMutation proves verification is read-only and fails closed for every missing anchor or durable proof.
func TestControllerVerifyNetworkRuntimeReleasedRejectsUnprovenStateWithoutMutation(t *testing.T) {
	proof, err := (*Controller)(nil).VerifyNetworkRuntimeReleased(
		context.Background(),
		"operation-global-release",
		11,
		7,
	)
	if proof != "" || !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("nil VerifyNetworkRuntimeReleased() = %q, %v", proof, err)
	}
	proof, err = (&Controller{}).VerifyNetworkRuntimeReleased(
		context.Background(),
		"operation-global-release",
		11,
		7,
	)
	if proof != "" || !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero VerifyNetworkRuntimeReleased() = %q, %v", proof, err)
	}

	tests := []struct {
		name   string
		ctx    func() context.Context
		id     domain.OperationID
		check  domain.Sequence
		rev    domain.Sequence
		mutate func(*Controller, *testGlobalNetworkReleasePlanStore, *testDataPlane)
		want   string
	}{
		{
			name:  "malformed operation",
			ctx:   context.Background,
			check: 11,
			rev:   7,
			want:  "operation ID",
		},
		{
			name: "zero checkpoint",
			ctx:  context.Background,
			id:   "operation-global-release",
			rev:  7,
			want: "checkpoint revision",
		},
		{
			name:  "oversized checkpoint",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: domain.MaximumSequence + 1,
			rev:   7,
			want:  "checkpoint revision",
		},
		{
			name:  "zero network revision",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			want:  "network revision",
		},
		{
			name:  "oversized network revision",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   domain.MaximumSequence + 1,
			want:  "network revision",
		},
		{
			name:  "canceled context",
			ctx:   canceledReleaseVerificationContext,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			want:  context.Canceled.Error(),
		},
		{
			name:  "absent plan",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				store *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				store.found = false
			},
			want: "active durable plan",
		},
		{
			name:  "plan read error",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				store *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				store.err = errors.New("read release plan")
			},
			want: "read release plan",
		},
		{
			name:  "wrong durable phase",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				store *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				store.plan.Phase = state.GlobalNetworkReleasePlanPhaseRuntimeRelease
			},
			want: "active durable plan",
		},
		{
			name:  "durable checkpoint drift",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				store *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				store.plan.CheckpointRevision = 12
			},
			want: "active durable plan",
		},
		{
			name:  "new lifecycle",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.state = controllerStateNew
			},
			want: "does not own",
		},
		{
			name:  "release mode absent",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.releaseMode = false
			},
			want: "does not own",
		},
		{
			name:  "retirement absent",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.releaseRuntimeRetired = false
			},
			want: "does not own",
		},
		{
			name:  "zero generation",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.runtimeGeneration = 0
			},
			want: "does not own",
		},
		{
			name:  "operation fence drift",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.releaseFence.operationID = "operation-other-release"
			},
			want: "retained release fence",
		},
		{
			name:  "network fence drift",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.releaseFence.networkRevision = 8
			},
			want: "retained release fence",
		},
		{
			name:  "runtime revision drift",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				controller *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				_ *testDataPlane,
			) {
				controller.runtimeNetworkRevision = 8
			},
			want: "retained release fence",
		},
		{
			name:  "dead anchor",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				anchor *testDataPlane,
			) {
				close(anchor.done)
			},
			want: "anchor is not alive",
		},
		{
			name:  "configured anchor",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				anchor *testDataPlane,
			) {
				anchor.snapshot.DNS = dataplane.DNSStatus{
					Configured: true,
				}
			},
			want: "ready zero-listener anchor",
		},
		{
			name:  "non-ready anchor",
			ctx:   context.Background,
			id:    "operation-global-release",
			check: 11,
			rev:   7,
			mutate: func(
				_ *Controller,
				_ *testGlobalNetworkReleasePlanStore,
				anchor *testDataPlane,
			) {
				anchor.snapshot.State = dataplane.StateStopped
			},
			want: "ready zero-listener anchor",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, store, anchor := readyReleasedVerificationController(t)
			if test.mutate != nil {
				test.mutate(controller, store, anchor)
			}
			before := captureReleaseVerificationState(controller, anchor)
			proof, err := controller.VerifyNetworkRuntimeReleased(
				test.ctx(),
				test.id,
				test.check,
				test.rev,
			)
			if proof != "" || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"VerifyNetworkRuntimeReleased() = %q, %v, want empty proof and error containing %q",
					proof,
					err,
					test.want,
				)
			}
			assertReleaseVerificationUnchanged(t, before, controller, store, anchor)
		})
	}
}

// TestControllerVerifyNetworkRuntimeReleasedRejectsCloseDuringSnapshot proves a snapshot cannot authorize after Close claims lifecycle ownership.
func TestControllerVerifyNetworkRuntimeReleasedRejectsCloseDuringSnapshot(t *testing.T) {
	controller, _, anchor := readyReleasedVerificationController(t)
	blocked := &blockedSnapshotDataPlane{
		dataPlane:       anchor,
		snapshotEntered: make(chan struct{}),
		snapshotRelease: make(chan struct{}),
	}
	controller.dataPlane = blocked
	go controller.monitor(&testMaterialStore{})

	verification := make(chan struct {
		proof string
		err   error
	}, 1)
	go func() {
		proof, err := controller.VerifyNetworkRuntimeReleased(
			context.Background(),
			"operation-global-release",
			11,
			7,
		)
		verification <- struct {
			proof string
			err   error
		}{
			proof: proof,
			err:   err,
		}
	}()
	waitControllerSignal(t, blocked.snapshotEntered, "release verification snapshot")
	closed := make(chan error, 1)
	go func() { closed <- controller.Close(context.Background()) }()
	waitControllerSignal(t, controller.stop, "release verification shutdown")
	close(blocked.snapshotRelease)
	result := <-verification
	if result.proof != "" || result.err == nil || !strings.Contains(result.err.Error(), "changed during anchor observation") {
		t.Fatalf("VerifyNetworkRuntimeReleased() = %q, %v", result.proof, result.err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// releaseVerificationState captures all process-local fields that verification must only observe.
type releaseVerificationState struct {
	state           controllerState
	runtime         dataPlane
	done            <-chan struct{}
	generation      uint64
	mode            bool
	retired         bool
	fence           networkReleaseFence
	runtimeRevision domain.Sequence
	snapshot        dataplane.Snapshot
}

// blockedSnapshotDataPlane pauses one snapshot so shutdown can claim lifecycle ownership between observation checks.
type blockedSnapshotDataPlane struct {
	dataPlane
	snapshotEntered chan struct{}
	snapshotRelease chan struct{}
	snapshotOnce    sync.Once
}

// Snapshot blocks exactly one observation at the boundary required to reproduce a concurrent Close.
func (runtime *blockedSnapshotDataPlane) Snapshot() dataplane.Snapshot {
	runtime.snapshotOnce.Do(func() {
		close(runtime.snapshotEntered)
		<-runtime.snapshotRelease
	})
	return runtime.dataPlane.Snapshot()
}

// readyReleasedVerificationController creates one exact, durable effects-authorized release anchor.
func readyReleasedVerificationController(t *testing.T) (*Controller, *testGlobalNetworkReleasePlanStore, *testDataPlane) {
	t.Helper()
	controller, store, _, anchor := readyReleaseController(t)
	anchor.snapshot = dataplane.Snapshot{
		State:  dataplane.StateReady,
		Relays: []dataplane.RelayStatus{},
	}
	store.plan.Phase = state.GlobalNetworkReleasePlanPhaseVerifyEffects
	controller.dataPlane = anchor
	controller.runtimeDone = anchor.done
	controller.runtimeGeneration = 2
	controller.releaseMode = true
	controller.releaseRuntimeRetired = true
	controller.releaseFence = releaseFenceFromPlan(store.plan)
	return controller, store, anchor
}

// canceledReleaseVerificationContext supplies a caller cancellation before any durable observation.
func canceledReleaseVerificationContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// captureReleaseVerificationState records every local field that successful and failed proofs must leave unchanged.
func captureReleaseVerificationState(controller *Controller, anchor *testDataPlane) releaseVerificationState {
	return releaseVerificationState{
		state:           controller.state,
		runtime:         controller.dataPlane,
		done:            controller.runtimeDone,
		generation:      controller.runtimeGeneration,
		mode:            controller.releaseMode,
		retired:         controller.releaseRuntimeRetired,
		fence:           controller.releaseFence,
		runtimeRevision: controller.runtimeNetworkRevision,
		snapshot:        anchor.snapshot,
	}
}

// assertReleaseVerificationUnchanged rejects hidden runtime or durable mutations by a proof attempt.
func assertReleaseVerificationUnchanged(t *testing.T, before releaseVerificationState, controller *Controller, store *testGlobalNetworkReleasePlanStore, anchor *testDataPlane) {
	t.Helper()
	unchanged := controller.state == before.state &&
		controller.dataPlane == before.runtime &&
		controller.runtimeDone == before.done &&
		controller.runtimeGeneration == before.generation &&
		controller.releaseMode == before.mode &&
		controller.releaseRuntimeRetired == before.retired &&
		controller.releaseFence == before.fence &&
		controller.runtimeNetworkRevision == before.runtimeRevision &&
		reflect.DeepEqual(anchor.snapshot, before.snapshot)
	if !unchanged {
		t.Fatal("verification mutated controller or runtime state")
	}
	store.mutex.Lock()
	advanceCalls := len(store.advanceCalls)
	store.mutex.Unlock()
	if advanceCalls != 0 || anchor.starts.Load() != 0 || anchor.closes.Load() != 0 {
		t.Fatalf("verification mutated durable or runtime state: advances %d, starts %d, closes %d", advanceCalls, anchor.starts.Load(), anchor.closes.Load())
	}
}

// releaseTestPlanStore creates one minimal active plan for cold release-anchor tests.
func releaseTestPlanStore(phase state.GlobalNetworkReleasePlanPhase) *testGlobalNetworkReleasePlanStore {
	return &testGlobalNetworkReleasePlanStore{
		found: true,
		plan: state.GlobalNetworkReleasePlanRecord{
			Operation: state.OperationRecord{
				Operation: domain.Operation{
					ID: "operation-global-release",
				},
			},
			Phase:              phase,
			CheckpointRevision: 1,
			NetworkRevision:    1,
		},
	}
}

// readyReleaseController assembles an exact full listener generation without invoking unrelated startup dependencies.
func readyReleaseController(t *testing.T) (*Controller, *testGlobalNetworkReleasePlanStore, *testDataPlane, *testDataPlane) {
	t.Helper()
	networkUpdatedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	listeners := dataplane.ListenerPlan{
		DNS:   netip.MustParseAddrPort("127.0.0.1:1053"),
		HTTP:  netip.MustParseAddrPort("127.0.0.1:1080"),
		HTTPS: netip.MustParseAddrPort("127.0.0.1:1443"),
	}
	foundation, err := dataplane.NewDesiredState(listeners, nil, nil, 0)
	if err != nil {
		t.Fatalf("NewDesiredState() error = %v", err)
	}
	previous := newTestDataPlane(nil)
	previous.snapshot = dataplane.Snapshot{
		State: dataplane.StateReady,
		DNS: dataplane.DNSStatus{
			Configured: true,
			Address:    listeners.DNS,
			Running:    true,
		},
		Ingress: dataplane.IngressStatus{
			Configured:   true,
			HTTPAddress:  listeners.HTTP,
			HTTPSAddress: listeners.HTTPS,
			Running:      true,
		},
		Relays: []dataplane.RelayStatus{},
	}
	anchor := newTestDataPlane(nil)
	plan := state.GlobalNetworkReleasePlanRecord{
		Operation: state.OperationRecord{
			Operation: domain.Operation{
				ID: "operation-global-release",
			},
		},
		Phase:              state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
		CheckpointRevision: 11,
		NetworkRevision:    7,
	}
	plan.Authority.Root.Fingerprint = validTestRoot().Fingerprint
	plan.NetworkUpdatedAt = networkUpdatedAt
	plan.Authority.Projection.NetworkRevision = plan.NetworkRevision
	plan.Authority.Projection.NetworkUpdatedAt = networkUpdatedAt
	plan.Authority.Projection.Listeners.DNS.Bind = listeners.DNS
	plan.Authority.Projection.Listeners.HTTP.Bind = listeners.HTTP
	plan.Authority.Projection.Listeners.HTTPS.Bind = listeners.HTTPS
	store := &testGlobalNetworkReleasePlanStore{
		found: true,
		plan:  plan,
	}
	dependencies := testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, previous)
	dependencies.globalNetworkReleasePlans = store
	dependencies.newDataPlane = func(dataplane.Config) (dataPlane, error) { return anchor, nil }
	controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
	controller.state = controllerStateReady
	controller.runtimeContext = context.Background()
	controller.dataPlane = previous
	controller.runtimeDone = previous.done
	controller.runtimeGeneration = 1
	controller.runtimeNetworkRevision = 7
	controller.root = validTestRoot()
	controller.httpFoundation = foundation
	controller.publishedHTTPRoutes = []dataplane.HTTPRoute{}
	controller.managedNativeRoutes = []dataplane.NativeRoute{}
	store.advance = func(request state.AdvanceGlobalNetworkReleaseRuntimeRequest) (state.GlobalNetworkReleasePlanRecord, error) {
		store.plan.Phase = state.GlobalNetworkReleasePlanPhaseLowPorts
		store.plan.CheckpointRevision = 12
		return store.plan, nil
	}
	return controller, store, previous, anchor
}

// TestControllerStartDiscoversActiveReleasePlan proves direct recovery cannot bind full listeners before lifecycle recovery arms it.
func TestControllerStartDiscoversActiveReleasePlan(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	anchor := newTestDataPlane(nil)
	dependencies := testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, anchor)
	dependencies.globalNetworkReleasePlans = &testGlobalNetworkReleasePlanStore{
		found: true,
		plan: state.GlobalNetworkReleasePlanRecord{
			Operation: state.OperationRecord{
				Operation: domain.Operation{
					ID: "operation-global-release",
				},
			},
			Phase:              state.GlobalNetworkReleasePlanPhaseLowPorts,
			CheckpointRevision: 2,
			NetworkRevision:    1,
		},
	}
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		t.Fatal("Start() opened certificate material for an active release")
		return nil, nil
	}
	dependencies.newDesiredState = func(state.RuntimeState) (dataplane.DesiredState, error) {
		t.Fatal("Start() read ordinary desired state for an active release")
		return dataplane.DesiredState{}, nil
	}
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		if config.CertificateProvider != nil || config.Desired.ListenerPlan() != (dataplane.ListenerPlan{}) {
			t.Fatalf("release anchor config = %#v", config)
		}
		return anchor, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if source.calls.Load() != 0 || !controller.NetworkReleaseArmed() {
		t.Fatalf("release start calls = %d, armed = %t", source.calls.Load(), controller.NetworkReleaseArmed())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerNetworkReleaseArmedHandlesInvalidControllers proves recovery workers can safely query optional runtime ownership.
func TestControllerNetworkReleaseArmedHandlesInvalidControllers(t *testing.T) {
	if (*Controller)(nil).NetworkReleaseArmed() {
		t.Fatal("nil controller reports network release armed")
	}
	if (&Controller{}).NetworkReleaseArmed() {
		t.Fatal("zero-value controller reports network release armed")
	}
}
