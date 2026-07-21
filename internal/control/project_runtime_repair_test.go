package control

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestProjectRuntimeRepairRequestsRequireCanonicalSelections verifies direct callers cannot bypass identifier validation.
func TestProjectRuntimeRepairRequestsRequireCanonicalSelections(t *testing.T) {
	inspectionID := ProjectRuntimeRepairInspectionID(strings.Repeat("a", projectRuntimeRepairOpaqueHexLength))
	fingerprint := ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", projectRuntimeRepairOpaqueHexLength))
	if err := (InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}).Validate(); err != nil {
		t.Fatalf("InspectProjectRuntimeRepairRequest.Validate() error = %v", err)
	}
	if err := (ConfirmProjectRuntimeRepairRequest{
		ProjectID:    "project-orders",
		InspectionID: inspectionID,
		Fingerprint:  fingerprint,
	}).Validate(); err != nil {
		t.Fatalf("ConfirmProjectRuntimeRepairRequest.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "inspect project",
			validate: func() error {
				return (InspectProjectRuntimeRepairRequest{ProjectID: " bad "}).Validate()
			},
		},
		{
			name: "confirm project",
			validate: func() error {
				return (ConfirmProjectRuntimeRepairRequest{ProjectID: "", InspectionID: inspectionID, Fingerprint: fingerprint}).Validate()
			},
		},
		{
			name: "inspection ID",
			validate: func() error {
				return (ConfirmProjectRuntimeRepairRequest{ProjectID: "project-orders", Fingerprint: fingerprint}).Validate()
			},
		},
		{
			name: "fingerprint",
			validate: func() error {
				return (ConfirmProjectRuntimeRepairRequest{ProjectID: "project-orders", InspectionID: inspectionID}).Validate()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestProjectRuntimeRepairOpaqueSelectorsRequireLowercaseSHA256Shape prevents ambiguous or lossy plan selectors.
func TestProjectRuntimeRepairOpaqueSelectorsRequireLowercaseSHA256Shape(t *testing.T) {
	valid := strings.Repeat("0123456789abcdef", 4)
	if err := ProjectRuntimeRepairInspectionID(valid).Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairInspectionID.Validate() error = %v", err)
	}
	if err := ProjectRuntimeRepairCandidateFingerprint(valid).Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairCandidateFingerprint.Validate() error = %v", err)
	}

	for _, value := range []string{
		"",
		strings.Repeat("a", projectRuntimeRepairOpaqueHexLength-1),
		strings.Repeat("a", projectRuntimeRepairOpaqueHexLength+1),
		strings.Repeat("A", projectRuntimeRepairOpaqueHexLength),
		strings.Repeat("g", projectRuntimeRepairOpaqueHexLength),
		strings.Repeat("0", projectRuntimeRepairOpaqueHexLength-1) + " ",
	} {
		if err := ProjectRuntimeRepairInspectionID(value).Validate(); err == nil {
			t.Errorf("ProjectRuntimeRepairInspectionID(%q).Validate() error = nil", value)
		}
		if err := ProjectRuntimeRepairCandidateFingerprint(value).Validate(); err == nil {
			t.Errorf("ProjectRuntimeRepairCandidateFingerprint(%q).Validate() error = nil", value)
		}
	}
}

// TestProjectRuntimeRepairDisplayFactsRemainFixedAndBounded validates every human-visible candidate field.
func TestProjectRuntimeRepairDisplayFactsRemainFixedAndBounded(t *testing.T) {
	valid := runtimeRepairContractTestDisplayFacts(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairDisplayFacts.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ProjectRuntimeRepairDisplayFacts)
	}{
		{name: "empty command", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Command = "" }},
		{name: "different command", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Command = "forj dev --force" }},
		{name: "empty checkout", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Checkout = "" }},
		{name: "relative checkout", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Checkout = "orders" }},
		{name: "checkout whitespace", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Checkout += " " }},
		{name: "checkout control", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Checkout += "\n" }},
		{
			name: "checkout over bound",
			mutate: func(facts *ProjectRuntimeRepairDisplayFacts) {
				facts.Checkout = string(filepath.Separator) + strings.Repeat("a", maximumRegistrationPathBytes)
			},
		},
		{name: "invalid endpoint", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "not-an-endpoint" }},
		{name: "non-loopback endpoint", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "192.0.2.10:3000" }},
		{name: "IPv6 endpoint", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "[::1]:3000" }},
		{name: "mapped endpoint", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "[::ffff:127.0.0.1]:3000" }},
		{name: "zero port", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "127.0.0.1:0" }},
		{name: "noncanonical endpoint", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.Endpoint = "127.0.0.1:03000" }},
		{name: "zero root PID", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.RootPID = 0 }},
		{name: "zero members", mutate: func(facts *ProjectRuntimeRepairDisplayFacts) { facts.MemberCount = 0 }},
		{
			name: "members over bound",
			mutate: func(facts *ProjectRuntimeRepairDisplayFacts) {
				facts.MemberCount = maximumProjectRuntimeMemberCount + 1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("ProjectRuntimeRepairDisplayFacts.Validate(%#v) error = nil", candidate)
			}
		})
	}
}

