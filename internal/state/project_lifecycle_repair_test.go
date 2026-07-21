package state

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// retainedRuntimeRepairFixture owns one initialized project at the exact missing-evidence quarantine boundary.
type retainedRuntimeRepairFixture struct {
	store      *Store
	connection *gorm.DB
	boundary   RetainedProjectRuntimeRepairBoundary
}

// TestRetainedProjectRuntimeRepairBoundaryReturnsExactDurableAuthority proves inspection receives every state and network fence from one database instant.
func TestRetainedProjectRuntimeRepairBoundaryReturnsExactDurableAuthority(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	boundary := fixture.boundary

	if err := boundary.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if boundary.Project.Project.ID != "project-alpha" || boundary.Project.Project.State != domain.ProjectUnavailable {
		t.Fatalf("repair project = %#v", boundary.Project)
	}
	if boundary.SessionID == "" || boundary.SessionGeneration == 0 {
		t.Fatalf("repair session fence = %q generation %d", boundary.SessionID, boundary.SessionGeneration)
	}
	if boundary.RecoveryOperation.Operation.Kind != domain.OperationKindProjectStart ||
		boundary.RecoveryOperation.Operation.State != domain.OperationFailed ||
		boundary.RecoveryOperation.Operation.Phase != domain.ProjectRecoveryRequiredPhase ||
		boundary.RecoveryOperation.Operation.Problem == nil ||
		boundary.RecoveryOperation.Operation.Problem.Code != domain.ProjectRecoveryAmbiguousLaunchProblemCode {
		t.Fatalf("repair operation = %#v", boundary.RecoveryOperation)
	}
	if boundary.NetworkRevision == 0 || boundary.PrimaryLease.Key.ProjectID != boundary.Project.Project.ID ||
		boundary.PrimaryLease.Key.Kind() != "primary" || boundary.PrimaryLeaseGeneration == 0 {
		t.Fatalf("repair network fence = revision %d, lease %#v, generation %d", boundary.NetworkRevision, boundary.PrimaryLease, boundary.PrimaryLeaseGeneration)
	}

	_, err := fixture.store.ActiveProjectSession(t.Context(), boundary.Project.Project.ID)
	var missing *ProjectSessionProcessEvidenceMissingError
	if !errors.As(err, &missing) || missing.SessionID != boundary.SessionID || missing.Generation != boundary.SessionGeneration {
		t.Fatalf("retained missing evidence = %#v, %v", missing, err)
	}
}

// TestUnattributedProjectRuntimeInspectionBoundaryRequiresNoSession proves the separate listener case cannot reuse retained session authority.
func TestUnattributedProjectRuntimeInspectionBoundaryRequiresNoSession(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	if _, err := fixture.store.UnattributedProjectRuntimeInspectionBoundary(t.Context(), fixture.boundary.Project.Project.ID); err == nil {
		t.Fatal("UnattributedProjectRuntimeInspectionBoundary(active session) error = nil")
	} else {
		var missing *ProjectSessionProcessEvidenceMissingError
		if !errors.As(err, &missing) || missing.SessionID != fixture.boundary.SessionID {
			t.Fatalf("UnattributedProjectRuntimeInspectionBoundary(active session) error = %v, want retained session rejection", err)
		}
	}

	project, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), retainedRuntimeRepairCompletionRequest(fixture.boundary))
	if err != nil {
		t.Fatalf("CompleteRetainedProjectRuntimeRepair() error = %v", err)
	}
	boundary, err := fixture.store.UnattributedProjectRuntimeInspectionBoundary(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatalf("UnattributedProjectRuntimeInspectionBoundary() error = %v", err)
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("UnattributedProjectRuntimeInspectionBoundary.Validate() error = %v", err)
	}
	if boundary.Project.Project.State != domain.ProjectStopped || boundary.PrimaryLease != fixture.boundary.PrimaryLease ||
		boundary.PrimaryLeaseGeneration != fixture.boundary.PrimaryLeaseGeneration {
		t.Fatalf("unattributed boundary = %#v, want stopped project and retained lease", boundary)
	}
}

