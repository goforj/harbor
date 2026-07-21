package control

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

const (
	projectRuntimeRepairOpaqueHexLength = 64
	maximumProjectRuntimeMemberCount    = 1<<16 - 1
	projectRuntimeRepairCommand         = "forj dev"
)

// ProjectRuntimeRepairInspectionDisposition identifies the only three inspection result shapes exposed to clients.
type ProjectRuntimeRepairInspectionDisposition string

const (
	// ProjectRuntimeRepairInspectionConfirmable means one bounded candidate can be presented for explicit confirmation.
	ProjectRuntimeRepairInspectionConfirmable ProjectRuntimeRepairInspectionDisposition = "confirmable"
	// ProjectRuntimeRepairInspectionNotActionable means native inspection could not isolate one safe candidate.
	ProjectRuntimeRepairInspectionNotActionable ProjectRuntimeRepairInspectionDisposition = "not_actionable"
	// ProjectRuntimeRepairInspectionUnsupported means this daemon has no native implementation for its operating system.
	ProjectRuntimeRepairInspectionUnsupported ProjectRuntimeRepairInspectionDisposition = "unsupported"
)

// ProjectRuntimeRepairNotActionableReason explains a non-confirmable observation without exposing native evidence.
type ProjectRuntimeRepairNotActionableReason string

const (
	// ProjectRuntimeRepairReasonNone means no correlated runtime candidate was found.
	ProjectRuntimeRepairReasonNone ProjectRuntimeRepairNotActionableReason = "none"
	// ProjectRuntimeRepairReasonAmbiguous means more than one candidate or ownership scope correlated.
	ProjectRuntimeRepairReasonAmbiguous ProjectRuntimeRepairNotActionableReason = "ambiguous"
	// ProjectRuntimeRepairReasonForeign means the only correlated candidate belongs to another user.
	ProjectRuntimeRepairReasonForeign ProjectRuntimeRepairNotActionableReason = "foreign"
	// ProjectRuntimeRepairReasonUnreadable means required native facts could not be read completely.
	ProjectRuntimeRepairReasonUnreadable ProjectRuntimeRepairNotActionableReason = "unreadable"
)

// ProjectRuntimeRepairInspectionID is an opaque, daemon-owned handle for one short-lived inspection plan.
type ProjectRuntimeRepairInspectionID string

// Validate reports whether the inspection ID is the canonical lowercase encoding of 32 random bytes.
func (id ProjectRuntimeRepairInspectionID) Validate() error {
	return validateProjectRuntimeRepairOpaqueHex("project runtime repair inspection ID", string(id))
}

// ProjectRuntimeRepairCandidateFingerprint identifies the exact server-retained candidate shown by an inspection.
type ProjectRuntimeRepairCandidateFingerprint string

// Validate reports whether the candidate fingerprint is a canonical lowercase SHA-256 value.
func (fingerprint ProjectRuntimeRepairCandidateFingerprint) Validate() error {
	return validateProjectRuntimeRepairOpaqueHex("project runtime repair candidate fingerprint", string(fingerprint))
}

// InspectProjectRuntimeRepairRequest selects one project while leaving all process and network derivation to the daemon.
type InspectProjectRuntimeRepairRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
}

// Validate reports whether the inspection request identifies one registered project.
func (request InspectProjectRuntimeRepairRequest) Validate() error {
	return request.ProjectID.Validate()
}

// ConfirmProjectRuntimeRepairRequest echoes only the opaque candidate selection returned by inspection.
type ConfirmProjectRuntimeRepairRequest struct {
	ProjectID    domain.ProjectID                         `json:"project_id"`
	InspectionID ProjectRuntimeRepairInspectionID         `json:"inspection_id"`
	Fingerprint  ProjectRuntimeRepairCandidateFingerprint `json:"candidate_fingerprint"`
}

// Validate reports whether confirmation selects one complete daemon-owned inspection plan.
func (request ConfirmProjectRuntimeRepairRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.InspectionID.Validate(); err != nil {
		return err
	}
	return request.Fingerprint.Validate()
}

