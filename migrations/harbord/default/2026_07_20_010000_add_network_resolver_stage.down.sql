-- Down migration (sqlite)
DROP TABLE IF EXISTS harbor_network_resolver_stage_rollback_guard;

CREATE TEMP TABLE harbor_network_resolver_stage_rollback_guard (
    resolver_rows INTEGER NOT NULL CHECK (resolver_rows = 0)
);

INSERT INTO harbor_network_resolver_stage_rollback_guard (resolver_rows)
SELECT COUNT(*) FROM network_state WHERE stage = 'resolver';

DROP TABLE harbor_network_resolver_stage_rollback_guard;

ALTER TABLE network_state
    ADD COLUMN previous_stage TEXT NOT NULL DEFAULT 'full' CHECK (previous_stage IN ('identity', 'full'));

UPDATE network_state SET previous_stage = stage;

ALTER TABLE network_state DROP COLUMN stage;

ALTER TABLE network_state RENAME COLUMN previous_stage TO stage;
