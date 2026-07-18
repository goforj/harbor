package harbordruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

const controllerTestWait = 3 * time.Second

// testRuntimeStateSource returns one deterministic aggregate while recording construction-time access.
type testRuntimeStateSource struct {
	snapshot           domain.Snapshot
	network            state.NetworkRecord
	networkInitialized bool
	err                error
	calls              atomic.Int64
	entered            chan struct{}
	block              bool
	after              func()
}

// RuntimeState returns the configured state or waits for cancellation at a controlled startup boundary.
func (source *testRuntimeStateSource) RuntimeState(ctx context.Context) (state.RuntimeState, error) {
	source.calls.Add(1)
	if source.entered != nil {
		select {
		case <-source.entered:
		default:
			close(source.entered)
		}
	}
	if source.block {
		<-ctx.Done()
		return state.RuntimeState{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return state.RuntimeState{}, err
	}
	if source.after != nil {
		source.after()
	}
	network := source.network
	if !source.networkInitialized && reflect.DeepEqual(network, state.NetworkRecord{}) {
		network = validControllerUninitializedNetwork()
	}
	return state.RuntimeState{
		Snapshot:           source.snapshot,
		Network:            network,
		NetworkInitialized: source.networkInitialized,
	}, source.err
}

// testEventLog records cleanup order without imposing timing on the controller.
type testEventLog struct {
	mutex  sync.Mutex
	events []string
}

// add appends one lifecycle boundary.
func (log *testEventLog) add(event string) {
	if log == nil {
		return
	}
	log.mutex.Lock()
	log.events = append(log.events, event)
	log.mutex.Unlock()
}

// snapshot returns an isolated event sequence.
func (log *testEventLog) snapshot() []string {
	if log == nil {
		return nil
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return append([]string(nil), log.events...)
}

// testMaterialStore supplies the complete certificate persistence contract without touching disk.
type testMaterialStore struct {
	closes       atomic.Int64
	closeErr     error
	closeEntered chan struct{}
	closeRelease chan struct{}
	events       *testEventLog
}

// LoadAuthority is unavailable because controller tests replace certificate bootstrap as one boundary.
func (store *testMaterialStore) LoadAuthority(context.Context, localca.Config) (*localca.Authority, error) {
	return nil, errors.New("test material store does not load authorities")
}

// CreateAuthority is unavailable because controller tests replace certificate bootstrap as one boundary.
func (store *testMaterialStore) CreateAuthority(context.Context, *localca.Authority) error {
	return errors.New("test material store does not create authorities")
}

// LoadLeaf is unavailable because controller lifecycle tests never authorize a routed host.
func (store *testMaterialStore) LoadLeaf(context.Context, *localca.Authority, []string) (localca.Leaf, error) {
	return localca.Leaf{}, errors.New("test material store does not load leaves")
}

// PutLeaf is unavailable because controller lifecycle tests never authorize a routed host.
func (store *testMaterialStore) PutLeaf(context.Context, *localca.Authority, localca.Leaf) error {
	return errors.New("test material store does not persist leaves")
}

// Close records release after the data-plane boundary has completed.
func (store *testMaterialStore) Close() error {
	store.closes.Add(1)
	store.events.add("material.close")
	if store.closeEntered != nil {
		close(store.closeEntered)
	}
	if store.closeRelease != nil {
		<-store.closeRelease
	}
	return store.closeErr
}

// testCertificateAuthority exposes public material and records provider propagation without issuing leaves.
type testCertificateAuthority struct {
	root             certificates.Root
	rootErr          error
	afterRoot        func()
	certificateCalls atomic.Int64
}

// EnsureLeaf rejects issuance because listener-only generations have no authorized host.
func (authority *testCertificateAuthority) EnsureLeaf(context.Context, string) (certificates.LeafResult, error) {
	return certificates.LeafResult{}, errors.New("listener-only generation must not ensure leaves")
}

// Certificate records provider calls while keeping controller tests independent from TLS fixtures.
func (authority *testCertificateAuthority) Certificate(context.Context, string) (*tls.Certificate, error) {
	authority.certificateCalls.Add(1)
	return nil, errors.New("test certificate is unavailable")
}

// PublicRoot returns deterministic public-only authority metadata.
func (authority *testCertificateAuthority) PublicRoot() (certificates.Root, error) {
	root := authority.root
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	if authority.afterRoot != nil {
		authority.afterRoot()
	}
	return root, authority.rootErr
}

// testDataPlane provides deterministic lifecycle faults and completion signals.
type testDataPlane struct {
	mutex        sync.Mutex
	done         chan struct{}
	doneFunc     func() <-chan struct{}
	doneOnce     sync.Once
	snapshot     dataplane.Snapshot
	terminalErr  error
	startErr     error
	startEntered chan struct{}
	afterStart   func()
	blockStart   bool
	closeErr     error
	closeMode    testCloseMode
	closeRelease chan struct{}
	closeEntered chan struct{}
	starts       atomic.Int64
	closes       atomic.Int64
	doneCalls    atomic.Int64
	events       *testEventLog
}

// testCloseMode selects one deliberately conforming or broken child shutdown behavior.
type testCloseMode uint8

const (
	testCloseCompletes testCloseMode = iota
	testCloseReturnsWithoutDone
	testCloseBlocks
	testCloseBlocksAfterDone
)

// Start publishes readiness or waits for cancellation at a deterministic race boundary.
func (runtime *testDataPlane) Start(ctx context.Context) error {
	runtime.starts.Add(1)
	if runtime.startEntered != nil {
		close(runtime.startEntered)
	}
	if runtime.blockStart {
		<-ctx.Done()
		if runtime.startErr != nil {
			return errors.Join(ctx.Err(), runtime.startErr)
		}
		return ctx.Err()
	}
	if runtime.startErr != nil {
		return runtime.startErr
	}
	runtime.mutex.Lock()
	runtime.snapshot.State = dataplane.StateReady
	runtime.mutex.Unlock()
	if runtime.afterStart != nil {
		runtime.afterStart()
	}
	return nil
}

// Snapshot returns the current payload-free child state.
func (runtime *testDataPlane) Snapshot() dataplane.Snapshot {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	snapshot := runtime.snapshot
	snapshot.Relays = append(make([]dataplane.RelayStatus, 0, len(snapshot.Relays)), snapshot.Relays...)
	return snapshot
}

// Done exposes the stable child completion signal, including deliberate nil-channel faults.
func (runtime *testDataPlane) Done() <-chan struct{} {
	runtime.doneCalls.Add(1)
	if runtime.doneFunc != nil {
		return runtime.doneFunc()
	}
	return runtime.done
}

// Err returns the retained child failure.
func (runtime *testDataPlane) Err() error {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	return runtime.terminalErr
}

// Close follows the configured child behavior while recording data-plane-before-material order.
func (runtime *testDataPlane) Close(context.Context) error {
	runtime.closes.Add(1)
	runtime.events.add("runtime.close")
	if runtime.closeEntered != nil {
		close(runtime.closeEntered)
	}
	if runtime.done != nil && channelClosed(runtime.done) && runtime.closeMode != testCloseBlocksAfterDone {
		return runtime.Err()
	}
	switch runtime.closeMode {
	case testCloseReturnsWithoutDone:
		return runtime.closeErr
	case testCloseBlocks:
		<-runtime.closeRelease
		runtime.complete(runtime.closeErr)
		return runtime.closeErr
	case testCloseBlocksAfterDone:
		<-runtime.closeRelease
		return runtime.closeErr
	default:
		runtime.complete(runtime.closeErr)
		return runtime.closeErr
	}
}

// complete publishes one terminal result exactly once.
func (runtime *testDataPlane) complete(err error) {
	runtime.mutex.Lock()
	if runtime.snapshot.State != dataplane.StateFailed {
		runtime.terminalErr = err
		runtime.snapshot.State = dataplane.StateStopped
	}
	runtime.mutex.Unlock()
	if runtime.done == nil {
		return
	}
	runtime.doneOnce.Do(func() {
		close(runtime.done)
	})
}

// fail publishes an unexpected terminal child failure.
func (runtime *testDataPlane) fail(err error) {
	runtime.mutex.Lock()
	runtime.terminalErr = err
	runtime.snapshot.State = dataplane.StateFailed
	runtime.mutex.Unlock()
	runtime.doneOnce.Do(func() {
		close(runtime.done)
	})
}

// validControllerSnapshot returns one initialized empty durable projection.
func validControllerSnapshot() domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		CapturedAt:        time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// validControllerUninitializedNetwork returns the explicit empty aggregate for a host that has not completed setup.
func validControllerUninitializedNetwork() state.NetworkRecord {
	return state.NetworkRecord{
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: state.DataPlaneReservations{
			Endpoints:            []state.EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
}

// validControllerProject returns one legal project for lifecycle and durable projection fixtures.
func validControllerProject() domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        "orders",
		Name:      "Orders",
		Path:      "/workspace/orders",
		Slug:      "orders",
		State:     domain.ProjectStopped,
		UpdatedAt: time.Date(2026, time.July, 18, 11, 0, 0, 0, time.UTC),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
}

// validTestRoot returns bounded public authority metadata for fake bootstrap.
func validTestRoot() certificates.Root {
	return certificates.Root{
		CertificatePEM: []byte("public certificate"),
		Fingerprint:    strings.Repeat("a", 64),
		NotBefore:      time.Date(2026, time.July, 18, 11, 55, 0, 0, time.UTC),
		NotAfter:       time.Date(2036, time.July, 15, 12, 0, 0, 0, time.UTC),
	}
}

// emptyDesiredState constructs the same validated zero-listener generation used in production.
func emptyDesiredState() dataplane.DesiredState {
	desired, err := dataplane.NewDesiredState(dataplane.ListenerPlan{}, nil, nil, 0)
	if err != nil {
		panic(err)
	}
	return desired
}

// newTestDataPlane creates one ready-capable fake with initialized status collections.
func newTestDataPlane(events *testEventLog) *testDataPlane {
	return &testDataPlane{
		done:         make(chan struct{}),
		snapshot:     dataplane.Snapshot{State: dataplane.StateNew, Relays: []dataplane.RelayStatus{}},
		closeRelease: make(chan struct{}),
		events:       events,
	}
}

// testControllerDependencies creates a complete fake production graph for focused lifecycle tests.
func testControllerDependencies(
	material *testMaterialStore,
	authority certificateAuthority,
	runtime dataPlane,
) dependencies {
	return dependencies{
		openMaterial: func() (certificateMaterialStore, error) {
			return material, nil
		},
		bootstrap: func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error) {
			return authority, nil
		},
		newDesiredState: func(state.RuntimeState) (dataplane.DesiredState, error) {
			return emptyDesiredState(), nil
		},
		newDataPlane: func(dataplane.Config) (dataPlane, error) {
			return runtime, nil
		},
		cleanupTimeout: 50 * time.Millisecond,
	}
}

// newFakeController constructs one normal controller or fails the owning test immediately.
func newFakeController(t *testing.T, source runtimeStateSource, dependencies dependencies) *Controller {
	t.Helper()
	controller, err := newController(source, dependencies)
	if err != nil {
		t.Fatalf("newController() error = %v", err)
	}
	return controller
}

// waitControllerSignal waits for a deterministic lifecycle boundary.
func waitControllerSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(controllerTestWait):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// TestControllerConstructionIsSideEffectFree verifies dependency assembly performs neither durable reads nor material opens.
func TestControllerConstructionIsSideEffectFree(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	runtime := newTestDataPlane(nil)
	var opens atomic.Int64
	dependencies := testControllerDependencies(material, authority, runtime)
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		opens.Add(1)
		return material, nil
	}

	controller := newFakeController(t, source, dependencies)
	if source.calls.Load() != 0 || opens.Load() != 0 || material.closes.Load() != 0 {
		t.Fatalf("construction effects = runtime state %d, opens %d, closes %d", source.calls.Load(), opens.Load(), material.closes.Load())
	}
	select {
	case <-controller.Done():
		t.Fatal("constructed controller is terminal before use")
	default:
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() before Start error = %v", err)
	}
}

// TestProductionControllerConstructionPublishesItsShutdownBudget verifies assembly remains inert and gives the outer daemon a stable bound.
func TestProductionControllerConstructionPublishesItsShutdownBudget(t *testing.T) {
	controller, err := NewController(state.NewStore(nil, nil, nil, nil))
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if got := controller.ShutdownTimeout(); got != cleanupTimeout {
		t.Fatalf("ShutdownTimeout() = %s, want %s", got, cleanupTimeout)
	}
	if err := controller.Close(nil); err != nil {
		t.Fatalf("Close(nil) before Start error = %v", err)
	}
}

// TestNewControllerRejectsIncompleteDependencies verifies all required seams fail at construction.
func TestNewControllerRejectsIncompleteDependencies(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	var typedNilSource *testRuntimeStateSource
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	runtime := newTestDataPlane(nil)
	valid := testControllerDependencies(material, authority, runtime)

	tests := []struct {
		name         string
		source       runtimeStateSource
		dependencies dependencies
	}{
		{name: "source", source: nil, dependencies: valid},
		{name: "typed nil source", source: typedNilSource, dependencies: valid},
		{name: "material opener", source: source, dependencies: func() dependencies { value := valid; value.openMaterial = nil; return value }()},
		{name: "bootstrapper", source: source, dependencies: func() dependencies { value := valid; value.bootstrap = nil; return value }()},
		{name: "desired factory", source: source, dependencies: func() dependencies { value := valid; value.newDesiredState = nil; return value }()},
		{name: "runtime factory", source: source, dependencies: func() dependencies { value := valid; value.newDataPlane = nil; return value }()},
		{name: "cleanup timeout", source: source, dependencies: func() dependencies { value := valid; value.cleanupTimeout = 0; return value }()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, err := newController(test.source, test.dependencies)
			if err == nil || controller != nil {
				t.Fatalf("newController() = (%v, %v), want nil and wiring error", controller, err)
			}
		})
	}
	if controller, err := NewController(nil); err == nil || controller != nil {
		t.Fatalf("NewController(nil) = (%v, %v), want nil and wiring error", controller, err)
	}
}

