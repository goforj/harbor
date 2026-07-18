package state

import (
	"fmt"
	"strconv"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// ProjectRecord couples a validated project aggregate with the global revision that produced it.
type ProjectRecord struct {
	Project  domain.ProjectSnapshot
	Revision domain.Sequence
}

// Validate reports whether the project and its durable revision can be represented by the generated models.
func (record ProjectRecord) Validate() error {
	if err := record.Project.Validate(); err != nil {
		return err
	}
	_, err := sequenceToModelInt("project revision", record.Revision, false)
	return err
}

// RecentResourceRecord couples one project-scoped resource reference with its durable recency position.
type RecentResourceRecord struct {
	Reference  domain.ResourceRef
	AccessedAt time.Time
	Sequence   domain.Sequence
}

// Validate reports whether the recency record is complete and representable by the generated model.
func (record RecentResourceRecord) Validate() error {
	if err := record.Reference.Validate(); err != nil {
		return err
	}
	if err := validateStoredTime("recent resource access time", record.AccessedAt); err != nil {
		return err
	}
	_, err := sequenceToModelInt("recent resource sequence", record.Sequence, false)
	return err
}

// projectRecordFromModels converts one normalized persisted aggregate without changing query order.
func projectRecordFromModels(
	projectRow models.Project,
	appRows []models.ProjectApp,
	serviceRows []models.ProjectService,
	resourceRows []models.ProjectResource,
) (ProjectRecord, error) {
	projectKey := durableKey(projectRow.ProjectId, projectRow.Id)
	if projectRow.Id <= 0 {
		return ProjectRecord{}, corruptStateError("project", projectKey, fmt.Errorf("database ID must be positive"))
	}
	if projectRow.Revision <= 0 {
		return ProjectRecord{}, corruptStateError("project", projectKey, fmt.Errorf("revision must be positive"))
	}
	if err := validateStoredTime("project updated time", projectRow.UpdatedAt); err != nil {
		return ProjectRecord{}, corruptStateError("project", projectKey, err)
	}

	project := domain.ProjectSnapshot{
		ID:        domain.ProjectID(projectRow.ProjectId),
		Name:      projectRow.Name,
		Path:      projectRow.Path,
		Slug:      projectRow.Slug,
		State:     domain.ProjectState(projectRow.State),
		Favorite:  projectRow.Favorite,
		UpdatedAt: projectRow.UpdatedAt,
		Apps:      make([]domain.AppSnapshot, 0, len(appRows)),
		Services:  make([]domain.ServiceSnapshot, 0, len(serviceRows)),
		Resources: make([]domain.ResourceSnapshot, 0, len(resourceRows)),
	}
	if err := project.Validate(); err != nil {
		return ProjectRecord{}, corruptStateError("project", projectKey, err)
	}

	for _, row := range appRows {
		app, err := projectAppFromModel(projectRow.ProjectId, row)
		if err != nil {
			return ProjectRecord{}, err
		}
		project.Apps = append(project.Apps, app)
	}
	for _, row := range serviceRows {
		service, err := projectServiceFromModel(projectRow.ProjectId, row)
		if err != nil {
			return ProjectRecord{}, err
		}
		project.Services = append(project.Services, service)
	}
	for _, row := range resourceRows {
		resource, err := projectResourceFromModel(projectRow.ProjectId, row)
		if err != nil {
			return ProjectRecord{}, err
		}
		project.Resources = append(project.Resources, resource)
	}

	record := ProjectRecord{Project: project, Revision: domain.Sequence(projectRow.Revision)}
	if err := record.Validate(); err != nil {
		return ProjectRecord{}, corruptStateError("project", projectKey, err)
	}
	return record, nil
}

// projectModelsFromDomain prepares one validated aggregate for transactional upserts.
func projectModelsFromDomain(
	project domain.ProjectSnapshot,
	revision domain.Sequence,
) (models.Project, []models.ProjectApp, []models.ProjectService, []models.ProjectResource, error) {
	record := ProjectRecord{Project: project, Revision: revision}
	if err := record.Validate(); err != nil {
		return models.Project{}, nil, nil, nil, err
	}
	modelRevision := int(revision)

	projectRow := models.Project{
		ProjectId: string(project.ID),
		Name:      project.Name,
		Path:      project.Path,
		Slug:      project.Slug,
		State:     string(project.State),
		Favorite:  project.Favorite,
		UpdatedAt: project.UpdatedAt,
		Revision:  modelRevision,
	}
	appRows := make([]models.ProjectApp, 0, len(project.Apps))
	for _, app := range project.Apps {
		appRows = append(appRows, models.ProjectApp{
			ProjectId: string(project.ID),
			AppId:     string(app.ID),
			Name:      app.Name,
			State:     string(app.State),
			Active:    app.Active,
			Required:  app.Required,
		})
	}
	serviceRows := make([]models.ProjectService, 0, len(project.Services))
	for _, service := range project.Services {
		serviceRows = append(serviceRows, models.ProjectService{
			ProjectId: string(project.ID),
			ServiceId: string(service.ID),
			Name:      service.Name,
			Kind:      service.Kind,
			State:     string(service.State),
			Owner:     string(service.Owner),
			Selection: string(service.Selection),
			Required:  service.Required,
		})
	}
	resourceRows := make([]models.ProjectResource, 0, len(project.Resources))
	for _, resource := range project.Resources {
		ownerAppID, ownerServiceID := resourceOwnerToModel(resource.Owner)
		resourceRows = append(resourceRows, models.ProjectResource{
			ProjectId:      string(project.ID),
			ResourceId:     string(resource.ID),
			Name:           resource.Name,
			Kind:           resource.Kind,
			Url:            resource.URL,
			OwnerKind:      string(resource.Owner.Kind),
			OwnerAppId:     ownerAppID,
			OwnerServiceId: ownerServiceID,
		})
	}
	return projectRow, appRows, serviceRows, resourceRows, nil
}

// recentResourceRecordFromModel converts and validates one persisted recency row.
func recentResourceRecordFromModel(row models.RecentResource) (RecentResourceRecord, error) {
	key := scopedKey(row.ProjectId, row.ResourceId, row.Id)
	if row.Id <= 0 {
		return RecentResourceRecord{}, corruptStateError("recent resource", key, fmt.Errorf("database ID must be positive"))
	}
	record := RecentResourceRecord{
		Reference: domain.ResourceRef{
			ProjectID:  domain.ProjectID(row.ProjectId),
			ResourceID: domain.ResourceID(row.ResourceId),
		},
		AccessedAt: row.AccessedAt,
		Sequence:   domain.Sequence(row.Sequence),
	}
	if err := record.Validate(); err != nil {
		return RecentResourceRecord{}, corruptStateError("recent resource", key, err)
	}
	return record, nil
}

// recentResourceModelFromDomain prepares one validated recency record for a transactional upsert.
func recentResourceModelFromDomain(
	reference domain.ResourceRef,
	accessedAt time.Time,
	sequence domain.Sequence,
) (models.RecentResource, error) {
	record := RecentResourceRecord{Reference: reference, AccessedAt: accessedAt, Sequence: sequence}
	if err := record.Validate(); err != nil {
		return models.RecentResource{}, err
	}
	modelSequence := int(sequence)
	return models.RecentResource{
		ProjectId:  string(reference.ProjectID),
		ResourceId: string(reference.ResourceID),
		AccessedAt: accessedAt,
		Sequence:   modelSequence,
	}, nil
}

// projectAppFromModel validates one persisted App before adding it to its parent aggregate.
func projectAppFromModel(projectID string, row models.ProjectApp) (domain.AppSnapshot, error) {
	key := scopedKey(row.ProjectId, row.AppId, row.Id)
	if row.Id <= 0 {
		return domain.AppSnapshot{}, corruptStateError("project App", key, fmt.Errorf("database ID must be positive"))
	}
	if row.ProjectId != projectID {
		return domain.AppSnapshot{}, corruptStateError("project App", key, fmt.Errorf("project scope %q does not match parent %q", row.ProjectId, projectID))
	}
	app := domain.AppSnapshot{
		ID:       domain.AppID(row.AppId),
		Name:     row.Name,
		State:    domain.EntityState(row.State),
		Active:   row.Active,
		Required: row.Required,
	}
	if err := app.Validate(); err != nil {
		return domain.AppSnapshot{}, corruptStateError("project App", key, err)
	}
	return app, nil
}

// projectServiceFromModel validates one persisted service before adding it to its parent aggregate.
func projectServiceFromModel(projectID string, row models.ProjectService) (domain.ServiceSnapshot, error) {
	key := scopedKey(row.ProjectId, row.ServiceId, row.Id)
	if row.Id <= 0 {
		return domain.ServiceSnapshot{}, corruptStateError("project service", key, fmt.Errorf("database ID must be positive"))
	}
	if row.ProjectId != projectID {
		return domain.ServiceSnapshot{}, corruptStateError("project service", key, fmt.Errorf("project scope %q does not match parent %q", row.ProjectId, projectID))
	}
	service := domain.ServiceSnapshot{
		ID:        domain.ServiceID(row.ServiceId),
		Name:      row.Name,
		Kind:      row.Kind,
		State:     domain.EntityState(row.State),
		Owner:     domain.ServiceOwner(row.Owner),
		Selection: domain.ServiceSelection(row.Selection),
		Required:  row.Required,
	}
	if err := service.Validate(); err != nil {
		return domain.ServiceSnapshot{}, corruptStateError("project service", key, err)
	}
	return service, nil
}

// projectResourceFromModel validates one persisted resource and its exact nullable owner representation.
func projectResourceFromModel(projectID string, row models.ProjectResource) (domain.ResourceSnapshot, error) {
	key := scopedKey(row.ProjectId, row.ResourceId, row.Id)
	if row.Id <= 0 {
		return domain.ResourceSnapshot{}, corruptStateError("project resource", key, fmt.Errorf("database ID must be positive"))
	}
	if row.ProjectId != projectID {
		return domain.ResourceSnapshot{}, corruptStateError("project resource", key, fmt.Errorf("project scope %q does not match parent %q", row.ProjectId, projectID))
	}
	owner, err := resourceOwnerFromModel(row.OwnerKind, row.OwnerAppId, row.OwnerServiceId)
	if err != nil {
		return domain.ResourceSnapshot{}, corruptStateError("project resource", key, err)
	}
	resource := domain.ResourceSnapshot{
		ID:    domain.ResourceID(row.ResourceId),
		Name:  row.Name,
		Kind:  row.Kind,
		Owner: owner,
		URL:   row.Url,
	}
	if err := resource.Validate(); err != nil {
		return domain.ResourceSnapshot{}, corruptStateError("project resource", key, err)
	}
	return resource, nil
}

// resourceOwnerFromModel rejects partial or contradictory SQL nullable owner columns.
func resourceOwnerFromModel(kind string, appID, serviceID null.String) (domain.ResourceOwner, error) {
	switch domain.ResourceOwnerKind(kind) {
	case domain.ResourceOwnedByApp:
		if !appID.Valid || serviceID.Valid {
			return domain.ResourceOwner{}, fmt.Errorf("App owner requires only owner_app_id")
		}
		return domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: domain.AppID(appID.String)}, nil
	case domain.ResourceOwnedByService:
		if appID.Valid || !serviceID.Valid {
			return domain.ResourceOwner{}, fmt.Errorf("service owner requires only owner_service_id")
		}
		return domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: domain.ServiceID(serviceID.String)}, nil
	default:
		return domain.ResourceOwner{}, fmt.Errorf("unknown resource owner kind %q", kind)
	}
}

// resourceOwnerToModel preserves the one-of nullable owner representation guaranteed by domain validation.
func resourceOwnerToModel(owner domain.ResourceOwner) (null.String, null.String) {
	if owner.Kind == domain.ResourceOwnedByApp {
		return null.StringFrom(string(owner.AppID)), null.String{}
	}
	return null.String{}, null.StringFrom(string(owner.ServiceID))
}

// durableKey keeps corruption errors actionable even when a durable identity column is empty.
func durableKey(identity string, databaseID int) string {
	if identity != "" {
		return identity
	}
	return "database-id:" + strconv.Itoa(databaseID)
}

// scopedKey retains both scoped identities and the surrogate ID in corruption diagnostics.
func scopedKey(parentID, childID string, databaseID int) string {
	return durableKey(parentID, databaseID) + "/" + durableKey(childID, databaseID)
}
