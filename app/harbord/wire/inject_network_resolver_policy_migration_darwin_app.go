//go:build darwin && cgo

package wire

import (
	"fmt"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// provideNetworkResolverPolicyMigrationCapability assembles Darwin's optional legacy resolver-policy retirement authority.
func provideNetworkResolverPolicyMigrationCapability(
	operations *state.OperationJournal,
	store *state.Store,
	plans *state.NetworkResolverPolicyMigrationPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
	resolverObserver reconcile.NetworkResolverSetupResolverObserver,
) (networkResolverPolicyMigrationCapability, error) {
	platform, err := reconcile.CurrentNetworkResolverSetupPlatform()
	if err != nil {
		return networkResolverPolicyMigrationCapability{}, fmt.Errorf("create network resolver policy migration capability: %w", err)
	}
	coordinator := reconcile.NewNetworkResolverPolicyMigrationCoordinator(
		operations,
		store,
		plans,
		store,
		runtimeController,
		func() (reconcile.NetworkResolverPolicyMigrationIssuer, error) {
			return ticketissuer.OpenDefaultResolverService(plans, ownership, resolverObserver)
		},
		ownership,
		resolverObserver,
		platform,
		helper.SystemClock{},
	)
	return networkResolverPolicyMigrationCapability{
		authority: authority.NewNetworkResolverPolicyMigrationAuthority(coordinator),
	}, nil
}
