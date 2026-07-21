package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// projectUnattributedRuntimeTestStore retains one detached no-session boundary and can inject post-inspection drift.
type projectUnattributedRuntimeTestStore struct {
	boundary      state.UnattributedProjectRuntimeInspectionBoundary
	boundaryErr   error
	boundaryCalls int
	onBoundary    func(int, *state.UnattributedProjectRuntimeInspectionBoundary)
}

// UnattributedProjectRuntimeInspectionBoundary returns a fresh boundary snapshot for every coordinator read.
func (store *projectUnattributedRuntimeTestStore) UnattributedProjectRuntimeInspectionBoundary(
	_ context.Context,
	_ domain.ProjectID,
) (state.UnattributedProjectRuntimeInspectionBoundary, error) {
	store.boundaryCalls++
	if store.boundaryErr != nil {
		return state.UnattributedProjectRuntimeInspectionBoundary{}, store.boundaryErr
	}
	boundary := cloneProjectUnattributedRuntimeTestBoundary(store.boundary)
	if store.onBoundary != nil {
		store.onBoundary(store.boundaryCalls, &boundary)
		store.boundary = cloneProjectUnattributedRuntimeTestBoundary(boundary)
	}
	return boundary, nil
}

// projectUnattributedRuntimeTestRepairer returns fixed native states and records every attempted signal.
type projectUnattributedRuntimeTestRepairer struct {
	inspection        projectprocess.UnattributedRuntimeInspection
	inspectionErr     error
	confirmation      projectprocess.RuntimeRepairConfirmation
	confirmationErr   error
	inspectTargets    []projectprocess.RuntimeRepairTarget
	confirmCandidates []projectprocess.UnattributedRuntimeCandidate
}

// Inspect records the daemon-derived target before returning the configured no-session state.
func (repairer *projectUnattributedRuntimeTestRepairer) Inspect(
	_ context.Context,
	target projectprocess.RuntimeRepairTarget,
) (projectprocess.UnattributedRuntimeInspection, error) {
	repairer.inspectTargets = append(repairer.inspectTargets, target)
	return repairer.inspection, repairer.inspectionErr
}

// Confirm records the opaque candidate and returns the configured signal and settlement result.
func (repairer *projectUnattributedRuntimeTestRepairer) Confirm(
	_ context.Context,
	candidate projectprocess.UnattributedRuntimeCandidate,
) (projectprocess.RuntimeRepairConfirmation, error) {
	repairer.confirmCandidates = append(repairer.confirmCandidates, candidate.Clone())
	return repairer.confirmation, repairer.confirmationErr
}

// projectUnattributedRuntimeTestFixture owns deterministic no-session coordinator seams.
type projectUnattributedRuntimeTestFixture struct {
	coordinator *UnattributedProjectRuntimeCoordinator
	store       *projectUnattributedRuntimeTestStore
	repairer    *projectUnattributedRuntimeTestRepairer
	boundary    state.UnattributedProjectRuntimeInspectionBoundary
	target      projectprocess.RuntimeRepairTarget
	caller      ProjectRuntimeRepairCaller
}

