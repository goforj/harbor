-- Down migration (sqlite): do not discard an acknowledged global ownership release receipt.
DROP TABLE IF EXISTS temp.harbor_global_release_ownership_receipt_rollback_guard;

CREATE TEMP TABLE harbor_global_release_ownership_receipt_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

-- Projection depends on the ownership receipt, even when the receipt table is empty.
INSERT INTO harbor_global_release_ownership_receipt_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM network_global_release_ownership_receipts)
      OR EXISTS (
        SELECT 1
        FROM network_global_release_plans
        WHERE phase = 'projection'
    ) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_global_release_ownership_receipt_rollback_guard;

DROP TABLE network_global_release_ownership_receipts;
