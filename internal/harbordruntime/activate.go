package harbordruntime

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// ActivateNetwork publishes the exact full-stage listeners after durable setup commits their revision.
func (controller *Controller) ActivateNetwork(ctx context.Context, expectedRevision domain.Sequence) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedRevision == 0 || expectedRevision > domain.MaximumSequence {
		return fmt.Errorf("activate Harbor network: expected network revision must be between 1 and %d", domain.MaximumSequence)
	}

	controller.reconcileMutex.Lock()
	activation, err := controller.prepareNetworkActivation(ctx, expectedRevision)
	if err != nil {
		controller.reconcileMutex.Unlock()
		return err
	}
	if activation.replayed {
		controller.reconcileMutex.Unlock()
		return nil
	}
	if activation.promoteResolver {
		err := controller.promoteResolverGeneration(ctx, activation)
		controller.reconcileMutex.Unlock()
		if err != nil {
			return err
		}
		return wrapNetworkActivationError("publish current project routes", controller.Reconcile(ctx))
	}

	candidate, candidateDone, err := controller.startNetworkActivationCandidate(ctx, activation)
	if err != nil {
		controller.reconcileMutex.Unlock()
		return err
	}
	generation, err := controller.publishNetworkActivation(activation, candidate, candidateDone)
	if err != nil {
		cleanupErr := controller.closeDataPlane(candidate, candidateDone)
		controller.reconcileMutex.Unlock()
		return errors.Join(err, cleanupErr)
	}
	controller.watchRuntimeExit(generation, candidate, candidateDone)
	cleanupErr := controller.closeDataPlane(activation.current, activation.currentDone)
	controller.reconcileMutex.Unlock()

	reconcileErr := controller.Reconcile(ctx)
	return errors.Join(
		wrapNetworkActivationError("retire prior empty data-plane generation", cleanupErr),
		wrapNetworkActivationError("publish current project routes", reconcileErr),
	)
}

// networkActivation retains one immutable current-generation snapshot across candidate startup.
type networkActivation struct {
	current           dataPlane
	currentDone       <-chan struct{}
	currentGeneration uint64
	runtimeContext    context.Context
	authority         certificateAuthority
	desired           dataplane.DesiredState
	replayed          bool
	promoteResolver   bool
}

// prepareNetworkActivation proves both the current runtime foundation and the newly committed durable topology.
func (controller *Controller) prepareNetworkActivation(
	ctx context.Context,
	expectedRevision domain.Sequence,
) (networkActivation, error) {
	controller.mutex.RLock()
	activation := networkActivation{
		current:           controller.dataPlane,
		currentDone:       controller.runtimeDone,
		currentGeneration: controller.runtimeGeneration,
		runtimeContext:    controller.runtimeContext,
		authority:         controller.certificates,
	}
	lifecycle := controller.state
	currentFoundation := controller.httpFoundation
	controller.mutex.RUnlock()
	if lifecycle != controllerStateReady || activation.current == nil || activation.runtimeContext == nil || activation.authority == nil {
		return networkActivation{}, fmt.Errorf("activate Harbor network: %w", ErrNotReady)
	}

	runtimeState, err := controller.source.RuntimeState(ctx)
	if err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor network: read durable state: %w", err)
	}
	if err := runtimeState.Validate(); err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor network: validate durable state: %w", err)
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageFull {
		return networkActivation{}, fmt.Errorf("activate Harbor network: durable network is not at full stage")
	}
	if runtimeState.Network.Revision != expectedRevision {
		return networkActivation{}, fmt.Errorf(
			"activate Harbor network: durable network revision is %d, expected %d",
			runtimeState.Network.Revision,
			expectedRevision,
		)
	}
	desired, err := controller.dependencies.newDesiredState(runtimeState)
	if err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor network: construct data plane: %w", err)
	}
	if desired.ListenerPlan() == (dataplane.ListenerPlan{}) {
		return networkActivation{}, fmt.Errorf("activate Harbor network: full-stage durable state produced no shared listeners")
	}
	activation.desired = desired
	if sameHTTPFoundation(currentFoundation, desired) {
		snapshot := activation.current.Snapshot()
		if err := snapshot.Validate(); err != nil || snapshot.State != dataplane.StateReady {
			return networkActivation{}, fmt.Errorf("activate Harbor network: existing full generation is not ready")
		}
		activation.replayed = true
		return activation, nil
	}
	if resolverFoundationCanPromote(currentFoundation, desired) {
		snapshot := activation.current.Snapshot()
		if err := snapshot.Validate(); err != nil || snapshot.State != dataplane.StateReady ||
			!snapshot.DNS.Configured || !snapshot.DNS.Running || snapshot.DNS.Address != currentFoundation.ListenerPlan().DNS ||
			snapshot.Ingress.Configured {
			return networkActivation{}, fmt.Errorf("activate Harbor network: existing resolver generation is not ready")
		}
		activation.promoteResolver = true
		return activation, nil
	}
	if currentFoundation.ListenerPlan() != (dataplane.ListenerPlan{}) ||
		len(currentFoundation.NativeRoutes()) != 0 {
		return networkActivation{}, fmt.Errorf("activate Harbor network: live listener topology differs from the committed full stage")
	}
	return activation, nil
}

