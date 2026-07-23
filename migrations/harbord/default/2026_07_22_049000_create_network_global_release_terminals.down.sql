-- Down migration (sqlite): do not discard successful global network-release replay fences.
DROP TABLE IF EXISTS temp.harbor_global_release_terminal_rollback_guard;

CREATE TEMP TABLE harbor_global_release_terminal_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_global_release_terminal_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM network_global_release_terminals) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_terminal_rollback_guard;

DROP TABLE network_global_release_terminals;