// TestUnattributedProjectRuntimeInspectMapsNativeStates proves only exact actionable evidence creates a plan.
func TestUnattributedProjectRuntimeInspectMapsNativeStates(t *testing.T) {
	tests := []struct {
		name        string
		state       projectprocess.RuntimeRepairInspectionState
		diagnostic  projectprocess.RuntimeRepairDiagnostic
		disposition UnattributedProjectRuntimeInspectionDisposition
		reason      ProjectRuntimeRepairNotActionableReason
		actionable  bool
	}{
		{name: "missing", state: projectprocess.RuntimeRepairInspectionMissing, diagnostic: projectprocess.RuntimeRepairDiagnosticListenerMissing, disposition: UnattributedProjectRuntimeInspectionNotActionable, reason: ProjectRuntimeRepairReasonNone},
		{name: "ambiguous", state: projectprocess.RuntimeRepairInspectionAmbiguous, diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateAmbiguous, disposition: UnattributedProjectRuntimeInspectionNotActionable, reason: ProjectRuntimeRepairReasonAmbiguous},
		{name: "foreign", state: projectprocess.RuntimeRepairInspectionForeign, diagnostic: projectprocess.RuntimeRepairDiagnosticForeignOwner, disposition: UnattributedProjectRuntimeInspectionNotActionable, reason: ProjectRuntimeRepairReasonForeign},
		{name: "unreadable", state: projectprocess.RuntimeRepairInspectionUnreadable, diagnostic: projectprocess.RuntimeRepairDiagnosticNativeUnreadable, disposition: UnattributedProjectRuntimeInspectionNotActionable, reason: ProjectRuntimeRepairReasonUnreadable},
		{name: "unsupported", state: projectprocess.RuntimeRepairInspectionUnsupported, diagnostic: projectprocess.RuntimeRepairDiagnosticPlatformUnsupported, disposition: UnattributedProjectRuntimeInspectionUnsupported},
		{name: "actionable", state: projectprocess.RuntimeRepairInspectionActionable, diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateExact, disposition: UnattributedProjectRuntimeInspectionConfirmable, actionable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnattributedRuntimeTestFixture(t)
			fixture.repairer.inspection = projectprocess.UnattributedRuntimeInspection{State: test.state, Diagnostic: test.diagnostic}
			if test.actionable {
				fixture.repairer.inspection.Candidate = projectUnattributedRuntimeTestCandidate(fixture.target)
			}

			inspection, err := fixture.coordinator.Inspect(t.Context(), projectUnattributedRuntimeInspectRequest(fixture))
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if err := inspection.Validate(); err != nil {
				t.Fatalf("inspection.Validate() error = %v", err)
			}
			if inspection.Disposition != test.disposition || inspection.Reason != test.reason {
				t.Fatalf("Inspect() = %#v, want disposition %q reason %q", inspection, test.disposition, test.reason)
			}
			if !test.actionable {
				if inspection.Confirmable != nil || len(fixture.coordinator.plans) != 0 {
					t.Fatalf("non-actionable inspection retained a plan: %#v", inspection)
				}
				return
			}
			confirmable := inspection.Confirmable
			if confirmable == nil || confirmable.Fingerprint != ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)) ||
				confirmable.Display.Endpoint != fixture.target.Endpoint || confirmable.Display.CheckoutRoot != fixture.target.CheckoutRoot {
				t.Fatalf("actionable inspection = %#v", inspection)
			}
			if len(fixture.coordinator.plans) != 1 {
				t.Fatalf("retained plan count = %d, want 1", len(fixture.coordinator.plans))
			}
		})
	}
}

// TestUnattributedProjectRuntimeConfirmConsumesBindingMismatches proves no caller-controlled mismatch reaches native confirmation.
func TestUnattributedProjectRuntimeConfirmConsumesBindingMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UnattributedProjectRuntimeConfirmRequest)
	}{
		{name: "user", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) { request.Caller.UserID = "user-502" }},
		{name: "process", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) { request.Caller.ProcessID++ }},
		{name: "role", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) { request.Caller.Role = rpc.RoleDesktop }},
		{name: "project", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) { request.ProjectID = "project-other" }},
		{name: "fingerprint", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) {
			request.Fingerprint = ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", projectRuntimeRepairOpaqueHexLength))
		}},
		{name: "malformed fingerprint", mutate: func(request *UnattributedProjectRuntimeConfirmRequest) { request.Fingerprint = "not-hex" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnattributedRuntimeTestFixture(t)
			confirmable := inspectActionableProjectUnattributedRuntime(t, fixture)
			request := projectUnattributedRuntimeConfirmRequest(fixture, confirmable)
			test.mutate(&request)

			_, err := fixture.coordinator.Confirm(t.Context(), request)
			var mismatch *ProjectRuntimeRepairPlanMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("Confirm(mismatch) error = %v", err)
			}
			if len(fixture.repairer.confirmCandidates) != 0 || fixture.store.boundaryCalls != 1 {
				t.Fatalf("mismatch effects = boundary %d native %d", fixture.store.boundaryCalls, len(fixture.repairer.confirmCandidates))
			}

			_, err = fixture.coordinator.Confirm(t.Context(), projectUnattributedRuntimeConfirmRequest(fixture, confirmable))
			var missing *ProjectRuntimeRepairPlanNotFoundError
			if !errors.As(err, &missing) || len(fixture.repairer.confirmCandidates) != 0 {
				t.Fatalf("Confirm(after mismatch) error = %v, native calls = %d", err, len(fixture.repairer.confirmCandidates))
			}
		})
	}
}

