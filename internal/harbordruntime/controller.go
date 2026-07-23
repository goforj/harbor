package harbordruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

const cleanupTimeout = 35 * time.Second

var (
	// ErrNotInitialized reports use of a zero-value or otherwise unconstructed Controller.
	ErrNotInitialized = errors.New("harbord runtime controller is not initialized")
	// ErrAlreadyStarted reports a second attempt to start one one-shot Controller.
	ErrAlreadyStarted = errors.New("harbord runtime controller lifecycle has already started")
	// ErrClosed reports that shutdown consumed the Controller before startup could complete.
	ErrClosed = errors.New("harbord runtime controller is closed")
	// ErrNotReady reports that runtime or certificate observations were requested before startup published them.
	ErrNotReady = errors.New("harbord runtime controller is not ready")
	// ErrRuntimeStoppedUnexpectedly reports a child generation that relinquished authority without cancellation or an error.
	ErrRuntimeStoppedUnexpectedly = errors.New("Harbor data plane stopped unexpectedly")
	// ErrRuntimeShutdownIncomplete reports a child that did not publish terminal ownership within the cleanup bound.
	ErrRuntimeShutdownIncomplete = errors.New("Harbor data plane shutdown did not complete")
	// ErrProjectWithdrawalUnverified reports that the live data plane cannot prove a project has no published routes.
	ErrProjectWithdrawalUnverified = errors.New("project route withdrawal is not verified")
)

// runtimeStateSource supplies one consistent durable runtime aggregate before startup performs any filesystem mutation.
type runtimeStateSource interface {
	RuntimeState(context.Context) (state.RuntimeState, error)
}

// globalNetworkReleasePlanStore supplies the durable release fence and its runtime checkpoint mutation.
type globalNetworkReleasePlanStore interface {
	ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error)
	AdvanceGlobalNetworkReleaseRuntime(context.Context, state.AdvanceGlobalNetworkReleaseRuntimeRequest) (state.GlobalNetworkReleasePlanRecord, error)
}

// certificateMaterialStore joins the certificate manager's persistence contract with controller-owned closure.
type certificateMaterialStore interface {
	certificates.MaterialStore
	Close() error
}

// certificateAuthority is the ready certificate surface retained for the lifetime of one controller generation.
type certificateAuthority interface {
	EnsureLeaf(context.Context, string) (certificates.LeafResult, error)
	Certificate(context.Context, string) (*tls.Certificate, error)
	PublicRoot() (certificates.Root, error)
}

// dataPlane is the one-shot listener generation owned beneath the control endpoint.
type dataPlane interface {
	Start(context.Context) error
	ReplaceHTTPRoutes(dataplane.DesiredState) error
	Snapshot() dataplane.Snapshot
	Done() <-chan struct{}
	Err() error
	Close(context.Context) error
}

// httpIngressActivationDataPlane promotes one live DNS-only generation without rebinding its resolver listener.
type httpIngressActivationDataPlane interface {
	ActivateHTTPIngress(context.Context, dataplane.DesiredState) error
}

// managedNativeRouteDataPlane is the narrow post-start publication seam used by managed sessions.
type managedNativeRouteDataPlane interface {
	ReplaceNativeRoutes(context.Context, []dataplane.NativeRoute) error
}

// materialStoreOpener opens the protected certificate store only after durable state authorizes startup.
type materialStoreOpener func() (certificateMaterialStore, error)

// certificateBootstrapper loads or creates the one persisted authority used by the controller generation.
type certificateBootstrapper func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error)

// desiredStateFactory creates the immutable network generation from the already-validated durable aggregate.
type desiredStateFactory func(state.RuntimeState) (dataplane.DesiredState, error)

// dataPlaneFactory constructs a listener generation without starting it.
type dataPlaneFactory func(dataplane.Config) (dataPlane, error)

// nativeSocketProbe confirms that a direct native publication is actually accepting TCP connections.
type nativeSocketProbe func(context.Context, netip.AddrPort) error

// dependencies retain deterministic I/O boundaries without making production collaborators optional.
type dependencies struct {
	globalNetworkReleasePlans globalNetworkReleasePlanStore
	openMaterial              materialStoreOpener
	bootstrap                 certificateBootstrapper
	newDesiredState           desiredStateFactory
	newDataPlane              dataPlaneFactory
	certificateConfig         certificates.Config
	cleanupTimeout            time.Duration
	nativeSocketProbe         nativeSocketProbe
}

// controllerState records the one-shot lifecycle without exposing partially initialized collaborators.
type controllerState uint8

const (
	controllerStateNew controllerState = iota
	controllerStateStarting
	controllerStateReady
	controllerStateStopping
	controllerStateStopped
	controllerStateFailed
)

// runtimeExit identifies the exact child generation that relinquished listener authority.
type runtimeExit struct {
	generation uint64
	runtime    dataPlane
	done       <-chan struct{}
}

// Controller owns certificate material and the exact current data-plane generation beneath daemon authority.
type Controller struct {
	mutex                  sync.RWMutex
	reconcileMutex         sync.Mutex
	initialized            bool
	source                 runtimeStateSource
	dependencies           dependencies
	state                  controllerState
	parentContext          context.Context
	cancel                 context.CancelFunc
	stopParentWatch        func() bool
	stopCause              error
	unexpectedRuntimeExit  bool
	runtimeContext         context.Context
	runtimeGeneration      uint64
	runtimeExits           chan runtimeExit
	runtimeDone            <-chan struct{}
	dataPlane              dataPlane
	material               certificateMaterialStore
	certificates           certificateAuthority
	root                   certificates.Root
	httpFoundation         dataplane.DesiredState
	publishedHTTPRoutes    []dataplane.HTTPRoute
	managedNativeRoutes    []dataplane.NativeRoute
	terminalErr            error
	releaseMode            bool
	releaseFence           networkReleaseFence
	releaseRuntimeRetired  bool
	runtimeNetworkRevision domain.Sequence
	stop                   chan struct{}
	done                   chan struct{}
	stopOnce               sync.Once
	doneOnce               sync.Once
}

