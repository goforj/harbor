package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// projectRuntimeRepairTestStore retains one mutable durable boundary and records terminal mutations.
type projectRuntimeRepairTestStore struct {
	boundary        state.RetainedProjectRuntimeRepairBoundary
	boundaryErr     error
	boundaryCalls   int
	completionCalls []state.CompleteRetainedProjectRuntimeRepairRequest
	completionErr   error
	events          *[]string
}

// RetainedProjectRuntimeRepairBoundary returns a detached copy so test drift cannot mutate an already-retained plan through pointers.
func (store *projectRuntimeRepairTestStore) RetainedProjectRuntimeRepairBoundary(
	_ context.Context,
	_ domain.ProjectID,
) (state.RetainedProjectRuntimeRepairBoundary, error) {
	store.boundaryCalls++
	appendProjectRuntimeRepairTestEvent(store.events, "boundary")
	if store.boundaryErr != nil {
		return state.RetainedProjectRuntimeRepairBoundary{}, store.boundaryErr
	}
	return cloneProjectRuntimeRepairTestBoundary(store.boundary), nil
}

// CompleteRetainedProjectRuntimeRepair records exact fences and returns the stopped projection a real store would commit.
func (store *projectRuntimeRepairTestStore) CompleteRetainedProjectRuntimeRepair(
	_ context.Context,
	request state.CompleteRetainedProjectRuntimeRepairRequest,
) (state.ProjectRecord, error) {
	store.completionCalls = append(store.completionCalls, request)
	appendProjectRuntimeRepairTestEvent(store.events, "complete")
	if store.completionErr != nil {
		return state.ProjectRecord{}, store.completionErr
	}
	project := cloneProjectRuntimeRepairTestBoundary(store.boundary).Project.Project
	project.State = domain.ProjectStopped
	project.UpdatedAt = request.At
	return state.ProjectRecord{Project: project, Revision: store.boundary.Project.Revision + 1}, nil
}

// projectRuntimeRepairTestDiscoverer returns one configurable default runtime while recording durable derivation inputs.
type projectRuntimeRepairTestDiscoverer struct {
	target    projectdiscovery.RuntimeTarget
	err       error
	paths     []string
	addresses []netip.Addr
	events    *[]string
}

// DiscoverDefaultRuntimeAtAddress records the checkout and lease address before returning the configured target.
func (discoverer *projectRuntimeRepairTestDiscoverer) DiscoverDefaultRuntimeAtAddress(
	_ context.Context,
	path string,
	address netip.Addr,
) (projectdiscovery.RuntimeTarget, error) {
	discoverer.paths = append(discoverer.paths, path)
	discoverer.addresses = append(discoverer.addresses, address)
	appendProjectRuntimeRepairTestEvent(discoverer.events, "discover")
	if discoverer.err != nil {
		return projectdiscovery.RuntimeTarget{}, discoverer.err
	}
	return discoverer.target, nil
}

// projectRuntimeRepairTestRepairer controls fixed native inspection and confirmation states.
type projectRuntimeRepairTestRepairer struct {
	inspection        projectprocess.RuntimeRepairInspection
	inspectionErr     error
	confirmation      projectprocess.RuntimeRepairConfirmation
	confirmationErr   error
	inspectTargets    []projectprocess.RuntimeRepairTarget
	confirmCandidates []projectprocess.RuntimeRepairCandidate
	events            *[]string
}

// Inspect records the daemon-derived target and returns the configured fixed native state.
func (repairer *projectRuntimeRepairTestRepairer) Inspect(
	_ context.Context,
	target projectprocess.RuntimeRepairTarget,
) (projectprocess.RuntimeRepairInspection, error) {
	repairer.inspectTargets = append(repairer.inspectTargets, target)
	appendProjectRuntimeRepairTestEvent(repairer.events, "inspect")
	return repairer.inspection, repairer.inspectionErr
}