// TestUnattributedProjectRuntimeInspectionBoundaryRejectsInvalidFacts proves callers cannot manufacture a retry boundary from partial durable facts.
func TestUnattributedProjectRuntimeInspectionBoundaryRejectsInvalidFacts(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	project, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), retainedRuntimeRepairCompletionRequest(fixture.boundary))
	if err != nil {
		t.Fatalf("CompleteRetainedProjectRuntimeRepair() error = %v", err)
	}
	boundary, err := fixture.store.UnattributedProjectRuntimeInspectionBoundary(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatalf("UnattributedProjectRuntimeInspectionBoundary() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*UnattributedProjectRuntimeInspectionBoundary)
	}{
		{name: "active project", mutate: func(candidate *UnattributedProjectRuntimeInspectionBoundary) {
			candidate.Project.Project.State = domain.ProjectReady
		}},
		{name: "zero network revision", mutate: func(candidate *UnattributedProjectRuntimeInspectionBoundary) {
			candidate.NetworkRevision = 0
		}},
		{name: "zero network update time", mutate: func(candidate *UnattributedProjectRuntimeInspectionBoundary) {
			candidate.NetworkUpdatedAt = time.Time{}
		}},
		{name: "foreign primary lease", mutate: func(candidate *UnattributedProjectRuntimeInspectionBoundary) {
			candidate.PrimaryLease.Key.ProjectID = "project-other"
		}},
		{name: "zero lease generation", mutate: func(candidate *UnattributedProjectRuntimeInspectionBoundary) {
			candidate.PrimaryLeaseGeneration = 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := boundary
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestCompleteRetainedProjectRuntimeRepairRestoresStoppedProject proves exact finalization retires only the legacy row and leaves network authority unchanged.
func TestCompleteRetainedProjectRuntimeRepairRestoresStoppedProject(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	networkBefore, initialized, err := fixture.store.Network(t.Context())
	if err != nil || !initialized {
		t.Fatalf("Network(before) = %#v, %t, %v", networkBefore, initialized, err)
	}
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)
	request := retainedRuntimeRepairCompletionRequest(fixture.boundary)

	project, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), request)
	if err != nil {
		t.Fatalf("CompleteRetainedProjectRuntimeRepair() error = %v", err)
	}
	if !projectMatchesInactiveState(project.Project, domain.ProjectStopped, request.At) {
		t.Fatalf("repaired project = %#v", project)
	}
	if project.Revision != sequenceBefore+1 || projectStoreMutationSequence(t, fixture.store) != sequenceBefore+1 {
		t.Fatalf("repair sequence = project %d, high water %d, want %d", project.Revision, projectStoreMutationSequence(t, fixture.store), sequenceBefore+1)
	}
	requireLifecycleTestSessionCount(t, fixture.connection, request.ProjectID, 0)
	networkAfter, initialized, err := fixture.store.Network(t.Context())
	if err != nil || !initialized || !reflect.DeepEqual(networkAfter, networkBefore) {
		t.Fatalf("Network(after) = %#v, %t, %v; want %#v", networkAfter, initialized, err, networkBefore)
	}
	if _, err := fixture.store.RetainedProjectRuntimeRepairBoundary(t.Context(), request.ProjectID); err == nil {
		t.Fatal("RetainedProjectRuntimeRepairBoundary(after completion) error = nil")
	}

	queued := enqueueProjectLifecycleTestOperation(t, fixture.store, domain.OperationKindProjectStart, request.ProjectID, "repair-restart")
	startedAt := queued.Operation.RequestedAt.Add(time.Second)
	session := projectLifecycleTestPlannedSession(t, request.ProjectID, startedAt)
	started, err := fixture.store.BeginProjectStart(t.Context(), BeginProjectStartRequest{
		ProjectID:                 request.ProjectID,
		OperationID:               queued.Operation.ID,
		ExpectedOperationRevision: queued.Revision,
		ExpectedProjectRevision:   project.Revision,
		Session:                   session,
		Phase:                     "launching forj dev",
		At:                        startedAt,
	})
	if err != nil || started.Project.Project.State != domain.ProjectStarting {
		t.Fatalf("BeginProjectStart(after repair) = %#v, %v", started, err)
	}
}

// TestReleaseUnavailableProjectSessionRetiresReceiptFreeBoundaries proves a missing process receipt cannot strand a replacement start.
func TestReleaseUnavailableProjectSessionRetiresReceiptFreeBoundaries(t *testing.T) {
	for _, sessionState := range []domain.SessionState{domain.SessionAwaitingAttach, domain.SessionPlanned} {
		t.Run(string(sessionState), func(t *testing.T) {
			fixture := newRetainedRuntimeRepairFixture(t)
			if sessionState == domain.SessionPlanned {
				if err := fixture.connection.Model(&models.ProjectSession{}).
					Where("project_id = ?", string(fixture.boundary.Project.Project.ID)).
					Update("state", string(sessionState)).Error; err != nil {
					t.Fatalf("set receipt-free session state: %v", err)
				}
			}
			at := fixture.boundary.Project.Project.UpdatedAt.Add(time.Second)
			project, err := fixture.store.ReleaseUnavailableProjectSession(t.Context(), ReleaseUnavailableProjectSessionRequest{
				ProjectID: fixture.boundary.Project.Project.ID,
				At:        at,
			})
			if err != nil {
				t.Fatalf("ReleaseUnavailableProjectSession() error = %v", err)
			}
			if !projectMatchesInactiveState(project.Project, domain.ProjectStopped, project.Project.UpdatedAt) {
				t.Fatalf("released project = %#v", project)
			}
			requireLifecycleTestSessionCount(t, fixture.connection, fixture.boundary.Project.Project.ID, 0)
		})
	}
}

// TestReceiptFreeProjectRuntimeRepairBoundaryAcceptsPlannedLaunches proves a crash before process attachment still exposes the project listener fence.
func TestReceiptFreeProjectRuntimeRepairBoundaryAcceptsPlannedLaunches(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	if err := fixture.connection.Exec(
		"UPDATE project_sessions SET state = ?, updated_at = ? WHERE project_id = ?",
		string(domain.SessionPlanned),
		fixture.boundary.SessionUpdatedAt,
		string(fixture.boundary.Project.Project.ID),
	).Error; err != nil {
		t.Fatalf("set planned receipt-free session state: %v", err)
	}
	boundary, err := fixture.store.ReceiptFreeProjectRuntimeRepairBoundary(t.Context(), fixture.boundary.Project.Project.ID)
	if err != nil {
		t.Fatalf("ReceiptFreeProjectRuntimeRepairBoundary() error = %v", err)
	}
	if boundary.SessionID != fixture.boundary.SessionID || boundary.PrimaryLease != fixture.boundary.PrimaryLease {
		t.Fatalf("receipt-free boundary = %#v, want session %q and lease %#v", boundary, fixture.boundary.SessionID, fixture.boundary.PrimaryLease)
	}
}

// TestReleaseUnavailableProjectSessionRefusesProcessEvidence proves a durable process receipt still requires native settlement.
func TestReleaseUnavailableProjectSessionRefusesProcessEvidence(t *testing.T) {
	fixture := newProcessBackedProjectRuntimeRepairFixture(t)
	_, err := fixture.store.ReleaseUnavailableProjectSession(t.Context(), ReleaseUnavailableProjectSessionRequest{
		ProjectID: fixture.projectID,
		At:        fixture.request.At,
	})
	if err == nil {
		t.Fatal("ReleaseUnavailableProjectSession(process-backed) error = nil")
	}
	requireLifecycleTestSessionCount(t, fixture.connection, fixture.projectID, 1)
}

// TestCompleteRetainedProjectRuntimeRepairRejectsEveryCallerFence proves stale inspection data cannot retire retained authority.
func TestCompleteRetainedProjectRuntimeRepairRejectsEveryCallerFence(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	valid := retainedRuntimeRepairCompletionRequest(fixture.boundary)
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)
	tests := []struct {
		name   string
		mutate func(*CompleteRetainedProjectRuntimeRepairRequest)
	}{
		{name: "project revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedProjectRevision++ }},
		{name: "session identity", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.SessionID = "session-other" }},
		{name: "session generation", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedSessionGeneration++ }},
		{name: "session update time", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedSessionUpdatedAt = request.ExpectedSessionUpdatedAt.Add(time.Second)
		}},
		{name: "operation identity", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedRecoveryOperationID = "operation-other"
		}},
		{name: "operation revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedRecoveryOperationRevision++
		}},
		{name: "network revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedNetworkRevision++ }},
		{name: "network update time", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedNetworkUpdatedAt = request.ExpectedNetworkUpdatedAt.Add(time.Second)
		}},
		{name: "primary lease", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedPrimaryLease.Address = netip.MustParseAddr("127.77.0.99")
		}},
		{name: "primary lease generation", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedPrimaryLeaseGeneration++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if _, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), request); err == nil {
				t.Fatal("CompleteRetainedProjectRuntimeRepair(stale fence) error = nil")
			}
			requireLifecycleTestSessionCount(t, fixture.connection, valid.ProjectID, 1)
			project, err := fixture.store.Project(t.Context(), valid.ProjectID)
			if err != nil || project.Revision != fixture.boundary.Project.Revision || project.Project.State != domain.ProjectUnavailable {
				t.Fatalf("project after rejected fence = %#v, %v", project, err)
			}
			if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
				t.Fatalf("sequence after rejected fence = %d, want %d", got, sequenceBefore)
			}
		})
	}
}

