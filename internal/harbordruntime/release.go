package harbordruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// networkReleaseFence contains only the durable values needed to reject a changed release owner.
type networkReleaseFence struct {
	operationID        domain.OperationID
	phase              state.GlobalNetworkReleasePlanPhase
	checkpointRevision domain.Sequence
	networkRevision    domain.Sequence
}

// ArmNetworkRelease retains the exact active runtime-release fence for a subsequent cold anchor start.
func (controller *Controller) ArmNetworkRelease(ctx context.Context) (bool, error) {
	if controller == nil || !controller.initialized {
		return false, ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	controller.mutex.Lock()
	if controller.state != controllerStateNew {
		controller.mutex.Unlock()
		return false, errors.New("arm Harbor network release: controller has already started")
	}
	controller.mutex.Unlock()
	plan, found, err := controller.dependencies.globalNetworkReleasePlans.ReadActiveGlobalNetworkReleasePlan(ctx)
	if err != nil {
		return false, fmt.Errorf("arm Harbor network release: read active durable plan: %w", err)
	}
	if !found {
		return false, nil
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.state != controllerStateNew {
		return false, errors.New("arm Harbor network release: controller has already started")
	}
	controller.releaseFence = releaseFenceFromPlan(plan)
	controller.releaseMode = true
	return true, nil
}

// ReleaseNetworkRuntime proves and retires the live full generation before advancing its durable checkpoint.
func (controller *Controller) ReleaseNetworkRuntime(ctx context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, error) {
	if controller == nil || !controller.initialized {
		return state.GlobalNetworkReleasePlanRecord{}, ErrNotInitialized
	}
	if err := operationID.Validate(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release Harbor network runtime: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	plan, found, err := controller.dependencies.globalNetworkReleasePlans.ReadActiveGlobalNetworkReleasePlan(ctx)
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release Harbor network runtime: read active durable plan: %w", err)
	}
	if !found || plan.Operation.Operation.ID != operationID {
		return state.GlobalNetworkReleasePlanRecord{}, errors.New("release Harbor network runtime: durable release owner does not match")
	}
	if plan.Phase != state.GlobalNetworkReleasePlanPhaseRuntimeRelease && plan.Phase != state.GlobalNetworkReleasePlanPhaseLowPorts {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release Harbor network runtime: active plan phase is %q", plan.Phase)
	}
	if err := controller.ensureReleaseAnchor(ctx, plan); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	if plan.Phase == state.GlobalNetworkReleasePlanPhaseLowPorts {
		return plan, nil
	}
	if err := controller.requireAdvanceReleaseAnchor(plan); err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, err
	}
	advanced, err := controller.dependencies.globalNetworkReleasePlans.AdvanceGlobalNetworkReleaseRuntime(ctx, state.AdvanceGlobalNetworkReleaseRuntimeRequest{
		OperationID:        operationID,
		CheckpointRevision: plan.CheckpointRevision,
		NetworkRevision:    plan.NetworkRevision,
	})
	if err != nil {
		return state.GlobalNetworkReleasePlanRecord{}, fmt.Errorf("release Harbor network runtime: advance durable plan: %w", err)
	}
	controller.mutex.Lock()
	controller.releaseFence = releaseFenceFromPlan(advanced)
	controller.mutex.Unlock()
	return advanced, nil
}

// ensureReleaseAnchor either verifies the existing zero-listener generation or atomically replaces a proven full generation.
func (controller *Controller) ensureReleaseAnchor(ctx context.Context, plan state.GlobalNetworkReleasePlanRecord) error {
	controller.mutex.RLock()
	lifecycle := controller.state
	runtime := controller.dataPlane
	runtimeDone := controller.runtimeDone
	generation := controller.runtimeGeneration
	runtimeRevision := controller.runtimeNetworkRevision
	mode := controller.releaseMode
	retired := controller.releaseRuntimeRetired
	fence := controller.releaseFence
	foundation := controller.httpFoundation
	root := controller.root
	publishedHTTPRoutes := controller.publishedHTTPRoutes
	managedNativeRoutes := controller.managedNativeRoutes
	controller.mutex.RUnlock()
	if lifecycle != controllerStateReady || runtime == nil || runtimeDone == nil {
		return ErrNotReady
	}
	if mode {
		if fence.operationID != plan.Operation.Operation.ID || fence.networkRevision != plan.NetworkRevision {
			return errors.New("release Harbor network runtime: retained release fence does not match active plan")
		}
		if !retired {
			return errors.New("release Harbor network runtime: prior full generation retirement is not verified")
		}
		if err := validateReleaseAnchor(runtime, runtimeDone); err != nil {
			return err
		}
		controller.mutex.Lock()
		controller.releaseFence = releaseFenceFromPlan(plan)
		controller.runtimeNetworkRevision = plan.NetworkRevision
		controller.mutex.Unlock()
		return nil
	}
	if plan.Phase != state.GlobalNetworkReleasePlanPhaseRuntimeRelease {
		return errors.New("release Harbor network runtime: a low-ports replay requires an existing release anchor")
	}
	if runtimeRevision != plan.NetworkRevision || foundation.ListenerPlan() != (dataplane.ListenerPlan{
		DNS:   plan.Authority.Projection.Listeners.DNS.Bind,
		HTTP:  plan.Authority.Projection.Listeners.HTTP.Bind,
		HTTPS: plan.Authority.Projection.Listeners.HTTPS.Bind,
	}) {
		return errors.New("release Harbor network runtime: live generation does not match the durable full listener plan")
	}
	if root.Fingerprint != plan.Authority.Root.Fingerprint {
		return errors.New("release Harbor network runtime: live certificate root does not match durable full authority")
	}
	snapshot := runtime.Snapshot()
	if err := snapshot.Validate(); err != nil || snapshot.State != dataplane.StateReady || len(publishedHTTPRoutes) != 0 || len(managedNativeRoutes) != 0 || !snapshot.DNS.Running || !snapshot.Ingress.Running {
		return errors.New("release Harbor network runtime: live full generation is not a ready route-free listener generation")
	}
	if err := verifyPublishedRouteObservation(snapshot, publishedHTTPRoutes, foundation, managedNativeRoutes); err != nil {
		return fmt.Errorf("release Harbor network runtime: verify live listener generation: %w", err)
	}
	anchorDesired, err := releaseAnchorDesiredState()
	if err != nil {
		return err
	}
	anchor, anchorDone, err := controller.startReleaseAnchor(ctx, anchorDesired)
	if err != nil {
		return err
	}
	controller.mutex.Lock()
	if controller.state != controllerStateReady || controller.runtimeGeneration != generation {
		controller.mutex.Unlock()
		return errors.Join(errors.New("release Harbor network runtime: live generation changed during anchor startup"), controller.closeDataPlane(anchor, anchorDone))
	}
	controller.dataPlane = anchor
	controller.runtimeDone = anchorDone
	controller.runtimeGeneration++
	controller.httpFoundation = anchorDesired
	controller.publishedHTTPRoutes = nil
	controller.managedNativeRoutes = nil
	controller.releaseFence = releaseFenceFromPlan(plan)
	controller.releaseMode = true
	controller.releaseRuntimeRetired = false
	controller.runtimeNetworkRevision = plan.NetworkRevision
	anchorGeneration := controller.runtimeGeneration
	controller.mutex.Unlock()
	controller.watchRuntimeExit(anchorGeneration, anchor, anchorDone)
	if err := controller.closeDataPlane(runtime, runtimeDone); err != nil {
		return fmt.Errorf("release Harbor network runtime: retire full generation: %w", err)
	}
	stopped := runtime.Snapshot()
	if err := stopped.Validate(); err != nil || stopped.State != dataplane.StateStopped || stopped.DNS.Running || stopped.Ingress.Running || len(stopped.Relays) != 0 {
		return errors.New("release Harbor network runtime: retired full generation did not stop cleanly")
	}
	controller.mutex.Lock()
	controller.releaseRuntimeRetired = true
	controller.mutex.Unlock()
	return validateReleaseAnchor(anchor, anchorDone)
}

// startReleaseAnchor starts a certificate-free zero-listener generation and cleans it on every failed admission.
func (controller *Controller) startReleaseAnchor(ctx context.Context, desired dataplane.DesiredState) (dataPlane, <-chan struct{}, error) {
	anchor, err := controller.dependencies.newDataPlane(dataplane.Config{Desired: desired})
	if err != nil {
		return nil, nil, fmt.Errorf("construct release anchor: %w", err)
	}
	if requiredInterfaceIsNil(anchor) {
		return nil, nil, errors.New("release anchor factory returned nil")
	}
	done := anchor.Done()
	if done == nil {
		return nil, nil, errors.Join(errors.New("release anchor returned nil Done"), controller.closeDataPlane(anchor, done))
	}
	controller.mutex.RLock()
	runtimeContext := controller.runtimeContext
	controller.mutex.RUnlock()
	if runtimeContext == nil {
		return nil, nil, errors.Join(errors.New("release anchor has no controller runtime context"), controller.closeDataPlane(anchor, done))
	}
	startupContext, cancelStartup := context.WithCancel(runtimeContext)
	stopCallerCancellation := context.AfterFunc(ctx, cancelStartup)
	err = anchor.Start(startupContext)
	callerStillActive := stopCallerCancellation()
	if err != nil || !callerStillActive || ctx.Err() != nil || channelClosed(done) {
		cancelStartup()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = errors.New("release anchor stopped during startup")
		}
		return nil, nil, errors.Join(err, controller.closeDataPlane(anchor, done))
	}
	return anchor, done, nil
}

