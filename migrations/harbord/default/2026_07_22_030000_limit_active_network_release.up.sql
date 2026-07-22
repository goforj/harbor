-- Up migration (sqlite): serialize the machine-global network-release lifecycle.
CREATE UNIQUE INDEX operations_one_active_network_release_idx
    ON operations (kind)
    WHERE kind = 'network.release'
      AND project_id IS NULL
      AND state IN ('queued', 'running', 'requires_approval');