// TestProjectRuntimeRepairDisplayFactsAllowsCheckoutOwnedListener keeps the stale-app fallback visible without exposing native command text.
func TestProjectRuntimeRepairDisplayFactsAllowsCheckoutOwnedListener(t *testing.T) {
	facts := runtimeRepairContractTestDisplayFacts(t)
	facts.Command = projectRuntimeRepairProjectListener
	if err := facts.Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairDisplayFacts.Validate(project listener) error = %v", err)
	}
}

// TestProjectRuntimeRepairInspectionRequiresExactlyOneTaggedShape protects the client from ambiguous result combinations.
func TestProjectRuntimeRepairInspectionRequiresExactlyOneTaggedShape(t *testing.T) {
	confirmable := runtimeRepairContractTestConfirmable(t)
	if err := (ProjectRuntimeRepairInspection{
		ProjectID:   "project-orders",
		Disposition: ProjectRuntimeRepairInspectionConfirmable,
		Confirmable: &confirmable,
	}).Validate(); err != nil {
		t.Fatalf("confirmable inspection Validate() error = %v", err)
	}
	for _, reason := range []ProjectRuntimeRepairNotActionableReason{
		ProjectRuntimeRepairReasonNone,
		ProjectRuntimeRepairReasonAmbiguous,
		ProjectRuntimeRepairReasonForeign,
		ProjectRuntimeRepairReasonUnreadable,
	} {
		if err := (ProjectRuntimeRepairInspection{
			ProjectID:   "project-orders",
			Disposition: ProjectRuntimeRepairInspectionNotActionable,
			Reason:      reason,
		}).Validate(); err != nil {
			t.Errorf("not-actionable inspection with reason %q Validate() error = %v", reason, err)
		}
	}
	if err := (ProjectRuntimeRepairInspection{
		ProjectID:   "project-orders",
		Disposition: ProjectRuntimeRepairInspectionUnsupported,
	}).Validate(); err != nil {
		t.Fatalf("unsupported inspection Validate() error = %v", err)
	}

	for _, test := range []struct {
		name       string
		inspection ProjectRuntimeRepairInspection
	}{
		{
			name: "invalid project",
			inspection: ProjectRuntimeRepairInspection{
				Disposition: ProjectRuntimeRepairInspectionUnsupported,
			},
		},
		{
			name: "unknown disposition",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: "maybe",
			},
		},
		{
			name: "confirmable without details",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionConfirmable,
			},
		},
		{
			name: "confirmable with reason",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionConfirmable,
				Confirmable: &confirmable,
				Reason:      ProjectRuntimeRepairReasonAmbiguous,
			},
		},
		{
			name: "non-actionable with details",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionNotActionable,
				Confirmable: &confirmable,
				Reason:      ProjectRuntimeRepairReasonNone,
			},
		},
		{
			name: "non-actionable without reason",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionNotActionable,
			},
		},
		{
			name: "non-actionable with unknown reason",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionNotActionable,
				Reason:      "different",
			},
		},
		{
			name: "unsupported with details",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionUnsupported,
				Confirmable: &confirmable,
			},
		},
		{
			name: "unsupported with reason",
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionUnsupported,
				Reason:      ProjectRuntimeRepairReasonUnreadable,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.inspection.Validate(); err == nil {
				t.Fatalf("ProjectRuntimeRepairInspection.Validate(%#v) error = nil", test.inspection)
			}
		})
	}
}

