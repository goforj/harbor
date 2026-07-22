-- Up migration (sqlite): retain the exact verified trust disposition before loopback teardown.
DROP TABLE IF EXISTS temp.harbor_global_release_trust_receipt_upgrade_guard;

CREATE TEMP TABLE harbor_global_release_trust_receipt_upgrade_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_global_release_trust_receipt_upgrade_guard (safe)
SELECT CASE
    WHEN EXISTS (
        SELECT 1
        FROM network_global_release_plans
        WHERE phase NOT IN ('runtime_release', 'low_ports', 'resolver', 'trust')
    ) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_trust_receipt_upgrade_guard;

CREATE TABLE network_global_release_trust_receipts (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL UNIQUE,
    source_checkpoint_revision INTEGER NOT NULL CHECK (
        source_checkpoint_revision BETWEEN 1 AND 9007199254740991
    ),
    disposition TEXT NOT NULL CHECK (
        disposition IN ('owned', 'preexisting_unowned')
    ),
    confirmation_digest TEXT NOT NULL CHECK (
        length(CAST(confirmation_digest AS BLOB)) = 64
        AND confirmation_digest NOT GLOB '*[^0-9a-f]*'
    ),
    observation_fingerprint TEXT NOT NULL CHECK (
        length(CAST(observation_fingerprint AS BLOB)) = 64
        AND observation_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    verified_at DATETIME NOT NULL CHECK (
        length(CAST(verified_at AS BLOB)) > 0
        AND strftime('%Y-%m-%dT%H:%M:%SZ', verified_at) IS NOT NULL
    ),
    FOREIGN KEY (operation_id)
        REFERENCES network_global_release_plans(operation_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);
