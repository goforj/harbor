//go:build !darwin

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideNetworkReleaseCapabilityUnsupportedIsInert proves unsupported platforms do not dereference unavailable dependencies.
func TestProvideNetworkReleaseCapabilityUnsupportedIsInert(t *testing.T) {
	capability, err := provideNetworkReleaseCapability(
		(*models.NetworkStateRepo)(nil),
		(*state.OperationJournal)(nil),
		(*state.Store)(nil),
		(*state.MachineOwnershipProjectionSource)(nil),
		(*harbordruntime.Controller)(nil),
		(reconcile.NetworkResolverSetupResolverObserver)(nil),
	)
	if err != nil {
		t.Fatalf("provideNetworkReleaseCapability() error = %v", err)
	}
	if capability.authority != nil || capability.recovery != nil {
		t.Fatalf("unsupported capability = %#v, want empty capability", capability)
	}
}
