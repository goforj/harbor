-- Down migration (sqlite): canonical machine ownership remains in the protected helper store.
DROP TABLE IF EXISTS machine_ownership_projections;