// TestControllerRejectsProjectsBeforeMaterialMutation keeps empty startup honest until leases and bindings exist.
func TestControllerRejectsProjectsBeforeMaterialMutation(t *testing.T) {
	snapshot := validControllerSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{validControllerProject()}
	source := &testRuntimeStateSource{snapshot: snapshot}
	material := &testMaterialStore{}
	var opens atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil))
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		opens.Add(1)
		return material, nil
	}
	controller := newFakeController(t, source, dependencies)

	err := controller.Start(context.Background())
	if !errors.Is(err, ErrProjectsRequireNetworkProjection) {
		t.Fatalf("Start() error = %v, want %v", err, ErrProjectsRequireNetworkProjection)
	}
	if opens.Load() != 0 || material.closes.Load() != 0 {
		t.Fatalf("project rejection touched material: opens %d, closes %d", opens.Load(), material.closes.Load())
	}
	waitControllerSignal(t, controller.Done(), "project rejection")
}

// TestControllerRejectsInvalidRuntimeStateBeforeMaterialMutation proves network corruption cannot reach protected certificate storage.
func TestControllerRejectsInvalidRuntimeStateBeforeMaterialMutation(t *testing.T) {
	source := &testRuntimeStateSource{
		snapshot:           validControllerSnapshot(),
		network:            state.NetworkRecord{},
		networkInitialized: true,
	}
	material := &testMaterialStore{}
	var opens atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil))
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		opens.Add(1)
		return material, nil
	}
	controller := newFakeController(t, source, dependencies)

	err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate durable state") {
		t.Fatalf("Start() error = %v, want durable aggregate validation failure", err)
	}
	if opens.Load() != 0 || material.closes.Load() != 0 {
		t.Fatalf("invalid aggregate touched material: opens %d, closes %d", opens.Load(), material.closes.Load())
	}
	waitControllerSignal(t, controller.Done(), "invalid aggregate rejection")
}