// closedDone gives invalid and zero-value controllers an immediately observable terminal signal.
var closedDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// NewController constructs the production harbord runtime without reading state or touching certificate storage.
func NewController(source *state.Store, journal *state.OperationJournal) (*Controller, error) {
	if source == nil {
		return nil, fmt.Errorf("create harbord runtime controller: durable state source is required")
	}
	if journal == nil {
		return nil, fmt.Errorf("create harbord runtime controller: global network release plan store is required")
	}
	dependencies := productionDependencies()
	dependencies.globalNetworkReleasePlans = journal
	return newController(source, dependencies)
}

// newController validates every required boundary before retaining the side-effect-free assembly.
func newController(source runtimeStateSource, dependencies dependencies) (*Controller, error) {
	if requiredInterfaceIsNil(source) {
		return nil, fmt.Errorf("create harbord runtime controller: durable state source is required")
	}
	if requiredInterfaceIsNil(dependencies.globalNetworkReleasePlans) {
		return nil, fmt.Errorf("create harbord runtime controller: global network release plan store is required")
	}
	if dependencies.openMaterial == nil {
		return nil, fmt.Errorf("create harbord runtime controller: material store opener is required")
	}
	if dependencies.bootstrap == nil {
		return nil, fmt.Errorf("create harbord runtime controller: certificate bootstrapper is required")
	}
	if dependencies.newDesiredState == nil {
		return nil, fmt.Errorf("create harbord runtime controller: desired state factory is required")
	}
	if dependencies.newDataPlane == nil {
		return nil, fmt.Errorf("create harbord runtime controller: data plane factory is required")
	}
	if dependencies.nativeSocketProbe == nil {
		return nil, fmt.Errorf("create harbord runtime controller: native socket probe is required")
	}
	if dependencies.cleanupTimeout <= 0 {
		return nil, fmt.Errorf("create harbord runtime controller: cleanup timeout must be positive")
	}

	return &Controller{
		initialized:  true,
		source:       source,
		dependencies: dependencies,
		state:        controllerStateNew,
		runtimeExits: make(chan runtimeExit),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}, nil
}

// Start validates durable state before opening certificate material and publishing its infrastructure generation.
func (controller *Controller) Start(ctx context.Context) error {
	runContext, err := controller.beginStart(ctx)
	if err != nil {
		return err
	}

	plan, foundRelease, err := controller.dependencies.globalNetworkReleasePlans.ReadActiveGlobalNetworkReleasePlan(runContext)
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: read active network release plan: %w", err), nil, nil)
	}
	controller.mutex.Lock()
	armedRelease := controller.releaseMode
	fence := controller.releaseFence
	if foundRelease {
		observedFence := releaseFenceFromPlan(plan)
		if armedRelease && !sameReleaseFence(fence, observedFence) {
			controller.mutex.Unlock()
			return controller.failStart(errors.New("start harbord runtime: active release plan no longer matches armed fence"), nil, nil)
		}
		controller.releaseMode = true
		controller.releaseFence = observedFence
		controller.runtimeNetworkRevision = plan.NetworkRevision
		armedRelease = true
	} else if armedRelease {
		controller.mutex.Unlock()
		return controller.failStart(errors.New("start harbord runtime: armed release plan is absent"), nil, nil)
	}
	controller.mutex.Unlock()
	if armedRelease {
		desired, err := releaseAnchorDesiredState()
		if err != nil {
			return controller.failStart(fmt.Errorf("start harbord runtime: construct release anchor: %w", err), nil, nil)
		}
		runtime, runtimeDone, err := controller.startReleaseAnchor(runContext, desired)
		if err != nil {
			return controller.failStart(err, nil, runtime)
		}
		if err := controller.publishReady(nil, nil, certificates.Root{}, runtime, runtimeDone); err != nil {
			return controller.failStartWithDone(err, nil, runtime, runtimeDone)
		}
		controller.watchRuntimeExit(controller.runtimeGeneration, runtime, runtimeDone)
		go controller.monitor(nil)
		return nil
	}

	runtimeState, err := controller.source.RuntimeState(runContext)
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: read durable state: %w", err), nil, nil)
	}
	if err := runtimeState.Validate(); err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: validate durable state: %w", err), nil, nil)
	}
	desired, err := controller.dependencies.newDesiredState(runtimeState)
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: construct data plane: %w", err), nil, nil)
	}
	if err := controller.startupInterruption(runContext); err != nil {
		return controller.failStart(err, nil, nil)
	}

	material, err := controller.dependencies.openMaterial()
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: open certificate material: %w", err), nil, nil)
	}
	if requiredInterfaceIsNil(material) {
		return controller.failStart(errors.New("start harbord runtime: material store opener returned nil"), nil, nil)
	}
	if err := controller.startupInterruption(runContext); err != nil {
		return controller.failStart(err, material, nil)
	}

	authority, err := controller.dependencies.bootstrap(runContext, material, controller.dependencies.certificateConfig)
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: bootstrap certificates: %w", err), material, nil)
	}
	if requiredInterfaceIsNil(authority) {
		return controller.failStart(errors.New("start harbord runtime: certificate bootstrapper returned nil"), material, nil)
	}
	root, err := authority.PublicRoot()
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: read public certificate authority: %w", err), material, nil)
	}
	if err := controller.startupInterruption(runContext); err != nil {
		return controller.failStart(err, material, nil)
	}
	if runtimeState.NetworkInitialized && runtimeState.Network.Stage == state.NetworkStageResolver {
		desired, err = resolverDesiredState(runtimeState, root.Fingerprint)
		if err != nil {
			return controller.failStart(fmt.Errorf("start harbord runtime: construct resolver generation: %w", err), material, nil)
		}
	}

	runtime, err := controller.dependencies.newDataPlane(dataplane.Config{
		Desired:             desired,
		CertificateProvider: authority.Certificate,
	})
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: construct data plane: %w", err), material, nil)
	}
	if requiredInterfaceIsNil(runtime) {
		return controller.failStart(errors.New("start harbord runtime: data plane factory returned nil"), material, nil)
	}
	runtimeDone := runtime.Done()
	if runtimeDone == nil {
		return controller.failStartWithDone(
			errors.New("start harbord runtime: data plane returned a nil Done channel"),
			material,
			runtime,
			runtimeDone,
		)
	}
	if err := controller.registerRuntimeDone(runtimeDone); err != nil {
		return controller.failStartWithDone(err, material, runtime, runtimeDone)
	}
	if err := controller.startupInterruption(runContext); err != nil {
		return controller.failStartWithDone(err, material, runtime, runtimeDone)
	}
	if err := runtime.Start(runContext); err != nil {
		return controller.failStartWithDone(fmt.Errorf("start harbord runtime: %w", err), material, runtime, runtimeDone)
	}
	if err := controller.startupInterruption(runContext); err != nil {
		return controller.failStartWithDone(err, material, runtime, runtimeDone)
	}
	controller.initializeHTTPReconciliation(desired)
	if err := controller.reconcileHTTPRoutes(runContext, controllerStateStarting, runtime, authority); err != nil {
		return controller.failStartWithDone(fmt.Errorf("start harbord runtime: reconcile HTTP routes: %w", err), material, runtime, runtimeDone)
	}
	if err := controller.publishReady(material, authority, root, runtime, runtimeDone); err != nil {
		return controller.failStartWithDone(err, material, runtime, runtimeDone)
	}
	controller.mutex.Lock()
	if runtimeState.NetworkInitialized && runtimeState.Network.Stage == state.NetworkStageFull {
		controller.runtimeNetworkRevision = runtimeState.Network.Revision
	}
	controller.mutex.Unlock()

	controller.watchRuntimeExit(controller.runtimeGeneration, runtime, runtimeDone)
	go controller.monitor(material)
	return nil
}

