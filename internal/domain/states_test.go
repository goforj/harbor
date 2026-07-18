package domain

import "testing"

// TestProjectStateValidateCoversProductStates locks the domain to the complete aggregate state vocabulary.
func TestProjectStateValidateCoversProductStates(t *testing.T) {
	t.Parallel()

	states := []ProjectState{
		ProjectStopped,
		ProjectStarting,
		ProjectReady,
		ProjectRebuilding,
		ProjectDegraded,
		ProjectFailed,
		ProjectStopping,
		ProjectUnavailable,
	}
	for _, state := range states {
		if err := state.Validate(); err != nil {
			t.Errorf("ProjectState(%q).Validate() error = %v", state, err)
		}
	}
	if err := ProjectState("working").Validate(); err == nil {
		t.Fatal("ProjectState(working).Validate() error = nil, want unknown state error")
	}
}

// TestEntityStateValidateCoversSummaryStates verifies the App and service summary vocabulary.
func TestEntityStateValidateCoversSummaryStates(t *testing.T) {
	t.Parallel()

	states := []EntityState{
		EntityReady,
		EntityWorking,
		EntityDegraded,
		EntityFailed,
		EntityStopped,
		EntityUnavailable,
	}
	for _, state := range states {
		if err := state.Validate(); err != nil {
			t.Errorf("EntityState(%q).Validate() error = %v", state, err)
		}
	}
	if err := EntityState("starting").Validate(); err == nil {
		t.Fatal("EntityState(starting).Validate() error = nil, want unknown state error")
	}
}

// TestOperationStateTransitions exercises the complete transition matrix instead of isolated happy paths.
func TestOperationStateTransitions(t *testing.T) {
	t.Parallel()

	states := []OperationState{
		OperationQueued,
		OperationRunning,
		OperationRequiresApproval,
		OperationSucceeded,
		OperationFailed,
		OperationCancelled,
	}
	allowed := map[OperationState]map[OperationState]bool{
		OperationQueued: {
			OperationRunning:   true,
			OperationCancelled: true,
		},
		OperationRunning: {
			OperationRequiresApproval: true,
			OperationSucceeded:        true,
			OperationFailed:           true,
			OperationCancelled:        true,
		},
		OperationRequiresApproval: {
			OperationRunning:   true,
			OperationFailed:    true,
			OperationCancelled: true,
		},
	}

	for _, current := range states {
		if err := current.Validate(); err != nil {
			t.Fatalf("OperationState(%q).Validate() error = %v", current, err)
		}
		for _, next := range states {
			want := allowed[current][next]
			if got := current.CanTransitionTo(next); got != want {
				t.Errorf("OperationState(%q).CanTransitionTo(%q) = %t, want %t", current, next, got, want)
			}
		}
	}

	if OperationState("unknown").CanTransitionTo(OperationRunning) {
		t.Fatal("unknown operation state unexpectedly permits a transition")
	}
	if OperationRunning.CanTransitionTo(OperationState("unknown")) {
		t.Fatal("operation unexpectedly permits a transition to an unknown state")
	}
}

// TestOperationStateIsTerminal distinguishes final results from retryable approval state.
func TestOperationStateIsTerminal(t *testing.T) {
	t.Parallel()

	for _, state := range []OperationState{OperationSucceeded, OperationFailed, OperationCancelled} {
		if !state.IsTerminal() {
			t.Errorf("OperationState(%q).IsTerminal() = false, want true", state)
		}
	}
	for _, state := range []OperationState{OperationQueued, OperationRunning, OperationRequiresApproval} {
		if state.IsTerminal() {
			t.Errorf("OperationState(%q).IsTerminal() = true, want false", state)
		}
	}
}
