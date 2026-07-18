package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// Project stores the durable top-level projection for one managed Harbor project.
type Project struct {
	Id        int       `gorm:"column:id" json:"id"`
	ProjectId string    `gorm:"column:project_id" json:"project_id"`
	Name      string    `gorm:"column:name" json:"name"`
	Path      string    `gorm:"column:path" json:"path"`
	Slug      string    `gorm:"column:slug" json:"slug"`
	State     string    `gorm:"column:state" json:"state"`
	Favorite  bool      `gorm:"column:favorite" json:"favorite"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	Revision  int       `gorm:"column:revision" json:"revision"`
}

// TableName returns the database table name.
func (*Project) TableName() string {
	return "projects"
}

// Relationships returns the relationship paths for eager loading.
func (*Project) Relationships() []string {
	return []string{}
}

//
// Repository
//

// ProjectRepo provides persistence helpers for Project.
type ProjectRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewProjectRepo creates a new Project repository.
func NewProjectRepo(db *database.Connections) *ProjectRepo {
	return &ProjectRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *ProjectRepo) WithContext(ctx context.Context) *ProjectRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for Project.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *ProjectRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *ProjectRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a Project by primary key.
func (r *ProjectRepo) ByID(id any) (*Project, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model Project
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching Project rows by a where map.
func (r *ProjectRepo) GetWhere(where map[string]any) ([]Project, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []Project
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all Project rows.
func (r *ProjectRepo) All() ([]Project, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []Project
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first Project matching a where map.
func (r *ProjectRepo) FirstWhere(where map[string]any) (*Project, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model Project
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new Project row.
func (r *ProjectRepo) Create(model *Project) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing Project row.
func (r *ProjectRepo) Update(model *Project) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a Project row by primary key.
func (r *ProjectRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&Project{}, id).Error
}

// DeleteWhere removes Project rows matching a where map.
func (r *ProjectRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&Project{}).Error
}