// Confirm records the retained candidate and returns the configured signal/postcondition state.
func (repairer *projectRuntimeRepairTestRepairer) Confirm(
	_ context.Context,
	candidate projectprocess.RuntimeRepairCandidate,
) (projectprocess.RuntimeRepairConfirmation, error) {
	repairer.confirmCandidates = append(repairer.confirmCandidates, candidate.Clone())
	appendProjectRuntimeRepairTestEvent(repairer.events, "confirm")
	return repairer.confirmation, repairer.confirmationErr
}

// projectRuntimeRepairTestEntropy emits a different repeated byte for every inspection identity read.
type projectRuntimeRepairTestEntropy struct {
	next byte
}

// Read fills one identity with a deterministic byte while retaining canonical random-ID shape.
func (entropy *projectRuntimeRepairTestEntropy) Read(buffer []byte) (int, error) {
	entropy.next++
	for index := range buffer {
		buffer[index] = entropy.next
	}
	return len(buffer), nil
}

// projectRuntimeRepairTestFixture owns deterministic durable, discovery, native, time, and entropy seams.
type projectRuntimeRepairTestFixture struct {
	coordinator *ProjectRuntimeRepairCoordinator
	store       *projectRuntimeRepairTestStore
	discoverer  *projectRuntimeRepairTestDiscoverer
	repairer    *projectRuntimeRepairTestRepairer
	boundary    state.RetainedProjectRuntimeRepairBoundary
	target      projectprocess.RuntimeRepairTarget
	caller      ProjectRuntimeRepairCaller
	now         *time.Time
	events      *[]string
}

// TestProjectRuntimeRepairInspectMapsNativeStates proves only actionable native evidence creates a receipt-free confirmation plan.
func TestProjectRuntimeRepairInspectMapsNativeStates(t *testing.T) {
	tests := []struct {
		name        string
		state       projectprocess.RuntimeRepairInspectionState
		diagnostic  projectprocess.RuntimeRepairDiagnostic
		disposition ProjectRuntimeRepairInspectionDisposition
		reason      ProjectRuntimeRepairNotActionableReason
		actionable  bool
	}{
		{name: "missing", state: projectprocess.RuntimeRepairInspectionMissing, diagnostic: projectprocess.RuntimeRepairDiagnosticListenerMissing, disposition: ProjectRuntimeRepairInspectionNotActionable, reason: ProjectRuntimeRepairReasonNone},
		{name: "ambiguous", state: projectprocess.RuntimeRepairInspectionAmbiguous, diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateAmbiguous, disposition: ProjectRuntimeRepairInspectionNotActionable, reason: ProjectRuntimeRepairReasonAmbiguous},
		{name: "foreign", state: projectprocess.RuntimeRepairInspectionForeign, diagnostic: projectprocess.RuntimeRepairDiagnosticForeignOwner, disposition: ProjectRuntimeRepairInspectionNotActionable, reason: ProjectRuntimeRepairReasonForeign},
		{name: "unreadable", state: projectprocess.RuntimeRepairInspectionUnreadable, diagnostic: projectprocess.RuntimeRepairDiagnosticNativeUnreadable, disposition: ProjectRuntimeRepairInspectionNotActionable, reason: ProjectRuntimeRepairReasonUnreadable},
		{name: "unsupported", state: projectprocess.RuntimeRepairInspectionUnsupported, diagnostic: projectprocess.RuntimeRepairDiagnosticPlatformUnsupported, disposition: ProjectRuntimeRepairInspectionUnsupported},
		{name: "actionable", state: projectprocess.RuntimeRepairInspectionActionable, diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateExact, disposition: ProjectRuntimeRepairInspectionConfirmable, actionable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectRuntimeRepairTestFixture(t)
			fixture.repairer.inspection = projectprocess.RuntimeRepairInspection{State: test.state, Diagnostic: test.diagnostic}
			if test.actionable {
				fixture.repairer.inspection.Candidate = projectRuntimeRepairTestCandidate(fixture.target)
			}

			inspection, err := fixture.coordinator.Inspect(t.Context(), projectRuntimeRepairInspectRequest(fixture))
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
					t.Fatalf("non-actionable Inspect() retained candidate details or a plan: %#v", inspection)
				}
				return
			}
			confirmable := inspection.Confirmable
			if confirmable == nil || len(confirmable.InspectionID) != projectRuntimeRepairOpaqueHexLength ||
				confirmable.Fingerprint != ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)) ||
				!confirmable.ExpiresAt.Equal(fixture.now.Add(defaultProjectRuntimeRepairPlanTTL)) {
				t.Fatalf("actionable Inspect() = %#v", inspection)
			}
			if confirmable.Display.CheckoutRoot != fixture.target.CheckoutRoot || confirmable.Display.Endpoint != fixture.target.Endpoint ||
				confirmable.Display.RootPID != 4102 || confirmable.Display.ProcessCount != 3 {
				t.Fatalf("safe display = %#v", confirmable.Display)
			}
			if len(fixture.coordinator.plans) != 1 {
				t.Fatalf("retained plan count = %d, want 1", len(fixture.coordinator.plans))
			}
		})
	}
}

