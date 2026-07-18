-- Up migration (sqlite)
CREATE TABLE operation_journal_state (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    sequence INTEGER NOT NULL CHECK (sequence >= 0)
);

INSERT INTO operation_journal_state (id, sequence) VALUES (1, 0);

CREATE TABLE operations (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 256 AND id = trim(id)),
    intent_id TEXT NOT NULL UNIQUE CHECK (length(intent_id) BETWEEN 1 AND 256 AND intent_id = trim(intent_id)),
    kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 256 AND kind = trim(kind)),
    project_id TEXT CHECK (project_id IS NULL OR (length(project_id) BETWEEN 1 AND 256 AND project_id = trim(project_id))),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'requires_approval', 'succeeded', 'failed', 'cancelled')),
    phase TEXT NOT NULL CHECK (length(trim(phase)) > 0),
    problem_code TEXT CHECK (problem_code IS NULL OR (length(problem_code) BETWEEN 1 AND 128 AND problem_code = trim(problem_code))),
    problem_message TEXT CHECK (problem_message IS NULL OR (length(problem_message) BETWEEN 1 AND 4096 AND problem_message = trim(problem_message))),
    problem_retryable BOOLEAN CHECK (problem_retryable IS NULL OR problem_retryable IN (0, 1)),
    requested_at DATETIME NOT NULL,
    started_at DATETIME,
    finished_at DATETIME,
    revision INTEGER NOT NULL UNIQUE CHECK (revision > 0),
    CHECK (
        (problem_code IS NULL AND problem_message IS NULL AND problem_retryable IS NULL)
        OR
        (problem_code IS NOT NULL AND problem_message IS NOT NULL AND problem_retryable IS NOT NULL)
    ),
    CHECK (started_at IS NULL OR started_at >= requested_at),
    CHECK (finished_at IS NULL OR finished_at >= coalesce(started_at, requested_at)),
    CHECK (
        (state = 'queued' AND started_at IS NULL AND finished_at IS NULL AND problem_code IS NULL)
        OR
        (state IN ('running', 'requires_approval') AND started_at IS NOT NULL AND finished_at IS NULL AND problem_code IS NULL)
        OR
        (state = 'succeeded' AND started_at IS NOT NULL AND finished_at IS NOT NULL AND problem_code IS NULL)
        OR
        (state = 'failed' AND started_at IS NOT NULL AND finished_at IS NOT NULL AND problem_code IS NOT NULL)
        OR
        (state = 'cancelled' AND finished_at IS NOT NULL AND problem_code IS NULL)
    )
);

CREATE INDEX operations_active_revision_idx ON operations (state, revision, id);

CREATE TABLE operation_transitions (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    previous_state TEXT CHECK (previous_state IS NULL OR previous_state IN ('queued', 'running', 'requires_approval')),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'requires_approval', 'succeeded', 'failed', 'cancelled')),
    phase TEXT NOT NULL CHECK (length(trim(phase)) > 0),
    problem_code TEXT CHECK (problem_code IS NULL OR (length(problem_code) BETWEEN 1 AND 128 AND problem_code = trim(problem_code))),
    problem_message TEXT CHECK (problem_message IS NULL OR (length(problem_message) BETWEEN 1 AND 4096 AND problem_message = trim(problem_message))),
    problem_retryable BOOLEAN CHECK (problem_retryable IS NULL OR problem_retryable IN (0, 1)),
    occurred_at DATETIME NOT NULL,
    sequence INTEGER NOT NULL UNIQUE CHECK (sequence > 0),
    UNIQUE (operation_id, ordinal),
    CHECK (
        (problem_code IS NULL AND problem_message IS NULL AND problem_retryable IS NULL)
        OR
        (problem_code IS NOT NULL AND problem_message IS NOT NULL AND problem_retryable IS NOT NULL)
    ),
    CHECK ((state = 'failed' AND problem_code IS NOT NULL) OR (state <> 'failed' AND problem_code IS NULL)),
    CHECK (
        (ordinal = 1 AND previous_state IS NULL AND state = 'queued')
        OR
        (ordinal > 1 AND previous_state = 'queued' AND state IN ('running', 'cancelled'))
        OR
        (ordinal > 1 AND previous_state = 'running' AND state IN ('requires_approval', 'succeeded', 'failed', 'cancelled'))
        OR
        (ordinal > 1 AND previous_state = 'requires_approval' AND state IN ('running', 'failed', 'cancelled'))
    )
);

CREATE INDEX operation_transitions_sequence_idx ON operation_transitions (sequence, operation_id, ordinal);