// TestControllerRejectsDesiredProjectionBeforeMaterialMutation proves pure projection failures do not acquire protected state.
func TestControllerRejectsDesiredProjectionBeforeMaterialMutation(t *testing.T) {
	projectionErr := errors.New("desired projection failed")
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	var opens atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil))
	dependencies.newDesiredState = func(state.RuntimeState) (dataplane.DesiredState, error) {
		return dataplane.DesiredState{}, projectionErr
	}
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		opens.Add(1)
		return material, nil
	}
	controller := newFakeController(t, source, dependencies)

	err := controller.Start(context.Background())
	if !errors.Is(err, projectionErr) {
		t.Fatalf("Start() error = %v, want %v", err, projectionErr)
	}
	if opens.Load() != 0 || material.closes.Load() != 0 {
		t.Fatalf("projection failure touched material: opens %d, closes %d", opens.Load(), material.closes.Load())
	}
	waitControllerSignal(t, controller.Done(), "projection rejection")
}

// TestControllerStartsInitializedInfrastructureGeneration verifies the exact durable aggregate and certificate provider reach the data plane.
func TestControllerStartsInitializedInfrastructureGeneration(t *testing.T) {
	runtimeState := initializedControllerRuntimeState()
	source := &testRuntimeStateSource{
		snapshot:           runtimeState.Snapshot,
		network:            runtimeState.Network,
		networkInitialized: true,
	}
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	runtime := newTestDataPlane(nil)
	dependencies := testControllerDependencies(material, authority, runtime)
	var desiredInput state.RuntimeState
	dependencies.newDesiredState = func(input state.RuntimeState) (dataplane.DesiredState, error) {
		desiredInput = input
		return desiredStateFromRuntimeState(input)
	}
	var captured dataplane.Config
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		captured = config
		return runtime, nil
	}
	controller := newFakeController(t, source, dependencies)

	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !reflect.DeepEqual(desiredInput, runtimeState) {
		t.Fatalf("desired factory input = %#v, want exact runtime state %#v", desiredInput, runtimeState)
	}
	wantListeners := dataplane.ListenerPlan{
		DNS:   runtimeState.Network.Reservations.Listeners.DNS.Bind,
		HTTP:  runtimeState.Network.Reservations.Listeners.HTTP.Bind,
		HTTPS: runtimeState.Network.Reservations.Listeners.HTTPS.Bind,
	}
	if got := captured.Desired.ListenerPlan(); got != wantListeners {
		t.Fatalf("data plane listeners = %#v, want %#v", got, wantListeners)
	}
	if len(captured.Desired.HTTPRoutes()) != 0 || len(captured.Desired.NativeRoutes()) != 0 || len(captured.Desired.DNSRecords()) != 0 {
		t.Fatalf("data plane published pending routes: HTTP %v, native %v, DNS %v", captured.Desired.HTTPRoutes(), captured.Desired.NativeRoutes(), captured.Desired.DNSRecords())
	}
	if captured.CertificateProvider == nil {
		t.Fatal("data plane config omitted the bootstrapped certificate provider")
	}
	if _, err := captured.CertificateProvider(context.Background(), "orders.test"); err == nil {
		t.Fatal("captured certificate provider did not call the fake authority")
	}
	if authority.certificateCalls.Load() != 1 {
		t.Fatalf("certificate authority calls = %d, want one through captured config", authority.certificateCalls.Load())
	}
	if captured.StartupTimeout != 0 || captured.ShutdownTimeout != 0 {
		t.Fatalf("data plane lifecycle overrides = startup %s, shutdown %s, want defaults", captured.StartupTimeout, captured.ShutdownTimeout)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("cleanup calls = runtime %d, material %d, want one each", runtime.closes.Load(), material.closes.Load())
	}
}

// TestControllerStartsEmptyGenerationAndClosesInOrder proves readiness and defensive status publication.
func TestControllerStartsEmptyGenerationAndClosesInOrder(t *testing.T) {
	events := &testEventLog{}
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{events: events}
	authority := &testCertificateAuthority{root: validTestRoot()}
	runtime := newTestDataPlane(events)
	controller := newFakeController(t, source, testControllerDependencies(material, authority, runtime))

	if _, err := controller.NetworkSnapshot(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("NetworkSnapshot() before Start error = %v, want %v", err, ErrNotReady)
	}
	if _, err := controller.PublicRoot(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("PublicRoot() before Start error = %v, want %v", err, ErrNotReady)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	network, err := controller.NetworkSnapshot()
	if err != nil {
		t.Fatalf("NetworkSnapshot() error = %v", err)
	}
	if err := network.Validate(); err != nil {
		t.Fatalf("NetworkSnapshot().Validate() error = %v", err)
	}
	if network.State != dataplane.StateReady || network.DNS.Configured || network.Ingress.Configured || len(network.Relays) != 0 {
		t.Fatalf("empty network snapshot = %#v", network)
	}
	root, err := controller.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot() error = %v", err)
	}
	root.CertificatePEM[0] = 'X'
	again, err := controller.PublicRoot()
	if err != nil {
		t.Fatalf("second PublicRoot() error = %v", err)
	}
	if reflect.DeepEqual(root.CertificatePEM, again.CertificatePEM) {
		t.Fatal("mutating returned public root changed controller state")
	}

	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got, want := events.snapshot(), []string{"runtime.close", "material.close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup events = %v, want %v", got, want)
	}
	if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("cleanup calls = runtime %d, material %d", runtime.closes.Load(), material.closes.Load())
	}
}

