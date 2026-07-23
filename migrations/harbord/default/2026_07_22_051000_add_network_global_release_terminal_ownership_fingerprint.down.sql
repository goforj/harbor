-- Down migration (sqlite): do not discard terminal ownership evidence retained for exact replay.
DROP TABLE IF EXISTS temp.harbor_global_release_terminal_ownership_fingerprint_rollback_guard;

CREATE TEMP TABLE harbor_global_release_terminal_ownership_fingerprint_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_global_release_terminal_ownership_fingerprint_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (
        SELECT 1
        FROM network_global_release_terminals
        WHERE released_ownership_fingerprint IS NOT NULL
          AND released_ownership_fingerprint <> ''
    ) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_terminal_ownership_fingerprint_rollback_guard;

ALTER TABLE network_global_release_terminals DROP COLUMN released_ownership_fingerprint;
