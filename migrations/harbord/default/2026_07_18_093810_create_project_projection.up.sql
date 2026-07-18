-- Up migration (sqlite)
ALTER TABLE operation_journal_state RENAME TO harbor_state;

CREATE TABLE projects (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL UNIQUE CHECK (length(CAST(project_id AS BLOB)) BETWEEN 1 AND 256 AND project_id = trim(project_id)),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 512 AND name = trim(name)),
    path TEXT NOT NULL UNIQUE CHECK (length(CAST(path AS BLOB)) > 0 AND path = trim(path)),
    slug TEXT NOT NULL UNIQUE CHECK (
        length(CAST(slug AS BLOB)) BETWEEN 1 AND 63
        AND slug = lower(slug)
        AND slug NOT GLOB '*[^a-z0-9-]*'
        AND substr(slug, 1, 1) <> '-'
        AND substr(slug, -1, 1) <> '-'
    ),
    state TEXT NOT NULL CHECK (state IN ('stopped', 'starting', 'ready', 'rebuilding', 'degraded', 'failed', 'stopping', 'unavailable')),
    favorite BOOLEAN NOT NULL CHECK (favorite IN (0, 1)),
    updated_at DATETIME NOT NULL,
    revision INTEGER NOT NULL UNIQUE CHECK (revision > 0)
);

CREATE INDEX projects_presentation_order_idx ON projects (state, favorite DESC, updated_at DESC, project_id);

CREATE TABLE project_apps (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    app_id TEXT NOT NULL CHECK (length(CAST(app_id AS BLOB)) BETWEEN 1 AND 256 AND app_id = trim(app_id)),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 512 AND name = trim(name)),
    state TEXT NOT NULL CHECK (state IN ('ready', 'working', 'degraded', 'failed', 'stopped', 'unavailable')),
    active BOOLEAN NOT NULL CHECK (active IN (0, 1)),
    required BOOLEAN NOT NULL CHECK (required IN (0, 1)),
    UNIQUE (project_id, app_id)
);

CREATE INDEX project_apps_canonical_order_idx ON project_apps (project_id, app_id);

CREATE TABLE project_services (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    service_id TEXT NOT NULL CHECK (length(CAST(service_id AS BLOB)) BETWEEN 1 AND 256 AND service_id = trim(service_id)),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 512 AND name = trim(name)),
    kind TEXT NOT NULL CHECK (length(CAST(kind AS BLOB)) BETWEEN 1 AND 256 AND kind = trim(kind)),
    state TEXT NOT NULL CHECK (state IN ('ready', 'working', 'degraded', 'failed', 'stopped', 'unavailable')),
    owner TEXT NOT NULL CHECK (owner IN ('compose', 'external')),
    selection TEXT NOT NULL CHECK (selection IN ('selected', 'available')),
    required BOOLEAN NOT NULL CHECK (required IN (0, 1)),
    UNIQUE (project_id, service_id)
);

CREATE INDEX project_services_canonical_order_idx ON project_services (project_id, service_id);
CREATE INDEX project_services_selection_idx ON project_services (project_id, selection, state, service_id);

CREATE TABLE project_resources (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL CHECK (length(CAST(resource_id AS BLOB)) BETWEEN 1 AND 256 AND resource_id = trim(resource_id)),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 512 AND name = trim(name)),
    kind TEXT NOT NULL CHECK (length(CAST(kind AS BLOB)) BETWEEN 1 AND 256 AND kind = trim(kind)),
    url TEXT NOT NULL CHECK (length(CAST(url AS BLOB)) > 0 AND url = trim(url)),
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('app', 'service')),
    owner_app_id TEXT CHECK (owner_app_id IS NULL OR (length(CAST(owner_app_id AS BLOB)) BETWEEN 1 AND 256 AND owner_app_id = trim(owner_app_id))),
    owner_service_id TEXT CHECK (owner_service_id IS NULL OR (length(CAST(owner_service_id AS BLOB)) BETWEEN 1 AND 256 AND owner_service_id = trim(owner_service_id))),
    UNIQUE (project_id, resource_id),
    FOREIGN KEY (project_id, owner_app_id) REFERENCES project_apps(project_id, app_id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, owner_service_id) REFERENCES project_services(project_id, service_id) ON DELETE CASCADE,
    CHECK (
        (owner_kind = 'app' AND owner_app_id IS NOT NULL AND owner_service_id IS NULL)
        OR
        (owner_kind = 'service' AND owner_app_id IS NULL AND owner_service_id IS NOT NULL)
    )
);

CREATE INDEX project_resources_canonical_order_idx ON project_resources (project_id, resource_id);
CREATE INDEX project_resources_kind_idx ON project_resources (project_id, kind, resource_id);

CREATE TABLE recent_resources (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    accessed_at DATETIME NOT NULL,
    sequence INTEGER NOT NULL UNIQUE CHECK (sequence > 0),
    UNIQUE (project_id, resource_id),
    FOREIGN KEY (project_id, resource_id) REFERENCES project_resources(project_id, resource_id) ON DELETE CASCADE
);

CREATE INDEX recent_resources_canonical_order_idx ON recent_resources (sequence DESC, project_id, resource_id);