// Close requests shutdown and waits for the data plane and material store to close in ownership order.
func (controller *Controller) Close(ctx context.Context) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}

	controller.mutex.Lock()
	switch controller.state {
	case controllerStateNew:
		controller.stopCause = ErrClosed
		controller.state = controllerStateStopped
		controller.requestStopLocked()
		controller.closeDone()
		controller.mutex.Unlock()
		return nil
	case controllerStateStarting, controllerStateReady:
		controller.claimStopIntentLocked(ErrClosed)
		controller.mutex.Unlock()
	case controllerStateStopping:
		controller.mutex.Unlock()
	case controllerStateStopped, controllerStateFailed:
		err := controller.terminalErr
		controller.mutex.Unlock()
		return err
	default:
		controller.mutex.Unlock()
		return ErrNotInitialized
	}

	select {
	case <-controller.done:
		return controller.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done closes after startup rollback or complete data-plane and material-store shutdown.
func (controller *Controller) Done() <-chan struct{} {
	if controller == nil || !controller.initialized || controller.done == nil {
		return closedDone
	}
	return controller.done
}

// Err returns the retained startup, child, or cleanup failure after the controller becomes terminal.
func (controller *Controller) Err() error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	return controller.terminalErr
}

// ShutdownTimeout returns the child-cleanup escalation budget, not an upper bound on complete ownership release.
// A controller remains nonterminal until the child publishes Done and certificate material finishes closing.
func (controller *Controller) ShutdownTimeout() time.Duration {
	if controller == nil || !controller.initialized {
		return 0
	}
	return controller.dependencies.cleanupTimeout
}

// NetworkSnapshot returns the current payload-free data-plane observation after readiness publication.
func (controller *Controller) NetworkSnapshot() (dataplane.Snapshot, error) {
	if controller == nil || !controller.initialized {
		return dataplane.Snapshot{}, ErrNotInitialized
	}
	controller.mutex.RLock()
	runtime := controller.dataPlane
	controller.mutex.RUnlock()
	if runtime == nil {
		return dataplane.Snapshot{}, ErrNotReady
	}
	return runtime.Snapshot(), nil
}

// ReplaceManagedNativeRoutes publishes one complete, already-planned managed TCP route set through the live data plane.
//
// The caller must have joined every route to Harbor-owned reservations and an attached session. Keeping that proof
// outside the controller lets the network runtime remain ignorant of project and GoForj semantics while retaining one
// serialized publication boundary here.
func (controller *Controller) ReplaceManagedNativeRoutes(ctx context.Context, routes []dataplane.NativeRoute) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	routes = canonicalManagedNativeRoutes(routes)
	if err := controller.probeManagedDirectRoutes(ctx, routes); err != nil {
		return err
	}
	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	return controller.replaceNativeRoutesLocked(ctx, routes)
}

// replaceNativeRoutesLocked publishes one canonical replacement while its caller serializes all route mutations.
func (controller *Controller) replaceNativeRoutesLocked(
	ctx context.Context,
	routes []dataplane.NativeRoute,
) error {
	controller.mutex.RLock()
	lifecycle := controller.state
	runtime := controller.dataPlane
	releasing := controller.releaseMode
	controller.mutex.RUnlock()
	if releasing {
		return errors.New("managed native route publication is unavailable while network release is armed")
	}
	if lifecycle != controllerStateReady || runtime == nil {
		return ErrNotReady
	}
	managedRuntime, ok := runtime.(managedNativeRouteDataPlane)
	if !ok {
		return errors.New("managed native route publication is unavailable")
	}
	if err := managedRuntime.ReplaceNativeRoutes(ctx, routes); err != nil {
		return err
	}
	controller.mutex.Lock()
	controller.managedNativeRoutes = append([]dataplane.NativeRoute(nil), routes...)
	controller.mutex.Unlock()
	return nil
}

// ReconcileManagedNativeRoutes publishes a changed managed route set and leaves an already-live matching set untouched.
//
// Direct native publications intentionally have no relay route. Avoiding an empty replacement when the controller
// already serves no managed relays lets their barrier acknowledge the proven direct socket without mutating the relay
// generation, while a previous relay is still withdrawn when the desired set changes.
func (controller *Controller) ReconcileManagedNativeRoutes(ctx context.Context, routes []dataplane.NativeRoute) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	canonical := canonicalManagedNativeRoutes(routes)
	controller.mutex.RLock()
	current := append([]dataplane.NativeRoute(nil), controller.managedNativeRoutes...)
	controller.mutex.RUnlock()
	if err := controller.probeManagedDirectRoutes(ctx, canonical); err != nil {
		retained := managedRelayRoutes(current)
		if len(retained) != len(current) {
			err = errors.Join(err, controller.ReplaceManagedNativeRoutes(ctx, retained))
		}
		return err
	}
	if len(current) == 0 && len(canonical) == 0 || slices.Equal(current, canonical) {
		return nil
	}
	return controller.ReplaceManagedNativeRoutes(ctx, canonical)
}

