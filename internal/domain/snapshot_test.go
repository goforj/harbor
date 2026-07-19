package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSnapshotValidateAcceptsCanonicalOwnershipTree verifies the complete valid contract.
func TestSnapshotValidateAcceptsCanonicalOwnershipTree(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot(t)
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestSnapshotJSONKeepsOneOwnershipTree ensures aggregate views cannot become competing copies of project state.
func TestSnapshotJSONKeepsOneOwnershipTree(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(validSnapshot(t))
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	encoded := string(data)
	if strings.Count(encoded, `"services"`) != 1 {
		t.Fatalf("snapshot JSON services key count = %d, want one canonical project-owned collection", strings.Count(encoded, `"services"`))
	}
	if strings.Contains(encoded, `"logs"`) {
		t.Fatal("snapshot JSON unexpectedly embeds logs")
	}
	if !strings.Contains(encoded, `"recent_resource_ids"`) {
		t.Fatal("snapshot JSON does not retain recent resources by reference")
	}
}

// TestResourceOwnerValidateRequiresExactlyOneTypedOwner covers every invalid union shape.
func TestResourceOwnerValidateRequiresExactlyOneTypedOwner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		owner ResourceOwner
		valid bool
	}{
		{name: "App", owner: ResourceOwner{Kind: ResourceOwnedByApp, AppID: "app"}, valid: true},
		{name: "service", owner: ResourceOwner{Kind: ResourceOwnedByService, ServiceID: "mysql"}, valid: true},
		{name: "unknown kind", owner: ResourceOwner{Kind: "project", AppID: "app"}},
		{name: "App missing ID", owner: ResourceOwner{Kind: ResourceOwnedByApp}},
		{name: "App with service", owner: ResourceOwner{Kind: ResourceOwnedByApp, AppID: "app", ServiceID: "mysql"}},
		{name: "service missing ID", owner: ResourceOwner{Kind: ResourceOwnedByService}},
		{name: "service with App", owner: ResourceOwner{Kind: ResourceOwnedByService, AppID: "app", ServiceID: "mysql"}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.owner.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want invalid owner error")
			}
		})
	}
}

// TestServiceOwnershipAndSelectionValidate covers every stable ownership axis.
func TestServiceOwnershipAndSelectionValidate(t *testing.T) {
	t.Parallel()

	for _, owner := range []ServiceOwner{ServiceOwnerCompose, ServiceOwnerExternal} {
		if err := owner.Validate(); err != nil {
			t.Errorf("ServiceOwner(%q).Validate() error = %v", owner, err)
		}
	}
	if err := ServiceOwner("harbor").Validate(); err == nil {
		t.Fatal("ServiceOwner(harbor).Validate() error = nil, want unknown owner error")
	}
	for _, selection := range []ServiceSelection{ServiceSelected, ServiceAvailable} {
		if err := selection.Validate(); err != nil {
			t.Errorf("ServiceSelection(%q).Validate() error = %v", selection, err)
		}
	}
	if err := ServiceSelection("disabled").Validate(); err == nil {
		t.Fatal("ServiceSelection(disabled).Validate() error = nil, want unknown selection error")
	}
}

// TestSnapshotEntityValidatorsRejectInvalidFields exercises every summary boundary independently.
func TestSnapshotEntityValidatorsRejectInvalidFields(t *testing.T) {
	t.Parallel()

	validApp := AppSnapshot{ID: "app", Name: "API", State: EntityReady, Active: true, Required: true}
	validService := ServiceSnapshot{ID: "mysql", Name: "MySQL", Kind: "database", State: EntityReady, Owner: ServiceOwnerCompose, Selection: ServiceSelected, Required: true}
	validResource := validResource()
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "App ID", validate: func() error { value := validApp; value.ID = ""; return value.Validate() }},
		{name: "App name", validate: func() error { value := validApp; value.Name = ""; return value.Validate() }},
		{name: "App state", validate: func() error { value := validApp; value.State = "unknown"; return value.Validate() }},
		{name: "service ID", validate: func() error { value := validService; value.ID = ""; return value.Validate() }},
		{name: "service name", validate: func() error { value := validService; value.Name = ""; return value.Validate() }},
		{name: "service kind", validate: func() error { value := validService; value.Kind = ""; return value.Validate() }},
		{name: "service state", validate: func() error { value := validService; value.State = "unknown"; return value.Validate() }},
		{name: "service owner", validate: func() error { value := validService; value.Owner = "unknown"; return value.Validate() }},
		{name: "service selection", validate: func() error { value := validService; value.Selection = "unknown"; return value.Validate() }},
		{name: "resource ID", validate: func() error { value := validResource; value.ID = ""; return value.Validate() }},
		{name: "resource name", validate: func() error { value := validResource; value.Name = ""; return value.Validate() }},
		{name: "resource kind", validate: func() error { value := validResource; value.Kind = ""; return value.Validate() }},
		{name: "resource owner", validate: func() error { value := validResource; value.Owner = ResourceOwner{}; return value.Validate() }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}

