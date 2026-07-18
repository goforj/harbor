package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
	"time"
)

// OperationTransition stores one append-only lifecycle edge for a daemon operation.
type OperationTransition struct {
	Id               int         `gorm:"column:id" json:"id"`
	OperationId      string      `gorm:"column:operation_id" json:"operation_id"`
	Ordinal          int         `gorm:"column:ordinal" json:"ordinal"`
	PreviousState    null.String `gorm:"column:previous_state" json:"previous_state"`
	State            string      `gorm:"column:state" json:"state"`
	Phase            string      `gorm:"column:phase" json:"phase"`
	ProblemCode      null.String `gorm:"column:problem_code" json:"problem_code"`
	ProblemMessage   null.String `gorm:"column:problem_message" json:"problem_message"`
	ProblemRetryable null.Bool   `gorm:"column:problem_retryable" json:"problem_retryable"`
	OccurredAt       time.Time   `gorm:"column:occurred_at" json:"occurred_at"`
	Sequence         int         `gorm:"column:sequence" json:"sequence"`
}

// TableName returns the database table name.
func (*OperationTransition) TableName() string {
	return "operation_transitions"
}

// Relationships returns the relationship paths for eager loading.
func (*OperationTransition) Relationships() []string {
	return []string{}
}

//
// Repository
//

// OperationTransitionRepo provides persistence helpers for OperationTransition.
type OperationTransitionRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewOperationTransitionRepo creates a new OperationTransition repository.
func NewOperationTransitionRepo(db *database.Connections) *OperationTransitionRepo {
	return &OperationTransitionRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *OperationTransitionRepo) WithContext(ctx context.Context) *OperationTransitionRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for OperationTransition.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *OperationTransitionRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *OperationTransitionRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a OperationTransition by primary key.
func (r *OperationTransitionRepo) ByID(id any) (*OperationTransition, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model OperationTransition
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching OperationTransition rows by a where map.
func (r *OperationTransitionRepo) GetWhere(where map[string]any) ([]OperationTransition, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []OperationTransition
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all OperationTransition rows.
func (r *OperationTransitionRepo) All() ([]OperationTransition, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []OperationTransition
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first OperationTransition matching a where map.
func (r *OperationTransitionRepo) FirstWhere(where map[string]any) (*OperationTransition, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model OperationTransition
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new OperationTransition row.
func (r *OperationTransitionRepo) Create(model *OperationTransition) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing OperationTransition row.
func (r *OperationTransitionRepo) Update(model *OperationTransition) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a OperationTransition row by primary key.
func (r *OperationTransitionRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&OperationTransition{}, id).Error
}

// DeleteWhere removes OperationTransition rows matching a where map.
func (r *OperationTransitionRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&OperationTransition{}).Error
}
