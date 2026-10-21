-- Up migration (sqlite)
CREATE TABLE project_output_history (
    project_id TEXT NOT NULL PRIMARY KEY REFERENCES projects(project_id) ON DELETE CASCADE,
    session_id TEXT NOT NULL CHECK (length(CAST(session_id AS BLOB)) BETWEEN 1 AND 256 AND session_id = trim(session_id)),
    generation INTEGER NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
    ended_at DATETIME NOT NULL
);
