package state

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// TestProjectRecordValidateRejectsInvalidValues verifies aggregates and revisions cannot bypass domain and model limits.
func TestProjectRecordValidateRejectsInvalidValues(t *testing.T) {
	valid := projectConversionSnapshot()
	tests := []struct {
		name   string
		mutate func(*ProjectRecord)
	}{
		{name: "invalid project", mutate: func(record *ProjectRecord) { record.Project.Name = "" }},
		{name: "zero revision", mutate: func(record *ProjectRecord) { record.Revision = 0 }},
		{name: "overflowing revision", mutate: func(record *ProjectRecord) { record.Revision = domain.Sequence(math.MaxUint64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := ProjectRecord{Project: valid, Revision: 1}
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatalf("invalid project record %#v unexpectedly validated", record)
			}
		})
	}
}

// TestRecentResourceRecordValidateRejectsInvalidValues verifies scoped identity, time, and sequence constraints together.
func TestRecentResourceRecordValidateRejectsInvalidValues(t *testing.T) {
	valid := RecentResourceRecord{
		Reference:  domain.ResourceRef{ProjectID: "project-one", ResourceID: "resource-one"},
		AccessedAt: projectConversionTime(),
		Sequence:   1,
	}
	tests := []struct {
		name   string
		mutate func(*RecentResourceRecord)
	}{
		{name: "invalid project", mutate: func(record *RecentResourceRecord) { record.Reference.ProjectID = "" }},
		{name: "invalid resource", mutate: func(record *RecentResourceRecord) { record.Reference.ResourceID = "" }},
		{name: "zero time", mutate: func(record *RecentResourceRecord) { record.AccessedAt = time.Time{} }},
		{name: "non-UTC time", mutate: func(record *RecentResourceRecord) {
			record.AccessedAt = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.FixedZone("local", 3600))
		}},
		{name: "zero sequence", mutate: func(record *RecentResourceRecord) { record.Sequence = 0 }},
		{name: "overflowing sequence", mutate: func(record *RecentResourceRecord) { record.Sequence = domain.Sequence(math.MaxUint64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatalf("invalid recent resource record %#v unexpectedly validated", record)
			}
		})
	}
}

// TestProjectConversionRoundTripPreservesOrderAndOwners verifies normalized rows reconstruct the same canonical aggregate.
func TestProjectConversionRoundTripPreservesOrderAndOwners(t *testing.T) {
	project := projectConversionSnapshot()
	projectRow, appRows, serviceRows, resourceRows, err := projectModelsFromDomain(project, 9)
	if err != nil {
		t.Fatalf("convert project to models: %v", err)
	}
	if projectRow.Id != 0 || projectRow.Revision != 9 {
		t.Fatalf("project upsert model = %#v", projectRow)
	}
	if got := []string{appRows[0].AppId, appRows[1].AppId}; !reflect.DeepEqual(got, []string{"app-two", "app-one"}) {
		t.Fatalf("App model order = %v", got)
	}
	if got := []string{serviceRows[0].ServiceId, serviceRows[1].ServiceId}; !reflect.DeepEqual(got, []string{"service-two", "service-one"}) {
		t.Fatalf("service model order = %v", got)
	}
	if !resourceRows[0].OwnerAppId.Valid || resourceRows[0].OwnerServiceId.Valid {
		t.Fatalf("App resource owner columns = %#v", resourceRows[0])
	}
	if resourceRows[1].OwnerAppId.Valid || !resourceRows[1].OwnerServiceId.Valid {
		t.Fatalf("service resource owner columns = %#v", resourceRows[1])
	}

	projectRow.Id = 1
	for index := range appRows {
		appRows[index].Id = index + 1
	}
	for index := range serviceRows {
		serviceRows[index].Id = index + 1
	}
	for index := range resourceRows {
		resourceRows[index].Id = index + 1
	}
	record, err := projectRecordFromModels(projectRow, appRows, serviceRows, resourceRows)
	if err != nil {
		t.Fatalf("convert project from models: %v", err)
	}
	if record.Revision != 9 || !reflect.DeepEqual(record.Project, project) {
		t.Fatalf("round-trip record = %#v, want %#v at revision 9", record.Project, project)
	}
}

