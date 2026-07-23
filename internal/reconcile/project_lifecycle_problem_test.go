package reconcile

import "testing"

// TestFailedStartBeforeReadinessProblemPointsToRetainedTrace keeps failed starts actionable without persisting child output in lifecycle state.
func TestFailedStartBeforeReadinessProblemPointsToRetainedTrace(t *testing.T) {
	problem := lifecycleProblem("project.process.exited", failedStartBeforeReadinessCause())
	if problem.Code != "project.process.exited" {
		t.Fatalf("problem code = %q, want %q", problem.Code, "project.process.exited")
	}
	if problem.Message != "project runtime exited before readiness; inspect Harbor's retained launch trace at _data/harbor/forj-dev.log" {
		t.Fatalf("problem message = %q", problem.Message)
	}
	if !problem.Retryable {
		t.Fatal("problem must remain retryable")
	}
}
