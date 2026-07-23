package domain

import (
	"strings"
	"testing"
	"time"
)

// TestOperationKindsKeepStableWireValues verifies clients can persist the reserved lifecycle kinds.
func TestOperationKindsKeepStableWireValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind OperationKind
		want OperationKind
	}{
		{name: "network setup", kind: OperationKindNetworkSetup, want: "network.setup"},
		{name: "network resolver setup", kind: OperationKindNetworkResolverSetup, want: "network.resolver.setup"},
		{name: "network resolver policy migration", kind: OperationKindNetworkResolverPolicyMigration, want: "network.resolver.policy-migration"},
		{name: "network data-plane setup", kind: OperationKindNetworkDataPlaneSetup, want: "network.data-plane.setup"},
		{name: "network release", kind: OperationKindNetworkRelease, want: "network.release"},
		{name: "start", kind: OperationKindProjectStart, want: "project.start"},
		{name: "stop", kind: OperationKindProjectStop, want: "project.stop"},
		{name: "unregister", kind: OperationKindProjectUnregister, want: "project.unregister"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.kind != test.want {
				t.Fatalf("operation kind = %q, want %q", test.kind, test.want)
			}
		})
	}
}

// TestGlobalNetworkOperationsRequireGlobalScope keeps machine setup and release outside project aggregates.
func TestGlobalNetworkOperationsRequireGlobalScope(t *testing.T) {
	t.Parallel()
	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		kind OperationKind
		want string
	}{
		{kind: OperationKindNetworkSetup, want: "network setup operation"},
		{kind: OperationKindNetworkResolverSetup, want: "network setup operation"},
		{kind: OperationKindNetworkResolverPolicyMigration, want: "network setup operation"},
		{kind: OperationKindNetworkDataPlaneSetup, want: "network setup operation"},
		{kind: OperationKindNetworkRelease, want: "network release operation"},
	}
	for _, test := range tests {
		kind := test.kind
		if _, err := NewOperation(
			"operation-network-setup",
			"intent-network-setup",
			kind,
			"project-01",
			requestedAt,
		); err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "must not identify a project") {
			t.Fatalf("NewOperation(%s with project) error = %v", kind, err)
		}
		if _, err := NewOperation(
			"operation-network-setup",
			"intent-network-setup",
			kind,
			"",
			requestedAt,
		); err != nil {
			t.Fatalf("NewOperation(global %s) error = %v", kind, err)
		}
	}
}

// TestProjectLifecycleOperationsRequireProject keeps reserved operations correlated with their owning aggregate.
func TestProjectLifecycleOperationsRequireProject(t *testing.T) {
	t.Parallel()
	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		kind OperationKind
	}{
		{name: "start", kind: OperationKindProjectStart},
		{name: "stop", kind: OperationKindProjectStop},
		{name: "unregister", kind: OperationKindProjectUnregister},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewOperation(
				OperationID("operation-"+test.name),
				IntentID("intent-"+test.name),
				test.kind,
				"",
				requestedAt,
			); err == nil || !strings.Contains(err.Error(), "must identify a project") {
				t.Fatalf("NewOperation(%s without project) error = %v", test.name, err)
			}
		})
	}
	if _, err := NewOperation("operation-host", "intent-host", "host.setup", "", requestedAt); err != nil {
		t.Fatalf("NewOperation(unscoped host operation) error = %v", err)
	}
	if _, err := NewOperation("operation-additive", "intent-additive", "project.future", "", requestedAt); err != nil {
		t.Fatalf("NewOperation(unscoped additive project operation) error = %v", err)
	}
}