// TestControllerStartupFailuresRollbackOwnedResources verifies every acquisition boundary unwinds in order.
func TestControllerStartupFailuresRollbackOwnedResources(t *testing.T) {
	readErr := errors.New("state unavailable")
	openErr := errors.New("material unavailable")
	bootstrapErr := errors.New("bootstrap failed")
	rootErr := errors.New("root failed")
	desiredErr := errors.New("desired failed")
	runtimeFactoryErr := errors.New("runtime factory failed")
	startErr := errors.New("runtime start failed")

	tests := []struct {
		name              string
		mutate            func(*testRuntimeStateSource, *testMaterialStore, *testCertificateAuthority, *testDataPlane, *dependencies)
		want              error
		wantMaterialClose int64
		wantRuntimeClose  int64
	}{
		{name: "state read", mutate: func(source *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, _ *dependencies) {
			source.err = readErr
		}, want: readErr},
		{name: "state validation", mutate: func(source *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, _ *dependencies) {
			source.snapshot.Projects = nil
		}, want: errors.New("validate durable state")},
		{name: "material open", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.openMaterial = func() (certificateMaterialStore, error) { return nil, openErr }
		}, want: openErr},
		{name: "nil material", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.openMaterial = func() (certificateMaterialStore, error) { return nil, nil }
		}, want: errors.New("returned nil")},
		{name: "typed nil material", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			var material *testMaterialStore
			dependencies.openMaterial = func() (certificateMaterialStore, error) { return material, nil }
		}, want: errors.New("returned nil")},
		{name: "bootstrap", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.bootstrap = func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error) {
				return nil, bootstrapErr
			}
		}, want: bootstrapErr, wantMaterialClose: 1},
		{name: "nil authority", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.bootstrap = func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error) {
				return nil, nil
			}
		}, want: errors.New("returned nil"), wantMaterialClose: 1},
		{name: "typed nil authority", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			var authority *testCertificateAuthority
			dependencies.bootstrap = func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error) {
				return authority, nil
			}
		}, want: errors.New("returned nil"), wantMaterialClose: 1},
		{name: "public root", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, authority *testCertificateAuthority, _ *testDataPlane, _ *dependencies) {
			authority.rootErr = rootErr
		}, want: rootErr, wantMaterialClose: 1},
		{name: "desired", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.newDesiredState = func(state.RuntimeState) (dataplane.DesiredState, error) {
				return dataplane.DesiredState{}, desiredErr
			}
		}, want: desiredErr},
		{name: "runtime factory", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.newDataPlane = func(dataplane.Config) (dataPlane, error) { return nil, runtimeFactoryErr }
		}, want: runtimeFactoryErr, wantMaterialClose: 1},
		{name: "nil runtime", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			dependencies.newDataPlane = func(dataplane.Config) (dataPlane, error) { return nil, nil }
		}, want: errors.New("returned nil"), wantMaterialClose: 1},
		{name: "typed nil runtime", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
			var runtime *testDataPlane
			dependencies.newDataPlane = func(dataplane.Config) (dataPlane, error) { return runtime, nil }
		}, want: errors.New("returned nil"), wantMaterialClose: 1},
		{name: "runtime start", mutate: func(_ *testRuntimeStateSource, _ *testMaterialStore, _ *testCertificateAuthority, runtime *testDataPlane, _ *dependencies) {
			runtime.startErr = startErr
		}, want: startErr, wantMaterialClose: 1, wantRuntimeClose: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := &testEventLog{}
			source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
			material := &testMaterialStore{events: events}
			authority := &testCertificateAuthority{root: validTestRoot()}
			runtime := newTestDataPlane(events)
			dependencies := testControllerDependencies(material, authority, runtime)
			test.mutate(source, material, authority, runtime, &dependencies)
			controller := newFakeController(t, source, dependencies)

			err := controller.Start(context.Background())
			if err == nil || !errorMatches(err, test.want) {
				t.Fatalf("Start() error = %v, want %v", err, test.want)
			}
			if material.closes.Load() != test.wantMaterialClose || runtime.closes.Load() != test.wantRuntimeClose {
				t.Fatalf("cleanup calls = material %d, runtime %d; want %d, %d", material.closes.Load(), runtime.closes.Load(), test.wantMaterialClose, test.wantRuntimeClose)
			}
			if test.wantRuntimeClose != 0 {
				if got := events.snapshot(); !reflect.DeepEqual(got, []string{"runtime.close", "material.close"}) {
					t.Fatalf("cleanup events = %v", got)
				}
			}
			waitControllerSignal(t, controller.Done(), "startup rollback")
		})
	}
}

// TestControllerHonorsCancellationAtStartupBoundaries verifies every acquired prefix unwinds before readiness publication.
func TestControllerHonorsCancellationAtStartupBoundaries(t *testing.T) {
	tests := []struct {
		name              string
		mutate            func(context.CancelFunc, *testRuntimeStateSource, *testCertificateAuthority, *testDataPlane, *dependencies)
		wantMaterialClose int64
		wantRuntimeClose  int64
	}{
		{
			name: "after durable runtime state",
			mutate: func(cancel context.CancelFunc, source *testRuntimeStateSource, _ *testCertificateAuthority, _ *testDataPlane, _ *dependencies) {
				source.after = cancel
			},
		},
		{
			name: "after material open",
			mutate: func(cancel context.CancelFunc, _ *testRuntimeStateSource, _ *testCertificateAuthority, _ *testDataPlane, dependencies *dependencies) {
				openMaterial := dependencies.openMaterial
				dependencies.openMaterial = func() (certificateMaterialStore, error) {
					material, err := openMaterial()
					cancel()
					return material, err
				}
			},
			wantMaterialClose: 1,
		},
		{
			name: "after public root",
			mutate: func(cancel context.CancelFunc, _ *testRuntimeStateSource, authority *testCertificateAuthority, _ *testDataPlane, _ *dependencies) {
				authority.afterRoot = cancel
			},
			wantMaterialClose: 1,
		},
		{
			name: "after data plane start",
			mutate: func(cancel context.CancelFunc, _ *testRuntimeStateSource, _ *testCertificateAuthority, runtime *testDataPlane, _ *dependencies) {
				runtime.afterStart = cancel
			},
			wantMaterialClose: 1,
			wantRuntimeClose:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
			material := &testMaterialStore{}
			authority := &testCertificateAuthority{root: validTestRoot()}
			runtime := newTestDataPlane(nil)
			dependencies := testControllerDependencies(material, authority, runtime)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.mutate(cancel, source, authority, runtime, &dependencies)
			controller := newFakeController(t, source, dependencies)

			err := controller.Start(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Start() error = %v, want cancellation", err)
			}
			waitControllerSignal(t, controller.Done(), "cancelled startup rollback")
			if err := controller.Err(); err != nil {
				t.Fatalf("Err() = %v, want nil after expected cancellation", err)
			}
			if material.closes.Load() != test.wantMaterialClose || runtime.closes.Load() != test.wantRuntimeClose {
				t.Fatalf(
					"cleanup calls = material %d, runtime %d; want %d, %d",
					material.closes.Load(),
					runtime.closes.Load(),
					test.wantMaterialClose,
					test.wantRuntimeClose,
				)
			}
		})
	}
}

