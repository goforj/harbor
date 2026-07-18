package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
	"time"
)

// NetworkSharedListener records one advertised and actual shared network socket.
type NetworkSharedListener struct {
	Id                int       `gorm:"column:id" json:"id"`
	NetworkStateId    int       `gorm:"column:network_state_id" json:"network_state_id"`
	Kind              string    `gorm:"column:kind" json:"kind"`
	Mode              string    `gorm:"column:mode" json:"mode"`
	AdvertisedAddress string    `gorm:"column:advertised_address" json:"advertised_address"`
	AdvertisedPort    int       `gorm:"column:advertised_port" json:"advertised_port"`
	BindAddress       string    `gorm:"column:bind_address" json:"bind_address"`
	BindPort          int       `gorm:"column:bind_port" json:"bind_port"`
	Generation        int       `gorm:"column:generation" json:"generation"`
	VerifiedAt        time.Time `gorm:"column:verified_at" json:"verified_at"`
}

// TableName returns the database table name.
func (*NetworkSharedListener) TableName() string {
	return "network_shared_listeners"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkSharedListener) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkSharedListenerRepo provides persistence helpers for NetworkSharedListener.
type NetworkSharedListenerRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkSharedListenerRepo creates a new NetworkSharedListener repository.
func NewNetworkSharedListenerRepo(db *database.Connections) *NetworkSharedListenerRepo {
	return &NetworkSharedListenerRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkSharedListenerRepo) WithContext(ctx context.Context) *NetworkSharedListenerRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkSharedListener.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkSharedListenerRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkSharedListenerRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkSharedListener by primary key.
func (r *NetworkSharedListenerRepo) ByID(id any) (*NetworkSharedListener, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSharedListener
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkSharedListener rows by a where map.
func (r *NetworkSharedListenerRepo) GetWhere(where map[string]any) ([]NetworkSharedListener, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSharedListener
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkSharedListener rows.
func (r *NetworkSharedListenerRepo) All() ([]NetworkSharedListener, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkSharedListener
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkSharedListener matching a where map.
func (r *NetworkSharedListenerRepo) FirstWhere(where map[string]any) (*NetworkSharedListener, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkSharedListener
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkSharedListener row.
func (r *NetworkSharedListenerRepo) Create(model *NetworkSharedListener) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkSharedListener row.
func (r *NetworkSharedListenerRepo) Update(model *NetworkSharedListener) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkSharedListener row by primary key.
func (r *NetworkSharedListenerRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkSharedListener{}, id).Error
}

// DeleteWhere removes NetworkSharedListener rows matching a where map.
func (r *NetworkSharedListenerRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkSharedListener{}).Error
}
