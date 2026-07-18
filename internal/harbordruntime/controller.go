package harbordruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
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
	// ErrProjectsRequireNetworkProjection prevents registered projects from being presented through an empty network generation.
	ErrProjectsRequireNetworkProjection = errors.New("registered projects require durable network leases and bindings")
	// ErrRuntimeStoppedUnexpectedly reports a child generation that relinquished authority without cancellation or an error.
	ErrRuntimeStoppedUnexpectedly = errors.New("Harbor data plane stopped unexpectedly")
	// ErrRuntimeShutdownIncomplete reports a child that did not publish terminal ownership within the cleanup bound.
	ErrRuntimeShutdownIncomplete = errors.New("Harbor data plane shutdown did not complete")
)

// snapshotSource supplies the complete durable state considered before startup performs any filesystem mutation.
type snapshotSource interface {
	Snapshot(context.Context) (domain.Snapshot, error)
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
	Snapshot() dataplane.Snapshot
	Done() <-chan struct{}
	Err() error
	Close(context.Context) error
}

// materialStoreOpener opens the protected certificate store only after durable state authorizes startup.
type materialStoreOpener func() (certificateMaterialStore, error)

// certificateBootstrapper loads or creates the one persisted authority used by the controller generation.
type certificateBootstrapper func(context.Context, certificates.MaterialStore, certificates.Config) (certificateAuthority, error)

// desiredStateFactory creates the immutable network generation after certificate authority is available.
type desiredStateFactory func() (dataplane.DesiredState, error)

// dataPlaneFactory constructs a listener generation without starting it.
type dataPlaneFactory func(dataplane.Config) (dataPlane, error)

// dependencies retain deterministic I/O boundaries without making production collaborators optional.
type dependencies struct {
	openMaterial      materialStoreOpener
	bootstrap         certificateBootstrapper
	newDesiredState   desiredStateFactory
	newDataPlane      dataPlaneFactory
	certificateConfig certificates.Config
	cleanupTimeout    time.Duration
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

// Controller owns certificate material and one immutable data-plane generation beneath daemon authority.
type Controller struct {
	mutex                 sync.RWMutex
	initialized           bool
	source                snapshotSource
	dependencies          dependencies
	state                 controllerState
	parentContext         context.Context
	cancel                context.CancelFunc
	stopParentWatch       func() bool
	stopCause             error
	unexpectedRuntimeExit bool
	runtimeDone           <-chan struct{}
	dataPlane             dataPlane
	material              certificateMaterialStore
	certificates          certificateAuthority
	root                  certificates.Root
	terminalErr           error
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once
	doneOnce              sync.Once
}

// closedDone gives invalid and zero-value controllers an immediately observable terminal signal.
var closedDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// NewController constructs the production harbord runtime without reading state or touching certificate storage.
func NewController(source *state.Store) (*Controller, error) {
	if source == nil {
		return nil, fmt.Errorf("create harbord runtime controller: durable state source is required")
	}
	return newController(source, productionDependencies())
}

// newController validates every required boundary before retaining the side-effect-free assembly.
func newController(source snapshotSource, dependencies dependencies) (*Controller, error) {
	if requiredInterfaceIsNil(source) {
		return nil, fmt.Errorf("create harbord runtime controller: durable state source is required")
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
	if dependencies.cleanupTimeout <= 0 {
		return nil, fmt.Errorf("create harbord runtime controller: cleanup timeout must be positive")
	}

	return &Controller{
		initialized:  true,
		source:       source,
		dependencies: dependencies,
		state:        controllerStateNew,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}, nil
}

// Start validates durable state before opening certificate material and publishing one ready empty generation.
func (controller *Controller) Start(ctx context.Context) error {
	runContext, err := controller.beginStart(ctx)
	if err != nil {
		return err
	}

	snapshot, err := controller.source.Snapshot(runContext)
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: read durable state: %w", err), nil, nil)
	}
	if err := snapshot.Validate(); err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: validate durable state: %w", err), nil, nil)
	}
	if len(snapshot.Projects) != 0 {
		return controller.failStart(
			fmt.Errorf("start harbord runtime: %w: found %d registered projects", ErrProjectsRequireNetworkProjection, len(snapshot.Projects)),
			nil,
			nil,
		)
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

	desired, err := controller.dependencies.newDesiredState()
	if err != nil {
		return controller.failStart(fmt.Errorf("start harbord runtime: construct empty data plane: %w", err), material, nil)
	}
	if !desired.Empty() {
		return controller.failStart(errors.New("start harbord runtime: transitional desired state must be empty"), material, nil)
	}
	runtime, err := controller.dependencies.newDataPlane(dataplane.Config{Desired: desired})
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
	if err := controller.publishReady(material, authority, root, runtime, runtimeDone); err != nil {
		return controller.failStartWithDone(err, material, runtime, runtimeDone)
	}

	go controller.monitor(runtime, runtimeDone, material)
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
	controller.certificates = authority
	controller.root = cloneRoot(root)
	controller.state = controllerStateReady
	return nil
}

// monitor owns ordered terminal cleanup for explicit stop, parent cancellation, and child failure.
func (controller *Controller) monitor(
	runtime dataPlane,
	runtimeDone <-chan struct{},
	material certificateMaterialStore,
) {
	select {
	case <-controller.stop:
	case <-runtimeDone:
	}
	unexpectedExit := controller.observeRuntimeExit(runtimeDone)

	runtimeErr := controller.closeDataPlane(runtime, runtimeDone)
	materialErr := material.Close()
	terminal := distinctRuntimeError(runtimeErr, materialErr)
	if unexpectedExit {
		terminal = distinctRuntimeError(terminal, ErrRuntimeStoppedUnexpectedly)
	}
	controller.finish(terminal)
}

// observeRuntimeExit orders child completion against the stop intent that woke the monitor.
func (controller *Controller) observeRuntimeExit(runtimeDone <-chan struct{}) bool {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state == controllerStateReady && channelClosed(runtimeDone) {
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
		newDesiredState: func() (dataplane.DesiredState, error) {
			return dataplane.NewDesiredState(dataplane.ListenerPlan{}, nil, nil, 0)
		},
		newDataPlane: func(config dataplane.Config) (dataPlane, error) {
			return dataplane.NewRuntime(config)
		},
		cleanupTimeout: cleanupTimeout,
	}
}

// compile-time interface checks keep future trust changes from drifting across the controller boundary.
var (
	_ certificateMaterialStore = (*materialstore.Store)(nil)
	_ certificateAuthority     = (*certificates.Manager)(nil)
	_ dataPlane                = (*dataplane.Runtime)(nil)
)
