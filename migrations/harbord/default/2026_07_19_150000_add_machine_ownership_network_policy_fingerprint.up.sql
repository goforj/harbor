-- Up migration (sqlite): bind schema-two ownership confirmations to the exact host-network policy they authorize.
CREATE TABLE machine_ownership_projections_policy_bound (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    network_state_id INTEGER NOT NULL UNIQUE CHECK (network_state_id = 1),
    ownership_schema_version INTEGER NOT NULL CHECK (ownership_schema_version IN (1, 2)),
    installation_id TEXT NOT NULL CHECK (
        length(CAST(installation_id AS BLOB)) BETWEEN 1 AND 128
        AND installation_id = trim(installation_id)
        AND installation_id NOT GLOB '*[^A-Za-z0-9._-]*'
        AND substr(installation_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND substr(installation_id, -1, 1) GLOB '[A-Za-z0-9]'
    ),
    owner_identity TEXT NOT NULL CHECK (
        length(CAST(owner_identity AS BLOB)) BETWEEN 1 AND 256
        AND owner_identity = trim(owner_identity)
        AND owner_identity NOT GLOB '*[^A-Za-z0-9-]*'
    ),
    ownership_generation INTEGER NOT NULL CHECK (
        ownership_generation BETWEEN 1 AND 9007199254740991
    ),
    loopback_pool_prefix TEXT NOT NULL CHECK (
        length(CAST(loopback_pool_prefix AS BLOB)) BETWEEN 11 AND 18
        AND loopback_pool_prefix = trim(loopback_pool_prefix)
        AND loopback_pool_prefix GLOB '127.[0-9]*.[0-9]*.[0-9]*/29'
        AND loopback_pool_prefix NOT GLOB '*[^0-9./]*'
        AND length(loopback_pool_prefix) - length(replace(loopback_pool_prefix, '.', '')) = 3
        AND substr(loopback_pool_prefix, -3) = '/29'
        AND (
            loopback_pool_prefix GLOB '*.0/29'
            OR loopback_pool_prefix GLOB '*.8/29'
            OR loopback_pool_prefix GLOB '*.16/29'
            OR loopback_pool_prefix GLOB '*.24/29'
            OR loopback_pool_prefix GLOB '*.32/29'
            OR loopback_pool_prefix GLOB '*.40/29'
            OR loopback_pool_prefix GLOB '*.48/29'
            OR loopback_pool_prefix GLOB '*.56/29'
            OR loopback_pool_prefix GLOB '*.64/29'
            OR loopback_pool_prefix GLOB '*.72/29'
            OR loopback_pool_prefix GLOB '*.80/29'
            OR loopback_pool_prefix GLOB '*.88/29'
            OR loopback_pool_prefix GLOB '*.96/29'
            OR loopback_pool_prefix GLOB '*.104/29'
            OR loopback_pool_prefix GLOB '*.112/29'
            OR loopback_pool_prefix GLOB '*.120/29'
            OR loopback_pool_prefix GLOB '*.128/29'
            OR loopback_pool_prefix GLOB '*.136/29'
            OR loopback_pool_prefix GLOB '*.144/29'
            OR loopback_pool_prefix GLOB '*.152/29'
            OR loopback_pool_prefix GLOB '*.160/29'
            OR loopback_pool_prefix GLOB '*.168/29'
            OR loopback_pool_prefix GLOB '*.176/29'
            OR loopback_pool_prefix GLOB '*.184/29'
            OR loopback_pool_prefix GLOB '*.192/29'
            OR loopback_pool_prefix GLOB '*.200/29'
            OR loopback_pool_prefix GLOB '*.208/29'
            OR loopback_pool_prefix GLOB '*.216/29'
            OR loopback_pool_prefix GLOB '*.224/29'
            OR loopback_pool_prefix GLOB '*.232/29'
            OR loopback_pool_prefix GLOB '*.240/29'
            OR loopback_pool_prefix GLOB '*.248/29'
        )
    ),
    network_policy_fingerprint TEXT CHECK (
        (ownership_schema_version = 1 AND network_policy_fingerprint IS NULL)
        OR
        (ownership_schema_version = 2
            AND network_policy_fingerprint IS NOT NULL
            AND length(network_policy_fingerprint) = 64
            AND length(CAST(network_policy_fingerprint AS BLOB)) = 64
            AND network_policy_fingerprint NOT GLOB '*[^0-9a-f]*')
    ),
    ticket_verifier_key TEXT NOT NULL CHECK (
        length(CAST(ticket_verifier_key AS BLOB)) = 44
        AND substr(ticket_verifier_key, -1) = '='
        AND substr(ticket_verifier_key, 1, 43) NOT GLOB '*[^A-Za-z0-9+/]*'
    ),
    record_fingerprint TEXT NOT NULL CHECK (
        length(CAST(record_fingerprint AS BLOB)) = 64
        AND record_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    confirmed_at DATETIME NOT NULL,
    FOREIGN KEY (network_state_id)
        REFERENCES network_state(id)
        ON UPDATE RESTRICT ON DELETE CASCADE
);

INSERT INTO machine_ownership_projections_policy_bound
    (id, network_state_id, ownership_schema_version, installation_id, owner_identity,
     ownership_generation, loopback_pool_prefix, network_policy_fingerprint,
     ticket_verifier_key, record_fingerprint, confirmed_at)
SELECT id, network_state_id, ownership_schema_version, installation_id, owner_identity,
       ownership_generation, loopback_pool_prefix, NULL,
       ticket_verifier_key, record_fingerprint, confirmed_at
FROM machine_ownership_projections;

DROP TABLE machine_ownership_projections;
ALTER TABLE machine_ownership_projections_policy_bound RENAME TO machine_ownership_projections;
