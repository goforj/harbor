package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/desktop/internal/projectreadiness"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectcreation"
)

// TestProjectPrerequisitesUsesAuthenticatedRegistration proves frontend IDs cannot introduce arbitrary checkout paths.
func TestProjectPrerequisitesUsesAuthenticatedRegistration(t *testing.T) {
	app, client := connectedTestApp()
	project := client.snapshot.Projects[0]
	called := false
	adapter := app.projectCreators[projectcreation.GoForj.ID]
	adapter.checkProject = func(_ context.Context, observed domain.ProjectSnapshot) projectreadiness.Report {
		called = true
		if !reflect.DeepEqual(project, observed) {
			t.Fatalf("project = %+v", observed)
		}
		return projectreadiness.Report{ProviderID: "goforj", ProjectID: string(observed.ID), Checks: []projectreadiness.Check{{ID: "go", Status: "ready"}}}
	}
	app.projectCreators[projectcreation.GoForj.ID] = adapter
	result, err := app.ProjectPrerequisites(string(project.ID))
	if err != nil || !called || result.ProjectID != string(project.ID) || client.startRequest != (control.StartProjectRequest{}) {
		t.Fatalf("result = %+v, %v", result, err)
	}
	for _, id := range []string{"", "../outside", "missing-project"} {
		called = false
		if _, err := app.ProjectPrerequisites(id); err == nil || called {
			t.Fatalf("invalid ID %q reached inspection", id)
		}
	}
	client.snapshotErr = errors.New("snapshot unavailable")
	if _, err := app.ProjectPrerequisites(string(project.ID)); err == nil {
		t.Fatal("snapshot failure hidden")
	}
	client.snapshotErr = nil
	client.snapshot.Projects[0].Path = "relative"
	if _, err := app.ProjectPrerequisites(string(project.ID)); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
}

// TestCreationPrerequisitesBelongToTheirAdapter proves the shared desktop contract does not assume GoForj form fields.
func TestCreationPrerequisitesBelongToTheirAdapter(t *testing.T) {
	app, _ := connectedTestApp()
	adapter := app.projectCreators[projectcreation.GoForj.ID]
	adapter.checkCreation = func(_ context.Context, options map[string]string) projectreadiness.Report {
		if options["other-stack-option"] != "chosen" {
			t.Fatal(options)
		}
		return projectreadiness.Report{ProviderID: "other-stack", Checks: []projectreadiness.Check{{ID: "tool", Status: "ready"}}}
	}
	app.projectCreators["other-stack"] = adapter
	request := projectcreation.Request{ProviderID: "other-stack", Options: map[string]string{"other-stack-option": "chosen"}}
	if result, err := app.ProjectCreationPrerequisites(request); err != nil || result.ProviderID != "other-stack" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	request.ProviderID = "unknown"
	if _, err := app.ProjectCreationPrerequisites(request); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	request.ProviderID = "other-stack"
	request.Options = map[string]string{strings.Repeat("x", 49): "value"}
	if _, err := app.ProjectCreationPrerequisites(request); err == nil {
		t.Fatal("oversized option key accepted")
	}
	request.Options = map[string]string{"value": strings.Repeat("x", 4097)}
	if _, err := app.ProjectCreationPrerequisites(request); err == nil {
		t.Fatal("oversized option value accepted")
	}
	request.Options = map[string]string{}
	for index := range 65 {
		request.Options[strings.Repeat("x", index+1)] = "value"
	}
	if _, err := app.ProjectCreationPrerequisites(request); err == nil {
		t.Fatal("too many options accepted")
	}
	app.client = nil
	if _, err := app.ProjectCreationPrerequisites(projectcreation.Request{}); err == nil {
		t.Fatal("disconnected inspection accepted")
	}
}
