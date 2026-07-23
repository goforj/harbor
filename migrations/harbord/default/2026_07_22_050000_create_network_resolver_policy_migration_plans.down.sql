-- Down migration (sqlite): never discard a durable resolver-policy retirement authority.
DROP TABLE IF EXISTS temp.harbor_network_resolver_policy_migration_rollback_guard;

CREATE TEMP TABLE harbor_network_resolver_policy_migration_rollback_guard (
    safe INTEGER NOT NULL CHECK (safe = 1)
);

INSERT INTO harbor_network_resolver_policy_migration_rollback_guard (safe)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM network_resolver_policy_migration_plans) THEN 0
    ELSE 1
END;

DROP TABLE temp.harbor_network_resolver_policy_migration_rollback_guard;

DROP TABLE network_resolver_policy_migration_plans;
DROP INDEX operations_one_active_network_resolver_policy_migration_idx;