// validateReleaseAnchor proves the current generation has no listener or route authority.
func validateReleaseAnchor(runtime dataPlane, done <-chan struct{}) error {
	if done == nil || channelClosed(done) {
		return errors.New("release Harbor network runtime: anchor is not alive")
	}
	snapshot := runtime.Snapshot()
	if err := snapshot.Validate(); err != nil || snapshot.State != dataplane.StateReady || snapshot.DNS.Configured || snapshot.Ingress.Configured || len(snapshot.Relays) != 0 {
		return errors.New("release Harbor network runtime: current generation is not a ready zero-listener anchor")
	}
	return nil
}

// requireAdvanceReleaseAnchor repeats process-local proof immediately before the durable checkpoint mutation.
func (controller *Controller) requireAdvanceReleaseAnchor(plan state.GlobalNetworkReleasePlanRecord) error {
	controller.mutex.RLock()
	lifecycle := controller.state
	mode := controller.releaseMode
	retired := controller.releaseRuntimeRetired
	fence := controller.releaseFence
	runtime := controller.dataPlane
	done := controller.runtimeDone
	generation := controller.runtimeGeneration
	controller.mutex.RUnlock()
	if lifecycle != controllerStateReady || !mode || !retired || generation == 0 {
		return errors.New("release Harbor network runtime: controller no longer owns a ready retired release anchor")
	}
	if fence.operationID != plan.Operation.Operation.ID || fence.networkRevision != plan.NetworkRevision || plan.Phase != state.GlobalNetworkReleasePlanPhaseRuntimeRelease {
		return errors.New("release Harbor network runtime: durable plan no longer matches the release anchor")
	}
	return validateReleaseAnchor(runtime, done)
}

// releaseAnchorDesiredState constructs the only permitted release runtime topology.
func releaseAnchorDesiredState() (dataplane.DesiredState, error) {
	return dataplane.NewDesiredState(dataplane.ListenerPlan{}, nil, nil, 0)
}

// releaseFenceFromPlan redacts the durable plan to its exact controller admission fence.
func releaseFenceFromPlan(plan state.GlobalNetworkReleasePlanRecord) networkReleaseFence {
	return networkReleaseFence{
		operationID:        plan.Operation.Operation.ID,
		phase:              plan.Phase,
		checkpointRevision: plan.CheckpointRevision,
		networkRevision:    plan.NetworkRevision,
	}
}

// sameReleaseFence compares every retained release boundary field.
func sameReleaseFence(left networkReleaseFence, right networkReleaseFence) bool {
	return left == right
}
