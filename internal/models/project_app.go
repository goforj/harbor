package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// ProjectApp stores an application projected within one managed Harbor project.
type ProjectApp struct {
	Id        int    `gorm:"column:id" json:"id"`
	ProjectId string `gorm:"column:project_id" json:"project_id"`
	AppId     string `gorm:"column:app_id" json:"app_id"`
	Name      string `gorm:"column:name" json:"name"`
	State     string `gorm:"column:state" json:"state"`
	Active    bool   `gorm:"column:active" json:"active"`
	Required  bool   `gorm:"column:required" json:"required"`
}

// TableName returns the database table name.
func (*ProjectApp) TableName() string {
	return "project_apps"
}

// Relationships returns the relationship paths for eager loading.
func (*ProjectApp) Relationships() []string {
	return []string{}
}

//
// Repository
//

// ProjectAppRepo provides persistence helpers for ProjectApp.
type ProjectAppRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewProjectAppRepo creates a new ProjectApp repository.
func NewProjectAppRepo(db *database.Connections) *ProjectAppRepo {
	return &ProjectAppRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *ProjectAppRepo) WithContext(ctx context.Context) *ProjectAppRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for ProjectApp.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *ProjectAppRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *ProjectAppRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a ProjectApp by primary key.
func (r *ProjectAppRepo) ByID(id any) (*ProjectApp, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectApp
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching ProjectApp rows by a where map.
func (r *ProjectAppRepo) GetWhere(where map[string]any) ([]ProjectApp, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectApp
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all ProjectApp rows.
func (r *ProjectAppRepo) All() ([]ProjectApp, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectApp
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first ProjectApp matching a where map.
func (r *ProjectAppRepo) FirstWhere(where map[string]any) (*ProjectApp, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectApp
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new ProjectApp row.
func (r *ProjectAppRepo) Create(model *ProjectApp) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing ProjectApp row.
func (r *ProjectAppRepo) Update(model *ProjectApp) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a ProjectApp row by primary key.
func (r *ProjectAppRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&ProjectApp{}, id).Error
}

// DeleteWhere removes ProjectApp rows matching a where map.
func (r *ProjectAppRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&ProjectApp{}).Error
}