// TestProjectSnapshotValidateRejectsBrokenOwnership covers duplicate identities and dangling resource owners.
func TestProjectSnapshotValidateRejectsBrokenOwnership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProjectSnapshot)
		want   string
	}{
		{name: "duplicate App", mutate: func(project *ProjectSnapshot) { project.Apps = append(project.Apps, project.Apps[0]) }, want: "duplicate App ID"},
		{name: "duplicate service", mutate: func(project *ProjectSnapshot) { project.Services = append(project.Services, project.Services[0]) }, want: "duplicate service ID"},
		{name: "duplicate resource", mutate: func(project *ProjectSnapshot) { project.Resources = append(project.Resources, project.Resources[0]) }, want: "duplicate resource ID"},
		{name: "unknown App owner", mutate: func(project *ProjectSnapshot) { project.Resources[0].Owner.AppID = "missing" }, want: "unknown App"},
		{name: "unknown service owner", mutate: func(project *ProjectSnapshot) { project.Resources[1].Owner.ServiceID = "missing" }, want: "unknown service"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := validSnapshot(t)
			project := &snapshot.Projects[0]
			test.mutate(project)
			err := project.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectSnapshotValidateRejectsInvalidProjectFields covers the project-owned part of the contract.
func TestProjectSnapshotValidateRejectsInvalidProjectFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProjectSnapshot)
	}{
		{name: "ID", mutate: func(project *ProjectSnapshot) { project.ID = "" }},
		{name: "name", mutate: func(project *ProjectSnapshot) { project.Name = "" }},
		{name: "path", mutate: func(project *ProjectSnapshot) { project.Path = "" }},
		{name: "slug", mutate: func(project *ProjectSnapshot) { project.Slug = "" }},
		{name: "state", mutate: func(project *ProjectSnapshot) { project.State = "unknown" }},
		{name: "updated time", mutate: func(project *ProjectSnapshot) { project.UpdatedAt = time.Time{} }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project := validSnapshot(t).Projects[0]
			test.mutate(&project)
			if err := project.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}

// TestProjectSnapshotValidateRequiresCanonicalDNSLabelSlug keeps persisted project domains unique after DNS normalization.
func TestProjectSnapshotValidateRequiresCanonicalDNSLabelSlug(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"a", "orders", "orders-api", "orders2-api3", "123"} {
		slug := slug
		t.Run("valid "+slug, func(t *testing.T) {
			t.Parallel()
			project := validSnapshot(t).Projects[0]
			project.Slug = slug
			if err := project.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	invalid := map[string]string{
		"empty":           "",
		"uppercase":       "Orders",
		"underscore":      "orders_api",
		"dot":             "orders.api",
		"leading hyphen":  "-orders",
		"trailing hyphen": "orders-",
		"Unicode":         "ordérs",
		"too long":        strings.Repeat("a", 64),
	}
	for name, slug := range invalid {
		name := name
		slug := slug
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			project := validSnapshot(t).Projects[0]
			project.Slug = slug
			if err := project.Validate(); err == nil || !strings.Contains(err.Error(), "project slug") {
				t.Fatalf("Validate() error = %v, want project slug error", err)
			}
		})
	}
}

// TestProjectSnapshotValidateRejectsNilCollections keeps every project collection encoded as an array.
func TestProjectSnapshotValidateRejectsNilCollections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProjectSnapshot)
		want   string
	}{
		{name: "Apps", mutate: func(project *ProjectSnapshot) { project.Apps = nil }, want: "apps"},
		{name: "services", mutate: func(project *ProjectSnapshot) { project.Services = nil }, want: "services"},
		{name: "resources", mutate: func(project *ProjectSnapshot) { project.Resources = nil }, want: "resources"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			project := validSnapshot(t).Projects[0]
			test.mutate(&project)
			err := project.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want nil %s collection error", err, test.want)
			}
		})
	}
}