// TestUnattributedProjectRuntimeConfirmRejectsDurableDrift proves durable authority is reread before any native signal.
func TestUnattributedProjectRuntimeConfirmRejectsDurableDrift(t *testing.T) {
	fixture := newProjectUnattributedRuntimeTestFixture(t)
	confirmable := inspectActionableProjectUnattributedRuntime(t, fixture)
	fixture.store.boundary.Project.Project.Favorite = !fixture.store.boundary.Project.Project.Favorite

	_, err := fixture.coordinator.Confirm(t.Context(), projectUnattributedRuntimeConfirmRequest(fixture, confirmable))
	var drift *ProjectRuntimeRepairDurableDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Confirm(durable drift) error = %v", err)
	}
	if len(fixture.repairer.confirmCandidates) != 0 || len(fixture.coordinator.plans) != 0 {
		t.Fatalf("durable drift effects = native %d plans %d", len(fixture.repairer.confirmCandidates), len(fixture.coordinator.plans))
	}
}

// TestUnattributedProjectRuntimeConfirmReturnsUnchangedProject proves settled native cleanup has no durable completion authority.
func TestUnattributedProjectRuntimeConfirmReturnsUnchangedProject(t *testing.T) {
	fixture := newProjectUnattributedRuntimeTestFixture(t)
	confirmable := inspectActionableProjectUnattributedRuntime(t, fixture)

	confirmation, err := fixture.coordinator.Confirm(t.Context(), projectUnattributedRuntimeConfirmRequest(fixture, confirmable))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if !reflect.DeepEqual(confirmation.Project, fixture.boundary.Project) {
		t.Fatalf("Confirm() project = %#v, want %#v", confirmation.Project, fixture.boundary.Project)
	}
	if confirmation.Project.Project.ID != fixture.boundary.Project.Project.ID || confirmation.Project.Project.State != domain.ProjectStopped ||
		confirmation.Project.Revision != fixture.boundary.Project.Revision {
		t.Fatalf("Confirm() = %#v, want unchanged route-free project", confirmation)
	}
	if fixture.store.boundaryCalls != 3 || len(fixture.repairer.confirmCandidates) != 1 || len(fixture.coordinator.plans) != 0 {
		t.Fatalf("settled effects = boundary %d native %d plans %d", fixture.store.boundaryCalls, len(fixture.repairer.confirmCandidates), len(fixture.coordinator.plans))
	}
}

