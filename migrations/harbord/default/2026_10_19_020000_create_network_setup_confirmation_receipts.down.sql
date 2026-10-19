-- Refuse rollback while verified effects still need durable confirmation.
CREATE TABLE IF NOT EXISTS network_setup_confirmation_rollback_guard (pending INTEGER NOT NULL CHECK (pending = 0));
INSERT INTO network_setup_confirmation_rollback_guard SELECT count(*) FROM network_setup_confirmation_receipts r JOIN operations o ON o.id = r.operation_id WHERE o.state IN ('queued','running','requires_approval');
DROP TABLE network_setup_confirmation_rollback_guard;
DROP TABLE network_setup_confirmation_receipts;
