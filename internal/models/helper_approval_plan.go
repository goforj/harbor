package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// HelperApprovalPlan stores one exact privileged lease effect bound to a durable operation revision.
type HelperApprovalPlan struct {
	Id                      int      `gorm:"column:id" json:"id"`
	OperationId             string   `gorm:"column:operation_id" json:"operation_id"`
	OperationRevision       int      `gorm:"column:operation_revision" json:"operation_revision"`
	NetworkStateId          int      `gorm:"column:network_state_id" json:"network_state_id"`
	Mutation                string   `gorm:"column:mutation" json:"mutation"`
	LeaseState              string   `gorm:"column:lease_state" json:"lease_state"`
	ProjectId               string   `gorm:"column:project_id" json:"project_id"`
	Kind                    string   `gorm:"column:kind" json:"kind"`
	SecondaryId             string   `gorm:"column:secondary_id" json:"secondary_id"`
	Address                 string   `gorm:"column:address" json:"address"`
	OwnershipInstallationId string   `gorm:"column:ownership_installation_id" json:"ownership_installation_id"`
	OwnershipGeneration     int      `gorm:"column:ownership_generation" json:"ownership_generation"`
	LoopbackAddressLeaseId  null.Int `gorm:"column:loopback_address_lease_id" json:"loopback_address_lease_id"`
}

// TableName returns the database table name.
func (*HelperApprovalPlan) TableName() string {
	return "helper_approval_plans"
}

// Relationships returns the relationship paths for eager loading.
func (*HelperApprovalPlan) Relationships() []string {
	return []string{}
}

//
// Repository
//

// HelperApprovalPlanRepo provides persistence helpers for HelperApprovalPlan.
type HelperApprovalPlanRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewHelperApprovalPlanRepo creates a new HelperApprovalPlan repository.
func NewHelperApprovalPlanRepo(db *database.Connections) *HelperApprovalPlanRepo {
	return &HelperApprovalPlanRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *HelperApprovalPlanRepo) WithContext(ctx context.Context) *HelperApprovalPlanRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for HelperApprovalPlan.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *HelperApprovalPlanRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *HelperApprovalPlanRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a HelperApprovalPlan by primary key.
func (r *HelperApprovalPlanRepo) ByID(id any) (*HelperApprovalPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HelperApprovalPlan
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching HelperApprovalPlan rows by a where map.
func (r *HelperApprovalPlanRepo) GetWhere(where map[string]any) ([]HelperApprovalPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HelperApprovalPlan
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all HelperApprovalPlan rows.
func (r *HelperApprovalPlanRepo) All() ([]HelperApprovalPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HelperApprovalPlan
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first HelperApprovalPlan matching a where map.
func (r *HelperApprovalPlanRepo) FirstWhere(where map[string]any) (*HelperApprovalPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HelperApprovalPlan
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new HelperApprovalPlan row.
func (r *HelperApprovalPlanRepo) Create(model *HelperApprovalPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing HelperApprovalPlan row.
func (r *HelperApprovalPlanRepo) Update(model *HelperApprovalPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a HelperApprovalPlan row by primary key.
func (r *HelperApprovalPlanRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&HelperApprovalPlan{}, id).Error
}

// DeleteWhere removes HelperApprovalPlan rows matching a where map.
func (r *HelperApprovalPlanRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&HelperApprovalPlan{}).Error
}
