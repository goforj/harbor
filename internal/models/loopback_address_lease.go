package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
	"time"
)

// LoopbackAddressLease records active or quarantined ownership of one project loopback identity.
type LoopbackAddressLease struct {
	Id                      int         `gorm:"column:id" json:"id"`
	NetworkStateId          int         `gorm:"column:network_state_id" json:"network_state_id"`
	ProjectId               null.String `gorm:"column:project_id" json:"project_id"`
	SourceProjectId         string      `gorm:"column:source_project_id" json:"source_project_id"`
	Kind                    string      `gorm:"column:kind" json:"kind"`
	SecondaryId             string      `gorm:"column:secondary_id" json:"secondary_id"`
	Address                 string      `gorm:"column:address" json:"address"`
	State                   string      `gorm:"column:state" json:"state"`
	LeaseGeneration         int         `gorm:"column:lease_generation" json:"lease_generation"`
	OwnershipInstallationId string      `gorm:"column:ownership_installation_id" json:"ownership_installation_id"`
	OwnershipGeneration     int         `gorm:"column:ownership_generation" json:"ownership_generation"`
	EnsureEvidence          string      `gorm:"column:ensure_evidence" json:"ensure_evidence"`
	LeasedAt                time.Time   `gorm:"column:leased_at" json:"leased_at"`
	ReleaseGeneration       null.Int    `gorm:"column:release_generation" json:"release_generation"`
	ReleaseEvidence         null.String `gorm:"column:release_evidence" json:"release_evidence"`
	ReleasedAt              *time.Time  `gorm:"column:released_at" json:"released_at"`
	QuarantinedAt           *time.Time  `gorm:"column:quarantined_at" json:"quarantined_at"`
	ReuseAfter              *time.Time  `gorm:"column:reuse_after" json:"reuse_after"`
	QuarantineReason        null.String `gorm:"column:quarantine_reason" json:"quarantine_reason"`
}

// TableName returns the database table name.
func (*LoopbackAddressLease) TableName() string {
	return "loopback_address_leases"
}

// Relationships returns the relationship paths for eager loading.
func (*LoopbackAddressLease) Relationships() []string {
	return []string{}
}

//
// Repository
//

// LoopbackAddressLeaseRepo provides persistence helpers for LoopbackAddressLease.
type LoopbackAddressLeaseRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewLoopbackAddressLeaseRepo creates a new LoopbackAddressLease repository.
func NewLoopbackAddressLeaseRepo(db *database.Connections) *LoopbackAddressLeaseRepo {
	return &LoopbackAddressLeaseRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *LoopbackAddressLeaseRepo) WithContext(ctx context.Context) *LoopbackAddressLeaseRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for LoopbackAddressLease.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *LoopbackAddressLeaseRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *LoopbackAddressLeaseRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a LoopbackAddressLease by primary key.
func (r *LoopbackAddressLeaseRepo) ByID(id any) (*LoopbackAddressLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model LoopbackAddressLease
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching LoopbackAddressLease rows by a where map.
func (r *LoopbackAddressLeaseRepo) GetWhere(where map[string]any) ([]LoopbackAddressLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []LoopbackAddressLease
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all LoopbackAddressLease rows.
func (r *LoopbackAddressLeaseRepo) All() ([]LoopbackAddressLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []LoopbackAddressLease
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first LoopbackAddressLease matching a where map.
func (r *LoopbackAddressLeaseRepo) FirstWhere(where map[string]any) (*LoopbackAddressLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model LoopbackAddressLease
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new LoopbackAddressLease row.
func (r *LoopbackAddressLeaseRepo) Create(model *LoopbackAddressLease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing LoopbackAddressLease row.
func (r *LoopbackAddressLeaseRepo) Update(model *LoopbackAddressLease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a LoopbackAddressLease row by primary key.
func (r *LoopbackAddressLeaseRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&LoopbackAddressLease{}, id).Error
}

// DeleteWhere removes LoopbackAddressLease rows matching a where map.
func (r *LoopbackAddressLeaseRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&LoopbackAddressLease{}).Error
}
