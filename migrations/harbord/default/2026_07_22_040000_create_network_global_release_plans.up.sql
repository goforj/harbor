-- Up migration (sqlite): retain one exact authority snapshot for the global network-release workflow.
CREATE TABLE network_global_release_plans (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL UNIQUE,
    operation_revision INTEGER NOT NULL CHECK (
        operation_revision BETWEEN 1 AND 9007199254740991
    ),
    network_state_id INTEGER NOT NULL UNIQUE CHECK (network_state_id = 1),
    network_revision INTEGER NOT NULL CHECK (
        network_revision BETWEEN 1 AND 9007199254740991
    ),
    network_updated_at DATETIME NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN (
        'runtime_release',
        'low_ports',
        'resolver',
        'trust',
        'loopbacks',
        'verify_effects',
        'ownership',
        'projection'
    )),
    authority_payload TEXT NOT NULL CHECK (
        length(CAST(authority_payload AS BLOB)) BETWEEN 2 AND 65536
    ),
    authority_digest TEXT NOT NULL CHECK (
        length(CAST(authority_digest AS BLOB)) = 64
        AND authority_digest NOT GLOB '*[^0-9a-f]*'
    ),
    FOREIGN KEY (operation_id, operation_revision)
        REFERENCES operations(id, revision)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    FOREIGN KEY (network_state_id, network_revision)
        REFERENCES network_state(id, revision)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);