// TestProjectRuntimeRepairInspectReplacesPriorProjectPlan proves a fresh inspection invalidates an older one for the project.
func TestProjectRuntimeRepairInspectReplacesPriorProjectPlan(t *testing.T) {
	fixture := newProjectRuntimeRepairTestFixture(t)
	first := inspectActionableProjectRuntimeRepair(t, fixture)
	second := inspectActionableProjectRuntimeRepair(t, fixture)
	if first.InspectionID == second.InspectionID || len(fixture.coordinator.plans) != 1 {
		t.Fatalf("replacement plans = first %q second %q count %d", first.InspectionID, second.InspectionID, len(fixture.coordinator.plans))
	}

	_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, first))
	var missing *ProjectRuntimeRepairPlanNotFoundError
	if !errors.As(err, &missing) || len(fixture.repairer.confirmCandidates) != 0 {
		t.Fatalf("Confirm(replaced plan) error = %v, native calls = %d", err, len(fixture.repairer.confirmCandidates))
	}
}

// TestProjectRuntimeRepairConfirmConsumesBindingMismatches proves caller, project, and fingerprint drift emit zero signals and invalidate the plan.
func TestProjectRuntimeRepairConfirmConsumesBindingMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectRuntimeRepairConfirmRequest)
	}{
		{name: "user", mutate: func(request *ProjectRuntimeRepairConfirmRequest) { request.Caller.UserID = "user-502" }},
		{name: "process", mutate: func(request *ProjectRuntimeRepairConfirmRequest) { request.Caller.ProcessID++ }},
		{name: "role", mutate: func(request *ProjectRuntimeRepairConfirmRequest) { request.Caller.Role = rpc.RoleDesktop }},
		{name: "project", mutate: func(request *ProjectRuntimeRepairConfirmRequest) { request.ProjectID = "project-other" }},
		{name: "fingerprint", mutate: func(request *ProjectRuntimeRepairConfirmRequest) {
			request.Fingerprint = ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", projectRuntimeRepairOpaqueHexLength))
		}},
		{name: "malformed fingerprint", mutate: func(request *ProjectRuntimeRepairConfirmRequest) {
			request.Fingerprint = "not-hex"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectRuntimeRepairTestFixture(t)
			confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
			request := projectRuntimeRepairConfirmRequest(fixture, confirmable)
			test.mutate(&request)

			_, err := fixture.coordinator.Confirm(t.Context(), request)
			var mismatch *ProjectRuntimeRepairPlanMismatchError
			if !errors.As(err, &mismatch) {
				t.Fatalf("Confirm(mismatch) error = %v", err)
			}
			if len(fixture.repairer.confirmCandidates) != 0 || len(fixture.store.completionCalls) != 0 || fixture.store.boundaryCalls != 1 {
				t.Fatalf("mismatch effects = boundary %d native %d complete %d", fixture.store.boundaryCalls, len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
			}

			_, err = fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
			var missing *ProjectRuntimeRepairPlanNotFoundError
			if !errors.As(err, &missing) || len(fixture.repairer.confirmCandidates) != 0 {
				t.Fatalf("Confirm(after mismatch) error = %v, native calls = %d", err, len(fixture.repairer.confirmCandidates))
			}
		})
	}
}