// TestProjectRecordFromModelsInitializesEmptyCollections verifies empty queries still produce canonical JSON arrays.
func TestProjectRecordFromModelsInitializesEmptyCollections(t *testing.T) {
	project := projectConversionSnapshot()
	project.Apps = []domain.AppSnapshot{}
	project.Services = []domain.ServiceSnapshot{}
	project.Resources = []domain.ResourceSnapshot{}
	projectRow, _, _, _, err := projectModelsFromDomain(project, 1)
	if err != nil {
		t.Fatalf("convert empty project: %v", err)
	}
	projectRow.Id = 1
	record, err := projectRecordFromModels(projectRow, nil, nil, nil)
	if err != nil {
		t.Fatalf("convert empty project rows: %v", err)
	}
	if record.Project.Apps == nil || record.Project.Services == nil || record.Project.Resources == nil {
		t.Fatalf("empty project collections = %#v", record.Project)
	}
}

// TestProjectRecordFromModelsRejectsCorruptProject verifies persisted top-level fields fail through an identified corruption boundary.
func TestProjectRecordFromModelsRejectsCorruptProject(t *testing.T) {
	valid, apps, services, resources := projectConversionModels(t)
	tests := []struct {
		name   string
		mutate func(*models.Project)
	}{
		{name: "nonpositive database ID", mutate: func(row *models.Project) { row.Id = 0 }},
		{name: "zero revision", mutate: func(row *models.Project) { row.Revision = 0 }},
		{name: "zero update time", mutate: func(row *models.Project) { row.UpdatedAt = time.Time{} }},
		{name: "non-UTC update time", mutate: func(row *models.Project) {
			row.UpdatedAt = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.FixedZone("local", -18000))
		}},
		{name: "invalid project ID", mutate: func(row *models.Project) { row.ProjectId = " project " }},
		{name: "invalid name", mutate: func(row *models.Project) { row.Name = "" }},
		{name: "invalid path", mutate: func(row *models.Project) { row.Path = " /project " }},
		{name: "invalid slug", mutate: func(row *models.Project) { row.Slug = " project-one " }},
		{name: "invalid state", mutate: func(row *models.Project) { row.State = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			_, err := projectRecordFromModels(row, apps, services, resources)
			assertProjectConversionCorruption(t, err, "project", row.ProjectId)
		})
	}
	t.Run("empty identity retains database ID", func(t *testing.T) {
		row := valid
		row.ProjectId = ""
		_, err := projectRecordFromModels(row, apps, services, resources)
		assertProjectConversionCorruption(t, err, "project", "database-id:1")
	})
}

// TestProjectRecordFromModelsRejectsCorruptApps verifies each App row is scoped and domain-valid before aggregation.
func TestProjectRecordFromModelsRejectsCorruptApps(t *testing.T) {
	project, apps, services, resources := projectConversionModels(t)
	tests := []struct {
		name   string
		mutate func(*models.ProjectApp)
	}{
		{name: "nonpositive database ID", mutate: func(row *models.ProjectApp) { row.Id = 0 }},
		{name: "wrong project scope", mutate: func(row *models.ProjectApp) { row.ProjectId = "project-two" }},
		{name: "invalid App ID", mutate: func(row *models.ProjectApp) { row.AppId = " app " }},
		{name: "invalid name", mutate: func(row *models.ProjectApp) { row.Name = "" }},
		{name: "invalid state", mutate: func(row *models.ProjectApp) { row.State = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := append([]models.ProjectApp(nil), apps...)
			test.mutate(&rows[0])
			_, err := projectRecordFromModels(project, rows, services, resources)
			assertProjectConversionCorruption(t, err, "project App", rows[0].AppId)
		})
	}
}