// TestProjectRuntimeRepairConfirmableRequiresAUTCExpiry validates the temporal fence using the same zero-offset rule as domain snapshots.
func TestProjectRuntimeRepairConfirmableRequiresAUTCExpiry(t *testing.T) {
	valid := runtimeRepairContractTestConfirmable(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairConfirmable.Validate() error = %v", err)
	}
	zeroOffset := valid
	zeroOffset.ExpiresAt = time.Date(2026, time.July, 20, 12, 5, 0, 0, time.FixedZone("zero-offset", 0))
	if err := zeroOffset.Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairConfirmable.Validate(zero offset) error = %v", err)
	}
	for _, expiry := range []time.Time{
		{},
		time.Date(2026, time.July, 20, 13, 5, 0, 0, time.FixedZone("plus-one", 60*60)),
	} {
		candidate := valid
		candidate.ExpiresAt = expiry
		if err := candidate.Validate(); err == nil {
			t.Errorf("ProjectRuntimeRepairConfirmable.Validate() accepted expiry %#v", expiry)
		}
	}
}

// TestProjectRuntimeRepairInspectionJSONContainsOnlyReviewedDisplayFacts locks out native evidence and execution authority.
func TestProjectRuntimeRepairInspectionJSONContainsOnlyReviewedDisplayFacts(t *testing.T) {
	inspectionID := strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)
	fingerprint := strings.Repeat("b", projectRuntimeRepairOpaqueHexLength)
	expiresAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	confirmable := ProjectRuntimeRepairConfirmable{
		Candidate: ProjectRuntimeRepairDisplayFacts{
			Command:     projectRuntimeRepairCommand,
			Checkout:    "/workspace/orders",
			Endpoint:    "127.0.0.1:3000",
			RootPID:     321,
			MemberCount: 4,
		},
		InspectionID:         ProjectRuntimeRepairInspectionID(inspectionID),
		CandidateFingerprint: ProjectRuntimeRepairCandidateFingerprint(fingerprint),
		ExpiresAt:            expiresAt,
	}
	payload, err := json.Marshal(projectRuntimeRepairInspectionResponse{Inspection: ProjectRuntimeRepairInspection{
		ProjectID:   "project-orders",
		Disposition: ProjectRuntimeRepairInspectionConfirmable,
		Confirmable: &confirmable,
	}})
	if err != nil {
		t.Fatalf("json.Marshal(confirmable inspection) error = %v", err)
	}
	want := `{"inspection":{"project_id":"project-orders","disposition":"confirmable","confirmable":{"candidate":{"command":"forj dev","checkout":"/workspace/orders","endpoint":"127.0.0.1:3000","root_pid":321,"member_count":4},"inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `","expires_at":"2026-07-20T12:00:00Z"}}}`
	if string(payload) != want {
		t.Fatalf("confirmable inspection JSON = %s, want %s", payload, want)
	}

	for _, test := range []struct {
		inspection ProjectRuntimeRepairInspection
		want       string
	}{
		{
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionNotActionable,
				Reason:      ProjectRuntimeRepairReasonAmbiguous,
			},
			want: `{"inspection":{"project_id":"project-orders","disposition":"not_actionable","reason":"ambiguous"}}`,
		},
		{
			inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionUnsupported,
			},
			want: `{"inspection":{"project_id":"project-orders","disposition":"unsupported"}}`,
		},
	} {
		payload, err := json.Marshal(projectRuntimeRepairInspectionResponse{Inspection: test.inspection})
		if err != nil {
			t.Fatalf("json.Marshal(%s inspection) error = %v", test.inspection.Disposition, err)
		}
		if string(payload) != test.want {
			t.Errorf("%s inspection JSON = %s, want %s", test.inspection.Disposition, payload, test.want)
		}
	}
}

