package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// OperationJournalState stores the singleton sequence allocated to committed journal mutations.
type OperationJournalState struct {
	Id       int `gorm:"column:id" json:"id"`
	Sequence int `gorm:"column:sequence" json:"sequence"`
}

// TableName returns the database table name.
func (*OperationJournalState) TableName() string {
	return "operation_journal_state"
}

// Relationships returns the relationship paths for eager loading.
func (*OperationJournalState) Relationships() []string {
	return []string{}
}

//
// Repository
//

// OperationJournalStateRepo provides persistence helpers for OperationJournalState.
type OperationJournalStateRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewOperationJournalStateRepo creates a new OperationJournalState repository.
func NewOperationJournalStateRepo(db *database.Connections) *OperationJournalStateRepo {
	return &OperationJournalStateRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *OperationJournalStateRepo) WithContext(ctx context.Context) *OperationJournalStateRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for OperationJournalState.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *OperationJournalStateRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *OperationJournalStateRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a OperationJournalState by primary key.
func (r *OperationJournalStateRepo) ByID(id any) (*OperationJournalState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model OperationJournalState
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching OperationJournalState rows by a where map.
func (r *OperationJournalStateRepo) GetWhere(where map[string]any) ([]OperationJournalState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []OperationJournalState
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all OperationJournalState rows.
func (r *OperationJournalStateRepo) All() ([]OperationJournalState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []OperationJournalState
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first OperationJournalState matching a where map.
func (r *OperationJournalStateRepo) FirstWhere(where map[string]any) (*OperationJournalState, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model OperationJournalState
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new OperationJournalState row.
func (r *OperationJournalStateRepo) Create(model *OperationJournalState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing OperationJournalState row.
func (r *OperationJournalStateRepo) Update(model *OperationJournalState) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a OperationJournalState row by primary key.
func (r *OperationJournalStateRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&OperationJournalState{}, id).Error
}

// DeleteWhere removes OperationJournalState rows matching a where map.
func (r *OperationJournalStateRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&OperationJournalState{}).Error
}
