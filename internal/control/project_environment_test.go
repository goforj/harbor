package control

import (
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestProjectEnvironmentRequestValidationRejectsUnfencedOrBroadFileWrites verifies the editor cannot become a generic project file writer.
func TestProjectEnvironmentRequestValidationRejectsUnfencedOrBroadFileWrites(t *testing.T) {
	revision := strings.Repeat("a", 64)
	valid := SaveProjectEnvironmentFileRequest{
		ProjectID: domain.ProjectID("project-alpha"),
		Name:      ".env.local",
		Contents:  "APP_ENV=local\n",
		Revision:  revision,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() canonical request error = %v", err)
	}
	repository := SaveProjectEnvironmentFileRequest{
		ProjectID: domain.ProjectID("project-alpha"),
		Name:      ".harbor.yml",
		Contents:  "version: 1\nenvironment: {}\n",
	}
	if err := repository.Validate(); err != nil {
		t.Fatalf("Validate() new repository config error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*SaveProjectEnvironmentFileRequest)
	}{
		{name: "missing project", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.ProjectID = "" }},
		{name: "unrelated file", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Name = "config.json" }},
		{name: "traversal", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Name = "../.env" }},
		{name: "empty variant", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Name = ".env." }},
		{name: "missing revision", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Revision = "" }},
		{name: "uppercase revision", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Revision = strings.Repeat("A", 64) }},
		{name: "invalid UTF-8", mutate: func(request *SaveProjectEnvironmentFileRequest) { request.Contents = string([]byte{0xff}) }},
		{name: "oversized contents", mutate: func(request *SaveProjectEnvironmentFileRequest) {
			request.Contents = strings.Repeat("x", maximumProjectEnvironmentFileBytes+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", request)
			}
		})
	}
}

// TestDecodeProjectEnvironmentRequestsRequiresExactObjects verifies hidden or ambiguous JSON fields fail before authority dispatch.
func TestDecodeProjectEnvironmentRequestsRequiresExactObjects(t *testing.T) {
	revision := strings.Repeat("a", 64)
	if _, err := decodeProjectEnvironmentRequest([]byte(`{"project_id":"project-alpha"}`)); err != nil {
		t.Fatalf("decode canonical inspection request: %v", err)
	}
	if _, err := decodeSaveProjectEnvironmentFileRequest([]byte(
		`{"project_id":"project-alpha","name":".env","contents":"APP_ENV=local\n","revision":"` + revision + `"}`,
	)); err != nil {
		t.Fatalf("decode canonical save request: %v", err)
	}

	for _, payload := range []string{
		`{"project_id":"project-alpha","extra":true}`,
		`{"project_id":"project-alpha","project_id":"project-beta"}`,
		`{"project_id":"project-alpha"} {}`,
		`[]`,
		``,
	} {
		if _, err := decodeProjectEnvironmentRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeProjectEnvironmentRequest(%q) succeeded", payload)
		}
	}
}

// TestProjectEnvironmentValidationRequiresCanonicalSortedCollections verifies responses cannot smuggle duplicate or inconsistent values.
func TestProjectEnvironmentValidationRequiresCanonicalSortedCollections(t *testing.T) {
	revision := strings.Repeat("a", 64)
	valid := ProjectEnvironment{
		ProjectID:          domain.ProjectID("project-alpha"),
		OverridesAvailable: true,
		Overrides: []ProjectEnvironmentVariable{
			{Name: "APP_HOST", Value: "127.77.0.10", Source: "project.address"},
			{Name: "DB_HOST", Value: "127.77.0.10", Source: "project.address"},
		},
		Bindings: []ProjectEnvironmentBinding{
			{Name: "APP_HOST", Source: "project.address"},
			{Name: "DB_HOST", Source: "project.address"},
		},
		BindingsRevision: revision,
		Files: []ProjectEnvironmentFile{
			{Name: ".env", Contents: "APP_ENV=local\n", Revision: revision},
			{Name: ".env.local", Contents: "APP_DEBUG=true\n", Revision: revision},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() canonical environment error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ProjectEnvironment)
	}{
		{name: "nil overrides", mutate: func(environment *ProjectEnvironment) { environment.Overrides = nil }},
		{name: "nil bindings", mutate: func(environment *ProjectEnvironment) { environment.Bindings = nil }},
		{name: "nil files", mutate: func(environment *ProjectEnvironment) { environment.Files = nil }},
		{name: "unsorted overrides", mutate: func(environment *ProjectEnvironment) {
			environment.Overrides[0], environment.Overrides[1] = environment.Overrides[1], environment.Overrides[0]
		}},
		{name: "unsorted files", mutate: func(environment *ProjectEnvironment) {
			environment.Files[0], environment.Files[1] = environment.Files[1], environment.Files[0]
		}},
		{name: "unsorted bindings", mutate: func(environment *ProjectEnvironment) {
			environment.Bindings[0], environment.Bindings[1] = environment.Bindings[1], environment.Bindings[0]
		}},
		{name: "missing binding revision", mutate: func(environment *ProjectEnvironment) { environment.BindingsRevision = "" }},
		{name: "unsupported binding source", mutate: func(environment *ProjectEnvironment) { environment.Bindings[0].Source = "project.port" }},
		{name: "error while available", mutate: func(environment *ProjectEnvironment) { environment.OverrideError = "failed" }},
		{name: "values while unavailable", mutate: func(environment *ProjectEnvironment) { environment.OverridesAvailable = false }},
		{name: "missing override source", mutate: func(environment *ProjectEnvironment) { environment.Overrides[0].Source = "" }},
		{name: "noncanonical override source", mutate: func(environment *ProjectEnvironment) { environment.Overrides[0].Source = "project/address" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := valid
			environment.Overrides = append([]ProjectEnvironmentVariable(nil), valid.Overrides...)
			environment.Bindings = append([]ProjectEnvironmentBinding(nil), valid.Bindings...)
			environment.Files = append([]ProjectEnvironmentFile(nil), valid.Files...)
			test.mutate(&environment)
			if err := environment.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", environment)
			}
		})
	}
}
