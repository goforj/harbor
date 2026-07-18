package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
	"time"
)

// NetworkProjectRelease records staged and completed network teardown evidence for project unregister.
type NetworkProjectRelease struct {
	Id                   int         `gorm:"column:id" json:"id"`
	NetworkStateId       int         `gorm:"column:network_state_id" json:"network_state_id"`
	ProjectId            null.String `gorm:"column:project_id" json:"project_id"`
	SourceProjectId      string      `gorm:"column:source_project_id" json:"source_project_id"`
	OperationId          string      `gorm:"column:operation_id" json:"operation_id"`
	State                string      `gorm:"column:state" json:"state"`
	BeginGeneration      int         `gorm:"column:begin_generation" json:"begin_generation"`
	BeganAt              time.Time   `gorm:"column:began_at" json:"began_at"`
	CompletionGeneration null.Int    `gorm:"column:completion_generation" json:"completion_generation"`
	CompletedAt          *time.Time  `gorm:"column:completed_at" json:"completed_at"`
	ReleaseEvidence      null.String `gorm:"column:release_evidence" json:"release_evidence"`
}

// TableName returns the database table name.
func (*NetworkProjectRelease) TableName() string {
	return "network_project_releases"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkProjectRelease) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkProjectReleaseRepo provides persistence helpers for NetworkProjectRelease.
type NetworkProjectReleaseRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkProjectReleaseRepo creates a new NetworkProjectRelease repository.
func NewNetworkProjectReleaseRepo(db *database.Connections) *NetworkProjectReleaseRepo {
	return &NetworkProjectReleaseRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkProjectReleaseRepo) WithContext(ctx context.Context) *NetworkProjectReleaseRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkProjectRelease.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkProjectReleaseRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkProjectReleaseRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkProjectRelease by primary key.
func (r *NetworkProjectReleaseRepo) ByID(id any) (*NetworkProjectRelease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkProjectRelease
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkProjectRelease rows by a where map.
func (r *NetworkProjectReleaseRepo) GetWhere(where map[string]any) ([]NetworkProjectRelease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkProjectRelease
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkProjectRelease rows.
func (r *NetworkProjectReleaseRepo) All() ([]NetworkProjectRelease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkProjectRelease
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkProjectRelease matching a where map.
func (r *NetworkProjectReleaseRepo) FirstWhere(where map[string]any) (*NetworkProjectRelease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkProjectRelease
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkProjectRelease row.
func (r *NetworkProjectReleaseRepo) Create(model *NetworkProjectRelease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkProjectRelease row.
func (r *NetworkProjectReleaseRepo) Update(model *NetworkProjectRelease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkProjectRelease row by primary key.
func (r *NetworkProjectReleaseRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkProjectRelease{}, id).Error
}

// DeleteWhere removes NetworkProjectRelease rows matching a where map.
func (r *NetworkProjectReleaseRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkProjectRelease{}).Error
}
