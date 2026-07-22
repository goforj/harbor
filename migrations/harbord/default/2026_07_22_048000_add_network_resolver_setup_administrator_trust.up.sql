-- Up migration (sqlite): admit the administrator macOS trust profile while retaining legacy persisted plans.
CREATE TABLE network_resolver_setup_plans_upgrade (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL,
    operation_revision INTEGER NOT NULL CHECK (
        operation_revision BETWEEN 1 AND 9007199254740991
    ),
    network_state_id INTEGER NOT NULL UNIQUE CHECK (network_state_id = 1),
    network_revision INTEGER NOT NULL CHECK (
        network_revision BETWEEN 1 AND 9007199254740991
    ),
    source_ownership_fingerprint TEXT NOT NULL CHECK (
        length(CAST(source_ownership_fingerprint AS BLOB)) = 64
        AND source_ownership_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    target_ownership_schema_version INTEGER NOT NULL CHECK (
        target_ownership_schema_version = 2
    ),
    target_installation_id TEXT NOT NULL CHECK (
        length(CAST(target_installation_id AS BLOB)) BETWEEN 1 AND 128
        AND target_installation_id = trim(target_installation_id)
        AND target_installation_id NOT GLOB '*[^A-Za-z0-9._-]*'
        AND substr(target_installation_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND substr(target_installation_id, -1, 1) GLOB '[A-Za-z0-9]'
    ),
    target_owner_identity TEXT NOT NULL CHECK (
        length(CAST(target_owner_identity AS BLOB)) BETWEEN 1 AND 256
        AND target_owner_identity = trim(target_owner_identity)
        AND target_owner_identity NOT GLOB '*[^A-Za-z0-9-]*'
    ),
    target_ownership_generation INTEGER NOT NULL CHECK (
        target_ownership_generation BETWEEN 1 AND 9007199254740991
    ),
    target_loopback_pool_prefix TEXT NOT NULL CHECK (
        length(CAST(target_loopback_pool_prefix AS BLOB)) BETWEEN 11 AND 18
        AND target_loopback_pool_prefix = trim(target_loopback_pool_prefix)
        AND target_loopback_pool_prefix GLOB '127.[0-9]*.[0-9]*.[0-9]*/29'
        AND target_loopback_pool_prefix NOT GLOB '*[^0-9./]*'
        AND length(target_loopback_pool_prefix) - length(replace(target_loopback_pool_prefix, '.', '')) = 3
        AND substr(target_loopback_pool_prefix, -3) = '/29'
        AND (
            target_loopback_pool_prefix GLOB '*.0/29'
            OR target_loopback_pool_prefix GLOB '*.8/29'
            OR target_loopback_pool_prefix GLOB '*.16/29'
            OR target_loopback_pool_prefix GLOB '*.24/29'
            OR target_loopback_pool_prefix GLOB '*.32/29'
            OR target_loopback_pool_prefix GLOB '*.40/29'
            OR target_loopback_pool_prefix GLOB '*.48/29'
            OR target_loopback_pool_prefix GLOB '*.56/29'
            OR target_loopback_pool_prefix GLOB '*.64/29'
            OR target_loopback_pool_prefix GLOB '*.72/29'
            OR target_loopback_pool_prefix GLOB '*.80/29'
            OR target_loopback_pool_prefix GLOB '*.88/29'
            OR target_loopback_pool_prefix GLOB '*.96/29'
            OR target_loopback_pool_prefix GLOB '*.104/29'
            OR target_loopback_pool_prefix GLOB '*.112/29'
            OR target_loopback_pool_prefix GLOB '*.120/29'
            OR target_loopback_pool_prefix GLOB '*.128/29'
            OR target_loopback_pool_prefix GLOB '*.136/29'
            OR target_loopback_pool_prefix GLOB '*.144/29'
            OR target_loopback_pool_prefix GLOB '*.152/29'
            OR target_loopback_pool_prefix GLOB '*.160/29'
            OR target_loopback_pool_prefix GLOB '*.168/29'
            OR target_loopback_pool_prefix GLOB '*.176/29'
            OR target_loopback_pool_prefix GLOB '*.184/29'
            OR target_loopback_pool_prefix GLOB '*.192/29'
            OR target_loopback_pool_prefix GLOB '*.200/29'
            OR target_loopback_pool_prefix GLOB '*.208/29'
            OR target_loopback_pool_prefix GLOB '*.216/29'
            OR target_loopback_pool_prefix GLOB '*.224/29'
            OR target_loopback_pool_prefix GLOB '*.232/29'
            OR target_loopback_pool_prefix GLOB '*.240/29'
            OR target_loopback_pool_prefix GLOB '*.248/29'
        )
    ),
    target_network_policy_fingerprint TEXT NOT NULL CHECK (
        length(CAST(target_network_policy_fingerprint AS BLOB)) = 64
        AND target_network_policy_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    target_ticket_verifier_key TEXT NOT NULL CHECK (
        length(CAST(target_ticket_verifier_key AS BLOB)) = 44
        AND substr(target_ticket_verifier_key, -1) = '='
        AND substr(target_ticket_verifier_key, 1, 43) NOT GLOB '*[^A-Za-z0-9+/]*'
    ),
    policy_suffix TEXT NOT NULL CHECK (policy_suffix = '.test'),
    policy_authority_fingerprint TEXT NOT NULL CHECK (
        length(CAST(policy_authority_fingerprint AS BLOB)) = 64
        AND policy_authority_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    policy_resolver_mechanism TEXT NOT NULL CHECK (
        policy_resolver_mechanism IN (
            'darwin-resolver-file-v1',
            'ubuntu-systemd-resolved-v1',
            'windows-nrpt-v1'
        )
    ),
    policy_low_ports_mechanism TEXT NOT NULL CHECK (
        policy_low_ports_mechanism IN (
            'darwin-launchd-relay-v1',
            'ubuntu-nftables-v1',
            'windows-direct-low-ports-v1'
        )
    ),
    policy_trust_mechanism TEXT NOT NULL CHECK (
        policy_trust_mechanism IN (
            'darwin-current-user-trust-v1',
            'darwin-administrator-trust-v1',
            'ubuntu-system-trust-v1',
            'windows-current-user-trust-v1'
        )
    ),
    policy_dns_advertised_address TEXT NOT NULL CHECK (
        length(CAST(policy_dns_advertised_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_dns_advertised_address = trim(policy_dns_advertised_address)
        AND policy_dns_advertised_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_dns_advertised_address NOT GLOB '*[^0-9.]*'
        AND length(policy_dns_advertised_address) - length(replace(policy_dns_advertised_address, '.', '')) = 3
    ),
    policy_dns_advertised_port INTEGER NOT NULL CHECK (
        policy_dns_advertised_port BETWEEN 1 AND 65535
    ),
    policy_dns_bind_address TEXT NOT NULL CHECK (
        length(CAST(policy_dns_bind_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_dns_bind_address = trim(policy_dns_bind_address)
        AND policy_dns_bind_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_dns_bind_address NOT GLOB '*[^0-9.]*'
        AND length(policy_dns_bind_address) - length(replace(policy_dns_bind_address, '.', '')) = 3
    ),
    policy_dns_bind_port INTEGER NOT NULL CHECK (
        policy_dns_bind_port BETWEEN 1 AND 65535
    ),
    policy_http_advertised_address TEXT NOT NULL CHECK (
        length(CAST(policy_http_advertised_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_http_advertised_address = trim(policy_http_advertised_address)
        AND policy_http_advertised_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_http_advertised_address NOT GLOB '*[^0-9.]*'
        AND length(policy_http_advertised_address) - length(replace(policy_http_advertised_address, '.', '')) = 3
    ),
    policy_http_advertised_port INTEGER NOT NULL CHECK (
        policy_http_advertised_port BETWEEN 1 AND 65535
    ),
    policy_http_bind_address TEXT NOT NULL CHECK (
        length(CAST(policy_http_bind_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_http_bind_address = trim(policy_http_bind_address)
        AND policy_http_bind_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_http_bind_address NOT GLOB '*[^0-9.]*'
        AND length(policy_http_bind_address) - length(replace(policy_http_bind_address, '.', '')) = 3
    ),
    policy_http_bind_port INTEGER NOT NULL CHECK (
        policy_http_bind_port BETWEEN 1 AND 65535
    ),
    policy_https_advertised_address TEXT NOT NULL CHECK (
        length(CAST(policy_https_advertised_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_https_advertised_address = trim(policy_https_advertised_address)
        AND policy_https_advertised_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_https_advertised_address NOT GLOB '*[^0-9.]*'
        AND length(policy_https_advertised_address) - length(replace(policy_https_advertised_address, '.', '')) = 3
    ),
    policy_https_advertised_port INTEGER NOT NULL CHECK (
        policy_https_advertised_port BETWEEN 1 AND 65535
    ),
    policy_https_bind_address TEXT NOT NULL CHECK (
        length(CAST(policy_https_bind_address AS BLOB)) BETWEEN 7 AND 15
        AND policy_https_bind_address = trim(policy_https_bind_address)
        AND policy_https_bind_address GLOB '127.[0-9]*.[0-9]*.[0-9]*'
        AND policy_https_bind_address NOT GLOB '*[^0-9.]*'
        AND length(policy_https_bind_address) - length(replace(policy_https_bind_address, '.', '')) = 3
    ),
    policy_https_bind_port INTEGER NOT NULL CHECK (
        policy_https_bind_port BETWEEN 1 AND 65535
    ),
    FOREIGN KEY (operation_id, operation_revision)
        REFERENCES operations(id, revision)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    FOREIGN KEY (network_state_id, network_revision)
        REFERENCES network_state(id, revision)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CHECK (
        (policy_resolver_mechanism = 'darwin-resolver-file-v1'
            AND policy_low_ports_mechanism = 'darwin-launchd-relay-v1'
            AND policy_trust_mechanism = 'darwin-current-user-trust-v1')
        OR
        (policy_resolver_mechanism = 'darwin-resolver-file-v1'
            AND policy_low_ports_mechanism = 'darwin-launchd-relay-v1'
            AND policy_trust_mechanism = 'darwin-administrator-trust-v1')
        OR
        (policy_resolver_mechanism = 'ubuntu-systemd-resolved-v1'
            AND policy_low_ports_mechanism = 'ubuntu-nftables-v1'
            AND policy_trust_mechanism = 'ubuntu-system-trust-v1')
        OR
        (policy_resolver_mechanism = 'windows-nrpt-v1'
            AND policy_low_ports_mechanism = 'windows-direct-low-ports-v1'
            AND policy_trust_mechanism = 'windows-current-user-trust-v1')
    ),
    CHECK (
        policy_http_advertised_address = '127.0.0.1'
        AND policy_http_advertised_port = 80
        AND policy_https_advertised_address = '127.0.0.1'
        AND policy_https_advertised_port = 443
    ),
    CHECK (
        (
            policy_resolver_mechanism IN (
                'darwin-resolver-file-v1',
                'ubuntu-systemd-resolved-v1'
            )
            AND policy_dns_advertised_address = '127.0.0.1'
            AND policy_dns_advertised_port = policy_dns_bind_port
            AND policy_dns_advertised_address = policy_dns_bind_address
            AND policy_dns_bind_port BETWEEN 1024 AND 65535
            AND policy_http_bind_address = '127.0.0.1'
            AND policy_http_bind_port BETWEEN 1024 AND 65535
            AND policy_http_bind_port <> policy_http_advertised_port
            AND policy_https_bind_address = '127.0.0.1'
            AND policy_https_bind_port BETWEEN 1024 AND 65535
            AND policy_https_bind_port <> policy_https_advertised_port
        )
        OR
        (
            policy_resolver_mechanism = 'windows-nrpt-v1'
            AND policy_dns_advertised_address = policy_dns_bind_address
            AND policy_dns_advertised_port = 53
            AND policy_dns_bind_port = 53
            AND policy_dns_bind_address <> '127.0.0.1'
            AND policy_http_bind_address = policy_http_advertised_address
            AND policy_http_bind_port = policy_http_advertised_port
            AND policy_https_bind_address = policy_https_advertised_address
            AND policy_https_bind_port = policy_https_advertised_port
        )
    ),
    CHECK (
        (policy_dns_advertised_address <> policy_http_advertised_address OR policy_dns_advertised_port <> policy_http_advertised_port)
        AND (policy_dns_advertised_address <> policy_http_bind_address OR policy_dns_advertised_port <> policy_http_bind_port)
        AND (policy_dns_bind_address <> policy_http_advertised_address OR policy_dns_bind_port <> policy_http_advertised_port)
        AND (policy_dns_bind_address <> policy_http_bind_address OR policy_dns_bind_port <> policy_http_bind_port)
        AND (policy_dns_advertised_address <> policy_https_advertised_address OR policy_dns_advertised_port <> policy_https_advertised_port)
        AND (policy_dns_advertised_address <> policy_https_bind_address OR policy_dns_advertised_port <> policy_https_bind_port)
        AND (policy_dns_bind_address <> policy_https_advertised_address OR policy_dns_bind_port <> policy_https_advertised_port)
        AND (policy_dns_bind_address <> policy_https_bind_address OR policy_dns_bind_port <> policy_https_bind_port)
        AND (policy_http_advertised_address <> policy_https_advertised_address OR policy_http_advertised_port <> policy_https_advertised_port)
        AND (policy_http_advertised_address <> policy_https_bind_address OR policy_http_advertised_port <> policy_https_bind_port)
        AND (policy_http_bind_address <> policy_https_advertised_address OR policy_http_bind_port <> policy_https_advertised_port)
        AND (policy_http_bind_address <> policy_https_bind_address OR policy_http_bind_port <> policy_https_bind_port)
    )
);


INSERT INTO network_resolver_setup_plans_upgrade (
    id,
    operation_id,
    operation_revision,
    network_state_id,
    network_revision,
    source_ownership_fingerprint,
    target_ownership_schema_version,
    target_installation_id,
    target_owner_identity,
    target_ownership_generation,
    target_loopback_pool_prefix,
    target_network_policy_fingerprint,
    target_ticket_verifier_key,
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
    id,
    operation_id,
    operation_revision,
    network_state_id,
    network_revision,
    source_ownership_fingerprint,
    target_ownership_schema_version,
    target_installation_id,
    target_owner_identity,
    target_ownership_generation,
    target_loopback_pool_prefix,
    target_network_policy_fingerprint,
    target_ticket_verifier_key,
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
FROM network_resolver_setup_plans;

DROP TABLE network_resolver_setup_plans;

ALTER TABLE network_resolver_setup_plans_upgrade RENAME TO network_resolver_setup_plans;