// TestOperationTransitionCompletesApprovalLifecycle proves approval is visible and safely retryable.
func TestOperationTransitionCompletesApprovalLifecycle(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation("operation-01", "intent-01", "host.setup", "", requestedAt)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	if operation.State != OperationQueued || operation.StartedAt != nil || operation.FinishedAt != nil {
		t.Fatalf("NewOperation() = %+v, want an untouched queued operation", operation)
	}

	operation, err = operation.Transition(OperationRunning, "observing", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	startedAt := operation.StartedAt
	operation, err = operation.Transition(OperationRequiresApproval, "awaiting_consent", requestedAt.Add(2*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(requires_approval) error = %v", err)
	}
	operation, err = operation.Transition(OperationRunning, "applying", requestedAt.Add(3*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(running after approval) error = %v", err)
	}
	if operation.StartedAt != startedAt {
		t.Fatal("approval retry replaced the operation's original start timestamp")
	}
	operation, err = operation.Transition(OperationSucceeded, "complete", requestedAt.Add(4*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(succeeded) error = %v", err)
	}
	if operation.FinishedAt == nil || !operation.FinishedAt.Equal(requestedAt.Add(4*time.Second)) {
		t.Fatalf("FinishedAt = %v, want terminal transition time", operation.FinishedAt)
	}
}

// TestOperationTransitionRequiresProblemForFailure keeps structured failure data attached to terminal errors.
func TestOperationTransitionRequiresProblemForFailure(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation("operation-01", "intent-01", "project.favorite.set", "project-01", requestedAt)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(OperationRunning, "writing", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	if _, err := operation.Transition(OperationFailed, "failed", requestedAt.Add(2*time.Second), nil); err == nil {
		t.Fatal("Transition(failed without problem) error = nil, want validation error")
	}

	problem := &Problem{Code: "write_failed", Message: "Harbor could not persist the favorite.", Retryable: true}
	failed, err := operation.Transition(OperationFailed, "failed", requestedAt.Add(2*time.Second), problem)
	if err != nil {
		t.Fatalf("Transition(failed) error = %v", err)
	}
	if failed.Problem != problem {
		t.Fatal("Transition(failed) did not retain the structured problem")
	}
}

// TestOperationTransitionRejectsTimeBeforeExistingStart preserves chronological lifecycle evidence.
func TestOperationTransitionRejectsTimeBeforeExistingStart(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation("operation-01", "intent-01", "project.favorite.set", "project-01", requestedAt)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(OperationRunning, "running", requestedAt.Add(2*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	_, err = operation.Transition(OperationSucceeded, "complete", requestedAt.Add(time.Second), nil)
	if err == nil || !strings.Contains(err.Error(), "start time") {
		t.Fatalf("Transition() error = %v, want start-time ordering error", err)
	}
}

// TestOperationTransitionRejectsInvalidChanges covers illegal state, phase, problem, and time changes.
func TestOperationTransitionRejectsInvalidChanges(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation("operation-01", "intent-01", "project.favorite.set", "project-01", requestedAt)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}

	tests := []struct {
		name    string
		next    OperationState
		phase   string
		at      time.Time
		problem *Problem
		want    string
	}{
		{name: "skip running", next: OperationSucceeded, phase: "complete", at: requestedAt.Add(time.Second), want: "cannot transition"},
		{name: "same state", next: OperationQueued, phase: "queued", at: requestedAt.Add(time.Second), want: "cannot transition"},
		{name: "empty phase", next: OperationRunning, phase: " ", at: requestedAt.Add(time.Second), want: "phase"},
		{name: "local time", next: OperationRunning, phase: "running", at: requestedAt.In(time.FixedZone("local", 3600)), want: "UTC"},
		{name: "time travel", next: OperationRunning, phase: "running", at: requestedAt.Add(-time.Second), want: "precede"},
		{name: "problem while running", next: OperationRunning, phase: "running", at: requestedAt.Add(time.Second), problem: &Problem{Code: "unexpected", Message: "Unexpected."}, want: "must not contain"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := operation.Transition(test.next, test.phase, test.at, test.problem)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Transition() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestOperationValidateRejectsContradictoryStateFields covers persisted records that bypass constructors.
func TestOperationValidateRejectsContradictoryStateFields(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := requestedAt.Add(2 * time.Second)
	problem := &Problem{Code: "failed", Message: "The operation failed."}
	base := Operation{
		ID:          "operation-01",
		IntentID:    "intent-01",
		Kind:        "project.favorite.set",
		State:       OperationQueued,
		Phase:       "queued",
		RequestedAt: requestedAt,
	}

	tests := []struct {
		name      string
		operation Operation
	}{
		{name: "queued with start", operation: func() Operation { value := base; value.StartedAt = &startedAt; return value }()},
		{name: "running without start", operation: func() Operation { value := base; value.State = OperationRunning; value.Phase = "running"; return value }()},
		{name: "succeeded without finish", operation: func() Operation {
			value := base
			value.State = OperationSucceeded
			value.Phase = "complete"
			value.StartedAt = &startedAt
			return value
		}()},
		{name: "failed without problem", operation: func() Operation {
			value := base
			value.State = OperationFailed
			value.Phase = "failed"
			value.StartedAt = &startedAt
			value.FinishedAt = &finishedAt
			return value
		}()},
		{name: "cancelled with problem", operation: func() Operation {
			value := base
			value.State = OperationCancelled
			value.Phase = "cancelled"
			value.FinishedAt = &finishedAt
			value.Problem = problem
			return value
		}()},
		{name: "finished before started", operation: func() Operation {
			value := base
			value.State = OperationSucceeded
			value.Phase = "complete"
			value.StartedAt = &finishedAt
			value.FinishedAt = &startedAt
			return value
		}()},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.operation.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want contradictory state error")
			}
		})
	}
}

// TestNewOperationRejectsInvalidIdentityAndTime covers every constructor-owned invariant.
func TestNewOperationRejectsInvalidIdentityAndTime(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		id        OperationID
		intentID  IntentID
		kind      OperationKind
		projectID ProjectID
		at        time.Time
	}{
		{name: "operation ID", intentID: "intent-01", kind: "project.favorite.set", at: requestedAt},
		{name: "intent ID", id: "operation-01", kind: "project.favorite.set", at: requestedAt},
		{name: "operation kind", id: "operation-01", intentID: "intent-01", at: requestedAt},
		{name: "project ID", id: "operation-01", intentID: "intent-01", kind: "project.favorite.set", projectID: " project-01", at: requestedAt},
		{name: "unregister project ID", id: "operation-01", intentID: "intent-01", kind: OperationKindProjectUnregister, at: requestedAt},
		{name: "zero time", id: "operation-01", intentID: "intent-01", kind: "project.favorite.set"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewOperation(test.id, test.intentID, test.kind, test.projectID, test.at); err == nil {
				t.Fatal("NewOperation() error = nil, want validation error")
			}
		})
	}
}

// TestOperationValidateRejectsInvalidGeneralFields exercises validation before state-specific checks.
func TestOperationValidateRejectsInvalidGeneralFields(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	localStartedAt := startedAt.In(time.FixedZone("local", 3600))
	finishedBeforeRequest := requestedAt.Add(-time.Second)
	base := Operation{
		ID:          "operation-01",
		IntentID:    "intent-01",
		Kind:        "project.favorite.set",
		ProjectID:   "project-01",
		State:       OperationQueued,
		Phase:       "queued",
		RequestedAt: requestedAt,
	}

	tests := []struct {
		name   string
		mutate func(*Operation)
	}{
		{name: "operation ID", mutate: func(operation *Operation) { operation.ID = "" }},
		{name: "intent ID", mutate: func(operation *Operation) { operation.IntentID = "" }},
		{name: "project ID", mutate: func(operation *Operation) { operation.ProjectID = " project-01" }},
		{name: "kind", mutate: func(operation *Operation) { operation.Kind = "" }},
		{name: "unregister project", mutate: func(operation *Operation) {
			operation.Kind = OperationKindProjectUnregister
			operation.ProjectID = ""
		}},
		{name: "state", mutate: func(operation *Operation) { operation.State = "unknown" }},
		{name: "phase", mutate: func(operation *Operation) { operation.Phase = "" }},
		{name: "requested time", mutate: func(operation *Operation) { operation.RequestedAt = time.Time{} }},
		{name: "started time zone", mutate: func(operation *Operation) {
			operation.State = OperationRunning
			operation.Phase = "running"
			operation.StartedAt = &localStartedAt
		}},
		{name: "started before request", mutate: func(operation *Operation) {
			operation.State = OperationRunning
			operation.Phase = "running"
			operation.StartedAt = &finishedBeforeRequest
		}},
		{name: "finished before request", mutate: func(operation *Operation) {
			operation.State = OperationCancelled
			operation.Phase = "cancelled"
			operation.FinishedAt = &finishedBeforeRequest
		}},
		{name: "invalid problem", mutate: func(operation *Operation) {
			operation.State = OperationFailed
			operation.Phase = "failed"
			operation.StartedAt = &startedAt
			finishedAt := startedAt.Add(time.Second)
			operation.FinishedAt = &finishedAt
			operation.Problem = &Problem{}
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			operation := base
			test.mutate(&operation)
			if err := operation.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}
