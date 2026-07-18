package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// NetworkSetupEvidence retains verified setup evidence for one host integration component.
type NetworkSetupEvidence struct {
	Id             int       `gorm:"column:id" json:"id"`
	NetworkStateId int       `gorm:"column:network_state_id" json:"network_state_id"`
	Component      string    `gorm:"column:component" json:"component"`
	Evidence       string    `gorm:"column:evidence" json:"evidence"`
	Generation     int       `gorm:"column:generation" json:"generation"`
	VerifiedAt     time.Time `gorm:"column:verified_at" json:"verified_at"`
}

// TableName returns the database table name.
func (*NetworkSetupEvidence) TableName() string {
	return "network_setup_evidence"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkSetupEvidence) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkSetupEvidenceRepo provides persistence helpers for NetworkSetupEvidence.
type NetworkSetupEvidenceRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkSetupEvidenceRepo creates a new NetworkSetupEvidence repository.
func NewNetworkSetupEvidenceRepo(db *database.Connections) *NetworkSetupEvidenceRepo {
	return &NetworkSetupEvidenceRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkSetupEvidenceRepo) WithContext(ctx context.Context) *NetworkSetupEvidenceRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkSetupEvidence.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkSetupEvidenceRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkSetupEvidenceRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkSetupEvidence by primary key.
func (r *NetworkSetupEvidenceRepo) ByID(id any) (*NetworkSetupEvidence, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSetupEvidence
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkSetupEvidence rows by a where map.
func (r *NetworkSetupEvidenceRepo) GetWhere(where map[string]any) ([]NetworkSetupEvidence, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSetupEvidence
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkSetupEvidence rows.
func (r *NetworkSetupEvidenceRepo) All() ([]NetworkSetupEvidence, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSetupEvidence
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkSetupEvidence matching a where map.
func (r *NetworkSetupEvidenceRepo) FirstWhere(where map[string]any) (*NetworkSetupEvidence, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSetupEvidence
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkSetupEvidence row.
func (r *NetworkSetupEvidenceRepo) Create(model *NetworkSetupEvidence) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkSetupEvidence row.
func (r *NetworkSetupEvidenceRepo) Update(model *NetworkSetupEvidence) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkSetupEvidence row by primary key.
func (r *NetworkSetupEvidenceRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkSetupEvidence{}, id).Error
}

// DeleteWhere removes NetworkSetupEvidence rows matching a where map.
func (r *NetworkSetupEvidenceRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkSetupEvidence{}).Error
}