// TestResourceSnapshotValidateRestrictsOpenableURLs prevents credentials and unsafe schemes from entering the browser-opening contract.
func TestResourceSnapshotValidateRestrictsOpenableURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource ResourceSnapshot
		valid    bool
	}{
		{name: "HTTP", resource: validResource(), valid: true},
		{name: "HTTPS", resource: func() ResourceSnapshot {
			resource := validResource()
			resource.URL = "https://orders.test/swagger"
			return resource
		}(), valid: true},
		{name: "relative", resource: func() ResourceSnapshot { resource := validResource(); resource.URL = "/swagger"; return resource }()},
		{name: "file", resource: func() ResourceSnapshot {
			resource := validResource()
			resource.URL = "file:///tmp/project"
			return resource
		}()},
		{name: "credentials", resource: func() ResourceSnapshot {
			resource := validResource()
			resource.URL = "https://user:pass@orders.test"
			return resource
		}()},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.resource.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want invalid URL error")
			}
		})
	}
}

// TestSnapshotValidateRejectsDuplicateAndDanglingReferences covers snapshot-level identity consistency.
func TestSnapshotValidateRejectsDuplicateAndDanglingReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{name: "schema", mutate: func(snapshot *Snapshot) { snapshot.SchemaVersion++ }, want: "schema version"},
		{name: "capture time", mutate: func(snapshot *Snapshot) { snapshot.CapturedAt = time.Time{} }, want: "capture time"},
		{name: "invalid project", mutate: func(snapshot *Snapshot) { snapshot.Projects[0].Name = "" }, want: "project"},
		{name: "invalid operation", mutate: func(snapshot *Snapshot) { snapshot.Operations[0].Kind = "" }, want: "operation"},
		{name: "operation for unknown project", mutate: func(snapshot *Snapshot) { snapshot.Operations[0].ProjectID = "missing" }, want: "unknown project"},
		{name: "duplicate project", mutate: func(snapshot *Snapshot) { snapshot.Projects = append(snapshot.Projects, snapshot.Projects[0]) }, want: "duplicate project ID"},
		{name: "duplicate operation", mutate: func(snapshot *Snapshot) { snapshot.Operations = append(snapshot.Operations, snapshot.Operations[0]) }, want: "duplicate operation ID"},
		{name: "duplicate intent", mutate: func(snapshot *Snapshot) {
			operation := snapshot.Operations[0]
			operation.ID = "operation-02"
			snapshot.Operations = append(snapshot.Operations, operation)
		}, want: "duplicate intent ID"},
		{name: "duplicate recent resource", mutate: func(snapshot *Snapshot) {
			snapshot.RecentResourceIDs = append(snapshot.RecentResourceIDs, snapshot.RecentResourceIDs[0])
		}, want: "duplicate recent resource"},
		{name: "unknown recent project", mutate: func(snapshot *Snapshot) { snapshot.RecentResourceIDs[0].ProjectID = "missing" }, want: "unknown project"},
		{name: "unknown recent resource", mutate: func(snapshot *Snapshot) { snapshot.RecentResourceIDs[0].ResourceID = "missing" }, want: "unknown resource"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := validSnapshot(t)
			test.mutate(&snapshot)
			err := snapshot.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestSnapshotValidateRejectsNilCollections keeps every top-level collection encoded as an array.
func TestSnapshotValidateRejectsNilCollections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{name: "projects", mutate: func(snapshot *Snapshot) { snapshot.Projects = nil }, want: "projects"},
		{name: "operations", mutate: func(snapshot *Snapshot) { snapshot.Operations = nil }, want: "operations"},
		{name: "recent resources", mutate: func(snapshot *Snapshot) { snapshot.RecentResourceIDs = nil }, want: "recent_resource_ids"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := validSnapshot(t)
			test.mutate(&snapshot)
			err := snapshot.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want nil %s collection error", err, test.want)
			}
		})
	}
}

// TestSnapshotValidateBoundsOnlyTerminalHistory keeps replacement payloads finite without imposing a ceiling on live daemon work.
func TestSnapshotValidateBoundsOnlyTerminalHistory(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot(t)
	for index := 0; index < SnapshotRecentTerminalOperationLimit; index++ {
		snapshot.Operations = append(snapshot.Operations, terminalSnapshotOperation(t, index))
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() at terminal operation limit error = %v", err)
	}
	snapshot.Operations = append(snapshot.Operations, terminalSnapshotOperation(t, SnapshotRecentTerminalOperationLimit))
	if err := snapshot.Validate(); err == nil || !strings.Contains(err.Error(), "terminal operations") {
		t.Fatalf("Validate() above terminal operation limit error = %v", err)
	}

	snapshot = validSnapshot(t)
	for index := 0; index <= SnapshotRecentTerminalOperationLimit; index++ {
		requestedAt := snapshot.CapturedAt.Add(time.Duration(index+1) * time.Minute)
		operation, err := NewOperation(
			OperationID(fmt.Sprintf("operation-active-%02d", index)),
			IntentID(fmt.Sprintf("intent-active-%02d", index)),
			"maintenance.run",
			"",
			requestedAt,
		)
		if err != nil {
			t.Fatalf("NewOperation() active %d error = %v", index, err)
		}
		snapshot.Operations = append(snapshot.Operations, operation)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() with active operations above terminal limit error = %v", err)
	}
}