// ProjectRuntimeRepairDisplayFacts contains only bounded, non-secret facts useful for recognizing one candidate.
type ProjectRuntimeRepairDisplayFacts struct {
	Command     string `json:"command"`
	Checkout    string `json:"checkout"`
	Endpoint    string `json:"endpoint"`
	RootPID     uint32 `json:"root_pid"`
	MemberCount uint32 `json:"member_count"`
}

// Validate reports whether display facts describe the fixed GoForj command and one bounded local candidate.
func (facts ProjectRuntimeRepairDisplayFacts) Validate() error {
	if facts.Command != projectRuntimeRepairCommand {
		return fmt.Errorf("project runtime repair command must be %q", projectRuntimeRepairCommand)
	}
	if err := (RegisterProjectRequest{Path: facts.Checkout}).Validate(); err != nil {
		return fmt.Errorf("project runtime repair checkout: %w", err)
	}
	endpoint, err := netip.ParseAddrPort(facts.Endpoint)
	if err != nil || !endpoint.Addr().Is4() || !endpoint.Addr().IsLoopback() ||
		endpoint.Addr() != endpoint.Addr().Unmap() || endpoint.Port() == 0 || endpoint.String() != facts.Endpoint {
		return errors.New("project runtime repair endpoint must be a canonical IPv4 loopback address and port")
	}
	if facts.RootPID == 0 {
		return errors.New("project runtime repair root PID must be positive")
	}
	if facts.MemberCount == 0 || facts.MemberCount > maximumProjectRuntimeMemberCount {
		return fmt.Errorf(
			"project runtime repair member count must be between 1 and %d",
			maximumProjectRuntimeMemberCount,
		)
	}
	return nil
}

// ProjectRuntimeRepairConfirmable contains one reviewed display projection and its opaque one-use selection.
type ProjectRuntimeRepairConfirmable struct {
	Candidate            ProjectRuntimeRepairDisplayFacts         `json:"candidate"`
	InspectionID         ProjectRuntimeRepairInspectionID         `json:"inspection_id"`
	CandidateFingerprint ProjectRuntimeRepairCandidateFingerprint `json:"candidate_fingerprint"`
	ExpiresAt            time.Time                                `json:"expires_at"`
}

// Validate reports whether a confirmable result contains one complete short-lived selection.
func (confirmable ProjectRuntimeRepairConfirmable) Validate() error {
	if err := confirmable.Candidate.Validate(); err != nil {
		return err
	}
	if err := confirmable.InspectionID.Validate(); err != nil {
		return err
	}
	if err := confirmable.CandidateFingerprint.Validate(); err != nil {
		return err
	}
	_, expiryOffset := confirmable.ExpiresAt.Zone()
	if confirmable.ExpiresAt.IsZero() || expiryOffset != 0 {
		return errors.New("project runtime repair expiry must be a nonzero UTC time")
	}
	return nil
}

// ProjectRuntimeRepairInspection is the tagged, daemon-authoritative result of inspecting one quarantined project.
type ProjectRuntimeRepairInspection struct {
	ProjectID   domain.ProjectID                          `json:"project_id"`
	Disposition ProjectRuntimeRepairInspectionDisposition `json:"disposition"`
	Confirmable *ProjectRuntimeRepairConfirmable          `json:"confirmable,omitempty"`
	Reason      ProjectRuntimeRepairNotActionableReason   `json:"reason,omitempty"`
}

// Validate reports whether inspection contains exactly the fields permitted by its disposition.
func (inspection ProjectRuntimeRepairInspection) Validate() error {
	if err := inspection.ProjectID.Validate(); err != nil {
		return err
	}
	switch inspection.Disposition {
	case ProjectRuntimeRepairInspectionConfirmable:
		if inspection.Confirmable == nil {
			return errors.New("confirmable project runtime repair inspection requires candidate details")
		}
		if inspection.Reason != "" {
			return errors.New("confirmable project runtime repair inspection must not contain a reason")
		}
		return inspection.Confirmable.Validate()
	case ProjectRuntimeRepairInspectionNotActionable:
		if inspection.Confirmable != nil {
			return errors.New("non-actionable project runtime repair inspection must not contain candidate details")
		}
		return inspection.Reason.Validate()
	case ProjectRuntimeRepairInspectionUnsupported:
		if inspection.Confirmable != nil || inspection.Reason != "" {
			return errors.New("unsupported project runtime repair inspection must not contain candidate details or a reason")
		}
		return nil
	default:
		return fmt.Errorf("unsupported project runtime repair inspection disposition %q", inspection.Disposition)
	}
}