// TestCompleteRetainedProjectRuntimeRepairRejectsDurableDrift proves post-inspection state changes cannot be mistaken for the inspected runtime.
func TestCompleteRetainedProjectRuntimeRepairRejectsDurableDrift(t *testing.T) {
	tests := []struct {
		name  string
		drift func(*testing.T, retainedRuntimeRepairFixture)
	}{
		{name: "session generation", drift: func(t *testing.T, fixture retainedRuntimeRepairFixture) {
			if err := fixture.connection.Model(&models.ProjectSession{}).
				Where("project_id = ?", string(fixture.boundary.Project.Project.ID)).
				Update("generation", int(fixture.boundary.SessionGeneration+1)).Error; err != nil {
				t.Fatalf("advance session generation: %v", err)
			}
		}},
		{name: "network revision", drift: func(t *testing.T, fixture retainedRuntimeRepairFixture) {
			err := fixture.store.mutations.mutate(t.Context(), "advance repair network fence", func(tx *gorm.DB) error {
				sequence, err := allocateHarborSequence(tx)
				if err != nil {
					return err
				}
				return tx.Model(&models.NetworkState{}).
					Where("id = ?", networkStateSingletonID).
					Updates(map[string]any{"revision": int(sequence), "updated_at": fixture.boundary.NetworkUpdatedAt.Add(time.Second)}).Error
			})
			if err != nil {
				t.Fatalf("advance network revision: %v", err)
			}
		}},
		{name: "primary lease generation", drift: func(t *testing.T, fixture retainedRuntimeRepairFixture) {
			if err := fixture.connection.Model(&models.LoopbackAddressLease{}).
				Where("project_id = ? AND kind = ? AND state = ?", string(fixture.boundary.Project.Project.ID), "primary", "leased").
				Update("lease_generation", int(fixture.boundary.PrimaryLeaseGeneration+1)).Error; err != nil {
				t.Fatalf("advance primary lease generation: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRetainedRuntimeRepairFixture(t)
			request := retainedRuntimeRepairCompletionRequest(fixture.boundary)
			test.drift(t, fixture)
			if _, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), request); err == nil {
				t.Fatal("CompleteRetainedProjectRuntimeRepair(after drift) error = nil")
			}
			requireLifecycleTestSessionCount(t, fixture.connection, request.ProjectID, 1)
			project, err := fixture.store.Project(t.Context(), request.ProjectID)
			if err != nil || project.Revision != fixture.boundary.Project.Revision || project.Project.State != domain.ProjectUnavailable {
				t.Fatalf("project after durable drift = %#v, %v", project, err)
			}
		})
	}
}

