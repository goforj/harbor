package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// OperationNotFoundError reports that no durable operation has the requested ID.
type OperationNotFoundError struct {
	OperationID domain.OperationID
}

// Error describes the missing operation identity.
func (err *OperationNotFoundError) Error() string {
	return fmt.Sprintf("operation %q was not found", err.OperationID)
}

// OperationIntentNotFoundError reports that no durable operation has the requested intent ID.
type OperationIntentNotFoundError struct {
	IntentID domain.IntentID
}

// Error describes the missing intent identity.
func (err *OperationIntentNotFoundError) Error() string {
	return fmt.Sprintf("operation intent %q was not found", err.IntentID)
}

// ProjectLifecycleOperationNotFoundError reports that a project has no durable lifecycle operation.
type ProjectLifecycleOperationNotFoundError struct {
	ProjectID domain.ProjectID
}

// Error describes the project whose lifecycle history is empty.
func (err *ProjectLifecycleOperationNotFoundError) Error() string {
	return fmt.Sprintf("project %q has no lifecycle operation", err.ProjectID)
}

// IntentConflictError reports an idempotency key reused for a different logical mutation.
type IntentConflictError struct {
	IntentID            domain.IntentID
	ExistingOperationID domain.OperationID
	ExistingKind        domain.OperationKind
	ExistingProjectID   domain.ProjectID
	RequestedKind       domain.OperationKind
	RequestedProjectID  domain.ProjectID
}

// Error describes how the requested intent differs from the durable intent.
func (err *IntentConflictError) Error() string {
	return fmt.Sprintf(
		"operation intent %q already belongs to operation %q with kind %q and project %q, not kind %q and project %q",
		err.IntentID,
		err.ExistingOperationID,
		err.ExistingKind,
		err.ExistingProjectID,
		err.RequestedKind,
		err.RequestedProjectID,
	)
}

// OperationIDConflictError reports an operation ID reused for a different intent.
type OperationIDConflictError struct {
	OperationID       domain.OperationID
	ExistingIntentID  domain.IntentID
	RequestedIntentID domain.IntentID
}

// Error describes which durable intent already owns the operation ID.
func (err *OperationIDConflictError) Error() string {
	return fmt.Sprintf(
		"operation ID %q already belongs to intent %q, not intent %q",
		err.OperationID,
		err.ExistingIntentID,
		err.RequestedIntentID,
	)
}

// StaleRevisionError reports an optimistic transition attempted against an obsolete revision.
type StaleRevisionError struct {
	OperationID domain.OperationID
	Expected    domain.Sequence
	Actual      domain.Sequence
}

// Error describes the requested and durable revisions.
func (err *StaleRevisionError) Error() string {
	return fmt.Sprintf(
		"operation %q revision is %d, not expected revision %d",
		err.OperationID,
		err.Actual,
		err.Expected,
	)
}

// ProjectNotFoundError reports that no durable project has the requested ID.
type ProjectNotFoundError struct {
	ProjectID domain.ProjectID
}

// ProjectSessionNotFoundError reports that a project does not own the requested active session.
type ProjectSessionNotFoundError struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
}

// ProjectSessionActiveError reports process authority that must be stopped before another project mutation.
type ProjectSessionActiveError struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
}

// ProjectSessionProcessEvidenceMissingError reports a legacy active session that crossed its process boundary without durable exact-process evidence.
type ProjectSessionProcessEvidenceMissingError struct {
	ProjectID  domain.ProjectID
	SessionID  domain.SessionID
	Owner      domain.SessionOwner
	State      domain.SessionState
	Generation uint64
	UpdatedAt  time.Time
}

// Error describes the unresolved session boundary without implying that its process is absent.
func (err *ProjectSessionProcessEvidenceMissingError) Error() string {
	return fmt.Sprintf(
		"project %q session %q in state %q has no durable exact-process evidence",
		err.ProjectID,
		err.SessionID,
		err.State,
	)
}

// Error describes the active project/session correlation without exposing credential material.
func (err *ProjectSessionActiveError) Error() string {
	return fmt.Sprintf("project %q has active session %q", err.ProjectID, err.SessionID)
}

// StaleSessionGenerationError reports a lifecycle mutation attempted against obsolete process authority.
type StaleSessionGenerationError struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	Expected  uint64
	Actual    uint64
}

// Error describes the requested and durable session generations.
func (err *StaleSessionGenerationError) Error() string {
	return fmt.Sprintf(
		"project %q session %q generation is %d, not expected generation %d",
		err.ProjectID,
		err.SessionID,
		err.Actual,
		err.Expected,
	)
}

// Error describes the missing project/session correlation without exposing credential material.
func (err *ProjectSessionNotFoundError) Error() string {
	if err.SessionID == "" {
		return fmt.Sprintf("project %q has no active session", err.ProjectID)
	}
	return fmt.Sprintf("session %q was not found for project %q", err.SessionID, err.ProjectID)
}

// Error describes the missing project identity.
func (err *ProjectNotFoundError) Error() string {
	return fmt.Sprintf("project %q was not found", err.ProjectID)
}

// ProjectBusyError reports that active operations still own work for a project whose unregister operation is completing.
type ProjectBusyError struct {
	ProjectID    domain.ProjectID
	OperationIDs []domain.OperationID
}

// Error describes the active operation identities that prevent unregister completion.
func (err *ProjectBusyError) Error() string {
	identities := make([]string, 0, len(err.OperationIDs))
	for _, operationID := range err.OperationIDs {
		identities = append(identities, fmt.Sprintf("%q", operationID))
	}
	return fmt.Sprintf("project %q has active operations: %s", err.ProjectID, strings.Join(identities, ", "))
}

// ResourceNotFoundError reports that no projected resource has the requested project-scoped identity.
type ResourceNotFoundError struct {
	Reference domain.ResourceRef
}

// Error describes the missing project-scoped resource identity.
func (err *ResourceNotFoundError) Error() string {
	return fmt.Sprintf("resource %q was not found in project %q", err.Reference.ResourceID, err.Reference.ProjectID)
}

// CorruptStateError reports a durable row that cannot be represented by Harbor's domain model.
type CorruptStateError struct {
	Entity string
	Key    string
	Cause  error
}

// Error identifies the corrupt durable entity without hiding the validation failure.
func (err *CorruptStateError) Error() string {
	return fmt.Sprintf("corrupt %s %q: %v", err.Entity, err.Key, err.Cause)
}

// Unwrap exposes the validation failure for callers that need its underlying classification.
func (err *CorruptStateError) Unwrap() error {
	return err.Cause
}

// corruptStateError gives every persistence conversion the same typed failure boundary.
func corruptStateError(entity, key string, cause error) error {
	return &CorruptStateError{Entity: entity, Key: key, Cause: cause}
}
