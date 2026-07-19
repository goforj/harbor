package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// MachineOwnershipProjection is the daemon-owned confirmation of authority enforced by the elevated helper.
type MachineOwnershipProjection struct {
	Id                     int       `gorm:"column:id" json:"id"`
	NetworkStateId         int       `gorm:"column:network_state_id" json:"network_state_id"`
	OwnershipSchemaVersion int       `gorm:"column:ownership_schema_version" json:"ownership_schema_version"`
	InstallationId         string    `gorm:"column:installation_id" json:"installation_id"`
	OwnerIdentity          string    `gorm:"column:owner_identity" json:"owner_identity"`
	OwnershipGeneration    int       `gorm:"column:ownership_generation" json:"ownership_generation"`
	LoopbackPoolPrefix     string    `gorm:"column:loopback_pool_prefix" json:"loopback_pool_prefix"`
	TicketVerifierKey      string    `gorm:"column:ticket_verifier_key" json:"ticket_verifier_key"`
	RecordFingerprint      string    `gorm:"column:record_fingerprint" json:"record_fingerprint"`
	ConfirmedAt            time.Time `gorm:"column:confirmed_at" json:"confirmed_at"`
}

// TableName returns the database table name.
func (*MachineOwnershipProjection) TableName() string {
	return "machine_ownership_projections"
}

// Relationships returns the relationship paths for eager loading.
func (*MachineOwnershipProjection) Relationships() []string {
	return []string{}
}

//
// Repository
//

// MachineOwnershipProjectionRepo provides persistence helpers for MachineOwnershipProjection.
type MachineOwnershipProjectionRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewMachineOwnershipProjectionRepo creates a new MachineOwnershipProjection repository.
func NewMachineOwnershipProjectionRepo(db *database.Connections) *MachineOwnershipProjectionRepo {
	return &MachineOwnershipProjectionRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *MachineOwnershipProjectionRepo) WithContext(ctx context.Context) *MachineOwnershipProjectionRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for MachineOwnershipProjection.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *MachineOwnershipProjectionRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *MachineOwnershipProjectionRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a MachineOwnershipProjection by primary key.
func (r *MachineOwnershipProjectionRepo) ByID(id any) (*MachineOwnershipProjection, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model MachineOwnershipProjection
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching MachineOwnershipProjection rows by a where map.
func (r *MachineOwnershipProjectionRepo) GetWhere(where map[string]any) ([]MachineOwnershipProjection, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []MachineOwnershipProjection
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all MachineOwnershipProjection rows.
func (r *MachineOwnershipProjectionRepo) All() ([]MachineOwnershipProjection, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []MachineOwnershipProjection
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first MachineOwnershipProjection matching a where map.
func (r *MachineOwnershipProjectionRepo) FirstWhere(where map[string]any) (*MachineOwnershipProjection, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model MachineOwnershipProjection
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new MachineOwnershipProjection row.
func (r *MachineOwnershipProjectionRepo) Create(model *MachineOwnershipProjection) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing MachineOwnershipProjection row.
func (r *MachineOwnershipProjectionRepo) Update(model *MachineOwnershipProjection) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a MachineOwnershipProjection row by primary key.
func (r *MachineOwnershipProjectionRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&MachineOwnershipProjection{}, id).Error
}

// DeleteWhere removes MachineOwnershipProjection rows matching a where map.
func (r *MachineOwnershipProjectionRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&MachineOwnershipProjection{}).Error
}