// ReconcileProjectNativeRoutes atomically replaces one project's observed TCP routes without disturbing its neighbors.
func (controller *Controller) ReconcileProjectNativeRoutes(
	ctx context.Context,
	projectID domain.ProjectID,
	routes []dataplane.NativeRoute,
) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("reconcile project native routes: %w", err)
	}
	prefix := string(projectID) + ":"
	for _, route := range routes {
		if !strings.HasPrefix(route.ID, prefix) {
			return fmt.Errorf("reconcile project native routes: route %q does not belong to project %q", route.ID, projectID)
		}
	}

	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	runtimeState, err := controller.source.RuntimeState(ctx)
	if err != nil {
		return fmt.Errorf("reconcile project native routes: read durable state: %w", err)
	}
	if err := validateProjectNativeRouteAuthority(runtimeState, projectID, routes); err != nil {
		return err
	}
	controller.mutex.RLock()
	current := append([]dataplane.NativeRoute(nil), controller.managedNativeRoutes...)
	controller.mutex.RUnlock()
	replacement := make([]dataplane.NativeRoute, 0, len(current)+len(routes))
	for _, route := range current {
		if !strings.HasPrefix(route.ID, prefix) {
			replacement = append(replacement, route)
		}
	}
	replacement = append(replacement, routes...)
	replacement = canonicalManagedNativeRoutes(replacement)
	if slices.Equal(current, replacement) {
		return nil
	}
	if err := controller.probeManagedDirectRoutes(ctx, routes); err != nil {
		return err
	}
	return controller.replaceNativeRoutesLocked(ctx, replacement)
}

// validateProjectNativeRouteAuthority binds observed routes to one ready project and its durable primary address.
func validateProjectNativeRouteAuthority(
	runtimeState state.RuntimeState,
	projectID domain.ProjectID,
	routes []dataplane.NativeRoute,
) error {
	if err := runtimeState.Validate(); err != nil {
		return fmt.Errorf("reconcile project native routes: invalid durable state: %w", err)
	}
	project, found := runtimeProject(runtimeState.Snapshot, projectID)
	if !found || project.State != domain.ProjectReady {
		return fmt.Errorf("reconcile project native routes: project %q is not ready", projectID)
	}
	var primary netip.Addr
	for _, lease := range runtimeState.Network.Leases {
		if lease.Key.Kind() == identity.LeaseKindPrimary && lease.Key.ProjectID == projectID {
			primary = lease.Address.Unmap()
			break
		}
	}
	if !primary.IsValid() {
		return fmt.Errorf("reconcile project native routes: project %q primary lease is missing", projectID)
	}
	for _, route := range routes {
		if route.Listen.Addr().Unmap() != primary {
			return fmt.Errorf(
				"reconcile project native routes: route %q address %s does not match project primary %s",
				route.ID,
				route.Listen.Addr(),
				primary,
			)
		}
	}
	return nil
}

// managedRelayRoutes removes DNS-only direct publications while retaining the relay generation.
func managedRelayRoutes(routes []dataplane.NativeRoute) []dataplane.NativeRoute {
	retained := make([]dataplane.NativeRoute, 0, len(routes))
	for _, route := range routes {
		if !route.Direct {
			retained = append(retained, route)
		}
	}
	return retained
}

// ManagedNativeRoutesLive proves that every requested managed route is currently served by the controller generation.
func (controller *Controller) ManagedNativeRoutesLive(ctx context.Context, routes []dataplane.NativeRoute) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	controller.mutex.RLock()
	lifecycle := controller.state
	runtime := controller.dataPlane
	releasing := controller.releaseMode
	managed := append([]dataplane.NativeRoute(nil), controller.managedNativeRoutes...)
	httpRoutes := append([]dataplane.HTTPRoute(nil), controller.publishedHTTPRoutes...)
	controller.mutex.RUnlock()
	if releasing {
		return errors.New("managed native route publication is unavailable while network release is armed")
	}
	routes = canonicalManagedNativeRoutes(routes)
	if lifecycle != controllerStateReady || runtime == nil {
		return ErrNotReady
	}
	if !slices.Equal(managed, routes) {
		return errors.New("managed native route publication does not match the controller generation")
	}
	snapshot := runtime.Snapshot()
	if snapshot.State != dataplane.StateReady {
		return fmt.Errorf("managed native route publication data plane is %q", snapshot.State)
	}
	if snapshot.DNS.Records != len(httpRoutes)+len(routes) {
		return fmt.Errorf("managed native route publication DNS has %d records, want %d managed and HTTP records", snapshot.DNS.Records, len(httpRoutes)+len(routes))
	}
	relayRoutes := make([]dataplane.NativeRoute, 0, len(routes))
	directRoutes := make([]dataplane.NativeRoute, 0, len(routes))
	for _, route := range routes {
		if route.Direct {
			directRoutes = append(directRoutes, route)
			continue
		}
		relayRoutes = append(relayRoutes, route)
	}
	if err := controller.probeManagedDirectRoutes(ctx, directRoutes); err != nil {
		withdrawErr := controller.withdrawManagedDirectRoutes(ctx, runtime, relayRoutes)
		return errors.Join(err, withdrawErr)
	}
	if len(snapshot.Directs) != len(directRoutes) {
		return fmt.Errorf("managed native route publication has %d direct entries, want %d", len(snapshot.Directs), len(directRoutes))
	}
	for index, route := range directRoutes {
		direct := snapshot.Directs[index]
		if direct.ID != route.ID || direct.Host != route.Host || direct.ListenAddress != route.Listen {
			return fmt.Errorf("managed direct native route %q differs from the controller generation", route.ID)
		}
	}
	if len(snapshot.Relays) != len(relayRoutes) {
		return fmt.Errorf("managed native route publication has %d relays, want %d", len(snapshot.Relays), len(relayRoutes))
	}
	for index, route := range relayRoutes {
		relay := snapshot.Relays[index]
		if relay.ID != route.ID || relay.Host != route.Host || relay.ListenAddress != route.Listen || relay.Upstream != route.Upstream || !relay.Running {
			return fmt.Errorf("managed native route %q is not live", route.ID)
		}
	}
	return nil
}

