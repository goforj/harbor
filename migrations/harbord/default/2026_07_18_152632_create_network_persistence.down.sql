-- Down migration (sqlite)
DROP TABLE IF EXISTS network_project_releases;
DROP TABLE IF EXISTS public_endpoint_leases;
DROP TABLE IF EXISTS loopback_address_leases;
DROP TABLE IF EXISTS network_shared_listeners;
DROP TABLE IF EXISTS network_setup_evidence;
DROP TABLE IF EXISTS network_pool_candidates;
DROP TABLE IF EXISTS network_state;
