//go:build !darwin

package wire

import (
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkResolverPolicyMigrationCapability leaves legacy resolver-policy retirement unavailable until reviewed platform adapters exist.
func provideNetworkResolverPolicyMigrationCapability(
	_ *state.OperationJournal,
	_ *state.Store,
	_ *state.NetworkResolverPolicyMigrationPlanSource,
	_ *state.MachineOwnershipProjectionSource,
	_ *harbordruntime.Controller,
	_ reconcile.NetworkResolverSetupResolverObserver,
) (networkResolverPolicyMigrationCapability, error) {
	return networkResolverPolicyMigrationCapability{}, nil
}