// withdrawManagedDirectRoutes removes direct DNS authority after a post-publication liveness failure.
func (controller *Controller) withdrawManagedDirectRoutes(ctx context.Context, runtime dataPlane, relayRoutes []dataplane.NativeRoute) error {
	managedRuntime, ok := runtime.(managedNativeRouteDataPlane)
	if !ok {
		return errors.New("managed native route withdrawal is unavailable")
	}
	if err := managedRuntime.ReplaceNativeRoutes(ctx, relayRoutes); err != nil {
		return fmt.Errorf("withdraw failed direct native routes: %w", err)
	}
	controller.mutex.Lock()
	controller.managedNativeRoutes = append([]dataplane.NativeRoute(nil), relayRoutes...)
	controller.mutex.Unlock()
	return nil
}

// probeManagedDirectRoutes proves direct service-owned listeners before their DNS names can be published.
func (controller *Controller) probeManagedDirectRoutes(ctx context.Context, routes []dataplane.NativeRoute) error {
	for _, route := range routes {
		if !route.Direct {
			continue
		}
		if err := controller.probeNativeSocket(ctx, route.Listen); err != nil {
			return fmt.Errorf("managed direct native route %q is not live: %w", route.ID, err)
		}
	}
	return nil
}

// probeNativeSocket uses the injected bounded probe so direct DNS authority never treats an observation tuple as liveness.
func (controller *Controller) probeNativeSocket(ctx context.Context, address netip.AddrPort) error {
	probe := controller.dependencies.nativeSocketProbe
	probeContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return probe(probeContext, address)
}

// probeNativeTCP opens and closes one TCP connection to prove a direct listener is accepting connections.
func probeNativeTCP(ctx context.Context, address netip.AddrPort) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address.String())
	if err != nil {
		return err
	}
	return connection.Close()
}

// VerifyProjectWithdrawn proves the current process generation cannot route traffic for a project before host identity release.
func (controller *Controller) VerifyProjectWithdrawn(
	ctx context.Context,
	projectID domain.ProjectID,
	networkRevision domain.Sequence,
) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("verify project withdrawal: %w", err)
	}
	if networkRevision == 0 || networkRevision > domain.MaximumSequence {
		return fmt.Errorf(
			"verify project withdrawal: network revision must be between 1 and %d",
			domain.MaximumSequence,
		)
	}

	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()

	controller.mutex.RLock()
	lifecycle := controller.state
	runtime := controller.dataPlane
	managedNativeRoutes := append([]dataplane.NativeRoute(nil), controller.managedNativeRoutes...)
	controller.mutex.RUnlock()
	if lifecycle == controllerStateNew && runtime == nil {
		return nil
	}
	if lifecycle != controllerStateReady || runtime == nil {
		return fmt.Errorf(
			"%w for project %q at network revision %d: data plane is not ready",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
		)
	}

	runtimeState, err := controller.source.RuntimeState(ctx)
	if err != nil {
		return fmt.Errorf(
			"%w for project %q at network revision %d: read durable state: %w",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			err,
		)
	}
	if err := runtimeState.Validate(); err != nil {
		return fmt.Errorf(
			"%w for project %q at network revision %d: invalid durable state: %v",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			err,
		)
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Revision != networkRevision {
		return fmt.Errorf(
			"%w for project %q at network revision %d: durable network revision is %d",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			runtimeState.Network.Revision,
		)
	}
	project, found := runtimeProject(runtimeState.Snapshot, projectID)
	if !found {
		return fmt.Errorf(
			"%w for project %q at network revision %d: durable project is missing",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
		)
	}

	snapshot := runtime.Snapshot()
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf(
			"%w for project %q at network revision %d: invalid data-plane observation: %v",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			err,
		)
	}
	if snapshot.State != dataplane.StateReady {
		return fmt.Errorf(
			"%w for project %q at network revision %d: data plane state is %q",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			snapshot.State,
		)
	}
	if err := verifyPublishedRouteObservation(snapshot, controller.publishedHTTPRoutes, controller.httpFoundation, managedNativeRoutes); err != nil {
		return fmt.Errorf(
			"%w for project %q at network revision %d: %v",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
			err,
		)
	}
	if projectHasPublishedHTTPRoute(project, controller.publishedHTTPRoutes) {
		return fmt.Errorf(
			"%w for project %q at network revision %d: project HTTP route remains live",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
		)
	}
	if projectHasPublishedNativeRoute(project.ID, managedNativeRoutes) {
		return fmt.Errorf(
			"%w for project %q at network revision %d: project native route remains live",
			ErrProjectWithdrawalUnverified,
			projectID,
			networkRevision,
		)
	}
	return nil
}

// runtimeProject binds route ownership to the validated durable slug instead of inferring it from an opaque route alone.
func runtimeProject(snapshot domain.Snapshot, projectID domain.ProjectID) (domain.ProjectSnapshot, bool) {
	for _, project := range snapshot.Projects {
		if project.ID == projectID {
			return project, true
		}
	}
	return domain.ProjectSnapshot{}, false
}