// TestOperationJournalRejectsStartBeforeRetainedRuntimeRepair proves a new start cannot supersede the marker that makes repair possible.
func TestOperationJournalRejectsStartBeforeRetainedRuntimeRepair(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)
	requestedAt := fixture.boundary.Project.Project.UpdatedAt.Add(time.Minute)
	operation, err := domain.NewOperation(
		"operation-start-before-repair",
		"intent-start-before-repair",
		domain.OperationKindProjectStart,
		fixture.boundary.Project.Project.ID,
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	if _, err := projectStoreMutationJournal(fixture.store).EnqueueProjectStart(t.Context(), operation); err == nil {
		t.Fatal("EnqueueProjectStart(start before repair) error = nil")
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after rejected start = %d, want %d", got, sequenceBefore)
	}
	boundary, err := fixture.store.RetainedProjectRuntimeRepairBoundary(t.Context(), fixture.boundary.Project.Project.ID)
	if err != nil || !reflect.DeepEqual(boundary, fixture.boundary) {
		t.Fatalf("repair boundary after rejected start = %#v, %v; want %#v", boundary, err, fixture.boundary)
	}
	recovery := fixture.boundary.RecoveryOperation.Operation
	replay, err := domain.NewOperation(recovery.ID, recovery.IntentID, recovery.Kind, recovery.ProjectID, recovery.RequestedAt)
	if err != nil {
		t.Fatalf("NewOperation(recovery replay) error = %v", err)
	}
	replayed, err := projectStoreMutationJournal(fixture.store).EnqueueProjectStart(t.Context(), replay)
	if err != nil || !reflect.DeepEqual(replayed, fixture.boundary.RecoveryOperation) {
		t.Fatalf("Enqueue(recovery replay) = %#v, %v; want %#v", replayed, err, fixture.boundary.RecoveryOperation)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after recovery replay = %d, want %d", got, sequenceBefore)
	}
}

