//go:build darwin && cgo

package wire

import (
	"fmt"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkDataPlaneSetupCapability assembles Darwin's optional trusted-ingress control and recovery authority.
func provideNetworkDataPlaneSetupCapability(
	network *models.NetworkStateRepo,
	operations *state.OperationJournal,
	store *state.Store,
	ownership *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
	lifecycle *reconcile.ProjectLifecycleCoordinator,
) (networkDataPlaneSetupCapability, error) {
	platform, err := reconcile.CurrentNetworkDataPlaneSetupPlatform()
	if err != nil {
		return networkDataPlaneSetupCapability{}, fmt.Errorf("create network data-plane setup capability: %w", err)
	}
	trustAdapter, err := trust.New()
	if err != nil {
		return networkDataPlaneSetupCapability{}, fmt.Errorf("create network data-plane trust adapter: %w", err)
	}
	lowPortAdapter, err := lowport.New()
	if err != nil {
		return networkDataPlaneSetupCapability{}, fmt.Errorf("create network data-plane low-port adapter: %w", err)
	}
	projection := state.NewNetworkDataPlaneSetupProjectionSource(network)
	trustPlans := state.NewNetworkDataPlaneTrustPlanSource(network, runtimeController, platform)
	lowPortPlans := state.NewNetworkDataPlaneLowPortPlanSource(network)
	coordinator := reconcile.NewNetworkDataPlaneSetupCoordinator(
		operations,
		store,
		projection,
		store,
		runtimeController,
		trustPlans,
		lowPortPlans,
		func() (reconcile.NetworkDataPlaneSetupTrustIssuer, error) {
			return ticketissuer.OpenDefaultTrustService(trustPlans, ownership, trustAdapter)
		},
		func() (reconcile.NetworkDataPlaneSetupLowPortIssuer, error) {
			return ticketissuer.OpenDefaultLowPortService(lowPortPlans, ownership, lowPortAdapter)
		},
		ownership,
		trustAdapter,
		lowPortAdapter,
		runtimeController,
		lifecycle,
		platform,
		helper.SystemClock{},
	)
	return networkDataPlaneSetupCapability{
		authority: authority.NewNetworkDataPlaneSetupAuthority(operations, coordinator),
		recovery:  coordinator,
	}, nil
}