// TestControllerRetainsParentDeadlineCause verifies private lifecycle cancellation does not erase the parent reason.
func TestControllerRetainsParentDeadlineCause(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot(), block: true}
	controller := newFakeController(
		t,
		source,
		testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := controller.Start(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start() error = %v, want parent deadline", err)
	}
	if err := controller.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil after expected parent deadline", err)
	}
}

// errorMatches supports both sentinel and message-only table expectations.
func errorMatches(got error, want error) bool {
	return errors.Is(got, want) || strings.Contains(got.Error(), want.Error())
}

// TestControllerCloseRacingStartupCancelsAndRollsBack verifies shutdown cannot strand a partial generation.
func TestControllerCloseRacingStartupCancelsAndRollsBack(t *testing.T) {
	t.Run("durable read", func(t *testing.T) {
		source := &testRuntimeStateSource{snapshot: validControllerSnapshot(), entered: make(chan struct{}), block: true}
		material := &testMaterialStore{}
		controller := newFakeController(t, source, testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil)))
		startResult := make(chan error, 1)
		go func() { startResult <- controller.Start(context.Background()) }()
		waitControllerSignal(t, source.entered, "durable read")
		if err := controller.Close(context.Background()); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := <-startResult; !errors.Is(err, ErrClosed) {
			t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
		}
		if material.closes.Load() != 0 {
			t.Fatalf("material closes = %d, want zero", material.closes.Load())
		}
	})

	t.Run("data plane start", func(t *testing.T) {
		events := &testEventLog{}
		source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
		material := &testMaterialStore{events: events}
		runtime := newTestDataPlane(events)
		runtime.blockStart = true
		runtime.startEntered = make(chan struct{})
		controller := newFakeController(t, source, testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime))
		startResult := make(chan error, 1)
		go func() { startResult <- controller.Start(context.Background()) }()
		waitControllerSignal(t, runtime.startEntered, "data plane start")
		if err := controller.Close(context.Background()); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := <-startResult; !errors.Is(err, ErrClosed) {
			t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
		}
		if got := events.snapshot(); !reflect.DeepEqual(got, []string{"runtime.close", "material.close"}) {
			t.Fatalf("cleanup events = %v", got)
		}
	})
}

// TestControllerCancellationDuringRuntimeStartRetainsRollbackFailure preserves mixed lifecycle and child cleanup causes.
func TestControllerCancellationDuringRuntimeStartRetainsRollbackFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger func(context.CancelFunc, *Controller) <-chan error
		want    error
	}{
		{
			name: "explicit close",
			trigger: func(_ context.CancelFunc, controller *Controller) <-chan error {
				result := make(chan error, 1)
				go func() { result <- controller.Close(context.Background()) }()
				return result
			},
			want: ErrClosed,
		},
		{
			name: "parent cancellation",
			trigger: func(cancel context.CancelFunc, _ *Controller) <-chan error {
				cancel()
				return nil
			},
			want: context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rollbackFailure := errors.New("runtime startup rollback failed")
			events := &testEventLog{}
			material := &testMaterialStore{events: events}
			runtime := newTestDataPlane(events)
			runtime.blockStart = true
			runtime.startEntered = make(chan struct{})
			runtime.startErr = rollbackFailure
			controller := newFakeController(
				t,
				&testRuntimeStateSource{snapshot: validControllerSnapshot()},
				testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startResult := make(chan error, 1)
			go func() { startResult <- controller.Start(ctx) }()
			waitControllerSignal(t, runtime.startEntered, "data plane startup")

			closeResult := test.trigger(cancel, controller)
			startErr := <-startResult
			if !errors.Is(startErr, test.want) || !errors.Is(startErr, rollbackFailure) {
				t.Fatalf("Start() error = %v, want %v and %v", startErr, test.want, rollbackFailure)
			}
			waitControllerSignal(t, controller.Done(), "mixed startup rollback")
			if err := controller.Err(); !errors.Is(err, rollbackFailure) {
				t.Fatalf("Err() = %v, want %v", err, rollbackFailure)
			}
			if closeResult != nil {
				if err := <-closeResult; !errors.Is(err, rollbackFailure) {
					t.Fatalf("concurrent Close() error = %v, want %v", err, rollbackFailure)
				}
			} else if err := controller.Close(context.Background()); !errors.Is(err, rollbackFailure) {
				t.Fatalf("terminal Close() error = %v, want %v", err, rollbackFailure)
			}
			if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
				t.Fatalf("cleanup calls = runtime %d, material %d, want one each", runtime.closes.Load(), material.closes.Load())
			}
			if got := events.snapshot(); !reflect.DeepEqual(got, []string{"runtime.close", "material.close"}) {
				t.Fatalf("cleanup events = %v", got)
			}
		})
	}
}

// TestControllerStopClaimPreventsPostSnapshotMutation proves ordered shutdown is visible before startup can acquire material.
func TestControllerStopClaimPreventsPostSnapshotMutation(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	var opens atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil))
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		opens.Add(1)
		return material, nil
	}
	var controller *Controller
	closeResult := make(chan error, 1)
	source.after = func() {
		go func() {
			closeResult <- controller.Close(context.Background())
		}()
		waitControllerSignal(t, controller.stop, "atomic startup stop claim")
	}
	controller = newFakeController(t, source, dependencies)

	if err := controller.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() error = %v, want %v", err, ErrClosed)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if opens.Load() != 0 || material.closes.Load() != 0 {
		t.Fatalf("post-stop material effects = opens %d, closes %d", opens.Load(), material.closes.Load())
	}
}

// TestControllerConcurrentCloseIsIdempotent verifies one child and store cleanup serves every waiter.
func TestControllerConcurrentCloseIsIdempotent(t *testing.T) {
	material := &testMaterialStore{}
	runtime := newTestDataPlane(nil)
	controller := newFakeController(
		t,
		&testRuntimeStateSource{snapshot: validControllerSnapshot()},
		testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
	)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	const callers = 32
	results := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			<-start
			results <- controller.Close(context.Background())
		}()
	}
	close(start)
	for range callers {
		if err := <-results; err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
	if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("cleanup calls = runtime %d, material %d", runtime.closes.Load(), material.closes.Load())
	}
}

// TestControllerShutdownWaitersHonorTheirOwnContext verifies one impatient waiter cannot interrupt shared cleanup.
func TestControllerShutdownWaitersHonorTheirOwnContext(t *testing.T) {
	material := &testMaterialStore{}
	runtime := newTestDataPlane(nil)
	runtime.closeMode = testCloseBlocks
	runtime.closeEntered = make(chan struct{})
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
	dependencies.cleanupTimeout = controllerTestWait
	controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- controller.Close(context.Background())
	}()
	waitControllerSignal(t, runtime.closeEntered, "data plane close")
	waitContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if err := controller.Close(waitContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Close() error = %v, want waiter cancellation", err)
	}
	close(runtime.closeRelease)
	if err := <-firstResult; err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("cleanup calls = runtime %d, material %d", runtime.closes.Load(), material.closes.Load())
	}
}

