-- Up migration (sqlite)
CREATE UNIQUE INDEX operations_network_setup_revision_idx
    ON operations (id, revision);

CREATE UNIQUE INDEX operations_one_active_network_setup_idx
    ON operations (kind)
    WHERE kind = 'network.setup'
      AND project_id IS NULL
      AND state IN ('queued', 'running', 'requires_approval');

CREATE TABLE network_setup_plans (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL,
    operation_revision INTEGER NOT NULL CHECK (
        operation_revision BETWEEN 1 AND 9007199254740991
    ),
    ownership_schema_version INTEGER NOT NULL CHECK (ownership_schema_version = 1),
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
    ownership_generation INTEGER NOT NULL CHECK (ownership_generation = 1),
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
    ticket_verifier_key TEXT NOT NULL CHECK (
        length(CAST(ticket_verifier_key AS BLOB)) = 44
        AND substr(ticket_verifier_key, -1) = '='
        AND substr(ticket_verifier_key, 1, 43) NOT GLOB '*[^A-Za-z0-9+/]*'
    ),
    FOREIGN KEY (operation_id, operation_revision)
        REFERENCES operations(id, revision)
        ON UPDATE RESTRICT ON DELETE CASCADE
);
