package models

import (
	"context"

	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// NetworkResolverPolicyMigrationPlan stores the sole authority to retire one legacy macOS resolver policy.
type NetworkResolverPolicyMigrationPlan struct {
	Id                             int    `gorm:"column:id" json:"id"`
	OperationId                    string `gorm:"column:operation_id" json:"operation_id"`
	OperationKind                  string `gorm:"column:operation_kind" json:"operation_kind"`
	OperationState                 string `gorm:"column:operation_state" json:"operation_state"`
	OperationPhase                 string `gorm:"column:operation_phase" json:"operation_phase"`
	OperationRevision              int    `gorm:"column:operation_revision" json:"operation_revision"`
	NetworkStateId                 int    `gorm:"column:network_state_id" json:"network_state_id"`
	NetworkRevision                int    `gorm:"column:network_revision" json:"network_revision"`
	SourceOwnershipSchemaVersion   int    `gorm:"column:source_ownership_schema_version" json:"source_ownership_schema_version"`
	SourceOwnershipFingerprint     string `gorm:"column:source_ownership_fingerprint" json:"source_ownership_fingerprint"`
	SourceInstallationId           string `gorm:"column:source_installation_id" json:"source_installation_id"`
	SourceOwnerIdentity            string `gorm:"column:source_owner_identity" json:"source_owner_identity"`
	SourceOwnershipGeneration      int    `gorm:"column:source_ownership_generation" json:"source_ownership_generation"`
	SourceLoopbackPoolPrefix       string `gorm:"column:source_loopback_pool_prefix" json:"source_loopback_pool_prefix"`
	SourceNetworkPolicyFingerprint string `gorm:"column:source_network_policy_fingerprint" json:"source_network_policy_fingerprint"`
	SourceTicketVerifierKey        string `gorm:"column:source_ticket_verifier_key" json:"source_ticket_verifier_key"`
	PostOwnershipFingerprint       string `gorm:"column:post_ownership_fingerprint" json:"post_ownership_fingerprint"`
	ReplacementPolicyFingerprint   string `gorm:"column:replacement_policy_fingerprint" json:"replacement_policy_fingerprint"`
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
func (*NetworkResolverPolicyMigrationPlan) TableName() string {
	return "network_resolver_policy_migration_plans"
}

// NetworkResolverPolicyMigrationPlanRepo provides the named-database builder used by durable plan readers.
type NetworkResolverPolicyMigrationPlanRepo struct {
	db  *database.Connections
	ctx context.Context
}

// NewNetworkResolverPolicyMigrationPlanRepo creates a repository for policy-migration plan reads.
func NewNetworkResolverPolicyMigrationPlanRepo(db *database.Connections) *NetworkResolverPolicyMigrationPlanRepo {
	return &NetworkResolverPolicyMigrationPlanRepo{db: db}
}

// WithContext returns a shallow repository clone bound to ctx.
func (repo *NetworkResolverPolicyMigrationPlanRepo) WithContext(ctx context.Context) *NetworkResolverPolicyMigrationPlanRepo {
	clone := *repo
	clone.ctx = ctx
	return &clone
}

// Builder opens the named harbord database for transactional plan reads.
func (repo *NetworkResolverPolicyMigrationPlanRepo) Builder() (*gorm.DB, error) {
	connection, err := repo.db.GetHarbord()
	if err != nil {
		return nil, err
	}
	if repo.ctx != nil {
		connection = connection.WithContext(repo.ctx)
	}
	return connection, nil
}