// TestProjectRuntimeRepairConfirmConsumesExpiredPlan proves expiry at the exact UTC deadline emits zero signals and requires reinspection.
func TestProjectRuntimeRepairConfirmConsumesExpiredPlan(t *testing.T) {
	fixture := newProjectRuntimeRepairTestFixture(t)
	confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
	*fixture.now = confirmable.ExpiresAt

	_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
	var expired *ProjectRuntimeRepairPlanExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("Confirm(expired) error = %v", err)
	}
	if fixture.store.boundaryCalls != 1 || len(fixture.repairer.confirmCandidates) != 0 || len(fixture.store.completionCalls) != 0 {
		t.Fatalf("expiry effects = boundary %d native %d complete %d", fixture.store.boundaryCalls, len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
	}

	_, err = fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
	var missing *ProjectRuntimeRepairPlanNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("Confirm(after expiry) error = %v", err)
	}
}

// TestProjectRuntimeRepairConfirmRejectsEveryDurableBoundaryClass proves every retained boundary fact participates in exact equality.
func TestProjectRuntimeRepairConfirmRejectsEveryDurableBoundaryClass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*state.RetainedProjectRuntimeRepairBoundary)
	}{
		{name: "project", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) {
			boundary.Project.Project.Favorite = !boundary.Project.Project.Favorite
		}},
		{name: "session identity", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) { boundary.SessionID = "session-other" }},
		{name: "session generation", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) { boundary.SessionGeneration++ }},
		{name: "session update", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) {
			boundary.SessionUpdatedAt = boundary.SessionUpdatedAt.Add(time.Second)
		}},
		{name: "recovery operation", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) {
			boundary.RecoveryOperation.Operation.Problem.Message = "A different retained runtime marker remains."
		}},
		{name: "network revision", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) { boundary.NetworkRevision++ }},
		{name: "network update", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) {
			boundary.NetworkUpdatedAt = boundary.NetworkUpdatedAt.Add(time.Second)
		}},
		{name: "primary lease", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) {
			boundary.PrimaryLease.Address = netip.MustParseAddr("127.77.0.11")
		}},
		{name: "primary lease generation", mutate: func(boundary *state.RetainedProjectRuntimeRepairBoundary) { boundary.PrimaryLeaseGeneration++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectRuntimeRepairTestFixture(t)
			confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
			test.mutate(&fixture.store.boundary)

			_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
			var drift *ProjectRuntimeRepairDurableDriftError
			if !errors.As(err, &drift) {
				t.Fatalf("Confirm(durable drift) error = %v", err)
			}
			if len(fixture.discoverer.paths) != 1 || len(fixture.repairer.confirmCandidates) != 0 || len(fixture.store.completionCalls) != 0 {
				t.Fatalf("durable drift effects = discovery %d native %d complete %d", len(fixture.discoverer.paths), len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
			}
		})
	}
}

// TestProjectRuntimeRepairConfirmClassifiesDurableReadFailure proves a consumed plan preserves its read cause while requiring reinspection.
func TestProjectRuntimeRepairConfirmClassifiesDurableReadFailure(t *testing.T) {
	fixture := newProjectRuntimeRepairTestFixture(t)
	confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
	fixture.store.boundaryErr = os.ErrNotExist

	_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
	var drift *ProjectRuntimeRepairDurableDriftError
	if !errors.As(err, &drift) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Confirm(durable read failure) error = %v", err)
	}
	if len(fixture.repairer.confirmCandidates) != 0 || len(fixture.store.completionCalls) != 0 {
		t.Fatalf("durable read failure effects = native %d complete %d", len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
	}
}

