-- Up migration (sqlite)
ALTER TABLE network_state
    ADD COLUMN stage TEXT NOT NULL DEFAULT 'full' CHECK (stage IN ('identity', 'full'));
