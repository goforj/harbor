-- Down migration (sqlite)
DROP TABLE IF EXISTS network_resolver_setup_plans;

DROP INDEX IF EXISTS operations_one_active_network_resolver_setup_idx;
DROP INDEX IF EXISTS network_state_resolver_setup_revision_idx;
