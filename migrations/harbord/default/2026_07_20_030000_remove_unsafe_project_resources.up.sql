-- Up migration (sqlite): discard legacy derived resources that cannot be safely
-- projected without Harbor-owned routing. Runtime resources are rebuilt from a
-- future successful project start. Checkouts, volumes, secrets, and operations
-- are not represented by this projection table.
DELETE FROM project_resources
WHERE url NOT GLOB 'http://127.*'
  AND url NOT GLOB 'https://127.*'
  AND url NOT GLOB 'http://[[]::1]*'
  AND url NOT GLOB 'https://[[]::1]*'
  AND url NOT GLOB 'http://*.test*'
  AND url NOT GLOB 'https://*.test*';