// TestProjectRuntimeRepairConfirmationRequiresRetryableRouteFreeProject verifies repair results remain retryable and route-free.
func TestProjectRuntimeRepairConfirmationRequiresRetryableRouteFreeProject(t *testing.T) {
	valid := runtimeRepairContractTestConfirmation(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("ProjectRuntimeRepairConfirmation.Validate() error = %v", err)
	}
	for _, state := range []domain.ProjectState{domain.ProjectFailed, domain.ProjectUnavailable} {
		retryable := valid
		retryable.Project.State = state
		if err := retryable.Validate(); err != nil {
			t.Errorf("ProjectRuntimeRepairConfirmation.Validate(%q) error = %v", state, err)
		}
	}

	invalidProject := valid
	invalidProject.Project.Path = ""
	activeProject := valid
	activeProject.Project.State = domain.ProjectReady
	activeApp := valid
	activeApp.Project.Apps = []domain.AppSnapshot{{ID: "app-orders", Name: "API", State: domain.EntityReady, Active: true}}
	activeService := valid
	activeService.Project.Services = []domain.ServiceSnapshot{{ID: "db-orders", Name: "Database", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected}}
	publicResource := valid
	publicResource.Project.Resources = []domain.ResourceSnapshot{{ID: "api", Name: "API", Kind: "app-http", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app-orders"}, URL: "http://orders.test"}}
	zeroRevision := valid
	zeroRevision.Revision = 0
	overflowRevision := valid
	overflowRevision.Revision = domain.MaximumSequence + 1
	for _, confirmation := range []ProjectRuntimeRepairConfirmation{
		invalidProject,
		activeProject,
		activeApp,
		activeService,
		publicResource,
		zeroRevision,
		overflowRevision,
	} {
		if err := confirmation.Validate(); err == nil {
			t.Errorf("ProjectRuntimeRepairConfirmation.Validate(%#v) error = nil", confirmation)
		}
	}
}

// TestProjectRuntimeRepairResultsCorrelateToTheSelectedProject prevents cross-project response confusion.
func TestProjectRuntimeRepairResultsCorrelateToTheSelectedProject(t *testing.T) {
	inspectRequest := InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}
	inspection := ProjectRuntimeRepairInspection{
		ProjectID:   inspectRequest.ProjectID,
		Disposition: ProjectRuntimeRepairInspectionUnsupported,
	}
	if err := validateProjectRuntimeRepairInspectionCorrelation(inspectRequest, inspection); err != nil {
		t.Fatalf("validateProjectRuntimeRepairInspectionCorrelation() error = %v", err)
	}
	inspection.ProjectID = "project-other"
	if err := validateProjectRuntimeRepairInspectionCorrelation(inspectRequest, inspection); err == nil {
		t.Fatal("validateProjectRuntimeRepairInspectionCorrelation(mismatch) error = nil")
	}

	confirmation := runtimeRepairContractTestConfirmation(t)
	confirmRequest := ConfirmProjectRuntimeRepairRequest{ProjectID: confirmation.Project.ID}
	if err := validateProjectRuntimeRepairConfirmationCorrelation(confirmRequest, confirmation); err != nil {
		t.Fatalf("validateProjectRuntimeRepairConfirmationCorrelation() error = %v", err)
	}
	confirmRequest.ProjectID = "project-other"
	if err := validateProjectRuntimeRepairConfirmationCorrelation(confirmRequest, confirmation); err == nil {
		t.Fatal("validateProjectRuntimeRepairConfirmationCorrelation(mismatch) error = nil")
	}
}

// runtimeRepairContractTestDisplayFacts returns one portable, bounded candidate projection.
func runtimeRepairContractTestDisplayFacts(t *testing.T) ProjectRuntimeRepairDisplayFacts {
	t.Helper()
	return ProjectRuntimeRepairDisplayFacts{
		Command:     projectRuntimeRepairCommand,
		Checkout:    filepath.Join(t.TempDir(), "orders"),
		Endpoint:    "127.0.0.1:3000",
		RootPID:     321,
		MemberCount: 4,
	}
}

// runtimeRepairContractTestConfirmable returns one complete short-lived selection.
func runtimeRepairContractTestConfirmable(t *testing.T) ProjectRuntimeRepairConfirmable {
	t.Helper()
	return ProjectRuntimeRepairConfirmable{
		Candidate:            runtimeRepairContractTestDisplayFacts(t),
		InspectionID:         ProjectRuntimeRepairInspectionID(strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)),
		CandidateFingerprint: ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", projectRuntimeRepairOpaqueHexLength)),
		ExpiresAt:            time.Date(2026, time.July, 20, 12, 5, 0, 0, time.UTC),
	}
}

// runtimeRepairContractTestConfirmation returns one valid authoritative stopped projection.
func runtimeRepairContractTestConfirmation(t *testing.T) ProjectRuntimeRepairConfirmation {
	t.Helper()
	return ProjectRuntimeRepairConfirmation{
		Project: domain.ProjectSnapshot{
			ID:        "project-orders",
			Name:      "Orders API",
			Path:      filepath.Join(t.TempDir(), "orders"),
			Slug:      "orders-api",
			State:     domain.ProjectStopped,
			UpdatedAt: time.Date(2026, time.July, 20, 12, 6, 0, 0, time.UTC),
			Apps:      []domain.AppSnapshot{},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 43,
	}
}
