package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

// ProjectRegistrationConflictKind identifies which stable registration identity is already owned.
type ProjectRegistrationConflictKind string

const (
	// ProjectRegistrationConflictIdentity means one opaque project identity is already bound to another checkout.
	ProjectRegistrationConflictIdentity ProjectRegistrationConflictKind = "identity"
	// ProjectRegistrationConflictSlug means another project already owns the proposed development-domain label.
	ProjectRegistrationConflictSlug ProjectRegistrationConflictKind = "slug"
)

// ProjectRegistrationConflictError reports an existing project that prevents a new registration.
type ProjectRegistrationConflictError struct {
	Kind               ProjectRegistrationConflictKind
	RequestedProjectID domain.ProjectID
	RequestedPath      string
	RequestedSlug      string
	ExistingProjectID  domain.ProjectID
	ExistingPath       string
	ExistingSlug       string
}

// Error describes the exact durable key that is already owned.
func (err *ProjectRegistrationConflictError) Error() string {
	switch err.Kind {
	case ProjectRegistrationConflictIdentity:
		return fmt.Sprintf(
			"project registration identity conflict: requested project %q at %q, but that identity belongs to %q",
			err.RequestedProjectID,
			err.RequestedPath,
			err.ExistingPath,
		)
	case ProjectRegistrationConflictSlug:
		return fmt.Sprintf(
			"project registration slug conflict: requested slug %q at %q, but project %q at %q already owns it",
			err.RequestedSlug,
			err.RequestedPath,
			err.ExistingProjectID,
			err.ExistingPath,
		)
	default:
		return fmt.Sprintf("project registration conflict %q", err.Kind)
	}
}

// newProjectRegistrationConflict preserves enough safe identity detail for daemon diagnostics.
func newProjectRegistrationConflict(
	kind ProjectRegistrationConflictKind,
	requested domain.ProjectSnapshot,
	existing domain.ProjectSnapshot,
) *ProjectRegistrationConflictError {
	return &ProjectRegistrationConflictError{
		Kind:               kind,
		RequestedProjectID: requested.ID,
		RequestedPath:      requested.Path,
		RequestedSlug:      requested.Slug,
		ExistingProjectID:  existing.ID,
		ExistingPath:       existing.Path,
		ExistingSlug:       existing.Slug,
	}
}