// TestUnattributedProjectRuntimeConfirmRejectsPostSettlementDrift proves a final durable read fences native cleanup results.
func TestUnattributedProjectRuntimeConfirmRejectsPostSettlementDrift(t *testing.T) {
	fixture := newProjectUnattributedRuntimeTestFixture(t)
	confirmable := inspectActionableProjectUnattributedRuntime(t, fixture)
	fixture.store.onBoundary = func(call int, boundary *state.UnattributedProjectRuntimeInspectionBoundary) {
		if call == 3 {
			boundary.NetworkRevision++
		}
	}

	_, err := fixture.coordinator.Confirm(t.Context(), projectUnattributedRuntimeConfirmRequest(fixture, confirmable))
	var drift *ProjectRuntimeRepairDurableDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("Confirm(post-settlement drift) error = %v", err)
	}
	if len(fixture.repairer.confirmCandidates) != 1 || len(fixture.coordinator.plans) != 0 {
		t.Fatalf("post-settlement drift effects = native %d plans %d", len(fixture.repairer.confirmCandidates), len(fixture.coordinator.plans))
	}
}

// TestProjectRuntimeRepairCoordinatorFallsBackToUnattributed proves the existing repair surface reaches no-session evidence without a second client protocol.
func TestProjectRuntimeRepairCoordinatorFallsBackToUnattributed(t *testing.T) {
	retained := newProjectRuntimeRepairTestFixture(t)
	unattributed := newProjectUnattributedRuntimeTestFixture(t)
	retained.coordinator.unattributed = unattributed.coordinator

	inspection, err := retained.coordinator.Inspect(t.Context(), ProjectRuntimeRepairInspectRequest{
		Caller:    unattributed.caller,
		ProjectID: unattributed.boundary.Project.Project.ID,
	})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.Disposition != ProjectRuntimeRepairInspectionConfirmable || inspection.Confirmable == nil {
		t.Fatalf("Inspect() = %#v, want confirmable fallback", inspection)
	}

	confirmed, err := retained.coordinator.Confirm(t.Context(), ProjectRuntimeRepairConfirmRequest{
		Caller:       unattributed.caller,
		ProjectID:    inspection.ProjectID,
		InspectionID: inspection.Confirmable.InspectionID,
		Fingerprint:  inspection.Confirmable.Fingerprint,
	})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmed.Project.ID != inspection.ProjectID || confirmed.Project.State != domain.ProjectStopped || confirmed.Revision != unattributed.boundary.Project.Revision {
		t.Fatalf("Confirm() = %#v, want unchanged no-session project", confirmed)
	}
	if retained.store.boundaryCalls != 0 || len(retained.repairer.confirmCandidates) != 0 || len(unattributed.repairer.confirmCandidates) != 1 {
		t.Fatalf("fallback effects = retained boundary %d retained native %d unattributed native %d", retained.store.boundaryCalls, len(retained.repairer.confirmCandidates), len(unattributed.repairer.confirmCandidates))
	}
}

// newProjectUnattributedRuntimeTestFixture constructs a valid route-free project from the retained-runtime test boundary.
func newProjectUnattributedRuntimeTestFixture(t *testing.T) *projectUnattributedRuntimeTestFixture {
	t.Helper()
	retained := newProjectRuntimeRepairTestFixture(t)
	project := retained.boundary.Project
	project.Project.State = domain.ProjectStopped
	boundary := state.UnattributedProjectRuntimeInspectionBoundary{
		Project:                project,
		NetworkRevision:        retained.boundary.NetworkRevision,
		NetworkUpdatedAt:       retained.boundary.NetworkUpdatedAt,
		PrimaryLease:           retained.boundary.PrimaryLease,
		PrimaryLeaseGeneration: retained.boundary.PrimaryLeaseGeneration,
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("unattributed test boundary Validate() error = %v", err)
	}
	target := retained.target
	store := &projectUnattributedRuntimeTestStore{boundary: cloneProjectUnattributedRuntimeTestBoundary(boundary)}
	repairer := &projectUnattributedRuntimeTestRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{
			State:      projectprocess.RuntimeRepairInspectionActionable,
			Diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateExact,
			Candidate:  projectUnattributedRuntimeTestCandidate(target),
		},
		confirmation: projectprocess.RuntimeRepairConfirmation{State: projectprocess.RuntimeRepairConfirmationSettled, Signaled: true},
	}
	coordinator := newUnattributedProjectRuntimeCoordinator(
		store,
		retained.discoverer,
		repairer,
		func() time.Time { return *retained.now },
		&projectRuntimeRepairTestEntropy{},
		defaultProjectRuntimeRepairPlanTTL,
		defaultProjectRuntimeRepairMaximumPlans,
	)
	return &projectUnattributedRuntimeTestFixture{
		coordinator: coordinator,
		store:       store,
		repairer:    repairer,
		boundary:    cloneProjectUnattributedRuntimeTestBoundary(boundary),
		target:      target,
		caller:      retained.caller,
	}
}

