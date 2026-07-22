package harbordruntime

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// ActivateResolver publishes the authoritative DNS generation after resolver setup reaches its durable stage.
//
// HTTP and HTTPS remain intentionally absent here. They require the separate low-port and trust proof gates that
// promote the durable network to full stage; DNS should not wait for those unrelated capabilities.
func (controller *Controller) ActivateResolver(ctx context.Context, expectedRevision domain.Sequence) error {
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
		return fmt.Errorf("activate Harbor resolver: expected network revision must be between 1 and %d", domain.MaximumSequence)
	}

	controller.reconcileMutex.Lock()
	controller.mutex.RLock()
	releasing := controller.releaseMode
	controller.mutex.RUnlock()
	if releasing {
		controller.reconcileMutex.Unlock()
		return errors.New("activate Harbor resolver: network release is armed")
	}
	activation, err := controller.prepareResolverActivation(ctx, expectedRevision)
	if err != nil {
		controller.reconcileMutex.Unlock()
		return err
	}
	if activation.replayed {
		controller.reconcileMutex.Unlock()
		return nil
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
		wrapNetworkActivationError("confirm resolver generation", reconcileErr),
	)
}

// prepareResolverActivation proves the exact resolver-stage durable revision before replacing the empty generation.
func (controller *Controller) prepareResolverActivation(
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
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: %w", ErrNotReady)
	}

	runtimeState, err := controller.source.RuntimeState(ctx)
	if err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: read durable state: %w", err)
	}
	if err := runtimeState.Validate(); err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: validate durable state: %w", err)
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageResolver {
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: durable network is not at resolver stage")
	}
	if runtimeState.Network.Revision != expectedRevision {
		return networkActivation{}, fmt.Errorf(
			"activate Harbor resolver: durable network revision is %d, expected %d",
			runtimeState.Network.Revision,
			expectedRevision,
		)
	}
	root, err := activation.authority.PublicRoot()
	if err != nil {
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: read public certificate authority: %w", err)
	}
	desired, err := resolverDesiredState(runtimeState, root.Fingerprint)
	if err != nil {
		return networkActivation{}, err
	}
	if sameHTTPFoundation(currentFoundation, desired) {
		snapshot := activation.current.Snapshot()
		if err := snapshot.Validate(); err != nil || snapshot.State != dataplane.StateReady {
			return networkActivation{}, fmt.Errorf("activate Harbor resolver: existing generation is not ready")
		}
		activation.replayed = true
		return activation, nil
	}
	if currentFoundation.ListenerPlan() != (dataplane.ListenerPlan{}) ||
		len(currentFoundation.NativeRoutes()) != 0 {
		return networkActivation{}, fmt.Errorf("activate Harbor resolver: live listener topology differs from the resolver-stage generation")
	}
	activation.desired = desired
	activation.networkRevision = expectedRevision
	return activation, nil
}

// resolverDesiredState derives only the exact DNS socket for a validated resolver-stage aggregate.
func resolverDesiredState(runtimeState state.RuntimeState, authorityFingerprint string) (dataplane.DesiredState, error) {
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageResolver {
		return dataplane.DesiredState{}, fmt.Errorf("derive Harbor resolver generation: durable network is not at resolver stage")
	}
	platform, err := resolverNetworkPlanPlatform(runtime.GOOS)
	if err != nil {
		return dataplane.DesiredState{}, err
	}
	policy, err := networkplan.Build(networkplan.Request{
		Platform:             platform,
		InstallationID:       runtimeState.Network.Ownership.InstallationID,
		Pool:                 runtimeState.Network.Pool,
		AuthorityFingerprint: authorityFingerprint,
	})
	if err != nil {
		return dataplane.DesiredState{}, fmt.Errorf("derive Harbor resolver generation: build host network policy: %w", err)
	}
	desired, err := dataplane.NewDesiredState(dataplane.ListenerPlan{DNS: policy.DNS.Bind}, nil, nil, 0)
	if err != nil {
		return dataplane.DesiredState{}, fmt.Errorf("derive Harbor resolver generation: construct DNS state: %w", err)
	}
	return desired, nil
}

// resolverNetworkPlanPlatform maps the product's supported OS profiles without importing the setup coordinator.
func resolverNetworkPlanPlatform(goos string) (networkplan.Platform, error) {
	switch goos {
	case "darwin":
		return networkplan.PlatformMacOS, nil
	case "linux":
		return networkplan.PlatformUbuntu2404, nil
	case "windows":
		return networkplan.PlatformWindows11, nil
	default:
		return "", fmt.Errorf("Harbor resolver generation is unsupported on %s", goos)
	}
}
