//go:build darwin && !cgo

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideNetworkResolverPolicyMigrationCapabilityDarwinNoCgoIsInert proves the unavailable Security.framework bridge does not dereference dependencies.
func TestProvideNetworkResolverPolicyMigrationCapabilityDarwinNoCgoIsInert(t *testing.T) {
	capability, err := provideNetworkResolverPolicyMigrationCapability(
		(*state.OperationJournal)(nil),
		(*state.Store)(nil),
		(*state.NetworkResolverPolicyMigrationPlanSource)(nil),
		(*state.MachineOwnershipProjectionSource)(nil),
		(*harbordruntime.Controller)(nil),
		(reconcile.NetworkResolverSetupResolverObserver)(nil),
	)
	if err != nil {
		t.Fatalf("provideNetworkResolverPolicyMigrationCapability() error = %v", err)
	}
	if capability.authority != nil {
		t.Fatalf("Darwin without cgo capability = %#v, want empty capability", capability)
	}
}
