-- Down migration (sqlite): this is an intentional one-way repair. Removed
-- runtime-resource projections are regenerated only from a future live start.
SELECT 1;
