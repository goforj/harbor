package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectapproval"
)

// fixtureDirectory creates an admitted checkout independently of the host's default temporary directory.
func fixtureDirectory(t *testing.T) string {
	t.Helper()
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(temporary, "harbor-package-ui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	return project
}

// pendingRemoval constructs a valid daemon operation at its native approval boundary.
func pendingRemoval(t *testing.T) control.ProjectUnregistration {
	t.Helper()
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation("operation-fixture", "intent-fixture", domain.OperationKindProjectUnregister, "project-fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "removing", now.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRequiresApproval, "approval", now.Add(2*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	return control.ProjectUnregistration{Operation: operation, Revision: 5}
}

// TestSelectionRefusesIncompleteOrForeignAuthority keeps fixture approval narrower than the ordinary product client.
func TestSelectionRefusesIncompleteOrForeignAuthority(t *testing.T) {
	selected := selection{"project-fixture", "operation-fixture", "intent-fixture", 5, fixtureDirectory(t)}
	if err := selected.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*selection){
		func(s *selection) { s.projectID = "" },
		func(s *selection) { s.operationID = "" },
		func(s *selection) { s.intentID = "" },
		func(s *selection) { s.revision = 0 },
		func(s *selection) { s.revision = domain.MaximumSequence + 1 },
		func(s *selection) { s.directory = "relative/project" },
		func(s *selection) { s.directory = t.TempDir() },
	} {
		copy := selected
		mutate(&copy)
		if err := copy.validate(); err == nil {
			t.Fatalf("accepted invalid selection: %#v", copy)
		}
	}
	projects := []domain.ProjectSnapshot{{ID: selected.projectID, Path: selected.directory}}
	if err := selected.validateProject(projects); err != nil {
		t.Fatal(err)
	}
	if err := selected.validateProject(nil); err == nil {
		t.Fatal("accepted absent fixture")
	}
	projects[0].Path = fixtureDirectory(t)
	if err := selected.validateProject(projects); err == nil {
		t.Fatal("accepted another fixture's root")
	}
	projects[0].Path = t.TempDir()
	if err := selected.validateProject(projects); err == nil {
		t.Fatal("accepted foreign root")
	}
}

// TestSelectionRejectsDriftBeforeApproval covers every identity and revision fence returned by a daemon replay.
func TestSelectionRejectsDriftBeforeApproval(t *testing.T) {
	selected := selection{"project-fixture", "operation-fixture", "intent-fixture", 5, fixtureDirectory(t)}
	pending := pendingRemoval(t)
	if err := selected.validatePending(pending); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*control.ProjectUnregistration){
		func(p *control.ProjectUnregistration) { p.Operation.ID = "operation-other" },
		func(p *control.ProjectUnregistration) { p.Operation.ProjectID = "project-other" },
		func(p *control.ProjectUnregistration) { p.Operation.IntentID = "intent-other" },
		func(p *control.ProjectUnregistration) { p.Operation.Kind = domain.OperationKindProjectStart },
		func(p *control.ProjectUnregistration) { p.Revision++ },
		func(p *control.ProjectUnregistration) { p.Revision = 0 },
		func(p *control.ProjectUnregistration) { p.Operation.State = domain.OperationRunning },
	} {
		copy := pending
		mutate(&copy)
		if err := selected.validatePending(copy); err == nil {
			t.Fatalf("accepted drift: %#v", copy)
		}
	}
}

// TestSelectionRequiresSuccessfulCorrelatedConfirmation prevents helper success alone from claiming daemon completion.
func TestSelectionRequiresSuccessfulCorrelatedConfirmation(t *testing.T) {
	selected := selection{"project-fixture", "operation-fixture", "intent-fixture", 5, fixtureDirectory(t)}
	pending := pendingRemoval(t)
	operation, err := pending.Operation.Transition(domain.OperationRunning, "resuming", pending.Operation.RequestedAt.Add(3*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationSucceeded, "removed", pending.Operation.RequestedAt.Add(4*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := control.ProjectUnregisterApprovalConfirmation{Operation: operation, Revision: 7}
	outcome := projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
	if err := selected.validateOutcome(outcome); err != nil {
		t.Fatal(err)
	}
	if err := selected.validateOutcome(projectapproval.Outcome{State: projectapproval.Declined}); err == nil {
		t.Fatal("accepted declined helper")
	}
	if err := selected.validateOutcome(projectapproval.Outcome{State: projectapproval.Succeeded}); err == nil {
		t.Fatal("accepted absent confirmation")
	}
	outcome.HelperFailure = &projectapproval.HelperFailure{}
	if err := selected.validateOutcome(outcome); err == nil {
		t.Fatal("accepted helper failure")
	}
	outcome.HelperFailure = nil
	for _, mutate := range []func(*control.ProjectUnregisterApprovalConfirmation){
		func(c *control.ProjectUnregisterApprovalConfirmation) { c.Operation.ID = "operation-other" },
		func(c *control.ProjectUnregisterApprovalConfirmation) { c.Operation.ProjectID = "project-other" },
		func(c *control.ProjectUnregisterApprovalConfirmation) { c.Operation.IntentID = "intent-other" },
		func(c *control.ProjectUnregisterApprovalConfirmation) { c.Revision = selected.revision },
		func(c *control.ProjectUnregisterApprovalConfirmation) { c.Revision = 0 },
	} {
		copy := confirmation
		mutate(&copy)
		outcome.Confirmation = &copy
		if err := selected.validateOutcome(outcome); err == nil {
			t.Fatalf("accepted uncorrelated completion: %#v", copy)
		}
	}
}
