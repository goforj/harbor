//go:build windows

package wire

import (
	"fmt"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkDataPlaneSetupCapability assembles Windows trusted ingress around machine trust and policy-proved direct listeners.
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
	trustAdapter, err := trust.NewMachine()
	if err != nil {
		return networkDataPlaneSetupCapability{}, fmt.Errorf("create network data-plane trust adapter: %w", err)
	}
	directLowPorts := windowsDirectLowPortBoundary{}
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
			return directLowPorts, nil
		},
		ownership,
		trustAdapter,
		directLowPorts,
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
