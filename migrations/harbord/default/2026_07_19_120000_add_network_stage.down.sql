-- Down migration (sqlite)
DROP TABLE IF EXISTS harbor_network_stage_rollback_guard;

CREATE TEMP TABLE harbor_network_stage_rollback_guard (
    identity_rows INTEGER NOT NULL CHECK (identity_rows = 0)
);

INSERT INTO harbor_network_stage_rollback_guard (identity_rows)
SELECT COUNT(*) FROM network_state WHERE stage = 'identity';

DROP TABLE harbor_network_stage_rollback_guard;

ALTER TABLE network_state DROP COLUMN stage;
