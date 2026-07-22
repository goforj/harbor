//go:build !darwin

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideNetworkDataPlaneSetupCapabilityUnsupportedIsInert proves unsupported platforms do not dereference unavailable dependencies.
func TestProvideNetworkDataPlaneSetupCapabilityUnsupportedIsInert(t *testing.T) {
	capability, err := provideNetworkDataPlaneSetupCapability(
		(*models.NetworkStateRepo)(nil),
		(*state.OperationJournal)(nil),
		(*state.Store)(nil),
		(*state.MachineOwnershipProjectionSource)(nil),
		(*harbordruntime.Controller)(nil),
		(*reconcile.ProjectLifecycleCoordinator)(nil),
	)
	if err != nil {
		t.Fatalf("provideNetworkDataPlaneSetupCapability() error = %v", err)
	}
	if capability.authority != nil || capability.recovery != nil {
		t.Fatalf("unsupported capability = %#v, want empty capability", capability)
	}
}
