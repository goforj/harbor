package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// HelperApprovalPlanSocketRequirement stores one canonical native socket protected by an approval plan.
type HelperApprovalPlanSocketRequirement struct {
	Id                   int    `gorm:"column:id" json:"id"`
	HelperApprovalPlanId int    `gorm:"column:helper_approval_plan_id" json:"helper_approval_plan_id"`
	Transport            string `gorm:"column:transport" json:"transport"`
	Port                 int    `gorm:"column:port" json:"port"`
}

// TableName returns the database table name.
func (*HelperApprovalPlanSocketRequirement) TableName() string {
	return "helper_approval_plan_socket_requirements"
}

// Relationships returns the relationship paths for eager loading.
func (*HelperApprovalPlanSocketRequirement) Relationships() []string {
	return []string{}
}

//
// Repository
//

// HelperApprovalPlanSocketRequirementRepo provides persistence helpers for HelperApprovalPlanSocketRequirement.
type HelperApprovalPlanSocketRequirementRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewHelperApprovalPlanSocketRequirementRepo creates a new HelperApprovalPlanSocketRequirement repository.
func NewHelperApprovalPlanSocketRequirementRepo(db *database.Connections) *HelperApprovalPlanSocketRequirementRepo {
	return &HelperApprovalPlanSocketRequirementRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *HelperApprovalPlanSocketRequirementRepo) WithContext(ctx context.Context) *HelperApprovalPlanSocketRequirementRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for HelperApprovalPlanSocketRequirement.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *HelperApprovalPlanSocketRequirementRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *HelperApprovalPlanSocketRequirementRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a HelperApprovalPlanSocketRequirement by primary key.
func (r *HelperApprovalPlanSocketRequirementRepo) ByID(id any) (*HelperApprovalPlanSocketRequirement, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HelperApprovalPlanSocketRequirement
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching HelperApprovalPlanSocketRequirement rows by a where map.
func (r *HelperApprovalPlanSocketRequirementRepo) GetWhere(where map[string]any) ([]HelperApprovalPlanSocketRequirement, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HelperApprovalPlanSocketRequirement
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all HelperApprovalPlanSocketRequirement rows.
func (r *HelperApprovalPlanSocketRequirementRepo) All() ([]HelperApprovalPlanSocketRequirement, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []HelperApprovalPlanSocketRequirement
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first HelperApprovalPlanSocketRequirement matching a where map.
func (r *HelperApprovalPlanSocketRequirementRepo) FirstWhere(where map[string]any) (*HelperApprovalPlanSocketRequirement, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model HelperApprovalPlanSocketRequirement
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new HelperApprovalPlanSocketRequirement row.
func (r *HelperApprovalPlanSocketRequirementRepo) Create(model *HelperApprovalPlanSocketRequirement) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing HelperApprovalPlanSocketRequirement row.
func (r *HelperApprovalPlanSocketRequirementRepo) Update(model *HelperApprovalPlanSocketRequirement) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a HelperApprovalPlanSocketRequirement row by primary key.
func (r *HelperApprovalPlanSocketRequirementRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&HelperApprovalPlanSocketRequirement{}, id).Error
}

// DeleteWhere removes HelperApprovalPlanSocketRequirement rows matching a where map.
func (r *HelperApprovalPlanSocketRequirementRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&HelperApprovalPlanSocketRequirement{}).Error
}
