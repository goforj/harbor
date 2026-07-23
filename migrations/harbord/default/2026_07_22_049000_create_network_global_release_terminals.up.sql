-- Up migration (sqlite): retain the minimal successful global network-release replay fence.
CREATE TABLE network_global_release_terminals (
    operation_id TEXT NOT NULL PRIMARY KEY,
    owner_identity TEXT NOT NULL CHECK (
        length(CAST(owner_identity AS BLOB)) BETWEEN 1 AND 256
        AND owner_identity = trim(owner_identity)
        AND owner_identity NOT GLOB '*[^A-Za-z0-9-]*'
    ),
    source_checkpoint_revision INTEGER NOT NULL CHECK (
        source_checkpoint_revision BETWEEN 1 AND 9007199254740991
    ),
    network_revision INTEGER NOT NULL CHECK (
        network_revision BETWEEN 1 AND 9007199254740991
    ),
    FOREIGN KEY (operation_id)
        REFERENCES operations(id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
);
