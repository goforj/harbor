package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// NetworkSetupPlan stores the singleton machine ownership authority selected before network state exists.
type NetworkSetupPlan struct {
	Id                     int    `gorm:"column:id" json:"id"`
	OperationId            string `gorm:"column:operation_id" json:"operation_id"`
	OperationRevision      int    `gorm:"column:operation_revision" json:"operation_revision"`
	OwnershipSchemaVersion int    `gorm:"column:ownership_schema_version" json:"ownership_schema_version"`
	InstallationId         string `gorm:"column:installation_id" json:"installation_id"`
	OwnerIdentity          string `gorm:"column:owner_identity" json:"owner_identity"`
	OwnershipGeneration    int    `gorm:"column:ownership_generation" json:"ownership_generation"`
	LoopbackPoolPrefix     string `gorm:"column:loopback_pool_prefix" json:"loopback_pool_prefix"`
	TicketVerifierKey      string `gorm:"column:ticket_verifier_key" json:"ticket_verifier_key"`
}

// TableName returns the database table name.
func (*NetworkSetupPlan) TableName() string {
	return "network_setup_plans"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkSetupPlan) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkSetupPlanRepo provides persistence helpers for NetworkSetupPlan.
type NetworkSetupPlanRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkSetupPlanRepo creates a new NetworkSetupPlan repository.
func NewNetworkSetupPlanRepo(db *database.Connections) *NetworkSetupPlanRepo {
	return &NetworkSetupPlanRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkSetupPlanRepo) WithContext(ctx context.Context) *NetworkSetupPlanRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkSetupPlan.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkSetupPlanRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkSetupPlanRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkSetupPlan by primary key.
func (r *NetworkSetupPlanRepo) ByID(id any) (*NetworkSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSetupPlan
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkSetupPlan rows by a where map.
func (r *NetworkSetupPlanRepo) GetWhere(where map[string]any) ([]NetworkSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSetupPlan
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkSetupPlan rows.
func (r *NetworkSetupPlanRepo) All() ([]NetworkSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSetupPlan
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkSetupPlan matching a where map.
func (r *NetworkSetupPlanRepo) FirstWhere(where map[string]any) (*NetworkSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSetupPlan
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkSetupPlan row.
func (r *NetworkSetupPlanRepo) Create(model *NetworkSetupPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkSetupPlan row.
func (r *NetworkSetupPlanRepo) Update(model *NetworkSetupPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkSetupPlan row by primary key.
func (r *NetworkSetupPlanRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkSetupPlan{}, id).Error
}

// DeleteWhere removes NetworkSetupPlan rows matching a where map.
func (r *NetworkSetupPlanRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkSetupPlan{}).Error
}
