package models

import (
	"context"
	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// NetworkResolverSetupPlan stores one operation-bound resolver policy awaiting privileged approval.
type NetworkResolverSetupPlan struct {
	Id                             int    `gorm:"column:id" json:"id"`
	OperationId                    string `gorm:"column:operation_id" json:"operation_id"`
	OperationRevision              int    `gorm:"column:operation_revision" json:"operation_revision"`
	NetworkStateId                 int    `gorm:"column:network_state_id" json:"network_state_id"`
	NetworkRevision                int    `gorm:"column:network_revision" json:"network_revision"`
	SourceOwnershipFingerprint     string `gorm:"column:source_ownership_fingerprint" json:"source_ownership_fingerprint"`
	TargetOwnershipSchemaVersion   int    `gorm:"column:target_ownership_schema_version" json:"target_ownership_schema_version"`
	TargetInstallationId           string `gorm:"column:target_installation_id" json:"target_installation_id"`
	TargetOwnerIdentity            string `gorm:"column:target_owner_identity" json:"target_owner_identity"`
	TargetOwnershipGeneration      int    `gorm:"column:target_ownership_generation" json:"target_ownership_generation"`
	TargetLoopbackPoolPrefix       string `gorm:"column:target_loopback_pool_prefix" json:"target_loopback_pool_prefix"`
	TargetNetworkPolicyFingerprint string `gorm:"column:target_network_policy_fingerprint" json:"target_network_policy_fingerprint"`
	TargetTicketVerifierKey        string `gorm:"column:target_ticket_verifier_key" json:"target_ticket_verifier_key"`
	PolicySuffix                   string `gorm:"column:policy_suffix" json:"policy_suffix"`
	PolicyAuthorityFingerprint     string `gorm:"column:policy_authority_fingerprint" json:"policy_authority_fingerprint"`
	PolicyResolverMechanism        string `gorm:"column:policy_resolver_mechanism" json:"policy_resolver_mechanism"`
	PolicyLowPortsMechanism        string `gorm:"column:policy_low_ports_mechanism" json:"policy_low_ports_mechanism"`
	PolicyTrustMechanism           string `gorm:"column:policy_trust_mechanism" json:"policy_trust_mechanism"`
	PolicyDnsAdvertisedAddress     string `gorm:"column:policy_dns_advertised_address" json:"policy_dns_advertised_address"`
	PolicyDnsAdvertisedPort        int    `gorm:"column:policy_dns_advertised_port" json:"policy_dns_advertised_port"`
	PolicyDnsBindAddress           string `gorm:"column:policy_dns_bind_address" json:"policy_dns_bind_address"`
	PolicyDnsBindPort              int    `gorm:"column:policy_dns_bind_port" json:"policy_dns_bind_port"`
	PolicyHttpAdvertisedAddress    string `gorm:"column:policy_http_advertised_address" json:"policy_http_advertised_address"`
	PolicyHttpAdvertisedPort       int    `gorm:"column:policy_http_advertised_port" json:"policy_http_advertised_port"`
	PolicyHttpBindAddress          string `gorm:"column:policy_http_bind_address" json:"policy_http_bind_address"`
	PolicyHttpBindPort             int    `gorm:"column:policy_http_bind_port" json:"policy_http_bind_port"`
	PolicyHttpsAdvertisedAddress   string `gorm:"column:policy_https_advertised_address" json:"policy_https_advertised_address"`
	PolicyHttpsAdvertisedPort      int    `gorm:"column:policy_https_advertised_port" json:"policy_https_advertised_port"`
	PolicyHttpsBindAddress         string `gorm:"column:policy_https_bind_address" json:"policy_https_bind_address"`
	PolicyHttpsBindPort            int    `gorm:"column:policy_https_bind_port" json:"policy_https_bind_port"`
}

// TableName returns the database table name.
func (*NetworkResolverSetupPlan) TableName() string {
	return "network_resolver_setup_plans"
}

// Relationships returns the relationship paths for eager loading.
func (*NetworkResolverSetupPlan) Relationships() []string {
	return []string{}
}

//
// Repository
//

// NetworkResolverSetupPlanRepo provides persistence helpers for NetworkResolverSetupPlan.
type NetworkResolverSetupPlanRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkResolverSetupPlanRepo creates a new NetworkResolverSetupPlan repository.
func NewNetworkResolverSetupPlanRepo(db *database.Connections) *NetworkResolverSetupPlanRepo {
	return &NetworkResolverSetupPlanRepo{db: db}
}

// WithContext returns a shallow repo clone bound to one execution context.
func (r *NetworkResolverSetupPlanRepo) WithContext(ctx context.Context) *NetworkResolverSetupPlanRepo {
	if r == nil {
		return nil
	}
	clone := *r
	clone.ctx = ctx
	return &clone
}

// Builder returns a query builder for NetworkResolverSetupPlan.
// Treat this as an escape hatch for quick manual querying; prefer repository methods.
func (r *NetworkResolverSetupPlanRepo) Builder() (*gorm.DB, error) {
	return r.connection()
}

// connection resolves the named harbord database connection for repository actions.
func (r *NetworkResolverSetupPlanRepo) connection() (*gorm.DB, error) {
	conn, err := r.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if r.ctx != nil {
		conn = conn.WithContext(r.ctx)
	}
	return conn, nil
}

// ByID fetches a NetworkResolverSetupPlan by primary key.
func (r *NetworkResolverSetupPlanRepo) ByID(id any) (*NetworkResolverSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkResolverSetupPlan
	if idAsString, ok := id.(string); ok {
		if err := conn.Where("id = ?", idAsString).First(&model).Error; err != nil {
			return nil, err
		}
	} else if err := conn.First(&model, id).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// GetWhere fetches matching NetworkResolverSetupPlan rows by a where map.
func (r *NetworkResolverSetupPlanRepo) GetWhere(where map[string]any) ([]NetworkResolverSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkResolverSetupPlan
	if err := conn.Where(where).Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// All fetches all NetworkResolverSetupPlan rows.
func (r *NetworkResolverSetupPlanRepo) All() ([]NetworkResolverSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var models []NetworkResolverSetupPlan
	if err := conn.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// FirstWhere fetches the first NetworkResolverSetupPlan matching a where map.
func (r *NetworkResolverSetupPlanRepo) FirstWhere(where map[string]any) (*NetworkResolverSetupPlan, error) {
	conn, err := r.connection()
	if err != nil {
		return nil, err
	}
	var model NetworkResolverSetupPlan
	if err := conn.Where(where).First(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

// Create inserts a new NetworkResolverSetupPlan row.
func (r *NetworkResolverSetupPlanRepo) Create(model *NetworkResolverSetupPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Create(model).Error
}

// Update persists changes to an existing NetworkResolverSetupPlan row.
func (r *NetworkResolverSetupPlanRepo) Update(model *NetworkResolverSetupPlan) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Save(model).Error
}

// DeleteByID removes a NetworkResolverSetupPlan row by primary key.
func (r *NetworkResolverSetupPlanRepo) DeleteByID(id any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Delete(&NetworkResolverSetupPlan{}, id).Error
}

// DeleteWhere removes NetworkResolverSetupPlan rows matching a where map.
func (r *NetworkResolverSetupPlanRepo) DeleteWhere(where map[string]any) error {
	conn, err := r.connection()
	if err != nil {
		return err
	}
	return conn.Where(where).Delete(&NetworkResolverSetupPlan{}).Error
}
