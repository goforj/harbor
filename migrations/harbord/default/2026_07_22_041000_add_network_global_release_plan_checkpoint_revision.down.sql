-- Down migration (sqlite): restore the released global release-plan schema.
DROP TABLE IF EXISTS temp.harbor_global_release_checkpoint_rollback_guard;

CREATE TEMP TABLE harbor_global_release_checkpoint_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_global_release_checkpoint_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (
        SELECT 1
        FROM network_global_release_plans
        WHERE phase <> 'runtime_release'
           OR checkpoint_revision <> operation_revision
    ) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_checkpoint_rollback_guard;

ALTER TABLE network_global_release_plans RENAME TO network_global_release_plans_checkpointed;

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

INSERT INTO network_global_release_plans (
    id,
    operation_id,
    operation_revision,
    network_state_id,
    network_revision,
    network_updated_at,
    phase,
    authority_payload,
    authority_digest
)
SELECT
    id,
    operation_id,
    operation_revision,
    network_state_id,
    network_revision,
    network_updated_at,
    phase,
    authority_payload,
    authority_digest
FROM network_global_release_plans_checkpointed;

DROP TABLE network_global_release_plans_checkpointed;
