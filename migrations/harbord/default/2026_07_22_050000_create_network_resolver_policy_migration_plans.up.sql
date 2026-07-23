-- Up migration (sqlite): bind one legacy macOS resolver retirement to its replacement policy.
CREATE UNIQUE INDEX operations_one_active_network_resolver_policy_migration_idx
    ON operations (kind)
    WHERE kind = 'network.resolver.policy-migration'
      AND project_id IS NULL
      AND state IN ('queued', 'running', 'requires_approval');

CREATE TABLE network_resolver_policy_migration_plans (
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