// TestControllerRetainsUnexpectedExitWhenDonePrecedesIntent proves child-first causality does not depend on monitor scheduling.
func TestControllerRetainsUnexpectedExitWhenDonePrecedesIntent(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger func(context.CancelFunc, *Controller) <-chan error
	}{
		{name: "explicit close", trigger: func(_ context.CancelFunc, controller *Controller) <-chan error {
			result := make(chan error, 1)
			go func() {
				result <- controller.Close(context.Background())
			}()
			return result
		}},
		{name: "parent cancellation", trigger: func(cancel context.CancelFunc, controller *Controller) <-chan error {
			result := make(chan error, 1)
			cancel()
			go func() {
				<-controller.Done()
				result <- controller.Err()
			}()
			return result
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for range 100 {
				material := &testMaterialStore{}
				runtime := newTestDataPlane(nil)
				controller := newFakeController(
					t,
					&testRuntimeStateSource{snapshot: validControllerSnapshot()},
					testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
				)
				ctx, cancel := context.WithCancel(context.Background())
				if err := controller.Start(ctx); err != nil {
					t.Fatalf("Start() error = %v", err)
				}

				controller.mutex.Lock()
				runtime.complete(nil)
				result := test.trigger(cancel, controller)
				controller.mutex.Unlock()

				if err := <-result; !errors.Is(err, ErrRuntimeStoppedUnexpectedly) {
					t.Fatalf("terminal error = %v, want %v", err, ErrRuntimeStoppedUnexpectedly)
				}
				cancel()
			}
		})
	}
}

// TestControllerTreatsDoneAfterIntentAsExpected proves explicit and parent stop claims win before later child completion.
func TestControllerTreatsDoneAfterIntentAsExpected(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger func(context.CancelFunc, *Controller) <-chan error
	}{
		{name: "explicit close", trigger: func(_ context.CancelFunc, controller *Controller) <-chan error {
			result := make(chan error, 1)
			go func() {
				result <- controller.Close(context.Background())
			}()
			return result
		}},
		{name: "parent cancellation", trigger: func(cancel context.CancelFunc, controller *Controller) <-chan error {
			result := make(chan error, 1)
			cancel()
			go func() {
				<-controller.Done()
				result <- controller.Err()
			}()
			return result
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			material := &testMaterialStore{}
			runtime := newTestDataPlane(nil)
			runtime.closeMode = testCloseBlocks
			runtime.closeEntered = make(chan struct{})
			dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
			dependencies.cleanupTimeout = controllerTestWait
			controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := controller.Start(ctx); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			result := test.trigger(cancel, controller)
			waitControllerSignal(t, controller.stop, "ordered stop claim")
			waitControllerSignal(t, runtime.closeEntered, "data plane close")
			runtime.complete(nil)
			close(runtime.closeRelease)

			if err := <-result; err != nil {
				t.Fatalf("terminal error = %v, want nil", err)
			}
		})
	}
}

// TestControllerPropagatesUnexpectedRuntimeFailure verifies child diagnostics survive joined cleanup.
func TestControllerPropagatesUnexpectedRuntimeFailure(t *testing.T) {
	failure := errors.New("listener failed")
	material := &testMaterialStore{}
	runtime := newTestDataPlane(nil)
	controller := newFakeController(
		t,
		&testRuntimeStateSource{snapshot: validControllerSnapshot()},
		testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
	)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	runtime.fail(failure)
	waitControllerSignal(t, controller.Done(), "runtime failure cleanup")
	if err := controller.Err(); !errors.Is(err, failure) {
		t.Fatalf("Err() = %v, want %v", err, failure)
	}
	if material.closes.Load() != 1 {
		t.Fatalf("material closes = %d, want one", material.closes.Load())
	}
	if err := controller.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("Close() after failure error = %v, want %v", err, failure)
	}
}

// TestControllerRetainsAuthorityAcrossBrokenRuntimeCleanup verifies escalation never releases child-owned material early.
func TestControllerRetainsAuthorityAcrossBrokenRuntimeCleanup(t *testing.T) {
	for _, test := range []struct {
		name string
		mode testCloseMode
	}{
		{name: "returns without Done", mode: testCloseReturnsWithoutDone},
		{name: "blocks past context", mode: testCloseBlocks},
	} {
		t.Run(test.name, func(t *testing.T) {
			material := &testMaterialStore{}
			runtime := newTestDataPlane(nil)
			runtime.closeMode = test.mode
			dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
			dependencies.cleanupTimeout = 20 * time.Millisecond
			controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
			if err := controller.Start(context.Background()); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			closeContext, cancelClose := context.WithTimeout(context.Background(), 75*time.Millisecond)
			err := controller.Close(closeContext)
			cancelClose()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close() error = %v, want caller deadline", err)
			}
			if material.closes.Load() != 0 {
				t.Fatalf("material closes = %d, want zero while child retains ownership", material.closes.Load())
			}
			select {
			case <-controller.Done():
				t.Fatal("Controller.Done closed before child Done")
			default:
			}
			if test.mode == testCloseBlocks {
				close(runtime.closeRelease)
			} else {
				runtime.complete(nil)
			}
			waitControllerSignal(t, controller.Done(), "post-escalation ownership release")
			if err := controller.Err(); !errors.Is(err, ErrRuntimeShutdownIncomplete) {
				t.Fatalf("Err() = %v, want %v", err, ErrRuntimeShutdownIncomplete)
			}
			if material.closes.Load() != 1 {
				t.Fatalf("material closes = %d, want one after child Done", material.closes.Load())
			}
		})
	}
}

// TestControllerCompletionIncludesMaterialClose proves the child budget does not truncate authority-store ownership.
func TestControllerCompletionIncludesMaterialClose(t *testing.T) {
	material := &testMaterialStore{closeEntered: make(chan struct{}), closeRelease: make(chan struct{})}
	runtime := newTestDataPlane(nil)
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
	dependencies.cleanupTimeout = 20 * time.Millisecond
	controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancelClose()
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- controller.Close(closeContext)
	}()
	waitControllerSignal(t, material.closeEntered, "material close")
	if err := <-closeResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want caller deadline", err)
	}
	select {
	case <-controller.Done():
		t.Fatal("Controller.Done closed before material Close returned")
	default:
	}
	close(material.closeRelease)
	waitControllerSignal(t, controller.Done(), "material ownership release")
	if err := controller.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
}

// TestControllerEscalatesCloseThatOutlivesChildDone verifies terminal ownership can advance after the child signal is authoritative.
func TestControllerEscalatesCloseThatOutlivesChildDone(t *testing.T) {
	material := &testMaterialStore{}
	runtime := newTestDataPlane(nil)
	runtime.closeMode = testCloseBlocksAfterDone
	runtime.closeEntered = make(chan struct{})
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
	dependencies.cleanupTimeout = 20 * time.Millisecond
	controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	runtime.complete(nil)
	waitControllerSignal(t, runtime.closeEntered, "post-Done data plane close")
	waitControllerSignal(t, controller.Done(), "post-Done cleanup escalation")
	if err := controller.Err(); !errors.Is(err, ErrRuntimeStoppedUnexpectedly) || !errors.Is(err, ErrRuntimeShutdownIncomplete) {
		t.Fatalf("Err() = %v, want unexpected exit and incomplete Close", err)
	}
	if material.closes.Load() != 1 {
		t.Fatalf("material closes = %d, want one after child Done", material.closes.Load())
	}
	close(runtime.closeRelease)
}