// resolverFoundationCanPromote admits only the DNS-only to full shared-listener transition.
func resolverFoundationCanPromote(current dataplane.DesiredState, next dataplane.DesiredState) bool {
	currentListeners := current.ListenerPlan()
	nextListeners := next.ListenerPlan()
	return currentListeners.DNS != (netip.AddrPort{}) &&
		currentListeners.HTTP == (netip.AddrPort{}) &&
		currentListeners.HTTPS == (netip.AddrPort{}) &&
		nextListeners.DNS == currentListeners.DNS &&
		nextListeners.HTTP != (netip.AddrPort{}) &&
		nextListeners.HTTPS != (netip.AddrPort{}) &&
		current.TTL() == next.TTL() &&
		len(current.HTTPRoutes()) == 0 &&
		len(current.NativeRoutes()) == 0 &&
		len(next.HTTPRoutes()) == 0 &&
		len(next.NativeRoutes()) == 0
}

// promoteResolverGeneration adds ingress to the current runtime before publishing its full foundation.
func (controller *Controller) promoteResolverGeneration(ctx context.Context, activation networkActivation) error {
	promoter, ok := activation.current.(httpIngressActivationDataPlane)
	if !ok {
		return errors.New("activate Harbor network: live resolver generation does not support HTTP ingress activation")
	}
	if err := promoter.ActivateHTTPIngress(ctx, activation.desired); err != nil {
		return fmt.Errorf("activate Harbor network: promote resolver generation: %w", err)
	}

	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateReady ||
		controller.runtimeGeneration != activation.currentGeneration ||
		channelClosed(activation.currentDone) {
		return errors.New("activate Harbor network: controller lifecycle changed during resolver promotion")
	}
	controller.httpFoundation = activation.desired
	controller.publishedHTTPRoutes = activation.desired.HTTPRoutes()
	return nil
}

// startNetworkActivationCandidate acquires the complete full-stage generation while the empty generation remains available for rollback.
func (controller *Controller) startNetworkActivationCandidate(
	ctx context.Context,
	activation networkActivation,
) (dataPlane, <-chan struct{}, error) {
	candidate, err := controller.dependencies.newDataPlane(dataplane.Config{
		Desired:             activation.desired,
		CertificateProvider: activation.authority.Certificate,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("activate Harbor network: construct replacement data plane: %w", err)
	}
	if requiredInterfaceIsNil(candidate) {
		return nil, nil, fmt.Errorf("activate Harbor network: data plane factory returned nil")
	}
	candidateDone := candidate.Done()
	if candidateDone == nil {
		return nil, nil, errors.Join(
			fmt.Errorf("activate Harbor network: replacement data plane returned no Done signal"),
			controller.closeDataPlane(candidate, candidateDone),
		)
	}
	if channelClosed(candidateDone) {
		return nil, nil, errors.Join(
			fmt.Errorf("activate Harbor network: replacement data plane stopped before startup"),
			controller.closeDataPlane(candidate, candidateDone),
		)
	}

	startupContext, cancelStartup := context.WithCancel(activation.runtimeContext)
	stopCallerCancellation := context.AfterFunc(ctx, cancelStartup)
	startErr := candidate.Start(startupContext)
	callerStillActive := stopCallerCancellation()
	if startErr != nil || !callerStillActive || ctx.Err() != nil || channelClosed(candidateDone) {
		cancelStartup()
		cause := startErr
		if cause == nil {
			cause = ctx.Err()
		}
		if cause == nil {
			cause = fmt.Errorf("replacement data plane stopped during startup")
		}
		return nil, nil, errors.Join(
			fmt.Errorf("activate Harbor network: start replacement data plane: %w", cause),
			controller.closeDataPlane(candidate, candidateDone),
		)
	}
	return candidate, candidateDone, nil
}

// publishNetworkActivation swaps generations only if no shutdown or concurrent topology owner crossed startup.
func (controller *Controller) publishNetworkActivation(
	activation networkActivation,
	candidate dataPlane,
	candidateDone <-chan struct{},
) (uint64, error) {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateReady ||
		controller.runtimeGeneration != activation.currentGeneration {
		return 0, fmt.Errorf("activate Harbor network: controller lifecycle changed during replacement startup")
	}
	if channelClosed(candidateDone) {
		return 0, fmt.Errorf("activate Harbor network: replacement data plane stopped before publication")
	}
	controller.runtimeGeneration++
	controller.dataPlane = candidate
	controller.runtimeDone = candidateDone
	controller.httpFoundation = activation.desired
	controller.publishedHTTPRoutes = activation.desired.HTTPRoutes()
	controller.managedNativeRoutes = nil
	return controller.runtimeGeneration, nil
}

// wrapNetworkActivationError adds one lifecycle boundary only when that boundary failed.
func wrapNetworkActivationError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("activate Harbor network: %s: %w", message, err)
}
