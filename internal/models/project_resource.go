package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// ProjectResource stores a project-scoped endpoint exposed by an application or service.
type ProjectResource struct {
	Id             int         `gorm:"column:id" json:"id"`
	ProjectId      string      `gorm:"column:project_id" json:"project_id"`
	ResourceId     string      `gorm:"column:resource_id" json:"resource_id"`
	Name           string      `gorm:"column:name" json:"name"`
	Kind           string      `gorm:"column:kind" json:"kind"`
	Url            string      `gorm:"column:url" json:"url"`
	OwnerKind      string      `gorm:"column:owner_kind" json:"owner_kind"`
	OwnerAppId     null.String `gorm:"column:owner_app_id" json:"owner_app_id"`
	OwnerServiceId null.String `gorm:"column:owner_service_id" json:"owner_service_id"`
}

// TableName returns the database table name.
func (*ProjectResource) TableName() string {
	return "project_resources"
}

// Relationships returns the relationship paths for eager loading.
func (*ProjectResource) Relationships() []string {
	return []string{}
}

//
// Repository
//

// ProjectResourceRepo provides persistence helpers for ProjectResource.
type ProjectResourceRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewProjectResourceRepo creates a new ProjectResource repository.
func NewProjectResourceRepo(db *database.Connections) *ProjectResourceRepo {
	return &ProjectResourceRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *ProjectResourceRepo) WithContext(ctx context.Context) *ProjectResourceRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for ProjectResource.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *ProjectResourceRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *ProjectResourceRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a ProjectResource by primary key.
func (r *ProjectResourceRepo) ByID(id any) (*ProjectResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectResource
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching ProjectResource rows by a where map.
func (r *ProjectResourceRepo) GetWhere(where map[string]any) ([]ProjectResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectResource
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all ProjectResource rows.
func (r *ProjectResourceRepo) All() ([]ProjectResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectResource
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first ProjectResource matching a where map.
func (r *ProjectResourceRepo) FirstWhere(where map[string]any) (*ProjectResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectResource
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new ProjectResource row.
func (r *ProjectResourceRepo) Create(model *ProjectResource) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing ProjectResource row.
func (r *ProjectResourceRepo) Update(model *ProjectResource) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a ProjectResource row by primary key.
func (r *ProjectResourceRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&ProjectResource{}, id).Error
}

// DeleteWhere removes ProjectResource rows matching a where map.
func (r *ProjectResourceRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&ProjectResource{}).Error
}