// TestProjectRuntimeRepairConfirmRejectsDiscoveryDriftAndFailure proves target changes and rediscovery failures emit zero signals.
func TestProjectRuntimeRepairConfirmRejectsDiscoveryDriftAndFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*projectRuntimeRepairTestFixture)
		cause  error
	}{
		{name: "target", mutate: func(fixture *projectRuntimeRepairTestFixture) {
			fixture.discoverer.target = projectRuntimeRepairTestDiscoveryTarget(t, fixture.boundary.PrimaryLease.Address, 3001)
		}},
		{name: "error", cause: os.ErrNotExist, mutate: func(fixture *projectRuntimeRepairTestFixture) { fixture.discoverer.err = os.ErrNotExist }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectRuntimeRepairTestFixture(t)
			confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
			test.mutate(fixture)

			_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
			var drift *ProjectRuntimeRepairDiscoveryDriftError
			if !errors.As(err, &drift) {
				t.Fatalf("Confirm(discovery drift) error = %v", err)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Confirm(discovery failure) error = %v, want cause %v", err, test.cause)
			}
			if len(fixture.repairer.confirmCandidates) != 0 || len(fixture.store.completionCalls) != 0 {
				t.Fatalf("discovery drift effects = native %d complete %d", len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
			}
		})
	}
}

// TestProjectRuntimeRepairConfirmRequiresSettledNativePostconditions proves drift and both failure signal phases cannot complete durable repair.
func TestProjectRuntimeRepairConfirmRequiresSettledNativePostconditions(t *testing.T) {
	preSignalErr := errors.New("native observation failed")
	tests := []struct {
		name         string
		confirmation projectprocess.RuntimeRepairConfirmation
		err          error
		drift        bool
		signaled     bool
	}{
		{name: "drifted", confirmation: projectprocess.RuntimeRepairConfirmation{State: projectprocess.RuntimeRepairConfirmationDrifted}, drift: true},
		{name: "failed before signal", confirmation: projectprocess.RuntimeRepairConfirmation{State: projectprocess.RuntimeRepairConfirmationFailed}, err: preSignalErr},
		{name: "failed after signal", confirmation: projectprocess.RuntimeRepairConfirmation{State: projectprocess.RuntimeRepairConfirmationFailed, Signaled: true}, err: projectprocess.ErrRuntimeRepairNotSettled, signaled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectRuntimeRepairTestFixture(t)
			confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
			fixture.repairer.confirmation = test.confirmation
			fixture.repairer.confirmationErr = test.err

			_, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
			if test.drift {
				var drift *ProjectRuntimeRepairNativeDriftError
				if !errors.As(err, &drift) {
					t.Fatalf("Confirm(native drift) error = %v", err)
				}
			} else {
				var failure *ProjectRuntimeRepairNativeFailureError
				if !errors.As(err, &failure) || failure.signaled != test.signaled || !errors.Is(err, test.err) {
					t.Fatalf("Confirm(native failure) error = %#v, want signaled %t cause %v", err, test.signaled, test.err)
				}
			}
			if len(fixture.repairer.confirmCandidates) != 1 || len(fixture.store.completionCalls) != 0 {
				t.Fatalf("native terminal effects = confirm %d complete %d", len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
			}
		})
	}
}

