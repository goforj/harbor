package control

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestProjectLifecycleRequestsRequireStableProjectAndIntent validates both method-specific request shapes.
func TestProjectLifecycleRequestsRequireStableProjectAndIntent(t *testing.T) {
	validStart := StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"}
	validStop := StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"}
	if err := validStart.Validate(); err != nil {
		t.Fatalf("StartProjectRequest.Validate() error = %v", err)
	}
	if err := validStop.Validate(); err != nil {
		t.Fatalf("StopProjectRequest.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		validate func() error
		want     string
	}{
		{name: "start project", validate: func() error { request := validStart; request.ProjectID = " bad "; return request.Validate() }, want: "project ID"},
		{name: "start intent", validate: func() error { request := validStart; request.IntentID = ""; return request.Validate() }, want: "intent ID"},
		{name: "stop project", validate: func() error { request := validStop; request.ProjectID = ""; return request.Validate() }, want: "project ID"},
		{name: "stop intent", validate: func() error { request := validStop; request.IntentID = " bad "; return request.Validate() }, want: "intent ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectLifecycleOperationAcceptsOnlyStartAndStopProgress keeps unrelated operations off the action response.
func TestProjectLifecycleOperationAcceptsOnlyStartAndStopProgress(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationKindProjectStart, domain.OperationKindProjectStop} {
		result := projectLifecycleTestResult(t, kind)
		if err := result.Validate(); err != nil {
			t.Fatalf("ProjectLifecycleOperation{%s}.Validate() error = %v", kind, err)
		}
	}

	invalid := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	invalid.Operation.Kind = domain.OperationKindProjectUnregister
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "start or stop") {
		t.Fatalf("ProjectLifecycleOperation(unregister).Validate() error = %v", err)
	}
	invalid = projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	invalid.Revision = 0
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("ProjectLifecycleOperation(zero revision).Validate() error = %v", err)
	}
	invalid.Revision = domain.Sequence(math.MaxUint64)
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("ProjectLifecycleOperation(overflow revision).Validate() error = %v", err)
	}
}

// TestProjectLifecycleCorrelationRequiresTheExactMethodIdentity prevents cross-action replay confusion.
func TestProjectLifecycleCorrelationRequiresTheExactMethodIdentity(t *testing.T) {
	result := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	if err := validateProjectLifecycleCorrelation("project-orders", "intent-start-orders", domain.OperationKindProjectStart, result); err != nil {
		t.Fatalf("validateProjectLifecycleCorrelation() error = %v", err)
	}
	for _, mutate := range []func(*ProjectLifecycleOperation){
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.ProjectID = "project-other" },
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.IntentID = "intent-other" },
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.Kind = domain.OperationKindProjectStop },
	} {
		candidate := result
		mutate(&candidate)
		if err := validateProjectLifecycleCorrelation("project-orders", "intent-start-orders", domain.OperationKindProjectStart, candidate); err == nil {
			t.Fatalf("validateProjectLifecycleCorrelation(%#v) error = nil", candidate)
		}
	}
}

// projectLifecycleTestResult creates one valid queued lifecycle operation response.
func projectLifecycleTestResult(t *testing.T, kind domain.OperationKind) ProjectLifecycleOperation {
	t.Helper()
	intent := domain.IntentID("intent-start-orders")
	if kind == domain.OperationKindProjectStop {
		intent = "intent-stop-orders"
	}
	operation, err := domain.NewOperation("operation-orders", intent, kind, "project-orders", time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return ProjectLifecycleOperation{Operation: operation, Revision: 41}
}
