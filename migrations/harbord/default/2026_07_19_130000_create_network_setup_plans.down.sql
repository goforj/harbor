-- Down migration (sqlite)
DROP TABLE IF EXISTS network_setup_plans;

DROP INDEX IF EXISTS operations_one_active_network_setup_idx;
DROP INDEX IF EXISTS operations_network_setup_revision_idx;
