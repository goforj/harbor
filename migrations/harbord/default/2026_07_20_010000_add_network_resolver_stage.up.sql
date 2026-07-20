-- Up migration (sqlite)
ALTER TABLE network_state
    ADD COLUMN resolver_stage TEXT NOT NULL DEFAULT 'full' CHECK (resolver_stage IN ('identity', 'resolver', 'full'));

UPDATE network_state SET resolver_stage = stage;

ALTER TABLE network_state DROP COLUMN stage;

ALTER TABLE network_state RENAME COLUMN resolver_stage TO stage;
