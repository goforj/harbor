-- Down migration (sqlite)
CREATE TABLE project_sessions_output_broker_old (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE CHECK (
        length(CAST(session_id AS BLOB)) BETWEEN 1 AND 256
        AND session_id = trim(session_id)
    ),
    project_id TEXT NOT NULL UNIQUE REFERENCES projects(project_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    owner TEXT NOT NULL CHECK (owner IN ('harbor', 'terminal')),
    state TEXT NOT NULL CHECK (state IN ('planned', 'awaiting_attach', 'attached', 'stopping', 'disconnected')),
    descriptor_digest TEXT NOT NULL CHECK (
        length(descriptor_digest) = 64
        AND descriptor_digest = lower(descriptor_digest)
        AND descriptor_digest NOT GLOB '*[^0-9a-f]*'
    ),
    credential_digest TEXT NOT NULL CHECK (
        length(credential_digest) = 64
        AND credential_digest = lower(credential_digest)
        AND credential_digest NOT GLOB '*[^0-9a-f]*'
    ),
    generation INTEGER NOT NULL CHECK (generation > 0),
    pid INTEGER CHECK (pid IS NULL OR pid > 0),
    birth_token TEXT CHECK (
        birth_token IS NULL
        OR (
            length(CAST(birth_token AS BLOB)) BETWEEN 1 AND 512
            AND birth_token = trim(birth_token)
        )
    ),
    executable_identity TEXT CHECK (
        executable_identity IS NULL
        OR (
            length(CAST(executable_identity AS BLOB)) BETWEEN 1 AND 4096
            AND executable_identity = trim(executable_identity)
        )
    ),
    argument_digest TEXT CHECK (
        argument_digest IS NULL
        OR (
            length(argument_digest) = 64
            AND argument_digest = lower(argument_digest)
            AND argument_digest NOT GLOB '*[^0-9a-f]*'
        )
    ),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL CHECK (updated_at >= created_at),
    CHECK (
        (state = 'planned'
            AND pid IS NULL
            AND birth_token IS NULL
            AND executable_identity IS NULL
            AND argument_digest IS NULL)
        OR
        (state <> 'planned'
            AND pid IS NOT NULL
            AND birth_token IS NOT NULL
            AND executable_identity IS NOT NULL
            AND argument_digest IS NOT NULL)
    )
);
INSERT INTO project_sessions_output_broker_old (
    id, session_id, project_id, owner, state, descriptor_digest, credential_digest, generation,
    pid, birth_token, executable_identity, argument_digest, created_at, updated_at
)
SELECT id, session_id, project_id, owner, state, descriptor_digest, credential_digest, generation,
    pid, birth_token, executable_identity, argument_digest, created_at, updated_at
FROM project_sessions;
DROP TABLE project_sessions;
ALTER TABLE project_sessions_output_broker_old RENAME TO project_sessions;
CREATE INDEX project_sessions_state_idx ON project_sessions (state, updated_at, project_id);
