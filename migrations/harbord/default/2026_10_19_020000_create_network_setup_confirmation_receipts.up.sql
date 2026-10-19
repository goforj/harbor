-- Up migration (sqlite)
CREATE TABLE network_setup_confirmation_receipts (
    plan_id INTEGER NOT NULL PRIMARY KEY CHECK (plan_id = 1),
    operation_id TEXT NOT NULL,
    approval_revision INTEGER NOT NULL CHECK (approval_revision BETWEEN 1 AND 9007199254740991),
    ownership_fingerprint TEXT NOT NULL CHECK (length(ownership_fingerprint) = 64 AND ownership_fingerprint NOT GLOB '*[^0-9a-f]*'),
    evidence_json TEXT NOT NULL CHECK (length(CAST(evidence_json AS BLOB)) BETWEEN 1 AND 131072 AND json_valid(evidence_json)),
    verified_at DATETIME NOT NULL,
    FOREIGN KEY (operation_id) REFERENCES operations (id) ON DELETE CASCADE
);
