-- Up migration (sqlite): repair development databases created before operation authority was embedded in resolver migration plans.
CREATE TABLE network_resolver_policy_migration_plans_repaired (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL UNIQUE,
    operation_kind TEXT NOT NULL CHECK (operation_kind = 'network.resolver.policy-migration'),
    operation_state TEXT NOT NULL CHECK (operation_state = 'requires_approval'),
    operation_phase TEXT NOT NULL CHECK (operation_phase = 'awaiting resolver policy migration approval'),
    operation_revision INTEGER NOT NULL CHECK (operation_revision BETWEEN 1 AND 9007199254740991),
    network_state_id INTEGER NOT NULL UNIQUE CHECK (network_state_id = 1),
    network_revision INTEGER NOT NULL CHECK (network_revision BETWEEN 1 AND 9007199254740991),
    source_ownership_schema_version INTEGER NOT NULL CHECK (source_ownership_schema_version = 2),
    source_ownership_fingerprint TEXT NOT NULL CHECK (length(CAST(source_ownership_fingerprint AS BLOB)) = 64),
    source_installation_id TEXT NOT NULL,
    source_owner_identity TEXT NOT NULL,
    source_ownership_generation INTEGER NOT NULL CHECK (source_ownership_generation BETWEEN 1 AND 9007199254740991),
    source_loopback_pool_prefix TEXT NOT NULL,
    source_network_policy_fingerprint TEXT NOT NULL CHECK (length(CAST(source_network_policy_fingerprint AS BLOB)) = 64),
    source_ticket_verifier_key TEXT NOT NULL,
    post_ownership_fingerprint TEXT NOT NULL CHECK (length(CAST(post_ownership_fingerprint AS BLOB)) = 64),
    replacement_policy_fingerprint TEXT NOT NULL CHECK (length(CAST(replacement_policy_fingerprint AS BLOB)) = 64),
    policy_suffix TEXT NOT NULL,
    policy_authority_fingerprint TEXT NOT NULL CHECK (length(CAST(policy_authority_fingerprint AS BLOB)) = 64),
    policy_resolver_mechanism TEXT NOT NULL CHECK (policy_resolver_mechanism = 'darwin-resolver-file-v1'),
    policy_low_ports_mechanism TEXT NOT NULL CHECK (policy_low_ports_mechanism = 'darwin-launchd-relay-v1'),
    policy_trust_mechanism TEXT NOT NULL CHECK (policy_trust_mechanism = 'darwin-current-user-trust-v1'),
    policy_dns_advertised_address TEXT NOT NULL,
    policy_dns_advertised_port INTEGER NOT NULL CHECK (policy_dns_advertised_port BETWEEN 1 AND 65535),
    policy_dns_bind_address TEXT NOT NULL,
    policy_dns_bind_port INTEGER NOT NULL CHECK (policy_dns_bind_port BETWEEN 1 AND 65535),
    policy_http_advertised_address TEXT NOT NULL,
    policy_http_advertised_port INTEGER NOT NULL CHECK (policy_http_advertised_port BETWEEN 1 AND 65535),
    policy_http_bind_address TEXT NOT NULL,
    policy_http_bind_port INTEGER NOT NULL CHECK (policy_http_bind_port BETWEEN 1 AND 65535),
    policy_https_advertised_address TEXT NOT NULL,
    policy_https_advertised_port INTEGER NOT NULL CHECK (policy_https_advertised_port BETWEEN 1 AND 65535),
    policy_https_bind_address TEXT NOT NULL,
    policy_https_bind_port INTEGER NOT NULL CHECK (policy_https_bind_port BETWEEN 1 AND 65535),
    FOREIGN KEY (operation_id) REFERENCES operations(id) ON DELETE RESTRICT,
    FOREIGN KEY (network_state_id) REFERENCES network_state(id) ON DELETE RESTRICT
);

INSERT INTO network_resolver_policy_migration_plans_repaired (
    id,
    operation_id,
    operation_kind,
    operation_state,
    operation_phase,
    operation_revision,
    network_state_id,
    network_revision,
    source_ownership_schema_version,
    source_ownership_fingerprint,
    source_installation_id,
    source_owner_identity,
    source_ownership_generation,
    source_loopback_pool_prefix,
    source_network_policy_fingerprint,
    source_ticket_verifier_key,
    post_ownership_fingerprint,
    replacement_policy_fingerprint,
    policy_suffix,
    policy_authority_fingerprint,
    policy_resolver_mechanism,
    policy_low_ports_mechanism,
    policy_trust_mechanism,
    policy_dns_advertised_address,
    policy_dns_advertised_port,
    policy_dns_bind_address,
    policy_dns_bind_port,
    policy_http_advertised_address,
    policy_http_advertised_port,
    policy_http_bind_address,
    policy_http_bind_port,
    policy_https_advertised_address,
    policy_https_advertised_port,
    policy_https_bind_address,
    policy_https_bind_port
)
SELECT
    plan.id,
    plan.operation_id,
    operation.kind,
    operation.state,
    operation.phase,
    plan.operation_revision,
    plan.network_state_id,
    plan.network_revision,
    plan.source_ownership_schema_version,
    plan.source_ownership_fingerprint,
    plan.source_installation_id,
    plan.source_owner_identity,
    plan.source_ownership_generation,
    plan.source_loopback_pool_prefix,
    plan.source_network_policy_fingerprint,
    plan.source_ticket_verifier_key,
    plan.post_ownership_fingerprint,
    plan.replacement_policy_fingerprint,
    plan.policy_suffix,
    plan.policy_authority_fingerprint,
    plan.policy_resolver_mechanism,
    plan.policy_low_ports_mechanism,
    plan.policy_trust_mechanism,
    plan.policy_dns_advertised_address,
    plan.policy_dns_advertised_port,
    plan.policy_dns_bind_address,
    plan.policy_dns_bind_port,
    plan.policy_http_advertised_address,
    plan.policy_http_advertised_port,
    plan.policy_http_bind_address,
    plan.policy_http_bind_port,
    plan.policy_https_advertised_address,
    plan.policy_https_advertised_port,
    plan.policy_https_bind_address,
    plan.policy_https_bind_port
FROM network_resolver_policy_migration_plans AS plan
INNER JOIN operations AS operation
    ON operation.id = plan.operation_id
   AND operation.revision = plan.operation_revision;

CREATE TEMP TABLE harbor_network_resolver_policy_migration_plan_repair_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_network_resolver_policy_migration_plan_repair_guard (safe)
SELECT CASE
    WHEN
        (SELECT COUNT(*) FROM network_resolver_policy_migration_plans_repaired) =
        (SELECT COUNT(*) FROM network_resolver_policy_migration_plans)
    THEN 1
    ELSE 0
END;

DROP TABLE temp.harbor_network_resolver_policy_migration_plan_repair_guard;
DROP TABLE network_resolver_policy_migration_plans;
ALTER TABLE network_resolver_policy_migration_plans_repaired RENAME TO network_resolver_policy_migration_plans;