// TestProjectRecordFromModelsRejectsCorruptServices verifies each service row is scoped and domain-valid before aggregation.
func TestProjectRecordFromModelsRejectsCorruptServices(t *testing.T) {
	project, apps, services, resources := projectConversionModels(t)
	tests := []struct {
		name   string
		mutate func(*models.ProjectService)
	}{
		{name: "nonpositive database ID", mutate: func(row *models.ProjectService) { row.Id = 0 }},
		{name: "wrong project scope", mutate: func(row *models.ProjectService) { row.ProjectId = "project-two" }},
		{name: "invalid service ID", mutate: func(row *models.ProjectService) { row.ServiceId = " service " }},
		{name: "invalid name", mutate: func(row *models.ProjectService) { row.Name = "" }},
		{name: "invalid kind", mutate: func(row *models.ProjectService) { row.Kind = " service-kind " }},
		{name: "invalid state", mutate: func(row *models.ProjectService) { row.State = "unknown" }},
		{name: "invalid owner", mutate: func(row *models.ProjectService) { row.Owner = "unknown" }},
		{name: "invalid selection", mutate: func(row *models.ProjectService) { row.Selection = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := append([]models.ProjectService(nil), services...)
			test.mutate(&rows[0])
			_, err := projectRecordFromModels(project, apps, rows, resources)
			assertProjectConversionCorruption(t, err, "project service", rows[0].ServiceId)
		})
	}
}

// TestProjectRecordFromModelsRejectsCorruptResources verifies resource scope, fields, and nullable ownership fail closed.
func TestProjectRecordFromModelsRejectsCorruptResources(t *testing.T) {
	project, apps, services, resources := projectConversionModels(t)
	tests := []struct {
		name   string
		mutate func(*models.ProjectResource)
	}{
		{name: "nonpositive database ID", mutate: func(row *models.ProjectResource) { row.Id = 0 }},
		{name: "wrong project scope", mutate: func(row *models.ProjectResource) { row.ProjectId = "project-two" }},
		{name: "invalid resource ID", mutate: func(row *models.ProjectResource) { row.ResourceId = " resource " }},
		{name: "invalid name", mutate: func(row *models.ProjectResource) { row.Name = "" }},
		{name: "invalid kind", mutate: func(row *models.ProjectResource) { row.Kind = " resource-kind " }},
		{name: "invalid URL", mutate: func(row *models.ProjectResource) { row.Url = "file:///tmp/resource" }},
		{name: "unknown owner kind", mutate: func(row *models.ProjectResource) { row.OwnerKind = "unknown" }},
		{name: "App owner missing App", mutate: func(row *models.ProjectResource) { row.OwnerAppId = null.String{} }},
		{name: "App owner has service", mutate: func(row *models.ProjectResource) { row.OwnerServiceId = null.StringFrom("service-one") }},
		{name: "App owner has empty App", mutate: func(row *models.ProjectResource) { row.OwnerAppId = null.StringFrom("") }},
		{name: "service owner missing service", mutate: func(row *models.ProjectResource) {
			row.OwnerKind = string(domain.ResourceOwnedByService)
			row.OwnerAppId = null.String{}
			row.OwnerServiceId = null.String{}
		}},
		{name: "service owner has App", mutate: func(row *models.ProjectResource) {
			row.OwnerKind = string(domain.ResourceOwnedByService)
			row.OwnerServiceId = null.StringFrom("service-one")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := append([]models.ProjectResource(nil), resources...)
			test.mutate(&rows[0])
			_, err := projectRecordFromModels(project, apps, services, rows)
			assertProjectConversionCorruption(t, err, "project resource", rows[0].ResourceId)
		})
	}
}

