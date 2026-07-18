-- Down migration (sqlite)
DROP TABLE IF EXISTS recent_resources;
DROP TABLE IF EXISTS project_resources;
DROP TABLE IF EXISTS project_services;
DROP TABLE IF EXISTS project_apps;
DROP TABLE IF EXISTS projects;

ALTER TABLE harbor_state RENAME TO operation_journal_state;
