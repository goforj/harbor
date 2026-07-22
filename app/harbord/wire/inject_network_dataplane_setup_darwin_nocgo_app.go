//go:build darwin && !cgo

package wire

import (
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkDataPlaneSetupCapability leaves trusted-ingress inert when the Darwin Security.framework bridge is unavailable.
func provideNetworkDataPlaneSetupCapability(
	_ *models.NetworkStateRepo,
	_ *state.OperationJournal,
	_ *state.Store,
	_ *state.MachineOwnershipProjectionSource,
	_ *harbordruntime.Controller,
	_ *reconcile.ProjectLifecycleCoordinator,
) (networkDataPlaneSetupCapability, error) {
	return networkDataPlaneSetupCapability{}, nil
}
