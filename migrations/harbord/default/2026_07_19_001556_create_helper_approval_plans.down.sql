-- Down migration (sqlite)
DROP TABLE IF EXISTS helper_approval_plan_socket_requirements;
DROP TABLE IF EXISTS helper_approval_plans;

DROP INDEX IF EXISTS loopback_address_leases_approval_identity_idx;
DROP INDEX IF EXISTS operations_approval_project_revision_idx;
