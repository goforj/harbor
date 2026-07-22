//go:build darwin && cgo

package wire

import (
	"fmt"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkReleaseCapability assembles Darwin's optional global network-release control and recovery authority.
func provideNetworkReleaseCapability(
	network *models.NetworkStateRepo,
	operations *state.OperationJournal,
	store *state.Store,
	ownership *state.MachineOwnershipProjectionSource,
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
	coordinator := reconcile.NewGlobalNetworkReleaseCoordinator(
		operations,
		store,
		projection,
		runtimeController,
		ownership,
		lowPortAdapter,
		lowPortPlans,
		func() (reconcile.GlobalNetworkReleaseLowPortIssuer, error) {
			return ticketissuer.OpenDefaultLowPortService(lowPortPlans, ownership, lowPortAdapter)
		},
		resolverPlans,
		func() (reconcile.GlobalNetworkReleaseResolverIssuer, error) {
			return ticketissuer.OpenDefaultResolverService(resolverPlans, ownership, resolverObserver)
		},
		trustPlans,
		func() (reconcile.GlobalNetworkReleaseTrustIssuer, error) {
			return ticketissuer.OpenDefaultTrustService(trustPlans, ownership, trustAdapter)
		},
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
