package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// NetworkPoolCandidate records one ordered IPv4 loopback candidate selected for the installation.
type NetworkPoolCandidate struct {
	Id             int    `gorm:"column:id" json:"id"`
	NetworkStateId int    `gorm:"column:network_state_id" json:"network_state_id"`
	Ordinal        int    `gorm:"column:ordinal" json:"ordinal"`
	Address        string `gorm:"column:address" json:"address"`
	Generation     int    `gorm:"column:generation" json:"generation"`
}

// TableName returns the database table name.
func (*NetworkPoolCandidate) TableName() string {
	return "network_pool_candidates"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkPoolCandidate) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkPoolCandidateRepo provides persistence helpers for NetworkPoolCandidate.
type NetworkPoolCandidateRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkPoolCandidateRepo creates a new NetworkPoolCandidate repository.
func NewNetworkPoolCandidateRepo(db *database.Connections) *NetworkPoolCandidateRepo {
	return &NetworkPoolCandidateRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkPoolCandidateRepo) WithContext(ctx context.Context) *NetworkPoolCandidateRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkPoolCandidate.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkPoolCandidateRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkPoolCandidateRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkPoolCandidate by primary key.
func (r *NetworkPoolCandidateRepo) ByID(id any) (*NetworkPoolCandidate, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkPoolCandidate
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkPoolCandidate rows by a where map.
func (r *NetworkPoolCandidateRepo) GetWhere(where map[string]any) ([]NetworkPoolCandidate, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkPoolCandidate
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkPoolCandidate rows.
func (r *NetworkPoolCandidateRepo) All() ([]NetworkPoolCandidate, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkPoolCandidate
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkPoolCandidate matching a where map.
func (r *NetworkPoolCandidateRepo) FirstWhere(where map[string]any) (*NetworkPoolCandidate, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkPoolCandidate
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkPoolCandidate row.
func (r *NetworkPoolCandidateRepo) Create(model *NetworkPoolCandidate) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkPoolCandidate row.
func (r *NetworkPoolCandidateRepo) Update(model *NetworkPoolCandidate) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkPoolCandidate row by primary key.
func (r *NetworkPoolCandidateRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkPoolCandidate{}, id).Error
}

// DeleteWhere removes NetworkPoolCandidate rows matching a where map.
func (r *NetworkPoolCandidateRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkPoolCandidate{}).Error
}
