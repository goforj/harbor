package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// NetworkState is the singleton durable owner of Harbor's network projection and global revision.
type NetworkState struct {
	Id                  int       `gorm:"column:id" json:"id"`
	InstallationId      string    `gorm:"column:installation_id" json:"installation_id"`
	OwnershipGeneration int       `gorm:"column:ownership_generation" json:"ownership_generation"`
	PoolNetwork         string    `gorm:"column:pool_network" json:"pool_network"`
	PoolPrefixLength    int       `gorm:"column:pool_prefix_length" json:"pool_prefix_length"`
	DnsSuffix           string    `gorm:"column:dns_suffix" json:"dns_suffix"`
	CreatedAt           time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at" json:"updated_at"`
	Revision            int       `gorm:"column:revision" json:"revision"`
}

// TableName returns the database table name.
func (*NetworkState) TableName() string {
	return "network_state"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkState) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkStateRepo provides persistence helpers for NetworkState.
type NetworkStateRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkStateRepo creates a new NetworkState repository.
func NewNetworkStateRepo(db *database.Connections) *NetworkStateRepo {
	return &NetworkStateRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkStateRepo) WithContext(ctx context.Context) *NetworkStateRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkState.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkStateRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkStateRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkState by primary key.
func (r *NetworkStateRepo) ByID(id any) (*NetworkState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkState
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkState rows by a where map.
func (r *NetworkStateRepo) GetWhere(where map[string]any) ([]NetworkState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkState
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkState rows.
func (r *NetworkStateRepo) All() ([]NetworkState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkState
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkState matching a where map.
func (r *NetworkStateRepo) FirstWhere(where map[string]any) (*NetworkState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkState
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkState row.
func (r *NetworkStateRepo) Create(model *NetworkState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkState row.
func (r *NetworkStateRepo) Update(model *NetworkState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkState row by primary key.
func (r *NetworkStateRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkState{}, id).Error
}

// DeleteWhere removes NetworkState rows matching a where map.
func (r *NetworkStateRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkState{}).Error
}
