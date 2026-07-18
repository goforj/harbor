package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// ProjectService stores an infrastructure service projected within one Harbor project.
type ProjectService struct {
	Id        int    `gorm:"column:id" json:"id"`
	ProjectId string `gorm:"column:project_id" json:"project_id"`
	ServiceId string `gorm:"column:service_id" json:"service_id"`
	Name      string `gorm:"column:name" json:"name"`
	Kind      string `gorm:"column:kind" json:"kind"`
	State     string `gorm:"column:state" json:"state"`
	Owner     string `gorm:"column:owner" json:"owner"`
	Selection string `gorm:"column:selection" json:"selection"`
	Required  bool   `gorm:"column:required" json:"required"`
}

// TableName returns the database table name.
func (*ProjectService) TableName() string {
	return "project_services"
}

// Relationships returns the relationship paths for eager loading.
func (*ProjectService) Relationships() []string {
	return []string{}
}

//
// Repository
//

// ProjectServiceRepo provides persistence helpers for ProjectService.
type ProjectServiceRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewProjectServiceRepo creates a new ProjectService repository.
func NewProjectServiceRepo(db *database.Connections) *ProjectServiceRepo {
	return &ProjectServiceRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *ProjectServiceRepo) WithContext(ctx context.Context) *ProjectServiceRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for ProjectService.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *ProjectServiceRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *ProjectServiceRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a ProjectService by primary key.
func (r *ProjectServiceRepo) ByID(id any) (*ProjectService, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectService
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching ProjectService rows by a where map.
func (r *ProjectServiceRepo) GetWhere(where map[string]any) ([]ProjectService, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectService
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all ProjectService rows.
func (r *ProjectServiceRepo) All() ([]ProjectService, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectService
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first ProjectService matching a where map.
func (r *ProjectServiceRepo) FirstWhere(where map[string]any) (*ProjectService, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectService
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new ProjectService row.
func (r *ProjectServiceRepo) Create(model *ProjectService) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing ProjectService row.
func (r *ProjectServiceRepo) Update(model *ProjectService) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a ProjectService row by primary key.
func (r *ProjectServiceRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&ProjectService{}, id).Error
}

// DeleteWhere removes ProjectService rows matching a where map.
func (r *ProjectServiceRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&ProjectService{}).Error
}