// Validate reports whether reason is one of the fixed non-actionable classifications.
func (reason ProjectRuntimeRepairNotActionableReason) Validate() error {
	switch reason {
	case ProjectRuntimeRepairReasonNone,
		ProjectRuntimeRepairReasonAmbiguous,
		ProjectRuntimeRepairReasonForeign,
		ProjectRuntimeRepairReasonUnreadable:
		return nil
	default:
		return fmt.Errorf("unsupported project runtime repair non-actionable reason %q", reason)
	}
}

// ProjectRuntimeRepairConfirmation is the authoritative retryable projection returned after native postconditions.
type ProjectRuntimeRepairConfirmation struct {
	Project  domain.ProjectSnapshot `json:"project"`
	Revision domain.Sequence        `json:"revision"`
}

// Validate reports whether confirmation contains one route-free retryable project at a JavaScript-safe revision.
func (confirmation ProjectRuntimeRepairConfirmation) Validate() error {
	if err := confirmation.Project.Validate(); err != nil {
		return err
	}
	switch confirmation.Project.State {
	case domain.ProjectStopped, domain.ProjectFailed, domain.ProjectUnavailable:
	default:
		return errors.New("project runtime repair confirmation must contain a retryable project")
	}
	if len(confirmation.Project.Resources) != 0 {
		return errors.New("project runtime repair confirmation must remain route-free")
	}
	for _, app := range confirmation.Project.Apps {
		if app.State != domain.EntityStopped || app.Active {
			return errors.New("project runtime repair confirmation must not contain an active App")
		}
	}
	for _, service := range confirmation.Project.Services {
		if service.State != domain.EntityStopped {
			return errors.New("project runtime repair confirmation must not contain an active service")
		}
	}
	if confirmation.Revision == 0 || confirmation.Revision > domain.MaximumSequence {
		return fmt.Errorf(
			"project runtime repair confirmation revision must be between 1 and %d",
			domain.MaximumSequence,
		)
	}
	return nil
}

// projectRuntimeRepairInspectionResponse keeps transport framing extensible around the tagged inspection result.
type projectRuntimeRepairInspectionResponse struct {
	Inspection ProjectRuntimeRepairInspection `json:"inspection"`
}

// projectRuntimeRepairConfirmationResponse keeps transport framing extensible around the stopped project result.
type projectRuntimeRepairConfirmationResponse struct {
	Confirmation ProjectRuntimeRepairConfirmation `json:"confirmation"`
}

// validateProjectRuntimeRepairInspectionCorrelation binds a response to the project selected by its request.
func validateProjectRuntimeRepairInspectionCorrelation(
	request InspectProjectRuntimeRepairRequest,
	inspection ProjectRuntimeRepairInspection,
) error {
	if inspection.ProjectID != request.ProjectID {
		return errors.New("project runtime repair inspection belongs to another project")
	}
	return nil
}

// validateProjectRuntimeRepairConfirmationCorrelation binds a stopped projection to the confirmed project selection.
func validateProjectRuntimeRepairConfirmationCorrelation(
	request ConfirmProjectRuntimeRepairRequest,
	confirmation ProjectRuntimeRepairConfirmation,
) error {
	if confirmation.Project.ID != request.ProjectID {
		return errors.New("project runtime repair confirmation belongs to another project")
	}
	return nil
}

// validateProjectRuntimeRepairOpaqueHex rejects truncated, padded, uppercase, or non-hex plan selectors.
func validateProjectRuntimeRepairOpaqueHex(name string, value string) error {
	if len(value) != projectRuntimeRepairOpaqueHexLength {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, projectRuntimeRepairOpaqueHexLength)
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, projectRuntimeRepairOpaqueHexLength)
		}
	}
	return nil
}
