package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
	"time"
)

// Operation stores the durable database representation of a daemon operation.
type Operation struct {
	Id               string      `gorm:"column:id" json:"id"`
	IntentId         string      `gorm:"column:intent_id" json:"intent_id"`
	Kind             string      `gorm:"column:kind" json:"kind"`
	ProjectId        null.String `gorm:"column:project_id" json:"project_id"`
	State            string      `gorm:"column:state" json:"state"`
	Phase            string      `gorm:"column:phase" json:"phase"`
	ProblemCode      null.String `gorm:"column:problem_code" json:"problem_code"`
	ProblemMessage   null.String `gorm:"column:problem_message" json:"problem_message"`
	ProblemRetryable null.Bool   `gorm:"column:problem_retryable" json:"problem_retryable"`
	RequestedAt      time.Time   `gorm:"column:requested_at" json:"requested_at"`
	StartedAt        *time.Time  `gorm:"column:started_at" json:"started_at"`
	FinishedAt       *time.Time  `gorm:"column:finished_at" json:"finished_at"`
	Revision         int         `gorm:"column:revision" json:"revision"`
}

// TableName returns the database table name.
func (*Operation) TableName() string {
	return "operations"
}

// Relationships returns the relationship paths for eager loading.
func (*Operation) Relationships() []string {
	return []string{}
}

//
// Repository
//

// OperationRepo provides persistence helpers for Operation.
type OperationRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewOperationRepo creates a new Operation repository.
func NewOperationRepo(db *database.Connections) *OperationRepo {
	return &OperationRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *OperationRepo) WithContext(ctx context.Context) *OperationRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for Operation.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *OperationRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *OperationRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a Operation by primary key.
func (r *OperationRepo) ByID(id any) (*Operation, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model Operation
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching Operation rows by a where map.
func (r *OperationRepo) GetWhere(where map[string]any) ([]Operation, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []Operation
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all Operation rows.
func (r *OperationRepo) All() ([]Operation, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []Operation
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first Operation matching a where map.
func (r *OperationRepo) FirstWhere(where map[string]any) (*Operation, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model Operation
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new Operation row.
func (r *OperationRepo) Create(model *Operation) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing Operation row.
func (r *OperationRepo) Update(model *Operation) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a Operation row by primary key.
func (r *OperationRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&Operation{}, id).Error
}

// DeleteWhere removes Operation rows matching a where map.
func (r *OperationRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&Operation{}).Error
}
