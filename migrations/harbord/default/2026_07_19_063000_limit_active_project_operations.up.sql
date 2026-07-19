-- Up migration (sqlite)
CREATE UNIQUE INDEX operations_one_active_project_idx
    ON operations (project_id)
    WHERE project_id IS NOT NULL
      AND state IN ('queued', 'running', 'requires_approval');
