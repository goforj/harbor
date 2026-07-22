-- Up migration (sqlite): retain sanitized trusted-ingress authority across helper and runtime boundaries.
CREATE TABLE network_data_plane_setup_plans (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    operation_revision INTEGER NOT NULL CHECK (
        operation_revision BETWEEN 1 AND 9007199254740991
    ),
    phase TEXT NOT NULL CHECK (phase IN ('low_port_approval', 'activation')),
    network_state_id INTEGER NOT NULL UNIQUE CHECK (network_state_id = 1)
        REFERENCES network_state(id) ON UPDATE RESTRICT ON DELETE RESTRICT,
    network_revision INTEGER NOT NULL CHECK (
        network_revision BETWEEN 1 AND 9007199254740991
    ),
    network_updated_at DATETIME NOT NULL,
    authority_payload TEXT NOT NULL CHECK (
        length(CAST(authority_payload AS BLOB)) BETWEEN 2 AND 65536
    ),
    authority_digest TEXT NOT NULL CHECK (
        length(CAST(authority_digest AS BLOB)) = 64
        AND authority_digest NOT GLOB '*[^0-9a-f]*'
    ),
    trust_evidence_digest TEXT NOT NULL CHECK (
        length(CAST(trust_evidence_digest AS BLOB)) = 64
        AND trust_evidence_digest NOT GLOB '*[^0-9a-f]*'
    ),
    trust_verified_at DATETIME NOT NULL,
    low_port_evidence_digest TEXT CHECK (
        low_port_evidence_digest IS NULL
        OR (
            length(CAST(low_port_evidence_digest AS BLOB)) = 64
            AND low_port_evidence_digest NOT GLOB '*[^0-9a-f]*'
        )
    ),
    activation_payload TEXT CHECK (
        activation_payload IS NULL
        OR length(CAST(activation_payload AS BLOB)) BETWEEN 2 AND 65536
    ),
    activation_digest TEXT CHECK (
        activation_digest IS NULL
        OR (
            length(CAST(activation_digest AS BLOB)) = 64
            AND activation_digest NOT GLOB '*[^0-9a-f]*'
        )
    ),
    CHECK (
        (
            phase = 'low_port_approval'
            AND low_port_evidence_digest IS NULL
            AND activation_payload IS NULL
            AND activation_digest IS NULL
        )
        OR (
            phase = 'activation'
            AND low_port_evidence_digest IS NOT NULL
            AND activation_payload IS NOT NULL
            AND activation_digest IS NOT NULL
        )
    )
);
