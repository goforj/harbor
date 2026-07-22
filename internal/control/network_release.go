package control

import (
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

const networkReleaseRuntimeOperationPhase = "releasing network runtime"

// StartNetworkReleaseRequest identifies one idempotent machine-global network release intent.
type StartNetworkReleaseRequest struct {
	IntentID domain.IntentID `json:"intent_id"`
}

// Validate reports whether the request contains one stable network release intent.
func (request StartNetworkReleaseRequest) Validate() error {
	return request.IntentID.Validate()
}

// ReadNetworkReleaseRequest selects one daemon-owned network release operation.
type ReadNetworkReleaseRequest struct {
	OperationID domain.OperationID `json:"operation_id"`
}

// Validate reports whether the request identifies one durable release operation.
func (request ReadNetworkReleaseRequest) Validate() error {
	return request.OperationID.Validate()
}

// NetworkReleasePhase identifies one ordered machine-global network release checkpoint.
type NetworkReleasePhase string

const (
	// NetworkReleasePhaseRuntimeRelease releases runtime-owned listeners first.
	NetworkReleasePhaseRuntimeRelease NetworkReleasePhase = "runtime_release"
	// NetworkReleasePhaseLowPorts releases privileged low-port integration.
	NetworkReleasePhaseLowPorts NetworkReleasePhase = "low_ports"
	// NetworkReleasePhaseResolver releases the Harbor resolver route.
	NetworkReleasePhaseResolver NetworkReleasePhase = "resolver"
	// NetworkReleasePhaseTrust releases the public-root trust entry when owned.
	NetworkReleasePhaseTrust NetworkReleasePhase = "trust"
	// NetworkReleasePhaseLoopbacks releases confirmed loopback targets.
	NetworkReleasePhaseLoopbacks NetworkReleasePhase = "loopbacks"
	// NetworkReleasePhaseVerifyEffects verifies all host effects are absent.
	NetworkReleasePhaseVerifyEffects NetworkReleasePhase = "verify_effects"
	// NetworkReleasePhaseOwnership releases the machine ownership record.
	NetworkReleasePhaseOwnership NetworkReleasePhase = "ownership"
	// NetworkReleasePhaseProjection removes the durable network projection last.
	NetworkReleasePhaseProjection NetworkReleasePhase = "projection"
)

// Validate reports whether the phase is in the fixed durable release plan.
func (phase NetworkReleasePhase) Validate() error {
	switch phase {
	case NetworkReleasePhaseRuntimeRelease, NetworkReleasePhaseLowPorts, NetworkReleasePhaseResolver,
		NetworkReleasePhaseTrust, NetworkReleasePhaseLoopbacks, NetworkReleasePhaseVerifyEffects,
		NetworkReleasePhaseOwnership, NetworkReleasePhaseProjection:
		return nil
	default:
		return fmt.Errorf("network release phase %q is unsupported", phase)
	}
}

// NetworkReleaseOperation reports one running durable global network release and its retained recovery checkpoint.
type NetworkReleaseOperation struct {
	Operation          domain.Operation    `json:"operation"`
	Revision           domain.Sequence     `json:"revision"`
	Phase              NetworkReleasePhase `json:"phase"`
	CheckpointRevision domain.Sequence     `json:"checkpoint_revision"`
	NetworkRevision    domain.Sequence     `json:"network_revision"`
}

// Validate reports whether the operation is a running global release aligned with its retained plan revisions.
func (operation NetworkReleaseOperation) Validate() error {
	if err := operation.Operation.Validate(); err != nil {
		return err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkRelease ||
		operation.Operation.ProjectID != "" ||
		operation.Operation.State != domain.OperationRunning ||
		operation.Operation.Phase != networkReleaseRuntimeOperationPhase {
		return errors.New("network release operation must be a running global retained-plan release")
	}
	if err := operation.Phase.Validate(); err != nil {
		return err
	}
	if err := validateNetworkReleaseRevision("operation", operation.Revision); err != nil {
		return err
	}
	if err := validateNetworkReleaseRevision("checkpoint", operation.CheckpointRevision); err != nil {
		return err
	}
	if err := validateNetworkReleaseRevision("network", operation.NetworkRevision); err != nil {
		return err
	}
	if operation.CheckpointRevision < operation.Revision || operation.Revision <= operation.NetworkRevision {
		return errors.New("network release revisions are not ordered")
	}
	return nil
}

// validateNetworkReleaseRevision keeps every release checkpoint exactly representable by supported clients.
func validateNetworkReleaseRevision(name string, revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("network release %s revision must be between 1 and %d", name, domain.MaximumSequence)
	}
	return nil
}
