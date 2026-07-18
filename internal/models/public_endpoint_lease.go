package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
	"time"
)

// PublicEndpointLease reserves one stable exact-name HTTP or native TCP endpoint without runtime upstream details.
type PublicEndpointLease struct {
	Id                     int       `gorm:"column:id" json:"id"`
	NetworkStateId         int       `gorm:"column:network_state_id" json:"network_state_id"`
	ProjectId              string    `gorm:"column:project_id" json:"project_id"`
	EndpointId             string    `gorm:"column:endpoint_id" json:"endpoint_id"`
	Protocol               string    `gorm:"column:protocol" json:"protocol"`
	Hostname               string    `gorm:"column:hostname" json:"hostname"`
	Address                string    `gorm:"column:address" json:"address"`
	Port                   int       `gorm:"column:port" json:"port"`
	LoopbackAddressLeaseId null.Int  `gorm:"column:loopback_address_lease_id" json:"loopback_address_lease_id"`
	Generation             int       `gorm:"column:generation" json:"generation"`
	CreatedAt              time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt              time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TableName returns the database table name.
func (*PublicEndpointLease) TableName() string {
	return "public_endpoint_leases"
}

// Relationships returns the relationship paths for eager loading.
func (*PublicEndpointLease) Relationships() []string {
	return []string{}
}

//
// Repository
//

// PublicEndpointLeaseRepo provides persistence helpers for PublicEndpointLease.
type PublicEndpointLeaseRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewPublicEndpointLeaseRepo creates a new PublicEndpointLease repository.
func NewPublicEndpointLeaseRepo(db *database.Connections) *PublicEndpointLeaseRepo {
	return &PublicEndpointLeaseRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *PublicEndpointLeaseRepo) WithContext(ctx context.Context) *PublicEndpointLeaseRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for PublicEndpointLease.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *PublicEndpointLeaseRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *PublicEndpointLeaseRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a PublicEndpointLease by primary key.
func (r *PublicEndpointLeaseRepo) ByID(id any) (*PublicEndpointLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model PublicEndpointLease
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching PublicEndpointLease rows by a where map.
func (r *PublicEndpointLeaseRepo) GetWhere(where map[string]any) ([]PublicEndpointLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []PublicEndpointLease
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all PublicEndpointLease rows.
func (r *PublicEndpointLeaseRepo) All() ([]PublicEndpointLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []PublicEndpointLease
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first PublicEndpointLease matching a where map.
func (r *PublicEndpointLeaseRepo) FirstWhere(where map[string]any) (*PublicEndpointLease, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model PublicEndpointLease
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new PublicEndpointLease row.
func (r *PublicEndpointLeaseRepo) Create(model *PublicEndpointLease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing PublicEndpointLease row.
func (r *PublicEndpointLeaseRepo) Update(model *PublicEndpointLease) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a PublicEndpointLease row by primary key.
func (r *PublicEndpointLeaseRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&PublicEndpointLease{}, id).Error
}

// DeleteWhere removes PublicEndpointLease rows matching a where map.
func (r *PublicEndpointLeaseRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&PublicEndpointLease{}).Error
}