// projectUnattributedRuntimeTestCandidate constructs a public candidate for the injected repairer seam.
func projectUnattributedRuntimeTestCandidate(target projectprocess.RuntimeRepairTarget) *projectprocess.UnattributedRuntimeCandidate {
	return &projectprocess.UnattributedRuntimeCandidate{
		Fingerprint: strings.Repeat("a", projectRuntimeRepairOpaqueHexLength),
		Display: projectprocess.RuntimeRepairDisplay{
			RootPID:      4102,
			Command:      "forj dev",
			CheckoutRoot: target.CheckoutRoot,
			Endpoint:     target.Endpoint,
			ProcessCount: 3,
		},
	}
}

// projectUnattributedRuntimeInspectRequest selects the fixture project for its authenticated caller.
func projectUnattributedRuntimeInspectRequest(fixture *projectUnattributedRuntimeTestFixture) UnattributedProjectRuntimeInspectRequest {
	return UnattributedProjectRuntimeInspectRequest{Caller: fixture.caller, ProjectID: fixture.boundary.Project.Project.ID}
}

// projectUnattributedRuntimeConfirmRequest echoes the fixture's opaque confirmation selection.
func projectUnattributedRuntimeConfirmRequest(
	fixture *projectUnattributedRuntimeTestFixture,
	confirmable *ProjectRuntimeRepairConfirmable,
) UnattributedProjectRuntimeConfirmRequest {
	return UnattributedProjectRuntimeConfirmRequest{
		Caller:       fixture.caller,
		ProjectID:    fixture.boundary.Project.Project.ID,
		InspectionID: confirmable.InspectionID,
		Fingerprint:  confirmable.Fingerprint,
	}
}

// inspectActionableProjectUnattributedRuntime returns the one-use selection from a successful inspection.
func inspectActionableProjectUnattributedRuntime(
	t *testing.T,
	fixture *projectUnattributedRuntimeTestFixture,
) *ProjectRuntimeRepairConfirmable {
	t.Helper()
	inspection, err := fixture.coordinator.Inspect(t.Context(), projectUnattributedRuntimeInspectRequest(fixture))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.Disposition != UnattributedProjectRuntimeInspectionConfirmable || inspection.Confirmable == nil {
		t.Fatalf("Inspect() = %#v, want confirmable", inspection)
	}
	return inspection.Confirmable
}

// cloneProjectUnattributedRuntimeTestBoundary isolates project slices for each simulated durable read.
func cloneProjectUnattributedRuntimeTestBoundary(
	boundary state.UnattributedProjectRuntimeInspectionBoundary,
) state.UnattributedProjectRuntimeInspectionBoundary {
	clone := boundary
	clone.Project.Project.Apps = make([]domain.AppSnapshot, len(boundary.Project.Project.Apps))
	copy(clone.Project.Project.Apps, boundary.Project.Project.Apps)
	clone.Project.Project.Services = make([]domain.ServiceSnapshot, len(boundary.Project.Project.Services))
	copy(clone.Project.Project.Services, boundary.Project.Project.Services)
	clone.Project.Project.Resources = make([]domain.ResourceSnapshot, len(boundary.Project.Project.Resources))
	copy(clone.Project.Project.Resources, boundary.Project.Project.Resources)
	return clone
}