// TestProjectRecordFromModelsRejectsAggregateCorruption verifies duplicate and dangling child relationships cannot escape reconstruction.
func TestProjectRecordFromModelsRejectsAggregateCorruption(t *testing.T) {
	project, apps, services, resources := projectConversionModels(t)
	t.Run("duplicate App", func(t *testing.T) {
		duplicate := apps[0]
		duplicate.Id = 99
		_, err := projectRecordFromModels(project, append(apps, duplicate), services, resources)
		assertProjectConversionCorruption(t, err, "project", project.ProjectId)
	})
	t.Run("dangling owner", func(t *testing.T) {
		rows := append([]models.ProjectResource(nil), resources...)
		rows[0].OwnerAppId = null.StringFrom("missing-app")
		_, err := projectRecordFromModels(project, apps, services, rows)
		assertProjectConversionCorruption(t, err, "project", project.ProjectId)
	})
}

// TestProjectModelsFromDomainRejectsInvalidInput verifies writes reject malformed aggregates and unsupported revisions.
func TestProjectModelsFromDomainRejectsInvalidInput(t *testing.T) {
	project := projectConversionSnapshot()
	tests := []struct {
		name     string
		revision domain.Sequence
		mutate   func(*domain.ProjectSnapshot)
	}{
		{name: "invalid aggregate", revision: 1, mutate: func(project *domain.ProjectSnapshot) { project.Resources = nil }},
		{name: "zero revision", revision: 0, mutate: func(*domain.ProjectSnapshot) {}},
		{name: "overflowing revision", revision: domain.Sequence(math.MaxUint64), mutate: func(*domain.ProjectSnapshot) {}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := project
			test.mutate(&candidate)
			_, _, _, _, err := projectModelsFromDomain(candidate, test.revision)
			if err == nil {
				t.Fatal("invalid project input unexpectedly converted")
			}
		})
	}
}

// TestRecentResourceConversionRoundTrip verifies recency persistence retains its scoped identity and ordering facts.
func TestRecentResourceConversionRoundTrip(t *testing.T) {
	reference := domain.ResourceRef{ProjectID: "project-one", ResourceID: "resource-one"}
	accessedAt := projectConversionTime()
	row, err := recentResourceModelFromDomain(reference, accessedAt, 12)
	if err != nil {
		t.Fatalf("convert recent resource to model: %v", err)
	}
	if row.Id != 0 {
		t.Fatalf("recent resource upsert ID = %d, want zero", row.Id)
	}
	row.Id = 4
	record, err := recentResourceRecordFromModel(row)
	if err != nil {
		t.Fatalf("convert recent resource from model: %v", err)
	}
	if record.Reference != reference || !record.AccessedAt.Equal(accessedAt) || record.Sequence != 12 {
		t.Fatalf("recent resource round trip = %#v", record)
	}
}

// TestRecentResourceRecordFromModelRejectsCorruptRows verifies every persisted recency field has a typed corruption boundary.
func TestRecentResourceRecordFromModelRejectsCorruptRows(t *testing.T) {
	valid := models.RecentResource{
		Id:         1,
		ProjectId:  "project-one",
		ResourceId: "resource-one",
		AccessedAt: projectConversionTime(),
		Sequence:   1,
	}
	tests := []struct {
		name   string
		mutate func(*models.RecentResource)
	}{
		{name: "nonpositive database ID", mutate: func(row *models.RecentResource) { row.Id = 0 }},
		{name: "invalid project ID", mutate: func(row *models.RecentResource) { row.ProjectId = " project " }},
		{name: "invalid resource ID", mutate: func(row *models.RecentResource) { row.ResourceId = " resource " }},
		{name: "zero access time", mutate: func(row *models.RecentResource) { row.AccessedAt = time.Time{} }},
		{name: "non-UTC access time", mutate: func(row *models.RecentResource) {
			row.AccessedAt = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.FixedZone("local", 3600))
		}},
		{name: "zero sequence", mutate: func(row *models.RecentResource) { row.Sequence = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			_, err := recentResourceRecordFromModel(row)
			assertProjectConversionCorruption(t, err, "recent resource", row.ResourceId)
		})
	}
}