// TestEnqueueProjectStartRejectsOtherOperationKinds keeps the specialized admission API limited to runtime starts.
func TestEnqueueProjectStartRejectsOtherOperationKinds(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)
	operation, err := domain.NewOperation(
		"operation-stop-through-start-admission",
		"intent-stop-through-start-admission",
		domain.OperationKindProjectStop,
		fixture.boundary.Project.Project.ID,
		fixture.boundary.Project.Project.UpdatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	if _, err := projectStoreMutationJournal(fixture.store).EnqueueProjectStart(t.Context(), operation); err == nil {
		t.Fatal("EnqueueProjectStart(stop) error = nil")
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after rejected operation kind = %d, want %d", got, sequenceBefore)
	}
}

// TestRetainedProjectRuntimeRepairBoundaryRejectsUnsafeSessionShapes covers authority that cannot be repaired through legacy missing-evidence recovery.
func TestRetainedProjectRuntimeRepairBoundaryRejectsUnsafeSessionShapes(t *testing.T) {
	tests := []struct {
		name   string
		update map[string]any
	}{
		{name: "complete process evidence", update: map[string]any{
			"pid": int64(5102), "birth_token": "replacement-birth", "executable_identity": "/tmp/forj", "argument_digest": strings.Repeat("d", 64),
		}},
		{name: "partial process evidence", update: map[string]any{"pid": int64(5102)}},
		{name: "terminal owner", update: map[string]any{"owner": string(domain.SessionOwnerTerminal)}},
		{name: "planned state", update: map[string]any{"state": string(domain.SessionPlanned)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRetainedRuntimeRepairFixture(t)
			if err := fixture.connection.Model(&models.ProjectSession{}).
				Where("project_id = ?", string(fixture.boundary.Project.Project.ID)).
				Updates(test.update).Error; err != nil {
				t.Fatalf("change session shape: %v", err)
			}
			if _, err := fixture.store.RetainedProjectRuntimeRepairBoundary(t.Context(), fixture.boundary.Project.Project.ID); err == nil {
				t.Fatal("RetainedProjectRuntimeRepairBoundary(unsafe session) error = nil")
			}
			if _, err := fixture.store.CompleteRetainedProjectRuntimeRepair(
				t.Context(), retainedRuntimeRepairCompletionRequest(fixture.boundary),
			); err == nil {
				t.Fatal("CompleteRetainedProjectRuntimeRepair(unsafe session) error = nil")
			}
			requireLifecycleTestSessionCount(t, fixture.connection, fixture.boundary.Project.Project.ID, 1)
		})
	}
}

