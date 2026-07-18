ALTER TABLE network_project_releases
ADD COLUMN release_set_digest TEXT CHECK (
    (state = 'releasing' AND release_set_digest IS NULL)
    OR
    (state = 'completed'
        AND release_set_digest IS NOT NULL
        AND length(CAST(release_set_digest AS BLOB)) = 64
        AND release_set_digest NOT GLOB '*[^0-9a-f]*')
);