// TestRecentResourceModelFromDomainRejectsInvalidInput verifies transactional recency writes cannot narrow invalid data.
func TestRecentResourceModelFromDomainRejectsInvalidInput(t *testing.T) {
	reference := domain.ResourceRef{ProjectID: "project-one", ResourceID: "resource-one"}
	tests := []struct {
		name       string
		reference  domain.ResourceRef
		accessedAt time.Time
		sequence   domain.Sequence
	}{
		{name: "invalid reference", reference: domain.ResourceRef{}, accessedAt: projectConversionTime(), sequence: 1},
		{name: "zero time", reference: reference, accessedAt: time.Time{}, sequence: 1},
		{name: "zero sequence", reference: reference, accessedAt: projectConversionTime(), sequence: 0},
		{name: "overflowing sequence", reference: reference, accessedAt: projectConversionTime(), sequence: domain.Sequence(math.MaxUint64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := recentResourceModelFromDomain(test.reference, test.accessedAt, test.sequence); err == nil {
				t.Fatal("invalid recent resource input unexpectedly converted")
			}
		})
	}
}

// projectConversionSnapshot returns one aggregate whose deliberately non-sorted children expose accidental reordering.
func projectConversionSnapshot() domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        "project-one",
		Name:      "Project One",
		Path:      "/workspace/project-one",
		Slug:      "project-one",
		State:     domain.ProjectReady,
		Favorite:  true,
		UpdatedAt: projectConversionTime(),
		Apps: []domain.AppSnapshot{
			{ID: "app-two", Name: "App Two", State: domain.EntityWorking, Active: true, Required: false},
			{ID: "app-one", Name: "App One", State: domain.EntityReady, Active: true, Required: true},
		},
		Services: []domain.ServiceSnapshot{
			{ID: "service-two", Name: "Service Two", Kind: "mail", State: domain.EntityReady, Owner: domain.ServiceOwnerExternal, Selection: domain.ServiceAvailable, Required: false},
			{ID: "service-one", Name: "Service One", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
		},
		Resources: []domain.ResourceSnapshot{
			{ID: "resource-two", Name: "App Resource", Kind: "web", URL: "https://app.project-one.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app-two"}},
			{ID: "resource-one", Name: "Service Resource", Kind: "admin", URL: "http://service.project-one.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "service-one"}},
		},
	}
}

// projectConversionModels adds surrogate IDs to the generated write shape so it represents persisted rows.
func projectConversionModels(t *testing.T) (models.Project, []models.ProjectApp, []models.ProjectService, []models.ProjectResource) {
	t.Helper()
	project, apps, services, resources, err := projectModelsFromDomain(projectConversionSnapshot(), 1)
	if err != nil {
		t.Fatalf("build project conversion models: %v", err)
	}
	project.Id = 1
	for index := range apps {
		apps[index].Id = index + 1
	}
	for index := range services {
		services[index].Id = index + 1
	}
	for index := range resources {
		resources[index].Id = index + 1
	}
	return project, apps, services, resources
}

// projectConversionTime returns a stable UTC timestamp for persistence conversion fixtures.
func projectConversionTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 123000000, time.UTC)
}

// assertProjectConversionCorruption verifies persisted failures retain their entity and useful scoped identity.
func assertProjectConversionCorruption(t *testing.T, err error, entity, identity string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error = %v, want CorruptStateError", err)
	}
	if corrupt.Entity != entity {
		t.Fatalf("corrupt entity = %q, want %q", corrupt.Entity, entity)
	}
	if identity != "" && !strings.Contains(corrupt.Key, identity) {
		t.Fatalf("corrupt key = %q, want identity %q", corrupt.Key, identity)
	}
}
