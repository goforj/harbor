package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// HarborState stores the singleton sequence shared by every durable daemon mutation.
type HarborState struct {
	Id       int `gorm:"column:id" json:"id"`
	Sequence int `gorm:"column:sequence" json:"sequence"`
}

// TableName returns the database table name.
func (*HarborState) TableName() string {
	return "harbor_state"
}

// Relationships returns the relationship paths for eager loading.
func (*HarborState) Relationships() []string {
	return []string{}
}

//
// Repository
//

// HarborStateRepo provides persistence helpers for HarborState.
type HarborStateRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewHarborStateRepo creates a new HarborState repository.
func NewHarborStateRepo(db *database.Connections) *HarborStateRepo {
	return &HarborStateRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *HarborStateRepo) WithContext(ctx context.Context) *HarborStateRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for HarborState.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *HarborStateRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *HarborStateRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a HarborState by primary key.
func (r *HarborStateRepo) ByID(id any) (*HarborState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HarborState
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching HarborState rows by a where map.
func (r *HarborStateRepo) GetWhere(where map[string]any) ([]HarborState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HarborState
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all HarborState rows.
func (r *HarborStateRepo) All() ([]HarborState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HarborState
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first HarborState matching a where map.
func (r *HarborStateRepo) FirstWhere(where map[string]any) (*HarborState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HarborState
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new HarborState row.
func (r *HarborStateRepo) Create(model *HarborState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing HarborState row.
func (r *HarborStateRepo) Update(model *HarborState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a HarborState row by primary key.
func (r *HarborStateRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&HarborState{}, id).Error
}

// DeleteWhere removes HarborState rows matching a where map.
func (r *HarborStateRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&HarborState{}).Error
}