// TestProcessBackedProjectRuntimeRepairUsesNativeFence proves exact process receipts use a separate durable boundary and can be retired only after native settlement.
func TestProcessBackedProjectRuntimeRepairUsesNativeFence(t *testing.T) {
	fixture := newProcessBackedProjectRuntimeRepairFixture(t)
	completed, err := fixture.store.CompleteProcessBackedProjectRuntimeRepair(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("CompleteProcessBackedProjectRuntimeRepair() error = %v", err)
	}
	if completed.Project.State != domain.ProjectStopped {
		t.Fatalf("completed project state = %q, want stopped", completed.Project.State)
	}
	requireLifecycleTestSessionCount(t, fixture.connection, fixture.projectID, 0)
}

// TestProcessBackedProjectRuntimeRepairAcceptsStoppingReceipt proves Stop/Restart quarantine can use the same exact native fence.
func TestProcessBackedProjectRuntimeRepairAcceptsStoppingReceipt(t *testing.T) {
	fixture := newProcessBackedProjectRuntimeRepairFixture(t)
	if err := fixture.connection.Exec(
		"UPDATE project_sessions SET state = ?, updated_at = ? WHERE project_id = ?",
		string(domain.SessionStopping), fixture.request.ExpectedSessionUpdatedAt, string(fixture.projectID),
	).Error; err != nil {
		t.Fatalf("set process-backed session stopping: %v", err)
	}
	boundary, err := fixture.store.ProcessBackedProjectRuntimeRepairBoundary(t.Context(), fixture.projectID)
	if err != nil {
		t.Fatalf("ProcessBackedProjectRuntimeRepairBoundary(stopping) error = %v", err)
	}
	if boundary.Process != fixture.request.ExpectedProcess {
		t.Fatalf("stopping process boundary = %#v, want %#v", boundary.Process, fixture.request.ExpectedProcess)
	}
	if _, err := fixture.store.CompleteProcessBackedProjectRuntimeRepair(t.Context(), fixture.request); err != nil {
		t.Fatalf("CompleteProcessBackedProjectRuntimeRepair(stopping) error = %v", err)
	}
	requireLifecycleTestSessionCount(t, fixture.connection, fixture.projectID, 0)
}

// TestCompleteProcessBackedProjectRuntimeRepairRejectsReceiptDrift proves a changed process receipt cannot retire the session row.
func TestCompleteProcessBackedProjectRuntimeRepairRejectsReceiptDrift(t *testing.T) {
	fixture := newProcessBackedProjectRuntimeRepairFixture(t)
	fixture.request.ExpectedProcess.BirthToken = "different-process-birth"
	if _, err := fixture.store.CompleteProcessBackedProjectRuntimeRepair(t.Context(), fixture.request); err == nil {
		t.Fatal("CompleteProcessBackedProjectRuntimeRepair(receipt drift) error = nil")
	}
	requireLifecycleTestSessionCount(t, fixture.connection, fixture.projectID, 1)
}

// processBackedProjectRuntimeRepairStateFixture owns one exact process-backed quarantine and completion fence.
type processBackedProjectRuntimeRepairStateFixture struct {
	store      *Store
	connection *gorm.DB
	projectID  domain.ProjectID
	request    CompleteProcessBackedProjectRuntimeRepairRequest
}