// TestControllerRejectsNilAndPreclosedRuntimeCompletion prevents monitoring an unusable child lifecycle.
func TestControllerRejectsNilAndPreclosedRuntimeCompletion(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		material := &testMaterialStore{}
		runtime := newTestDataPlane(nil)
		runtime.done = nil
		controller := newFakeController(
			t,
			&testRuntimeStateSource{snapshot: validControllerSnapshot()},
			testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
		)
		err := controller.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "nil Done") || !errors.Is(err, ErrRuntimeShutdownIncomplete) {
			t.Fatalf("Start() error = %v, want nil Done and bounded cleanup", err)
		}
		if runtime.starts.Load() != 0 || material.closes.Load() != 1 {
			t.Fatalf("calls = starts %d, material closes %d", runtime.starts.Load(), material.closes.Load())
		}
	})

	t.Run("nil with blocked close", func(t *testing.T) {
		material := &testMaterialStore{}
		runtime := newTestDataPlane(nil)
		runtime.done = nil
		runtime.closeMode = testCloseBlocks
		dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime)
		dependencies.cleanupTimeout = 20 * time.Millisecond
		controller := newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
		err := controller.Start(context.Background())
		if !errors.Is(err, ErrRuntimeShutdownIncomplete) {
			t.Fatalf("Start() error = %v, want %v", err, ErrRuntimeShutdownIncomplete)
		}
		close(runtime.closeRelease)
		if material.closes.Load() != 1 {
			t.Fatalf("material closes = %d, want one", material.closes.Load())
		}
	})

	t.Run("nil remains the captured ownership signal", func(t *testing.T) {
		material := &testMaterialStore{}
		runtime := newTestDataPlane(nil)
		subsequent := make(chan struct{})
		close(subsequent)
		runtime.doneFunc = func() <-chan struct{} {
			if runtime.doneCalls.Load() == 1 {
				return nil
			}
			return subsequent
		}
		controller := newFakeController(
			t,
			&testRuntimeStateSource{snapshot: validControllerSnapshot()},
			testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
		)
		err := controller.Start(context.Background())
		if !errors.Is(err, ErrRuntimeShutdownIncomplete) || !strings.Contains(err.Error(), "nil Done") {
			t.Fatalf("Start() error = %v, want captured nil Done failure", err)
		}
		if runtime.doneCalls.Load() != 1 {
			t.Fatalf("Done() calls = %d, want one captured signal", runtime.doneCalls.Load())
		}
		if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
			t.Fatalf("cleanup calls = runtime %d, material %d, want one each", runtime.closes.Load(), material.closes.Load())
		}
	})

	t.Run("preclosed", func(t *testing.T) {
		material := &testMaterialStore{}
		runtime := newTestDataPlane(nil)
		runtime.complete(nil)
		controller := newFakeController(
			t,
			&testRuntimeStateSource{snapshot: validControllerSnapshot()},
			testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
		)
		err := controller.Start(context.Background())
		if !errors.Is(err, ErrRuntimeStoppedUnexpectedly) {
			t.Fatalf("Start() error = %v, want %v", err, ErrRuntimeStoppedUnexpectedly)
		}
		if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
			t.Fatalf("cleanup calls = runtime %d, material %d", runtime.closes.Load(), material.closes.Load())
		}
	})

	for _, test := range []struct {
		name    string
		failure error
	}{
		{name: "stops during Start"},
		{name: "fails during Start", failure: errors.New("listener failed during Start")},
	} {
		t.Run(test.name, func(t *testing.T) {
			material := &testMaterialStore{}
			runtime := newTestDataPlane(nil)
			runtime.afterStart = func() {
				runtime.complete(test.failure)
			}
			controller := newFakeController(
				t,
				&testRuntimeStateSource{snapshot: validControllerSnapshot()},
				testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, runtime),
			)
			err := controller.Start(context.Background())
			want := test.failure
			if want == nil {
				want = ErrRuntimeStoppedUnexpectedly
			}
			if !errors.Is(err, want) {
				t.Fatalf("Start() error = %v, want %v", err, want)
			}
			if strings.Count(err.Error(), want.Error()) != 1 {
				t.Fatalf("Start() error duplicated child cause: %v", err)
			}
			if runtime.closes.Load() != 1 || material.closes.Load() != 1 {
				t.Fatalf("cleanup calls = runtime %d, material %d", runtime.closes.Load(), material.closes.Load())
			}
		})
	}
}

// TestControllerZeroAndClosedBehavior keeps invalid and consumed lifecycle states deterministic.
func TestControllerZeroAndClosedBehavior(t *testing.T) {
	var zero Controller
	if got := zero.ShutdownTimeout(); got != 0 {
		t.Fatalf("zero ShutdownTimeout() = %s, want zero", got)
	}
	if err := zero.Start(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero Start() error = %v", err)
	}
	if err := zero.Close(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero Close() error = %v", err)
	}
	if err := zero.Err(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero Err() = %v", err)
	}
	waitControllerSignal(t, zero.Done(), "zero Done")
	if _, err := zero.NetworkSnapshot(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero NetworkSnapshot() error = %v", err)
	}
	if _, err := zero.PublicRoot(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("zero PublicRoot() error = %v", err)
	}

	controller := newFakeController(
		t,
		&testRuntimeStateSource{snapshot: validControllerSnapshot()},
		testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil)),
	)
	if err := controller.Start(nil); err == nil {
		t.Fatal("Start(nil) error = nil")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() before Start error = %v", err)
	}
	if err := controller.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() after Close error = %v, want %v", err, ErrClosed)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
}

// TestControllerInternalLifecycleGuards verifies invariant failures remain explicit rather than publishing partial state.
func TestControllerInternalLifecycleGuards(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	runtime := newTestDataPlane(nil)
	controller := newFakeController(t, source, testControllerDependencies(material, authority, runtime))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := controller.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() with cancelled context error = %v, want cancellation", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() after unclaimed cancelled attempt error = %v", err)
	}
	if err := controller.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start() error = %v, want %v", err, ErrAlreadyStarted)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	guarded := newFakeController(t, source, testControllerDependencies(&testMaterialStore{}, authority, newTestDataPlane(nil)))
	if err := guarded.registerRuntimeDone(runtime.Done()); !errors.Is(err, ErrClosed) {
		t.Fatalf("registerRuntimeDone() before startup error = %v, want %v", err, ErrClosed)
	}
	if err := guarded.publishReady(material, authority, validTestRoot(), runtime, runtime.Done()); !errors.Is(err, ErrClosed) {
		t.Fatalf("publishReady() before startup error = %v, want %v", err, ErrClosed)
	}
	if err := guarded.startupInterruption(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("startupInterruption() before startup error = %v, want %v", err, ErrClosed)
	}
	if err := guarded.Close(context.Background()); err != nil {
		t.Fatalf("guarded Close() error = %v", err)
	}
	if err := guarded.startupInterruption(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("startupInterruption() after stop error = %v, want %v", err, ErrClosed)
	}

	invalid := newFakeController(t, source, testControllerDependencies(&testMaterialStore{}, authority, newTestDataPlane(nil)))
	invalid.mutex.Lock()
	invalid.state = controllerState(255)
	invalid.mutex.Unlock()
	if err := invalid.Close(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Close() with invalid state error = %v, want %v", err, ErrNotInitialized)
	}

	fresh := newFakeController(t, source, testControllerDependencies(&testMaterialStore{}, authority, newTestDataPlane(nil)))
	if got := fresh.orderedStartupCause(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("orderedStartupCause() = %v, want cancellation", got)
	}
	if err := fresh.Close(context.Background()); err != nil {
		t.Fatalf("fresh Close() error = %v", err)
	}
}

