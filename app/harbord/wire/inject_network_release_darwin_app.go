//go:build darwin && cgo

package wire

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/machinepaths"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkReleaseCapability assembles Darwin's optional global network-release control and recovery authority.
func provideNetworkReleaseCapability(
	network *models.NetworkStateRepo,
	operations *state.OperationJournal,
	store *state.Store,
	ownershipProjection *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
	resolverObserver reconcile.NetworkResolverSetupResolverObserver,
) (networkReleaseCapability, error) {
	platform, err := reconcile.CurrentGlobalNetworkReleasePlatform()
	if err != nil {
		return networkReleaseCapability{}, fmt.Errorf("create network release capability: %w", err)
	}
	trustAdapter, err := trust.New()
	if err != nil {
		return networkReleaseCapability{}, fmt.Errorf("create network release trust adapter: %w", err)
	}
	lowPortAdapter, err := lowport.New()
	if err != nil {
		return networkReleaseCapability{}, fmt.Errorf("create network release low-port adapter: %w", err)
	}
	projection := state.NewNetworkDataPlaneSetupProjectionSource(network)
	lowPortPlans := state.NewGlobalNetworkReleaseLowPortPlanSource(operations)
	resolverPlans := state.NewGlobalNetworkReleaseResolverPlanSource(operations)
	trustPlans := state.NewGlobalNetworkReleaseTrustPlanSource(operations)
	loopbackPlans := state.NewGlobalNetworkReleaseLoopbackPlanSource(operations)
	ownershipPlans := state.NewGlobalNetworkReleaseOwnershipPlanSource(operations)
	proofObserver, err := ownershipreleaseproof.NewDefaultObserver()
	if err != nil {
		return networkReleaseCapability{}, fmt.Errorf("create network release ownership proof observer: %w", err)
	}
	paths, err := machinepaths.Resolve()
	if err != nil {
		return networkReleaseCapability{}, fmt.Errorf("resolve network release ownership path: %w", err)
	}
	protectedOwnership := globalNetworkReleaseProtectedOwnershipObserver{path: paths.OwnershipPath}
	coordinator := reconcile.NewGlobalNetworkReleaseCoordinator(
		operations,
		store,
		projection,
		runtimeController,
		ownershipProjection,
		protectedOwnership,
		lowPortAdapter,
		lowPortPlans,
		func() (reconcile.GlobalNetworkReleaseLowPortIssuer, error) {
			return ticketissuer.OpenDefaultLowPortService(lowPortPlans, ownershipProjection, lowPortAdapter)
		},
		resolverPlans,
		func() (reconcile.GlobalNetworkReleaseResolverIssuer, error) {
			return ticketissuer.OpenDefaultResolverService(resolverPlans, ownershipProjection, resolverObserver)
		},
		trustPlans,
		func() (reconcile.GlobalNetworkReleaseTrustIssuer, error) {
			return ticketissuer.OpenDefaultTrustService(trustPlans, ownershipProjection, trustAdapter)
		},
		loopbackPlans,
		func() (reconcile.GlobalNetworkReleaseLoopbackIssuer, error) {
			return ticketissuer.OpenDefaultPoolReleaseService(loopbackPlans, ownershipProjection)
		},
		ownershipPlans,
		func() (reconcile.GlobalNetworkReleaseOwnershipIssuer, error) {
			return ticketissuer.OpenDefaultOwnershipReleaseService(ownershipPlans, ownershipProjection)
		},
		proofObserver,
		resolverObserver,
		trustAdapter,
		loopback.New(),
		runtimeController,
		platform,
		helper.SystemClock{},
	)
	return networkReleaseCapability{
		authority: authority.NewNetworkReleaseAuthority(operations, coordinator),
		recovery:  coordinator,
	}, nil
}

// globalNetworkReleaseProtectedOwnershipObserver reads only the protected helper-owned record during terminal release confirmation.
type globalNetworkReleaseProtectedOwnershipObserver struct {
	path string
}

// Observe opens a short-lived protected store so terminal confirmation cannot retain a stale file handle.
func (observer globalNetworkReleaseProtectedOwnershipObserver) Observe(ctx context.Context) (ownership.Observation, error) {
	return ownership.ObservePath(ctx, observer.path)
}