// newProcessBackedProjectRuntimeRepairFixture creates a quarantined process receipt with initialized network authority.
func newProcessBackedProjectRuntimeRepairFixture(t *testing.T) processBackedProjectRuntimeRepairStateFixture {
	t.Helper()
	store, connection := newNetworkInitializeTestHarness(t, true)
	if _, err := store.InitializeNetwork(t.Context(), networkMutationTestInitializeRequest()); err != nil {
		t.Fatalf("InitializeNetwork() error = %v", err)
	}
	project, session, process := projectLifecycleTestReadyProject(t, store, "project-alpha")
	projectRecord, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	at := project.UpdatedAt.Add(time.Second)
	operation, err := domain.NewOperation("operation-process-backed-repair", "intent-process-backed-repair", domain.OperationKindProjectStart, project.ID, at)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	problem := domain.Problem{Code: domain.ProjectRecoveryAmbiguousLaunchProblemCode, Message: "process scope unresolved", Retryable: false}
	if _, err := store.QuarantineTerminalProjectSession(t.Context(), QuarantineTerminalProjectSessionRequest{
		ProjectID: project.ID, ExpectedProjectRevision: projectRecord.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		Operation: operation, RunningPhase: domain.ProjectRecoveryIsolationPhase,
		FailurePhase: domain.ProjectRecoveryRequiredPhase, Problem: problem, At: at,
	}); err != nil {
		t.Fatalf("QuarantineTerminalProjectSession() error = %v", err)
	}

	boundary, err := store.ProcessBackedProjectRuntimeRepairBoundary(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("ProcessBackedProjectRuntimeRepairBoundary() error = %v", err)
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("ProcessBackedProjectRuntimeRepairBoundary.Validate() error = %v", err)
	}
	if boundary.Process != process || boundary.RecoveryOperation.Operation.Kind != domain.OperationKindProjectStart {
		t.Fatalf("process-backed boundary = %#v, want process %#v", boundary, process)
	}
	return processBackedProjectRuntimeRepairStateFixture{
		store: store, connection: connection, projectID: project.ID,
		request: CompleteProcessBackedProjectRuntimeRepairRequest{
			CompleteRetainedProjectRuntimeRepairRequest: retainedRuntimeRepairCompletionRequest(
				retainedProjectRuntimeRepairBoundaryFromProcess(boundary),
			),
			ExpectedProcess: boundary.Process,
		},
	}
}

// TestCompleteRetainedProjectRuntimeRepairRollsBackLateFailure proves session retirement and the stopped projection share one transaction.
func TestCompleteRetainedProjectRuntimeRepairRollsBackLateFailure(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	if err := fixture.connection.Exec(`CREATE TRIGGER fail_retained_runtime_repair BEFORE UPDATE OF state ON projects
		WHEN NEW.state = 'stopped' BEGIN SELECT RAISE(ABORT, 'injected retained runtime repair failure'); END`).Error; err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)
	request := retainedRuntimeRepairCompletionRequest(fixture.boundary)

	_, err := fixture.store.CompleteRetainedProjectRuntimeRepair(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "injected retained runtime repair failure") {
		t.Fatalf("CompleteRetainedProjectRuntimeRepair(injected failure) error = %v", err)
	}
	requireLifecycleTestSessionCount(t, fixture.connection, request.ProjectID, 1)
	project, readErr := fixture.store.Project(t.Context(), request.ProjectID)
	if readErr != nil || !reflect.DeepEqual(project, fixture.boundary.Project) {
		t.Fatalf("project after rollback = %#v, %v; want %#v", project, readErr, fixture.boundary.Project)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after rollback = %d, want %d", got, sequenceBefore)
	}
}

// TestValidateCompleteRetainedProjectRuntimeRepairRequestRejectsIncompleteFences covers every caller-owned finalization input.
func TestValidateCompleteRetainedProjectRuntimeRepairRequestRejectsIncompleteFences(t *testing.T) {
	fixture := newRetainedRuntimeRepairFixture(t)
	valid := retainedRuntimeRepairCompletionRequest(fixture.boundary)
	tests := []struct {
		name   string
		mutate func(*CompleteRetainedProjectRuntimeRepairRequest)
	}{
		{name: "project", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ProjectID = "" }},
		{name: "project revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedProjectRevision = 0 }},
		{name: "session", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.SessionID = "" }},
		{name: "session generation", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedSessionGeneration = 0 }},
		{name: "session update time", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedSessionUpdatedAt = time.Time{}
		}},
		{name: "operation", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedRecoveryOperationID = "" }},
		{name: "operation revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedRecoveryOperationRevision = 0
		}},
		{name: "network revision", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedNetworkRevision = 0 }},
		{name: "network update time", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedNetworkUpdatedAt = time.Time{}
		}},
		{name: "lease", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedPrimaryLease.Address = netip.Addr{}
		}},
		{name: "lease project", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) {
			request.ExpectedPrimaryLease.Key.ProjectID = "project-beta"
		}},
		{name: "lease generation", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.ExpectedPrimaryLeaseGeneration = 0 }},
		{name: "time", mutate: func(request *CompleteRetainedProjectRuntimeRepairRequest) { request.At = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := validateCompleteRetainedProjectRuntimeRepairRequest(request); err == nil {
				t.Fatal("validateCompleteRetainedProjectRuntimeRepairRequest() error = nil")
			}
		})
	}
}