// TestDistinctRuntimeErrorRetainsUniqueCauses verifies cleanup reporting neither drops nor duplicates diagnostics.
func TestDistinctRuntimeErrorRetainsUniqueCauses(t *testing.T) {
	terminal := errors.New("terminal")
	closeErr := errors.New("close")
	closeWrap := fmt.Errorf("close runtime: %w", terminal)
	terminalWrap := fmt.Errorf("terminal runtime: %w", closeErr)

	for _, test := range []struct {
		name     string
		terminal error
		closeErr error
		want     error
	}{
		{name: "terminal absent", closeErr: closeErr, want: closeErr},
		{name: "close absent", terminal: terminal, want: terminal},
		{name: "close contains terminal", terminal: terminal, closeErr: closeWrap, want: closeWrap},
		{name: "terminal contains close", terminal: terminalWrap, closeErr: closeErr, want: terminalWrap},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := distinctRuntimeError(test.terminal, test.closeErr); got != test.want {
				t.Fatalf("distinctRuntimeError() = %v, want %v", got, test.want)
			}
		})
	}

	joined := distinctRuntimeError(terminal, closeErr)
	if !errors.Is(joined, terminal) || !errors.Is(joined, closeErr) {
		t.Fatalf("distinctRuntimeError() = %v, want both unique causes", joined)
	}
	if requiredInterfaceIsNil(42) {
		t.Fatal("requiredInterfaceIsNil() rejected a non-nilable value")
	}
}

// TestLifecycleInterruptionClassificationRejectsMixedFailures prevents operational causes from being suppressed.
func TestLifecycleInterruptionClassificationRejectsMixedFailures(t *testing.T) {
	operationFailure := errors.New("operation failed")
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "cancellation", err: context.Canceled, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "explicit close", err: ErrClosed, want: true},
		{
			name: "nested lifecycle causes",
			err: fmt.Errorf(
				"startup interrupted: %w",
				errors.Join(context.Canceled, fmt.Errorf("close requested: %w", ErrClosed)),
			),
			want: true,
		},
		{name: "operation failure", err: operationFailure},
		{name: "cancellation with operation failure", err: errors.Join(context.Canceled, operationFailure)},
		{name: "deadline with operation failure", err: errors.Join(context.DeadlineExceeded, operationFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isLifecycleInterruptionOnly(test.err); got != test.want {
				t.Fatalf("isLifecycleInterruptionOnly() = %t, want %t", got, test.want)
			}
		})
	}
}

// TestControllerPersistsAuthorityAcrossRestart verifies production trust and data-plane implementations compose on disk.
func TestControllerPersistsAuthorityAcrossRestart(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	config := certificates.Config{}
	firstRoot := runRealController(t, directory, config)
	secondRoot := runRealController(t, directory, config)
	if firstRoot.Fingerprint != secondRoot.Fingerprint {
		t.Fatalf("restart fingerprint = %q, want %q", secondRoot.Fingerprint, firstRoot.Fingerprint)
	}
	if !reflect.DeepEqual(firstRoot.CertificatePEM, secondRoot.CertificatePEM) {
		t.Fatal("restart loaded different public authority material")
	}
}

// TestControllerFailsClosedForCorruptAndExpiredAuthority verifies bootstrap never replaces an established root implicitly.
func TestControllerFailsClosedForCorruptAndExpiredAuthority(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "certificates")
		first := runRealController(t, directory, certificates.Config{})
		generations := authorityGenerationDirectories(t, directory)
		certificatesOnDisk, err := filepath.Glob(filepath.Join(generations[0], "certificate.pem"))
		if err != nil || len(certificatesOnDisk) != 1 {
			t.Fatalf("authority certificate glob = %v, %v", certificatesOnDisk, err)
		}
		if err := os.WriteFile(certificatesOnDisk[0], []byte("corrupt certificate\n"), 0o600); err != nil {
			t.Fatalf("corrupt authority certificate: %v", err)
		}

		controller := realController(t, directory, certificates.Config{})
		if err := controller.Start(context.Background()); err == nil {
			t.Fatal("Start() with corrupt authority error = nil")
		}
		if len(authorityGenerationDirectories(t, directory)) != len(generations) {
			t.Fatal("corrupt authority startup published a replacement generation")
		}
		if root, err := controller.PublicRoot(); err == nil || root.Fingerprint == first.Fingerprint {
			t.Fatalf("corrupt startup PublicRoot() = %#v, %v", root, err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "certificates")
		base := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
		now := base
		config := certificates.Config{
			Authority: localca.Config{
				CAValidity:   2 * time.Hour,
				LeafValidity: time.Hour,
				Backdate:     time.Minute,
				Now:          func() time.Time { return now },
			},
			RenewalWindow: 10 * time.Minute,
		}
		first := runRealController(t, directory, config)
		generations := authorityGenerationDirectories(t, directory)
		now = base.Add(3 * time.Hour)
		controller := realController(t, directory, config)
		if err := controller.Start(context.Background()); err == nil {
			t.Fatal("Start() with expired authority error = nil")
		}
		if len(authorityGenerationDirectories(t, directory)) != len(generations) {
			t.Fatal("expired authority startup published a replacement generation")
		}
		now = base
		again := runRealController(t, directory, config)
		if again.Fingerprint != first.Fingerprint {
			t.Fatalf("authority after expired rejection = %q, want %q", again.Fingerprint, first.Fingerprint)
		}
	})
}

// realController constructs production dependencies while redirecting only the material root to a test directory.
func realController(t *testing.T, directory string, config certificates.Config) *Controller {
	t.Helper()
	dependencies := productionDependencies()
	dependencies.certificateConfig = config
	dependencies.openMaterial = func() (certificateMaterialStore, error) {
		return materialstore.Open(directory)
	}
	return newFakeController(t, &testRuntimeStateSource{snapshot: validControllerSnapshot()}, dependencies)
}

// runRealController starts, observes, and closes one production generation.
func runRealController(t *testing.T, directory string, config certificates.Config) certificates.Root {
	t.Helper()
	controller := realController(t, directory, config)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	network, err := controller.NetworkSnapshot()
	if err != nil {
		t.Fatalf("NetworkSnapshot() error = %v", err)
	}
	if err := network.Validate(); err != nil || network.State != dataplane.StateReady {
		t.Fatalf("ready network snapshot = %#v, %v", network, err)
	}
	root, err := controller.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot() error = %v", err)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return root
}

// authorityGenerationDirectories returns the immutable root generations currently present on disk.
func authorityGenerationDirectories(t *testing.T, directory string) []string {
	t.Helper()
	generations, err := filepath.Glob(filepath.Join(directory, "v1", "authority", "generations", "*"))
	if err != nil {
		t.Fatalf("glob authority generations: %v", err)
	}
	if len(generations) == 0 {
		t.Fatal("no authority generation was persisted")
	}
	return generations
}
