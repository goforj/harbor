//go:build darwin && cgo

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideNetworkResolverPolicyMigrationCapabilityDarwinAdvertisesAuthority proves production macOS builds expose the temporary retirement action.
func TestProvideNetworkResolverPolicyMigrationCapabilityDarwinAdvertisesAuthority(t *testing.T) {
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
	if capability.authority == nil {
		t.Fatal("Darwin capability authority is nil")
	}
}
