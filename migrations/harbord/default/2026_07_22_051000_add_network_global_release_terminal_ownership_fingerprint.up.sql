-- Up migration (sqlite): retain exact ownership receipt evidence for new global release terminals while preserving legacy replay records.
ALTER TABLE network_global_release_terminals
    ADD COLUMN released_ownership_fingerprint TEXT CHECK (
        released_ownership_fingerprint IS NULL
        OR released_ownership_fingerprint = ''
        OR (
            length(CAST(released_ownership_fingerprint AS BLOB)) = 64
            AND released_ownership_fingerprint NOT GLOB '*[^0-9a-f]*'
        )
    );