// newRetainedRuntimeRepairFixture creates the only legacy missing-evidence shape eligible for native inspection.
func newRetainedRuntimeRepairFixture(t *testing.T) retainedRuntimeRepairFixture {
	t.Helper()
	store, connection := newNetworkInitializeTestHarness(t, true)
	if _, err := store.InitializeNetwork(t.Context(), networkMutationTestInitializeRequest()); err != nil {
		t.Fatalf("InitializeNetwork() error = %v", err)
	}
	project, session, _ := projectLifecycleTestReadyProject(t, store, "project-alpha")
	if err := connection.Exec(
		`UPDATE project_sessions
		 SET pid = NULL, birth_token = NULL, executable_identity = NULL, argument_digest = NULL
		 WHERE project_id = ? AND session_id = ?`,
		string(project.ID),
		string(session.ID),
	).Error; err != nil {
		t.Fatalf("remove legacy process evidence: %v", err)
	}
	projectRecord, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("Project(before quarantine) error = %v", err)
	}
	at := project.UpdatedAt.Add(time.Second)
	operation, err := domain.NewOperation("operation-retained-runtime-repair", "intent-retained-runtime-repair", domain.OperationKindProjectStart, project.ID, at)
	if err != nil {
		t.Fatalf("NewOperation(repair quarantine) error = %v", err)
	}
	problem := domain.Problem{
		Code:      domain.ProjectRecoveryAmbiguousLaunchProblemCode,
		Message:   "Harbor restarted without enough evidence to identify the previous project process.",
		Retryable: false,
	}
	if _, err := store.QuarantineTerminalProjectSession(t.Context(), QuarantineTerminalProjectSessionRequest{
		ProjectID: project.ID, ExpectedProjectRevision: projectRecord.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		Operation: operation, RunningPhase: "isolating unresolved process authority",
		FailurePhase: domain.ProjectRecoveryRequiredPhase, Problem: problem, At: at,
	}); err != nil {
		t.Fatalf("QuarantineTerminalProjectSession() error = %v", err)
	}
	boundary, err := store.RetainedProjectRuntimeRepairBoundary(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("RetainedProjectRuntimeRepairBoundary() error = %v", err)
	}
	return retainedRuntimeRepairFixture{store: store, connection: connection, boundary: boundary}
}

// retainedRuntimeRepairCompletionRequest copies every server-derived fence into one explicit finalization request.
func retainedRuntimeRepairCompletionRequest(boundary RetainedProjectRuntimeRepairBoundary) CompleteRetainedProjectRuntimeRepairRequest {
	return CompleteRetainedProjectRuntimeRepairRequest{
		ProjectID:                         boundary.Project.Project.ID,
		ExpectedProjectRevision:           boundary.Project.Revision,
		SessionID:                         boundary.SessionID,
		ExpectedSessionGeneration:         boundary.SessionGeneration,
		ExpectedSessionUpdatedAt:          boundary.SessionUpdatedAt,
		ExpectedRecoveryOperationID:       boundary.RecoveryOperation.Operation.ID,
		ExpectedRecoveryOperationRevision: boundary.RecoveryOperation.Revision,
		ExpectedNetworkRevision:           boundary.NetworkRevision,
		ExpectedNetworkUpdatedAt:          boundary.NetworkUpdatedAt,
		ExpectedPrimaryLease:              boundary.PrimaryLease,
		ExpectedPrimaryLeaseGeneration:    boundary.PrimaryLeaseGeneration,
		At:                                boundary.Project.Project.UpdatedAt.Add(time.Second),
	}
}