// verifyPublishedRouteObservation prevents retained route identities from authorizing teardown after the runtime drifts.
func verifyPublishedRouteObservation(
	snapshot dataplane.Snapshot,
	httpRoutes []dataplane.HTTPRoute,
	foundation dataplane.DesiredState,
	managedNativeRoutes []dataplane.NativeRoute,
) error {
	listeners := foundation.ListenerPlan()
	if snapshot.DNS.Configured != listeners.DNS.IsValid() ||
		(snapshot.DNS.Configured && snapshot.DNS.Address != listeners.DNS) {
		return fmt.Errorf("data-plane DNS listener differs from the controller-owned generation")
	}
	ingressConfigured := listeners.HTTP.IsValid() && listeners.HTTPS.IsValid()
	if snapshot.Ingress.Configured != ingressConfigured ||
		(snapshot.Ingress.Configured &&
			(snapshot.Ingress.HTTPAddress != listeners.HTTP || snapshot.Ingress.HTTPSAddress != listeners.HTTPS)) {
		return fmt.Errorf("data-plane ingress listeners differ from the controller-owned generation")
	}
	if snapshot.Ingress.Routes != len(httpRoutes) {
		return fmt.Errorf(
			"data-plane ingress reports %d routes while the controller owns %d",
			snapshot.Ingress.Routes,
			len(httpRoutes),
		)
	}
	if len(foundation.NativeRoutes()) != 0 {
		return fmt.Errorf("native route ownership is unavailable")
	}
	managedRelays := make([]dataplane.NativeRoute, 0, len(managedNativeRoutes))
	for _, route := range managedNativeRoutes {
		if !route.Direct {
			managedRelays = append(managedRelays, route)
		}
	}
	if len(snapshot.Relays) != len(managedRelays) {
		return fmt.Errorf("data-plane native routes differ from controller-owned managed routes")
	}
	for index, route := range managedRelays {
		relay := snapshot.Relays[index]
		if relay.ID != route.ID || relay.Host != route.Host || relay.ListenAddress != route.Listen || relay.Upstream != route.Upstream || !relay.Running {
			return fmt.Errorf("data-plane native route %q differs from controller-owned route", route.ID)
		}
	}
	if snapshot.DNS.Records != len(httpRoutes)+len(managedNativeRoutes) {
		return fmt.Errorf(
			"data-plane DNS reports %d records while the controller owns %d HTTP and %d native routes",
			snapshot.DNS.Records,
			len(httpRoutes),
			len(managedNativeRoutes),
		)
	}
	return nil
}

// projectHasPublishedHTTPRoute treats either the stable ID or exact host as ownership so cross-wiring cannot authorize release.
func projectHasPublishedHTTPRoute(project domain.ProjectSnapshot, routes []dataplane.HTTPRoute) bool {
	routeID := project.Slug + ":" + string(appHTTPResourceID)
	host := project.Slug + ".test"
	for _, route := range routes {
		if route.ID == routeID || route.Host == host {
			return true
		}
	}
	return false
}

// projectHasPublishedNativeRoute reports whether one managed native route still belongs to the requested project.
func projectHasPublishedNativeRoute(projectID domain.ProjectID, routes []dataplane.NativeRoute) bool {
	prefix := string(projectID) + ":"
	for _, route := range routes {
		if strings.HasPrefix(route.ID, prefix) {
			return true
		}
	}
	return false
}

