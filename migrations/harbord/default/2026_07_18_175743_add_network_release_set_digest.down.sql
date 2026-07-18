CREATE TEMP TABLE harbor_network_release_digest_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_network_release_digest_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (
        SELECT 1
        FROM network_project_releases
        WHERE state = 'completed'
    ) THEN 0
    ELSE 1
END;

DROP TABLE harbor_network_release_digest_rollback_guard;

ALTER TABLE network_project_releases DROP COLUMN release_set_digest;
