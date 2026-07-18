package harbordruntime

import (
	"fmt"

	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// desiredStateFromRuntimeState maps only host-proven shared listener bindings into the process generation.
func desiredStateFromRuntimeState(runtimeState state.RuntimeState) (dataplane.DesiredState, error) {
	if err := runtimeState.Validate(); err != nil {
		return dataplane.DesiredState{}, fmt.Errorf("derive data plane from runtime state: %w", err)
	}

	listeners := dataplane.ListenerPlan{}
	if runtimeState.NetworkInitialized {
		reservations := runtimeState.Network.Reservations.Listeners
		listeners = dataplane.ListenerPlan{
			DNS:   reservations.DNS.Bind,
			HTTP:  reservations.HTTP.Bind,
			HTTPS: reservations.HTTPS.Bind,
		}
	}

	// Endpoint reservations prove public ownership only. Routes remain unpublished until a live session proves each private upstream.
	return dataplane.NewDesiredState(listeners, nil, nil, 0)
}
