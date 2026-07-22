-- Up migration (sqlite): serialize the machine-global trusted-ingress approval lifecycle.
CREATE UNIQUE INDEX operations_one_active_network_dataplane_setup_idx
    ON operations (kind)
    WHERE kind = 'network.data-plane.setup'
      AND project_id IS NULL
      AND state IN ('queued', 'running', 'requires_approval');