// canonicalManagedNativeRoutes keeps route comparisons stable when an authority source enumerates observations in map order.
func canonicalManagedNativeRoutes(routes []dataplane.NativeRoute) []dataplane.NativeRoute {
	canonical := append([]dataplane.NativeRoute(nil), routes...)
	slices.SortFunc(canonical, func(left, right dataplane.NativeRoute) int {
		if left.Host < right.Host {
			return -1
		}
		if left.Host > right.Host {
			return 1
		}
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	return canonical
}

// PublicRoot returns a defensive public-only copy of the authority retained by the ready generation.
func (controller *Controller) PublicRoot() (certificates.Root, error) {
	if controller == nil || !controller.initialized {
		return certificates.Root{}, ErrNotInitialized
	}
	controller.mutex.RLock()
	root := controller.root
	controller.mutex.RUnlock()
	if root.Fingerprint == "" {
		return certificates.Root{}, ErrNotReady
	}
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	return root, nil
}

// NetworkReleaseArmed reports whether this controller retains durable release-fence authority.
func (controller *Controller) NetworkReleaseArmed() bool {
	if controller == nil || !controller.initialized {
		return false
	}
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	return controller.releaseMode
}

// beginStart claims the one-shot lifecycle and installs ordered parent cancellation before any durable read.
func (controller *Controller) beginStart(ctx context.Context) (context.Context, error) {
	if controller == nil || !controller.initialized {
		return nil, ErrNotInitialized
	}
	if ctx == nil {
		return nil, errors.New("start harbord runtime: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateNew {
		if controller.state == controllerStateStopped && errors.Is(controller.stopCause, ErrClosed) {
			return nil, ErrClosed
		}
		return nil, ErrAlreadyStarted
	}
	runContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	controller.parentContext = ctx
	controller.cancel = cancel
	controller.runtimeContext = runContext
	controller.state = controllerStateStarting
	controller.stopParentWatch = context.AfterFunc(ctx, func() {
		controller.requestLifecycleStop(ctx.Err())
	})
	return runContext, nil
}

// registerRuntimeDone admits one stable child ownership signal before runtime startup can begin.
func (controller *Controller) registerRuntimeDone(runtimeDone <-chan struct{}) error {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateStarting {
		return controller.lifecycleInterruptionLocked()
	}
	controller.runtimeDone = runtimeDone
	if channelClosed(runtimeDone) {
		controller.claimUnexpectedRuntimeExitLocked()
		return ErrRuntimeStoppedUnexpectedly
	}
	return nil
}

// publishReady makes the complete generation observable only after every child is ready.
func (controller *Controller) publishReady(
	material certificateMaterialStore,
	authority certificateAuthority,
	root certificates.Root,
	runtime dataPlane,
	runtimeDone <-chan struct{},
) error {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateStarting {
		return controller.lifecycleInterruptionLocked()
	}
	if channelClosed(runtimeDone) {
		controller.claimUnexpectedRuntimeExitLocked()
		cause := runtime.Err()
		if cause == nil {
			cause = ErrRuntimeStoppedUnexpectedly
		}
		return fmt.Errorf("start harbord runtime: %w", cause)
	}
	controller.material = material
	controller.dataPlane = runtime
	controller.runtimeDone = runtimeDone
	controller.certificates = authority
	controller.root = cloneRoot(root)
	controller.runtimeGeneration = 1
	if controller.releaseMode {
		controller.releaseRuntimeRetired = true
	}
	controller.state = controllerStateReady
	return nil
}

// watchRuntimeExit forwards one child completion without letting a retired generation stop the controller.
func (controller *Controller) watchRuntimeExit(generation uint64, runtime dataPlane, runtimeDone <-chan struct{}) {
	go func() {
		select {
		case <-runtimeDone:
		case <-controller.done:
			return
		}
		select {
		case controller.runtimeExits <- runtimeExit{
			generation: generation,
			runtime:    runtime,
			done:       runtimeDone,
		}:
		case <-controller.done:
		}
	}()
}

// monitor owns ordered terminal cleanup for explicit stop, parent cancellation, and the current child failure.
func (controller *Controller) monitor(material certificateMaterialStore) {
	var exit runtimeExit
	for {
		select {
		case <-controller.stop:
			controller.reconcileMutex.Lock()
			exit = controller.currentRuntimeExit()
		case observed := <-controller.runtimeExits:
			controller.reconcileMutex.Lock()
			if !controller.isCurrentRuntimeExit(observed) {
				controller.reconcileMutex.Unlock()
				continue
			}
			exit = observed
		}
		break
	}
	defer controller.reconcileMutex.Unlock()
	unsettledExit := exit.runtime != nil && controller.observeRuntimeExit(exit.generation, exit.done)

	var runtimeErr error
	if exit.runtime != nil {
		runtimeErr = controller.closeDataPlane(exit.runtime, exit.done)
	}
	var materialErr error
	if material != nil {
		materialErr = material.Close()
	}
	terminal := distinctRuntimeError(runtimeErr, materialErr)
	if unsettledExit {
		terminal = distinctRuntimeError(terminal, ErrRuntimeStoppedUnexpectedly)
	}
	controller.finish(terminal)
}

// currentRuntimeExit snapshots the generation that owns listener authority at shutdown.
func (controller *Controller) currentRuntimeExit() runtimeExit {
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	return runtimeExit{
		generation: controller.runtimeGeneration,
		runtime:    controller.dataPlane,
		done:       controller.runtimeDone,
	}
}

// isCurrentRuntimeExit rejects completion notices from generations retired by activation.
func (controller *Controller) isCurrentRuntimeExit(exit runtimeExit) bool {
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	return exit.generation == controller.runtimeGeneration
}

// observeRuntimeExit orders current child completion against the stop intent that woke the monitor.
func (controller *Controller) observeRuntimeExit(generation uint64, runtimeDone <-chan struct{}) bool {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state == controllerStateReady && generation == controller.runtimeGeneration && channelClosed(runtimeDone) {
		controller.claimUnexpectedRuntimeExitLocked()
	}
	return controller.unexpectedRuntimeExit
}

// failStart rolls back partially acquired resources before publishing one terminal startup result.
func (controller *Controller) failStart(
	cause error,
	material certificateMaterialStore,
	runtime dataPlane,
) error {
	var runtimeDone <-chan struct{}
	if runtime != nil {
		runtimeDone = runtime.Done()
	}
	return controller.failStartWithDone(cause, material, runtime, runtimeDone)
}

// failStartWithDone preserves one captured child completion signal throughout bounded rollback.
func (controller *Controller) failStartWithDone(
	cause error,
	material certificateMaterialStore,
	runtime dataPlane,
	runtimeDone <-chan struct{},
) error {
	cause = controller.orderedStartupCause(cause)
	cleanup := controller.rollback(runtime, runtimeDone, material)
	result := distinctRuntimeError(cause, cleanup)
	expected := isLifecycleInterruptionOnly(cause)
	terminal := result
	if expected && cleanup == nil {
		terminal = nil
	}
	controller.finish(terminal)
	return result
}

// rollback closes a partial data-plane generation before releasing its certificate store.
func (controller *Controller) rollback(
	runtime dataPlane,
	runtimeDone <-chan struct{},
	material certificateMaterialStore,
) error {
	var runtimeErr error
	if runtime != nil {
		runtimeErr = controller.closeDataPlane(runtime, runtimeDone)
	}
	var materialErr error
	if material != nil {
		materialErr = material.Close()
	}
	return distinctRuntimeError(runtimeErr, materialErr)
}

// closeDataPlane escalates slow cleanup without releasing controller authority before child ownership ends.
func (controller *Controller) closeDataPlane(runtime dataPlane, runtimeDone <-chan struct{}) error {
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), controller.dependencies.cleanupTimeout)
	defer cancelCleanup()
	result := make(chan error, 1)
	go func() {
		result <- runtime.Close(cleanupContext)
	}()

	if runtimeDone == nil {
		select {
		case closeErr := <-result:
			return distinctRuntimeError(
				distinctRuntimeError(runtime.Err(), closeErr),
				fmt.Errorf("%w: data plane returned no Done signal", ErrRuntimeShutdownIncomplete),
			)
		case <-cleanupContext.Done():
			return distinctRuntimeError(
				runtime.Err(),
				fmt.Errorf("%w: %v", ErrRuntimeShutdownIncomplete, cleanupContext.Err()),
			)
		}
	}

	closeResults := (<-chan error)(result)
	cleanupDone := cleanupContext.Done()
	doneSignal := runtimeDone
	closeObserved := false
	doneObserved := channelClosed(runtimeDone)
	timedOut := false
	var closeErr error
	var incomplete error

	for !doneObserved {
		select {
		case closeErr = <-closeResults:
			closeObserved = true
			closeResults = nil
			if channelClosed(runtimeDone) {
				doneObserved = true
				continue
			}
			if incomplete == nil {
				incomplete = fmt.Errorf("%w: data plane Close returned before Done", ErrRuntimeShutdownIncomplete)
			}
		case <-doneSignal:
			doneObserved = true
			doneSignal = nil
		case <-cleanupDone:
			timedOut = true
			cleanupDone = nil
			if incomplete == nil {
				incomplete = fmt.Errorf("%w: %v", ErrRuntimeShutdownIncomplete, cleanupContext.Err())
			}
		}
	}

	if !closeObserved && !timedOut {
		select {
		case closeErr = <-closeResults:
			closeObserved = true
		case <-cleanupDone:
			timedOut = true
			incomplete = fmt.Errorf("%w: %v", ErrRuntimeShutdownIncomplete, cleanupContext.Err())
		}
	}
	if !closeObserved {
		select {
		case closeErr = <-closeResults:
		default:
		}
	}

	return distinctRuntimeError(distinctRuntimeError(runtime.Err(), closeErr), incomplete)
}

// finish publishes terminal state only after every acquired resource has completed cleanup.
func (controller *Controller) finish(terminal error) {
	controller.mutex.Lock()
	cancel := controller.cancel
	stopParentWatch := controller.stopParentWatch
	controller.parentContext = nil
	controller.cancel = nil
	controller.runtimeContext = nil
	controller.stopParentWatch = nil
	controller.terminalErr = terminal
	if terminal != nil {
		controller.state = controllerStateFailed
	} else {
		controller.state = controllerStateStopped
	}
	controller.closeDone()
	controller.mutex.Unlock()
	if stopParentWatch != nil {
		stopParentWatch()
	}
	if cancel != nil {
		cancel()
	}
}

// startupInterruption resolves parent cancellation and explicit stop through the lifecycle mutex.
func (controller *Controller) startupInterruption(ctx context.Context) error {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state == controllerStateStarting && controller.parentContext != nil {
		if err := controller.parentContext.Err(); err != nil {
			controller.claimStopIntentLocked(err)
		}
	}
	if controller.state != controllerStateStarting {
		return controller.lifecycleInterruptionLocked()
	}
	return ctx.Err()
}

// requestLifecycleStop publishes parent cancellation through the same ordering boundary as explicit shutdown.
func (controller *Controller) requestLifecycleStop(cause error) {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	controller.claimStopIntentLocked(cause)
}

// claimStopIntentLocked atomically publishes stop intent, lifecycle state, and private cancellation.
func (controller *Controller) claimStopIntentLocked(cause error) {
	if controller.state != controllerStateStarting && controller.state != controllerStateReady {
		return
	}
	if controller.runtimeDone != nil && channelClosed(controller.runtimeDone) {
		controller.claimUnexpectedRuntimeExitLocked()
		return
	}
	controller.stopCause = cause
	controller.state = controllerStateStopping
	controller.requestStopLocked()
	if controller.cancel != nil {
		controller.cancel()
	}
}

// claimUnexpectedRuntimeExitLocked preserves child-first causality before publishing cancellation to dependents.
func (controller *Controller) claimUnexpectedRuntimeExitLocked() {
	controller.unexpectedRuntimeExit = true
	controller.stopCause = ErrRuntimeStoppedUnexpectedly
	controller.state = controllerStateStopping
	controller.requestStopLocked()
	if controller.cancel != nil {
		controller.cancel()
	}
}

// lifecycleInterruptionLocked returns the cause retained by the transition that ended startup permission.
func (controller *Controller) lifecycleInterruptionLocked() error {
	if controller.stopCause != nil {
		return controller.stopCause
	}
	return ErrClosed
}

// orderedStartupCause replaces private cancellation with the lifecycle event that caused it.
func (controller *Controller) orderedStartupCause(cause error) error {
	if !errors.Is(cause, context.Canceled) {
		return cause
	}
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	if controller.stopCause != nil {
		if isLifecycleInterruptionOnly(cause) {
			return controller.stopCause
		}
		return distinctRuntimeError(controller.stopCause, cause)
	}
	return cause
}

// isLifecycleInterruptionOnly reports whether every leaf is an expected startup stop cause.
func isLifecycleInterruptionOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		found := false
		for _, cause := range joined.Unwrap() {
			if cause == nil {
				continue
			}
			found = true
			if !isLifecycleInterruptionOnly(cause) {
				return false
			}
		}
		return found
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if cause := wrapped.Unwrap(); cause != nil {
			return isLifecycleInterruptionOnly(cause)
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrClosed)
}