// TestProjectRuntimeRepairConfirmCompletesInFenceOrder proves settled native postconditions precede one exact durable completion and consume the plan.
func TestProjectRuntimeRepairConfirmCompletesInFenceOrder(t *testing.T) {
	fixture := newProjectRuntimeRepairTestFixture(t)
	confirmable := inspectActionableProjectRuntimeRepair(t, fixture)
	*fixture.events = nil
	*fixture.now = fixture.boundary.RecoveryOperation.Operation.RequestedAt.Add(-time.Hour)
	fixture.repairer.confirmation = projectprocess.RuntimeRepairConfirmation{
		State:    projectprocess.RuntimeRepairConfirmationSettled,
		Signaled: true,
	}

	completed, err := fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if completed.Project.ID != fixture.boundary.Project.Project.ID || completed.Project.State != domain.ProjectStopped ||
		completed.Revision != fixture.boundary.Project.Revision+1 {
		t.Fatalf("Confirm() = %#v", completed)
	}
	if want := []string{"boundary", "discover", "confirm", "complete"}; !reflect.DeepEqual(*fixture.events, want) {
		t.Fatalf("confirmation order = %#v, want %#v", *fixture.events, want)
	}
	if len(fixture.store.completionCalls) != 1 {
		t.Fatalf("completion calls = %d, want 1", len(fixture.store.completionCalls))
	}
	request := fixture.store.completionCalls[0]
	boundary := fixture.boundary
	if request.ProjectID != boundary.Project.Project.ID || request.ExpectedProjectRevision != boundary.Project.Revision ||
		request.SessionID != boundary.SessionID || request.ExpectedSessionGeneration != boundary.SessionGeneration ||
		!request.ExpectedSessionUpdatedAt.Equal(boundary.SessionUpdatedAt) ||
		request.ExpectedRecoveryOperationID != boundary.RecoveryOperation.Operation.ID ||
		request.ExpectedRecoveryOperationRevision != boundary.RecoveryOperation.Revision ||
		request.ExpectedNetworkRevision != boundary.NetworkRevision || !request.ExpectedNetworkUpdatedAt.Equal(boundary.NetworkUpdatedAt) ||
		request.ExpectedPrimaryLease != boundary.PrimaryLease || request.ExpectedPrimaryLeaseGeneration != boundary.PrimaryLeaseGeneration {
		t.Fatalf("completion fences = %#v, want boundary %#v", request, boundary)
	}
	_, offset := request.At.Zone()
	if !request.At.Equal(boundary.NetworkUpdatedAt) || offset != 0 {
		t.Fatalf("completion time = %s, want UTC fence %s", request.At, boundary.NetworkUpdatedAt)
	}
	if len(fixture.repairer.confirmCandidates) != 1 || fixture.repairer.confirmCandidates[0].Fingerprint != string(confirmable.Fingerprint) {
		t.Fatalf("native candidate calls = %#v", fixture.repairer.confirmCandidates)
	}

	_, err = fixture.coordinator.Confirm(t.Context(), projectRuntimeRepairConfirmRequest(fixture, confirmable))
	var missing *ProjectRuntimeRepairPlanNotFoundError
	if !errors.As(err, &missing) || len(fixture.repairer.confirmCandidates) != 1 || len(fixture.store.completionCalls) != 1 {
		t.Fatalf("second Confirm() error = %v, native %d complete %d", err, len(fixture.repairer.confirmCandidates), len(fixture.store.completionCalls))
	}
}

// TestProjectRuntimeRepairInspectHonorsPlanCapacity proves a valid plan for another project is never evicted beyond the configured bound.
func TestProjectRuntimeRepairInspectHonorsPlanCapacity(t *testing.T) {
	fixture := newProjectRuntimeRepairTestFixture(t)
	fixture.coordinator.maximumPlans = 1
	expiresAt := fixture.now.Add(defaultProjectRuntimeRepairPlanTTL)
	otherID, err := fixture.coordinator.storePlan(projectRuntimeRepairPlan{projectID: "project-other", expiresAt: expiresAt})
	if err != nil {
		t.Fatalf("storePlan(other project) error = %v", err)
	}

	_, err = fixture.coordinator.Inspect(t.Context(), projectRuntimeRepairInspectRequest(fixture))
	var capacity *ProjectRuntimeRepairPlanCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("Inspect(at capacity) error = %v", err)
	}
	if len(fixture.coordinator.plans) != 1 {
		t.Fatalf("plan count = %d, want 1", len(fixture.coordinator.plans))
	}
	if _, found := fixture.coordinator.plans[otherID]; !found {
		t.Fatal("Inspect(at capacity) evicted the valid plan for another project")
	}
}

