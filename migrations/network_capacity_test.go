package migrations

import (
	"testing"

	"github.com/goforj/harbor/internal/logger"
)

// TestCapacityMigrationPreservesLegacyAndSupportsSafeDowngrade checks the real embedded migration stream and its rollback guard.
func TestCapacityMigrationPreservesLegacyAndSupportsSafeDowngrade(t *testing.T) {
	connections, db := newMigrationReadinessHarness(t)
	t.Setenv("FORJ_APP", "harbord")
	if err := NewMigrateCmd(logger.NewSilentLogger(), connections).Run(); err != nil {
		t.Fatal(err)
	}
	var capacity Migration
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == "2026_10_19_010000_expand_loopback_capacity" {
			capacity = migration
		}
	}
	if capacity == nil {
		t.Fatal("capacity migration not registered")
	}
	if err := capacity.Down(db); err != nil {
		t.Fatal("empty installation cannot downgrade", err)
	}
	if err := capacity.Up(db); err != nil {
		t.Fatal("capacity migration cannot reapply", err)
	}
	if err := db.Exec(`INSERT INTO network_state (id,stage,installation_id,ownership_generation,pool_network,pool_prefix_length,dns_suffix,created_at,updated_at,revision) VALUES (1,'identity','installation-test',1,'127.77.0.0',24,'.test','2026-10-19 00:00:00','2026-10-19 00:00:00',1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := capacity.Down(db); err == nil {
		t.Fatal("downgrade discarded an expanded installation")
	}
	var prefix int
	if err := db.Raw("SELECT pool_prefix_length FROM network_state WHERE id=1").Scan(&prefix).Error; err != nil || prefix != 24 {
		t.Fatal("blocked downgrade changed network state", err)
	}
}
