package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// RecentResource stores the global recency order for a project-scoped resource.
type RecentResource struct {
	Id         int       `gorm:"column:id" json:"id"`
	ProjectId  string    `gorm:"column:project_id" json:"project_id"`
	ResourceId string    `gorm:"column:resource_id" json:"resource_id"`
	AccessedAt time.Time `gorm:"column:accessed_at" json:"accessed_at"`
	Sequence   int       `gorm:"column:sequence" json:"sequence"`
}

// TableName returns the database table name.
func (*RecentResource) TableName() string {
	return "recent_resources"
}

// Relationships returns the relationship paths for eager loading.
func (*RecentResource) Relationships() []string {
	return []string{}
}

//
// Repository
//

// RecentResourceRepo provides persistence helpers for RecentResource.
type RecentResourceRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewRecentResourceRepo creates a new RecentResource repository.
func NewRecentResourceRepo(db *database.Connections) *RecentResourceRepo {
	return &RecentResourceRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *RecentResourceRepo) WithContext(ctx context.Context) *RecentResourceRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for RecentResource.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *RecentResourceRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *RecentResourceRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a RecentResource by primary key.
func (r *RecentResourceRepo) ByID(id any) (*RecentResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model RecentResource
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching RecentResource rows by a where map.
func (r *RecentResourceRepo) GetWhere(where map[string]any) ([]RecentResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []RecentResource
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all RecentResource rows.
func (r *RecentResourceRepo) All() ([]RecentResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []RecentResource
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first RecentResource matching a where map.
func (r *RecentResourceRepo) FirstWhere(where map[string]any) (*RecentResource, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model RecentResource
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new RecentResource row.
func (r *RecentResourceRepo) Create(model *RecentResource) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing RecentResource row.
func (r *RecentResourceRepo) Update(model *RecentResource) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a RecentResource row by primary key.
func (r *RecentResourceRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&RecentResource{}, id).Error
}

// DeleteWhere removes RecentResource rows matching a where map.
func (r *RecentResourceRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&RecentResource{}).Error
}
