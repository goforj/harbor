package models

import (
	"context"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// ProjectSession stores the durable process correlation for one active project lifecycle.
type ProjectSession struct {
	Id                             int         `gorm:"column:id" json:"id"`
	SessionId                      string      `gorm:"column:session_id" json:"session_id"`
	ProjectId                      string      `gorm:"column:project_id" json:"project_id"`
	Owner                          string      `gorm:"column:owner" json:"owner"`
	State                          string      `gorm:"column:state" json:"state"`
	DescriptorDigest               string      `gorm:"column:descriptor_digest" json:"descriptor_digest"`
	CredentialDigest               string      `gorm:"column:credential_digest" json:"credential_digest"`
	Generation                     int         `gorm:"column:generation" json:"generation"`
	Pid                            null.Int    `gorm:"column:pid" json:"pid"`
	BirthToken                     null.String `gorm:"column:birth_token" json:"birth_token"`
	ExecutableIdentity             null.String `gorm:"column:executable_identity" json:"executable_identity"`
	ArgumentDigest                 null.String `gorm:"column:argument_digest" json:"argument_digest"`
	OutputBrokerEndpointReference  null.String `gorm:"column:output_broker_endpoint_reference" json:"output_broker_endpoint_reference"`
	OutputBrokerTicketDigest       null.String `gorm:"column:output_broker_ticket_digest" json:"output_broker_ticket_digest"`
	OutputBrokerManifestPath       null.String `gorm:"column:output_broker_manifest_path" json:"output_broker_manifest_path"`
	OutputBrokerPid                null.Int    `gorm:"column:output_broker_pid" json:"output_broker_pid"`
	OutputBrokerBirthToken         null.String `gorm:"column:output_broker_birth_token" json:"output_broker_birth_token"`
	OutputBrokerExecutableIdentity null.String `gorm:"column:output_broker_executable_identity" json:"output_broker_executable_identity"`
	OutputBrokerArgumentDigest     null.String `gorm:"column:output_broker_argument_digest" json:"output_broker_argument_digest"`
	CreatedAt                      time.Time   `gorm:"column:created_at" json:"created_at"`
	UpdatedAt                      time.Time   `gorm:"column:updated_at" json:"updated_at"`
}

// TableName returns the database table name.
func (*ProjectSession) TableName() string {
	return "project_sessions"
}

// Relationships returns the relationship paths for eager loading.
func (*ProjectSession) Relationships() []string {
	return []string{}
}

//
// Repository
//

// ProjectSessionRepo provides persistence helpers for ProjectSession.
type ProjectSessionRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewProjectSessionRepo creates a new ProjectSession repository.
func NewProjectSessionRepo(db *database.Connections) *ProjectSessionRepo {
	return &ProjectSessionRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *ProjectSessionRepo) WithContext(ctx context.Context) *ProjectSessionRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for ProjectSession.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *ProjectSessionRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *ProjectSessionRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a ProjectSession by primary key.
func (r *ProjectSessionRepo) ByID(id any) (*ProjectSession, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectSession
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching ProjectSession rows by a where map.
func (r *ProjectSessionRepo) GetWhere(where map[string]any) ([]ProjectSession, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectSession
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all ProjectSession rows.
func (r *ProjectSessionRepo) All() ([]ProjectSession, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []ProjectSession
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first ProjectSession matching a where map.
func (r *ProjectSessionRepo) FirstWhere(where map[string]any) (*ProjectSession, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model ProjectSession
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new ProjectSession row.
func (r *ProjectSessionRepo) Create(model *ProjectSession) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing ProjectSession row.
func (r *ProjectSessionRepo) Update(model *ProjectSession) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a ProjectSession row by primary key.
func (r *ProjectSessionRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&ProjectSession{}, id).Error
}

// DeleteWhere removes ProjectSession rows matching a where map.
func (r *ProjectSessionRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&ProjectSession{}).Error
}
