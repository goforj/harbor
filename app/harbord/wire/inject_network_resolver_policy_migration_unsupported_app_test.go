//go:build !darwin

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideNetworkResolverPolicyMigrationCapabilityUnsupportedIsInert proves unsupported platforms do not dereference unavailable dependencies.
func TestProvideNetworkResolverPolicyMigrationCapabilityUnsupportedIsInert(t *testing.T) {
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
		t.Fatalf("unsupported capability = %#v, want empty capability", capability)
	}
}