// TestSnapshotValidateAllowsTerminalOperationForRemovedProject preserves unregister outcomes after their project projection is retired.
func TestSnapshotValidateAllowsTerminalOperationForRemovedProject(t *testing.T) {
	t.Parallel()

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation(
		"operation-unregister",
		"intent-unregister",
		OperationKindProjectUnregister,
		"project-removed",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(OperationRunning, "removing project", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("Transition() running error = %v", err)
	}
	operation, err = operation.Transition(OperationSucceeded, "project removed", requestedAt.Add(2*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition() succeeded error = %v", err)
	}
	snapshot := Snapshot{
		SchemaVersion:     SnapshotSchemaVersion,
		Sequence:          3,
		CapturedAt:        requestedAt.Add(3 * time.Second),
		Projects:          []ProjectSnapshot{},
		Operations:        []Operation{operation},
		RecentResourceIDs: []ResourceRef{},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() terminal unregister error = %v", err)
	}
}

// TestResourceRefValidateRequiresBothScopedIDs prevents ambiguous cross-project lookup.
func TestResourceRefValidateRequiresBothScopedIDs(t *testing.T) {
	t.Parallel()

	if err := (ResourceRef{ProjectID: "project-01", ResourceID: "resource-01"}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := (ResourceRef{ResourceID: "resource-01"}).Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing project error")
	}
	if err := (ResourceRef{ProjectID: "project-01"}).Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing resource error")
	}
}

// validSnapshot creates a complete fixture whose nested references are internally consistent.
func validSnapshot(t *testing.T) Snapshot {
	t.Helper()

	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	operation, err := NewOperation("operation-01", "intent-01", "project.favorite.set", "project-01", now)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		Sequence:      42,
		CapturedAt:    now,
		Projects: []ProjectSnapshot{
			{
				ID:        "project-01",
				Name:      "orders-api",
				Path:      "/work/orders-api",
				Slug:      "orders",
				State:     ProjectReady,
				Favorite:  true,
				UpdatedAt: now,
				Apps: []AppSnapshot{
					{ID: "app", Name: "API", State: EntityReady, Active: true, Required: true},
				},
				Services: []ServiceSnapshot{
					{ID: "mysql", Name: "MySQL", Kind: "database", State: EntityReady, Owner: ServiceOwnerCompose, Selection: ServiceSelected, Required: true},
					{ID: "mailpit", Name: "Mailpit", Kind: "mail", State: EntityReady, Owner: ServiceOwnerCompose, Selection: ServiceSelected},
				},
				Resources: []ResourceSnapshot{
					validResource(),
					{ID: "mailpit", Name: "Mailpit", Kind: "mail", Owner: ResourceOwner{Kind: ResourceOwnedByService, ServiceID: "mysql"}, URL: "https://mail.orders.test"},
				},
			},
		},
		Operations: []Operation{operation},
		RecentResourceIDs: []ResourceRef{
			{ProjectID: "project-01", ResourceID: "api-reference"},
		},
	}
}

// terminalSnapshotOperation creates one valid completed operation with distinct durable identities.
func terminalSnapshotOperation(t *testing.T, index int) Operation {
	t.Helper()

	requestedAt := time.Date(2026, time.July, 18, 13, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute)
	operation, err := NewOperation(
		OperationID(fmt.Sprintf("operation-terminal-%02d", index)),
		IntentID(fmt.Sprintf("intent-terminal-%02d", index)),
		"maintenance.run",
		"",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() terminal %d error = %v", index, err)
	}
	operation, err = operation.Transition(OperationRunning, "running", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("Transition() terminal %d running error = %v", index, err)
	}
	operation, err = operation.Transition(OperationSucceeded, "complete", requestedAt.Add(2*time.Second), nil)
	if err != nil {
		t.Fatalf("Transition() terminal %d succeeded error = %v", index, err)
	}
	return operation
}

// validResource creates an App-owned browser resource used across validation cases.
func validResource() ResourceSnapshot {
	return ResourceSnapshot{
		ID:    "api-reference",
		Name:  "API Reference",
		Kind:  "api-reference",
		Owner: ResourceOwner{Kind: ResourceOwnedByApp, AppID: "app"},
		URL:   "http://orders.test/swagger",
	}
}