// requestStopLocked collapses every ordered shutdown transition into one monitor signal.
func (controller *Controller) requestStopLocked() {
	controller.stopOnce.Do(func() {
		close(controller.stop)
	})
}

// closeDone publishes complete cleanup exactly once across startup and shutdown races.
func (controller *Controller) closeDone() {
	controller.doneOnce.Do(func() {
		close(controller.done)
	})
}

// cloneRoot prevents status callers from mutating the controller's public authority bytes.
func cloneRoot(root certificates.Root) certificates.Root {
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	return root
}

// channelClosed reports terminal child ownership without blocking a cleanup path.
func channelClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// distinctRuntimeError retains cleanup failures without formatting one terminal child error twice.
func distinctRuntimeError(terminal error, closeErr error) error {
	if terminal == nil {
		return closeErr
	}
	if closeErr == nil {
		return terminal
	}
	if errors.Is(closeErr, terminal) {
		return closeErr
	}
	if errors.Is(terminal, closeErr) {
		return terminal
	}
	return errors.Join(terminal, closeErr)
}

// requiredInterfaceIsNil rejects typed-nil required collaborators before their methods can panic.
func requiredInterfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// productionDependencies connects the controller to Harbor's reviewed state, trust, and network implementations.
func productionDependencies() dependencies {
	return dependencies{
		openMaterial: func() (certificateMaterialStore, error) {
			return materialstore.OpenDefault()
		},
		bootstrap: func(
			ctx context.Context,
			store certificates.MaterialStore,
			config certificates.Config,
		) (certificateAuthority, error) {
			return certificates.Bootstrap(ctx, store, config)
		},
		newDesiredState: desiredStateFromRuntimeState,
		newDataPlane: func(config dataplane.Config) (dataPlane, error) {
			return dataplane.NewRuntime(config)
		},
		nativeSocketProbe: func(ctx context.Context, address netip.AddrPort) error {
			return probeNativeTCP(ctx, address)
		},
		cleanupTimeout: cleanupTimeout,
	}
}

// compile-time interface checks keep production state, trust, and network changes from drifting across the controller boundary.
var (
	_ runtimeStateSource       = (*state.Store)(nil)
	_ certificateMaterialStore = (*materialstore.Store)(nil)
	_ certificateAuthority     = (*certificates.Manager)(nil)
	_ dataPlane                = (*dataplane.Runtime)(nil)
)
