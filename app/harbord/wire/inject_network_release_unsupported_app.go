//go:build !darwin

package wire

import (
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkReleaseCapability leaves global network release unavailable until reviewed platform adapters exist.
func provideNetworkReleaseCapability(
	_ *models.NetworkStateRepo,
	_ *state.OperationJournal,
	_ *state.Store,
	_ *state.MachineOwnershipProjectionSource,
	_ *harbordruntime.Controller,
	_ reconcile.NetworkResolverSetupResolverObserver,
) (networkReleaseCapability, error) {
	return networkReleaseCapability{}, nil
}
