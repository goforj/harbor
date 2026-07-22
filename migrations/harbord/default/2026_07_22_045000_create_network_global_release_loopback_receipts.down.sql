-- Down migration (sqlite): do not discard an acknowledged loopback release receipt.
DROP TABLE IF EXISTS temp.harbor_global_release_loopback_receipt_rollback_guard;

CREATE TEMP TABLE harbor_global_release_loopback_receipt_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_global_release_loopback_receipt_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM network_global_release_loopback_receipts)
      OR EXISTS (
        SELECT 1
        FROM network_global_release_plans
        WHERE phase NOT IN (
            'runtime_release',
            'low_ports',
            'resolver',
            'trust',
            'loopbacks'
        )
    ) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_loopback_receipt_rollback_guard;

DROP TABLE network_global_release_loopback_receipts;
