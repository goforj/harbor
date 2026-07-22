//go:build !darwin

package wire

import (
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkDataPlaneSetupCapability leaves trusted-ingress unavailable until a reviewed platform adapter exists.
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