// newProjectRuntimeRepairTestFixture constructs one valid retained-runtime boundary and all injected coordinator dependencies.
func newProjectRuntimeRepairTestFixture(t *testing.T) *projectRuntimeRepairTestFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize test checkout: %v", err)
	}
	base := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	projectAt := base.Add(time.Minute)
	operation, err := domain.NewOperation(
		"operation-runtime-repair",
		"intent-runtime-repair",
		domain.OperationKindProjectStart,
		"project-orders",
		base,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "isolating unresolved process authority", base.Add(30*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	problem := &domain.Problem{
		Code:      domain.ProjectRecoveryAmbiguousLaunchProblemCode,
		Message:   "Harbor restarted without enough evidence to identify the previous project process.",
		Retryable: false,
	}
	operation, err = operation.Transition(domain.OperationFailed, domain.ProjectRecoveryRequiredPhase, projectAt, problem)
	if err != nil {
		t.Fatalf("Transition(failed) error = %v", err)
	}
	ownership, err := identity.NewOwnership("installation-test", 3)
	if err != nil {
		t.Fatalf("NewOwnership() error = %v", err)
	}
	leaseKey, err := identity.NewPrimaryKey("project-orders")
	if err != nil {
		t.Fatalf("NewPrimaryKey() error = %v", err)
	}
	address := netip.MustParseAddr("127.77.0.10")
	boundary := state.RetainedProjectRuntimeRepairBoundary{
		Project: state.ProjectRecord{
			Project: domain.ProjectSnapshot{
				ID: "project-orders", Name: "Orders", Path: root, Slug: "orders",
				State: domain.ProjectUnavailable, UpdatedAt: projectAt,
				Apps: []domain.AppSnapshot{}, Services: []domain.ServiceSnapshot{}, Resources: []domain.ResourceSnapshot{},
			},
			Revision: 42,
		},
		SessionID:              "session-runtime-repair",
		SessionGeneration:      7,
		SessionUpdatedAt:       base.Add(45 * time.Second),
		RecoveryOperation:      state.OperationRecord{Operation: operation, Revision: 41},
		NetworkRevision:        51,
		NetworkUpdatedAt:       base.Add(2 * time.Minute),
		PrimaryLease:           identity.Lease{Key: leaseKey, Address: address, Ownership: ownership},
		PrimaryLeaseGeneration: 9,
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("test boundary Validate() error = %v", err)
	}
	discoveryTarget := projectRuntimeRepairTestDiscoveryTarget(t, address, 3000)
	target := projectprocess.RuntimeRepairTarget{CheckoutRoot: root, Endpoint: netip.AddrPortFrom(address, 3000)}
	if err := target.Validate(); err != nil {
		t.Fatalf("test target Validate() error = %v", err)
	}
	now := base.Add(10 * time.Minute)
	events := make([]string, 0, 8)
	store := &projectRuntimeRepairTestStore{boundary: boundary, events: &events}
	discoverer := &projectRuntimeRepairTestDiscoverer{target: discoveryTarget, events: &events}
	repairer := &projectRuntimeRepairTestRepairer{
		inspection: projectprocess.RuntimeRepairInspection{
			State:      projectprocess.RuntimeRepairInspectionActionable,
			Diagnostic: projectprocess.RuntimeRepairDiagnosticCandidateExact,
			Candidate:  projectRuntimeRepairTestCandidate(target),
		},
		confirmation: projectprocess.RuntimeRepairConfirmation{State: projectprocess.RuntimeRepairConfirmationSettled, Signaled: true},
		events:       &events,
	}
	coordinator := newProjectRuntimeRepairCoordinator(
		store,
		discoverer,
		repairer,
		func() time.Time { return now },
		&projectRuntimeRepairTestEntropy{},
		defaultProjectRuntimeRepairPlanTTL,
		defaultProjectRuntimeRepairMaximumPlans,
	)
	return &projectRuntimeRepairTestFixture{
		coordinator: coordinator,
		store:       store,
		discoverer:  discoverer,
		repairer:    repairer,
		boundary:    cloneProjectRuntimeRepairTestBoundary(boundary),
		target:      target,
		caller:      ProjectRuntimeRepairCaller{UserID: "user-501", ProcessID: 7102, Role: rpc.RoleCLI},
		now:         &now,
		events:      &events,
	}
}

// projectRuntimeRepairTestDiscoveryTarget constructs one internally consistent default App target for a test port.
func projectRuntimeRepairTestDiscoveryTarget(t *testing.T, address netip.Addr, port uint16) projectdiscovery.RuntimeTarget {
	t.Helper()
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", address, port)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	return target
}

// projectRuntimeRepairTestCandidate constructs the safe half of an actionable candidate for an injected backend.
func projectRuntimeRepairTestCandidate(target projectprocess.RuntimeRepairTarget) *projectprocess.RuntimeRepairCandidate {
	return &projectprocess.RuntimeRepairCandidate{
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

// projectRuntimeRepairInspectRequest selects the fixture project for its authenticated caller.
func projectRuntimeRepairInspectRequest(fixture *projectRuntimeRepairTestFixture) ProjectRuntimeRepairInspectRequest {
	return ProjectRuntimeRepairInspectRequest{Caller: fixture.caller, ProjectID: fixture.boundary.Project.Project.ID}
}

// projectRuntimeRepairConfirmRequest echoes only the fixture's opaque safe selection.
func projectRuntimeRepairConfirmRequest(
	fixture *projectRuntimeRepairTestFixture,
	confirmable *ProjectRuntimeRepairConfirmable,
) ProjectRuntimeRepairConfirmRequest {
	return ProjectRuntimeRepairConfirmRequest{
		Caller:       fixture.caller,
		ProjectID:    fixture.boundary.Project.Project.ID,
		InspectionID: confirmable.InspectionID,
		Fingerprint:  confirmable.Fingerprint,
	}
}

// inspectActionableProjectRuntimeRepair returns the safe confirmation selection from one successful inspection.
func inspectActionableProjectRuntimeRepair(
	t *testing.T,
	fixture *projectRuntimeRepairTestFixture,
) *ProjectRuntimeRepairConfirmable {
	t.Helper()
	inspection, err := fixture.coordinator.Inspect(t.Context(), projectRuntimeRepairInspectRequest(fixture))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.Disposition != ProjectRuntimeRepairInspectionConfirmable || inspection.Confirmable == nil {
		t.Fatalf("Inspect() = %#v, want confirmable", inspection)
	}
	return inspection.Confirmable
}

// cloneProjectRuntimeRepairTestBoundary isolates pointer and slice fields so each durable read models a fresh decode.
func cloneProjectRuntimeRepairTestBoundary(
	boundary state.RetainedProjectRuntimeRepairBoundary,
) state.RetainedProjectRuntimeRepairBoundary {
	clone := boundary
	clone.Project.Project.Apps = make([]domain.AppSnapshot, len(boundary.Project.Project.Apps))
	copy(clone.Project.Project.Apps, boundary.Project.Project.Apps)
	clone.Project.Project.Services = make([]domain.ServiceSnapshot, len(boundary.Project.Project.Services))
	copy(clone.Project.Project.Services, boundary.Project.Project.Services)
	clone.Project.Project.Resources = make([]domain.ResourceSnapshot, len(boundary.Project.Project.Resources))
	copy(clone.Project.Project.Resources, boundary.Project.Project.Resources)
	operation := boundary.RecoveryOperation.Operation
	if operation.StartedAt != nil {
		startedAt := *operation.StartedAt
		operation.StartedAt = &startedAt
	}
	if operation.FinishedAt != nil {
		finishedAt := *operation.FinishedAt
		operation.FinishedAt = &finishedAt
	}
	if operation.Problem != nil {
		problem := *operation.Problem
		operation.Problem = &problem
	}
	clone.RecoveryOperation.Operation = operation
	return clone
}

// appendProjectRuntimeRepairTestEvent records ordering only when a test requested an event stream.
func appendProjectRuntimeRepairTestEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}
