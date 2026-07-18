package domain

import "fmt"

// ProjectState is the daemon-derived aggregate state of a registered project.
type ProjectState string

const (
	// ProjectStopped means no active project session is expected.
	ProjectStopped ProjectState = "stopped"
	// ProjectStarting means required lifecycle work has not reached readiness.
	ProjectStarting ProjectState = "starting"
	// ProjectReady means required Apps and managed services are reachable.
	ProjectReady ProjectState = "ready"
	// ProjectRebuilding means the last ready session is processing a source change.
	ProjectRebuilding ProjectState = "rebuilding"
	// ProjectDegraded means the project remains usable while a non-critical dependency is unhealthy.
	ProjectDegraded ProjectState = "degraded"
	// ProjectFailed means a required App, service, or reconciliation step failed.
	ProjectFailed ProjectState = "failed"
	// ProjectStopping means graceful project shutdown is in progress.
	ProjectStopping ProjectState = "stopping"
	// ProjectUnavailable means a required checkout, tool, engine, or host capability is absent.
	ProjectUnavailable ProjectState = "unavailable"
)

// Validate reports whether the project state is part of Harbor's aggregate state model.
func (state ProjectState) Validate() error {
	switch state {
	case ProjectStopped,
		ProjectStarting,
		ProjectReady,
		ProjectRebuilding,
		ProjectDegraded,
		ProjectFailed,
		ProjectStopping,
		ProjectUnavailable:
		return nil
	default:
		return fmt.Errorf("unknown project state %q", state)
	}
}

// EntityState summarizes the presentation state of an App or service without replacing its detailed runtime facts.
type EntityState string

const (
	// EntityReady means the entity is available for its intended use.
	EntityReady EntityState = "ready"
	// EntityWorking means the entity is performing non-terminal lifecycle work.
	EntityWorking EntityState = "working"
	// EntityDegraded means the entity remains usable with a non-critical failure.
	EntityDegraded EntityState = "degraded"
	// EntityFailed means the entity cannot provide a required capability.
	EntityFailed EntityState = "failed"
	// EntityStopped means the entity is intentionally inactive.
	EntityStopped EntityState = "stopped"
	// EntityUnavailable means the entity cannot currently be observed or used.
	EntityUnavailable EntityState = "unavailable"
)

// Validate reports whether the entity state is a recognized summary state.
func (state EntityState) Validate() error {
	switch state {
	case EntityReady,
		EntityWorking,
		EntityDegraded,
		EntityFailed,
		EntityStopped,
		EntityUnavailable:
		return nil
	default:
		return fmt.Errorf("unknown entity state %q", state)
	}
}

// OperationState describes the durable lifecycle of one daemon-owned operation.
type OperationState string

const (
	// OperationQueued means the intent is durable but has not started applying effects.
	OperationQueued OperationState = "queued"
	// OperationRunning means the daemon is reconciling the operation.
	OperationRunning OperationState = "running"
	// OperationRequiresApproval means an interactive client must authorize a bounded effect.
	OperationRequiresApproval OperationState = "requires_approval"
	// OperationSucceeded means every required postcondition was verified.
	OperationSucceeded OperationState = "succeeded"
	// OperationFailed means the operation reached a terminal error.
	OperationFailed OperationState = "failed"
	// OperationCancelled means the operation ended without applying any unsafe pending effect.
	OperationCancelled OperationState = "cancelled"
)

// Validate reports whether the operation state is recognized.
func (state OperationState) Validate() error {
	switch state {
	case OperationQueued,
		OperationRunning,
		OperationRequiresApproval,
		OperationSucceeded,
		OperationFailed,
		OperationCancelled:
		return nil
	default:
		return fmt.Errorf("unknown operation state %q", state)
	}
}

// IsTerminal reports whether an operation state may no longer transition.
func (state OperationState) IsTerminal() bool {
	switch state {
	case OperationSucceeded, OperationFailed, OperationCancelled:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether the operation lifecycle permits the requested state change.
func (state OperationState) CanTransitionTo(next OperationState) bool {
	if state.Validate() != nil || next.Validate() != nil || state == next {
		return false
	}

	switch state {
	case OperationQueued:
		return next == OperationRunning || next == OperationCancelled
	case OperationRunning:
		return next == OperationRequiresApproval || next == OperationSucceeded || next == OperationFailed || next == OperationCancelled
	case OperationRequiresApproval:
		return next == OperationRunning || next == OperationFailed || next == OperationCancelled
	default:
		return false
	}
}
