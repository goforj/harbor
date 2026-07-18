package domain

import (
	"fmt"
	"strings"
	"time"
)

// OperationKind identifies one bounded daemon mutation such as setting a favorite.
type OperationKind string

const (
	// OperationKindProjectUnregister identifies the atomic removal of one registered project.
	OperationKindProjectUnregister OperationKind = "project.unregister"
)

// Operation records the durable state of one idempotent intent.
type Operation struct {
	ID          OperationID    `json:"id"`
	IntentID    IntentID       `json:"intent_id"`
	Kind        OperationKind  `json:"kind"`
	ProjectID   ProjectID      `json:"project_id,omitempty"`
	State       OperationState `json:"state"`
	Phase       string         `json:"phase"`
	Problem     *Problem       `json:"problem,omitempty"`
	RequestedAt time.Time      `json:"requested_at"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
}

// NewOperation constructs a validated queued operation for one logical intent.
func NewOperation(id OperationID, intentID IntentID, kind OperationKind, projectID ProjectID, requestedAt time.Time) (Operation, error) {
	operation := Operation{
		ID:          id,
		IntentID:    intentID,
		Kind:        kind,
		ProjectID:   projectID,
		State:       OperationQueued,
		Phase:       string(OperationQueued),
		RequestedAt: requestedAt,
	}
	if err := operation.Validate(); err != nil {
		return Operation{}, err
	}
	return operation, nil
}

// Validate reports whether the operation identity, timestamps, and state-dependent fields agree.
func (operation Operation) Validate() error {
	if err := operation.ID.Validate(); err != nil {
		return err
	}
	if err := operation.IntentID.Validate(); err != nil {
		return err
	}
	if operation.ProjectID != "" {
		if err := operation.ProjectID.Validate(); err != nil {
			return err
		}
	}
	if err := validateOperationKind(operation.Kind); err != nil {
		return err
	}
	if operation.Kind == OperationKindProjectUnregister && operation.ProjectID == "" {
		return fmt.Errorf("project unregister operation must identify a project")
	}
	if err := operation.State.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(operation.Phase) == "" {
		return fmt.Errorf("operation phase must not be empty")
	}
	if err := validateDomainTime("operation requested time", operation.RequestedAt); err != nil {
		return err
	}
	if err := validateOptionalDomainTime("operation started time", operation.StartedAt); err != nil {
		return err
	}
	if err := validateOptionalDomainTime("operation finished time", operation.FinishedAt); err != nil {
		return err
	}
	if operation.StartedAt != nil && operation.StartedAt.Before(operation.RequestedAt) {
		return fmt.Errorf("operation started time must not precede its requested time")
	}
	if operation.FinishedAt != nil {
		lowerBound := operation.RequestedAt
		if operation.StartedAt != nil {
			lowerBound = *operation.StartedAt
		}
		if operation.FinishedAt.Before(lowerBound) {
			return fmt.Errorf("operation finished time must not precede its active lifetime")
		}
	}
	if operation.Problem != nil {
		if err := operation.Problem.Validate(); err != nil {
			return err
		}
	}
	return operation.validateStateFields()
}

// Transition returns a copy advanced to the requested operation state.
func (operation Operation) Transition(next OperationState, phase string, at time.Time, problem *Problem) (Operation, error) {
	if err := operation.Validate(); err != nil {
		return Operation{}, fmt.Errorf("current operation: %w", err)
	}
	if !operation.State.CanTransitionTo(next) {
		return Operation{}, fmt.Errorf("operation cannot transition from %q to %q", operation.State, next)
	}
	if strings.TrimSpace(phase) == "" {
		return Operation{}, fmt.Errorf("operation phase must not be empty")
	}
	if err := validateDomainTime("operation transition time", at); err != nil {
		return Operation{}, err
	}
	if at.Before(operation.RequestedAt) {
		return Operation{}, fmt.Errorf("operation transition time must not precede its requested time")
	}
	if operation.StartedAt != nil && at.Before(*operation.StartedAt) {
		return Operation{}, fmt.Errorf("operation transition time must not precede its start time")
	}

	nextOperation := operation
	nextOperation.State = next
	nextOperation.Phase = phase
	nextOperation.Problem = problem
	if next == OperationRunning && nextOperation.StartedAt == nil {
		startedAt := at
		nextOperation.StartedAt = &startedAt
	}
	if next.IsTerminal() {
		finishedAt := at
		nextOperation.FinishedAt = &finishedAt
	}
	if err := nextOperation.Validate(); err != nil {
		return Operation{}, err
	}
	return nextOperation, nil
}

// validateOperationKind keeps operation names extensible while rejecting ambiguous empty values.
func validateOperationKind(kind OperationKind) error {
	return validateIdentifier("operation kind", string(kind))
}

// validateDomainTime requires UTC so snapshots and fixtures do not depend on a client's local zone.
func validateDomainTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s must not be zero", name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", name)
	}
	return nil
}

// validateOptionalDomainTime applies the canonical time rule when a lifecycle timestamp is present.
func validateOptionalDomainTime(name string, value *time.Time) error {
	if value == nil {
		return nil
	}
	return validateDomainTime(name, *value)
}

// validateStateFields prevents presentation clients from inferring operation completion from contradictory fields.
func (operation Operation) validateStateFields() error {
	switch operation.State {
	case OperationQueued:
		if operation.StartedAt != nil || operation.FinishedAt != nil || operation.Problem != nil {
			return fmt.Errorf("queued operation must not contain start, finish, or problem fields")
		}
	case OperationRunning, OperationRequiresApproval:
		if operation.StartedAt == nil {
			return fmt.Errorf("%s operation must contain a start time", operation.State)
		}
		if operation.FinishedAt != nil || operation.Problem != nil {
			return fmt.Errorf("%s operation must not contain finish or problem fields", operation.State)
		}
	case OperationSucceeded:
		if operation.StartedAt == nil || operation.FinishedAt == nil {
			return fmt.Errorf("succeeded operation must contain start and finish times")
		}
		if operation.Problem != nil {
			return fmt.Errorf("succeeded operation must not contain a problem")
		}
	case OperationFailed:
		if operation.StartedAt == nil || operation.FinishedAt == nil || operation.Problem == nil {
			return fmt.Errorf("failed operation must contain start, finish, and problem fields")
		}
	case OperationCancelled:
		if operation.FinishedAt == nil {
			return fmt.Errorf("cancelled operation must contain a finish time")
		}
		if operation.Problem != nil {
			return fmt.Errorf("cancelled operation must not contain a problem")
		}
	}
	return nil
}
