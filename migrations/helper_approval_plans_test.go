package migrations

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

const helperApprovalPlansMigrationName = "2026_07_19_001556_create_helper_approval_plans"

// TestHelperApprovalPlansMigrationCreatesConstrainedSchema verifies the embedded migration installs every plan authority and lookup index.
func TestHelperApprovalPlansMigrationCreatesConstrainedSchema(t *testing.T) {
	databaseConnection := newHelperApprovalMigrationHarness(t)

	for table, columns := range map[string][]string{
		"helper_approval_plans": {
			"id",
			"operation_id",
			"operation_revision",
			"network_state_id",
			"mutation",
			"lease_state",
			"project_id",
			"kind",
			"secondary_id",
			"address",
			"ownership_installation_id",
			"ownership_generation",
			"loopback_address_lease_id",
		},
		"helper_approval_plan_socket_requirements": {
			"id",
			"helper_approval_plan_id",
			"transport",
			"port",
		},
	} {
		if !databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("migration did not create %s", table)
		}
		for _, column := range columns {
			if !databaseConnection.Migrator().HasColumn(table, column) {
				t.Fatalf("migration did not create %s.%s", table, column)
			}
		}
	}

	for table, indexes := range map[string][]string{
		"operations": {
			"operations_approval_project_revision_idx",
		},
		"loopback_address_leases": {
			"loopback_address_leases_approval_identity_idx",
		},
		"helper_approval_plans": {
			"helper_approval_plans_operation_idx",
			"helper_approval_plans_lease_idx",
		},
		"helper_approval_plan_socket_requirements": {
			"helper_approval_plan_socket_requirements_order_idx",
		},
	} {
		for _, index := range indexes {
			if !databaseConnection.Migrator().HasIndex(table, index) {
				t.Fatalf("migration did not create %s on %s", index, table)
			}
		}
	}
	assertHelperApprovalForeignKeys(t, databaseConnection)
}

// TestHelperApprovalPlansMigrationAcceptsDurableEffects verifies pending, active, and multi-effect operation plans retain their exact shape.
func TestHelperApprovalPlansMigrationAcceptsDurableEffects(t *testing.T) {
	t.Run("pending ensure with empty requirements", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-pending", "operation-pending", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)

		planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:       "operation-pending",
			OperationRevision: 2,
			ProjectID:         "project-pending",
			Mutation:          "ensure_loopback_identity",
			LeaseState:        "pending",
			Kind:              "primary",
			Address:           "127.77.0.10",
		})

		assertProjectionCount(t, databaseConnection, "helper_approval_plans", 1)
		assertProjectionCount(t, databaseConnection, "helper_approval_plan_socket_requirements", 0)
		var read struct {
			ID                     int
			LoopbackAddressLeaseID *int
		}
		if err := databaseConnection.Raw(
			"SELECT id, loopback_address_lease_id FROM helper_approval_plans WHERE id = ?",
			planID,
		).Scan(&read).Error; err != nil {
			t.Fatalf("read pending helper approval plan: %v", err)
		}
		if read.ID != planID || read.LoopbackAddressLeaseID != nil {
			t.Fatalf("pending helper approval plan = %#v, want ID %d without an active lease", read, planID)
		}
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})

	t.Run("active exact lease", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-active", "operation-active", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
		leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-active", "127.77.0.10")

		planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:            "operation-active",
			OperationRevision:      2,
			ProjectID:              "project-active",
			Mutation:               "release_loopback_identity",
			LeaseState:             "active",
			Kind:                   "primary",
			Address:                "127.77.0.10",
			LoopbackAddressLeaseID: &leaseID,
		})
		mustExecNetworkMigration(t, databaseConnection, `INSERT INTO helper_approval_plan_socket_requirements
			(helper_approval_plan_id, transport, port) VALUES (?, 'tcp4', 3306)`, planID)

		var read struct {
			LeaseID   int
			ProjectID string
			Address   string
		}
		if err := databaseConnection.Raw(`SELECT
			loopback_address_lease_id AS lease_id, project_id, address
			FROM helper_approval_plans WHERE id = ?`, planID).Scan(&read).Error; err != nil {
			t.Fatalf("read active helper approval plan: %v", err)
		}
		if read.LeaseID != leaseID || read.ProjectID != "project-active" || read.Address != "127.77.0.10" {
			t.Fatalf("active helper approval plan = %#v, want exact lease %d", read, leaseID)
		}
		assertProjectionCount(t, databaseConnection, "helper_approval_plan_socket_requirements", 1)
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})

	t.Run("active lease with historical ownership generation", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-historical", "operation-historical", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
		leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-historical", "127.77.0.10")
		mustExecNetworkMigration(t, databaseConnection, `UPDATE network_state
			SET ownership_generation = 2, updated_at = '2026-07-18T12:01:00Z', revision = 4
			WHERE id = 1`)

		planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:            "operation-historical",
			OperationRevision:      2,
			ProjectID:              "project-historical",
			Mutation:               "release_loopback_identity",
			LeaseState:             "active",
			Kind:                   "primary",
			Address:                "127.77.0.10",
			LoopbackAddressLeaseID: &leaseID,
		})

		var read struct {
			PlanGeneration  int
			LeaseGeneration int
			RootGeneration  int
		}
		if err := databaseConnection.Raw(`SELECT
			plans.ownership_generation AS plan_generation,
			leases.ownership_generation AS lease_generation,
			network_state.ownership_generation AS root_generation
			FROM helper_approval_plans AS plans
			JOIN loopback_address_leases AS leases ON leases.id = plans.loopback_address_lease_id
			JOIN network_state ON network_state.id = plans.network_state_id
			WHERE plans.id = ?`, planID).Scan(&read).Error; err != nil {
			t.Fatalf("read historical helper approval ownership: %v", err)
		}
		if read.PlanGeneration != 1 || read.LeaseGeneration != 1 || read.RootGeneration != 2 {
			t.Fatalf("historical ownership generations = %#v, want plan 1, lease 1, root 2", read)
		}
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})

	t.Run("multiple effects for one operation", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-multiple", "operation-multiple", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
		insertNetworkMigrationCandidate(t, databaseConnection, 2, "127.77.0.11", 1)

		insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:       "operation-multiple",
			OperationRevision: 2,
			ProjectID:         "project-multiple",
			Mutation:          "ensure_loopback_identity",
			LeaseState:        "pending",
			Kind:              "primary",
			Address:           "127.77.0.10",
		})
		insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:       "operation-multiple",
			OperationRevision: 2,
			ProjectID:         "project-multiple",
			Mutation:          "ensure_loopback_identity",
			LeaseState:        "pending",
			Kind:              "secondary",
			SecondaryID:       "mysql-3306",
			Address:           "127.77.0.11",
		})

		assertProjectionCount(t, databaseConnection, "helper_approval_plans", 2)
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})
}

// TestHelperApprovalPlansMigrationRejectsInvalidAuthority verifies direct writers cannot widen one operation-bound helper effect.
func TestHelperApprovalPlansMigrationRejectsInvalidAuthority(t *testing.T) {
	databaseConnection := newHelperApprovalMigrationHarness(t)
	seedHelperApprovalOwner(t, databaseConnection, "project-one", "operation-one", 1, 2)
	seedHelperApprovalOwner(t, databaseConnection, "project-two", "operation-two", 4, 5)
	insertNetworkMigrationState(t, databaseConnection, 6)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 2, "127.77.0.11", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 3, "127.77.0.12", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 4, "127.77.0.13", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 5, "127.77.0.14", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 6, "127.77.0.15", 1)
	leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-one", "127.77.0.10")
	secondaryLeaseID := insertHelperApprovalLease(
		t,
		databaseConnection,
		"project-one",
		"secondary",
		"mysql",
		"127.77.0.13",
		"installation-a",
		1,
	)
	installationLeaseID := insertHelperApprovalLease(
		t,
		databaseConnection,
		"project-one",
		"secondary",
		"redis",
		"127.77.0.14",
		"installation-b",
		1,
	)
	generationLeaseID := insertHelperApprovalLease(
		t,
		databaseConnection,
		"project-one",
		"secondary",
		"mail",
		"127.77.0.15",
		"installation-a",
		2,
	)

	validPending := helperApprovalPlanFixture{
		OperationID:       "operation-one",
		OperationRevision: 2,
		ProjectID:         "project-one",
		Mutation:          "ensure_loopback_identity",
		LeaseState:        "pending",
		Kind:              "secondary",
		SecondaryID:       "valid-secondary",
		Address:           "127.77.0.11",
	}
	validPlanID := insertHelperApprovalPlan(t, databaseConnection, validPending)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO helper_approval_plan_socket_requirements
		(helper_approval_plan_id, transport, port) VALUES (?, 'tcp4', 443)`, validPlanID)

	invalidPlans := []struct {
		name    string
		fixture helperApprovalPlanFixture
	}{
		{name: "operation project mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-one", OperationRevision: 2, ProjectID: "project-two",
			Mutation: "ensure_loopback_identity", LeaseState: "pending", Kind: "primary", Address: "127.77.0.12",
		}},
		{name: "missing operation revision", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 999, ProjectID: "project-two",
			Mutation: "ensure_loopback_identity", LeaseState: "pending", Kind: "primary", Address: "127.77.0.12",
		}},
		{name: "address outside candidate pool", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "ensure_loopback_identity", LeaseState: "pending", Kind: "primary", Address: "127.77.0.99",
		}},
		{name: "pending release", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "release_loopback_identity", LeaseState: "pending", Kind: "primary", Address: "127.77.0.12",
		}},
		{name: "active without lease", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "primary", Address: "127.77.0.12",
		}},
		{name: "active lease address mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "primary", Address: "127.77.0.12",
			LoopbackAddressLeaseID: &leaseID,
		}},
		{name: "active lease kind mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-one", OperationRevision: 2, ProjectID: "project-one",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "secondary", SecondaryID: "kind-mismatch", Address: "127.77.0.10",
			LoopbackAddressLeaseID: &leaseID,
		}},
		{name: "active lease secondary identity mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-one", OperationRevision: 2, ProjectID: "project-one",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "secondary", SecondaryID: "postgres", Address: "127.77.0.13",
			LoopbackAddressLeaseID: &secondaryLeaseID,
		}},
		{name: "active lease ownership installation mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-one", OperationRevision: 2, ProjectID: "project-one",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "secondary", SecondaryID: "redis", Address: "127.77.0.14",
			LoopbackAddressLeaseID: &installationLeaseID,
		}},
		{name: "active lease ownership generation mismatch", fixture: helperApprovalPlanFixture{
			OperationID: "operation-one", OperationRevision: 2, ProjectID: "project-one",
			Mutation: "release_loopback_identity", LeaseState: "active", Kind: "secondary", SecondaryID: "mail", Address: "127.77.0.15",
			LoopbackAddressLeaseID: &generationLeaseID,
		}},
		{name: "primary with secondary identity", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "ensure_loopback_identity", LeaseState: "pending", Kind: "primary", SecondaryID: "mysql", Address: "127.77.0.12",
		}},
		{name: "unsupported mutation", fixture: helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "run_command", LeaseState: "pending", Kind: "primary", Address: "127.77.0.12",
		}},
	}
	for _, test := range invalidPlans {
		t.Run(test.name, func(t *testing.T) {
			assertHelperApprovalPlanInsertFails(t, databaseConnection, test.fixture)
		})
	}

	t.Run("operation revision above durable upper bound", func(t *testing.T) {
		const oversizedRevision int64 = 9007199254740992
		insertProjectionProject(
			t,
			databaseConnection,
			"project-oversized",
			"/work/project-oversized",
			"project-oversized",
			7,
		)
		requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
		startedAt := requestedAt.Add(time.Minute)
		if err := databaseConnection.Exec(`INSERT INTO operations
			(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
			VALUES ('operation-oversized', 'intent-operation-oversized', 'project.start',
			'project-oversized', 'requires_approval', 'requires_approval', ?, ?, ?)`,
			requestedAt,
			startedAt,
			oversizedRevision,
		).Error; err != nil {
			t.Fatalf("insert oversized-revision operation fixture: %v", err)
		}
		assertMigrationStatementFails(t, databaseConnection, `INSERT INTO helper_approval_plans
			(operation_id, operation_revision, network_state_id, mutation, lease_state,
			 project_id, kind, secondary_id, address, ownership_installation_id,
			 ownership_generation, loopback_address_lease_id)
			VALUES ('operation-oversized', ?, 1, 'ensure_loopback_identity', 'pending',
			'project-oversized', 'primary', '', '127.77.0.12', 'installation-a', 1, NULL)`,
			oversizedRevision,
		)
	})

	t.Run("duplicate logical lease key", func(t *testing.T) {
		duplicate := validPending
		duplicate.OperationID = "operation-two"
		duplicate.OperationRevision = 5
		duplicate.Address = "127.77.0.12"
		assertHelperApprovalPlanInsertFails(t, databaseConnection, duplicate)
	})
	t.Run("duplicate address", func(t *testing.T) {
		duplicate := helperApprovalPlanFixture{
			OperationID: "operation-two", OperationRevision: 5, ProjectID: "project-two",
			Mutation: "ensure_loopback_identity", LeaseState: "pending", Kind: "primary", Address: validPending.Address,
		}
		assertHelperApprovalPlanInsertFails(t, databaseConnection, duplicate)
	})

	for _, requirement := range []struct {
		name      string
		transport string
		port      int
	}{
		{name: "unsupported transport", transport: "sctp4", port: 443},
		{name: "zero port", transport: "udp4", port: 0},
		{name: "oversized port", transport: "tcp4", port: 65536},
		{name: "duplicate requirement", transport: "tcp4", port: 443},
	} {
		t.Run(requirement.name, func(t *testing.T) {
			assertMigrationStatementFails(t, databaseConnection, `INSERT INTO helper_approval_plan_socket_requirements
				(helper_approval_plan_id, transport, port) VALUES (?, ?, ?)`,
				validPlanID, requirement.transport, requirement.port)
		})
	}
	assertHelperApprovalForeignKeys(t, databaseConnection)
}

// TestHelperApprovalPlansMigrationRejectsDeletedProjectAuthority verifies pending authority cannot exist without its project.
func TestHelperApprovalPlansMigrationRejectsDeletedProjectAuthority(t *testing.T) {
	t.Run("insert after project deletion", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-deleted", "operation-deleted", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
		mustExecNetworkMigration(t, databaseConnection,
			"DELETE FROM projects WHERE project_id = 'project-deleted'")

		assertHelperApprovalPlanInsertFails(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:       "operation-deleted",
			OperationRevision: 2,
			ProjectID:         "project-deleted",
			Mutation:          "ensure_loopback_identity",
			LeaseState:        "pending",
			Kind:              "primary",
			Address:           "127.77.0.10",
		})
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})

	t.Run("deletion under pending plan", func(t *testing.T) {
		databaseConnection := newHelperApprovalMigrationHarness(t)
		seedHelperApprovalOwner(t, databaseConnection, "project-delete", "operation-delete", 1, 2)
		insertNetworkMigrationState(t, databaseConnection, 3)
		insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
		insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
			OperationID:       "operation-delete",
			OperationRevision: 2,
			ProjectID:         "project-delete",
			Mutation:          "ensure_loopback_identity",
			LeaseState:        "pending",
			Kind:              "primary",
			Address:           "127.77.0.10",
		})

		assertMigrationStatementFails(t, databaseConnection,
			"DELETE FROM projects WHERE project_id = 'project-delete'")
		assertProjectionCount(t, databaseConnection, "projects", 1)
		assertProjectionCount(t, databaseConnection, "helper_approval_plans", 1)
		assertHelperApprovalForeignKeys(t, databaseConnection)
	})
}

// TestHelperApprovalPlansMigrationLocksOperationRevision verifies a plan must be retired in the same transaction that advances its operation.
func TestHelperApprovalPlansMigrationLocksOperationRevision(t *testing.T) {
	databaseConnection := newHelperApprovalMigrationHarness(t)
	seedHelperApprovalOwner(t, databaseConnection, "project-lock", "operation-lock", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
		OperationID:       "operation-lock",
		OperationRevision: 2,
		ProjectID:         "project-lock",
		Mutation:          "ensure_loopback_identity",
		LeaseState:        "pending",
		Kind:              "primary",
		Address:           "127.77.0.10",
	})

	assertMigrationStatementFails(t, databaseConnection,
		"UPDATE operations SET state = 'running', phase = 'resuming', revision = 4 WHERE id = 'operation-lock'")
	mustExecNetworkMigration(t, databaseConnection, "DELETE FROM helper_approval_plans WHERE id = ?", planID)
	mustExecNetworkMigration(t, databaseConnection,
		"UPDATE operations SET state = 'running', phase = 'resuming', revision = 4 WHERE id = 'operation-lock'")

	var operation struct {
		State    string
		Revision int
	}
	if err := databaseConnection.Raw(
		"SELECT state, revision FROM operations WHERE id = 'operation-lock'",
	).Scan(&operation).Error; err != nil {
		t.Fatalf("read advanced operation: %v", err)
	}
	if operation.State != "running" || operation.Revision != 4 {
		t.Fatalf("advanced operation = %#v, want running revision 4", operation)
	}
}

// TestHelperApprovalPlansMigrationCascadesRequirements verifies no socket authority survives its owning plan.
func TestHelperApprovalPlansMigrationCascadesRequirements(t *testing.T) {
	databaseConnection := newHelperApprovalMigrationHarness(t)
	seedHelperApprovalOwner(t, databaseConnection, "project-cascade", "operation-cascade", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
		OperationID:       "operation-cascade",
		OperationRevision: 2,
		ProjectID:         "project-cascade",
		Mutation:          "ensure_loopback_identity",
		LeaseState:        "pending",
		Kind:              "primary",
		Address:           "127.77.0.10",
	})
	for _, requirement := range []struct {
		transport string
		port      int
	}{{transport: "tcp4", port: 443}, {transport: "udp4", port: 53}} {
		mustExecNetworkMigration(t, databaseConnection, `INSERT INTO helper_approval_plan_socket_requirements
			(helper_approval_plan_id, transport, port) VALUES (?, ?, ?)`, planID, requirement.transport, requirement.port)
	}
	assertProjectionCount(t, databaseConnection, "helper_approval_plan_socket_requirements", 2)

	mustExecNetworkMigration(t, databaseConnection, "DELETE FROM helper_approval_plans WHERE id = ?", planID)
	assertProjectionCount(t, databaseConnection, "helper_approval_plan_socket_requirements", 0)
	assertHelperApprovalForeignKeys(t, databaseConnection)
}

// TestHelperApprovalPlansMigrationRollbackPreservesPriorState verifies reversal removes only approval-plan schema and data.
func TestHelperApprovalPlansMigrationRollbackPreservesPriorState(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)
	seedHelperApprovalOwner(t, databaseConnection, "project-rollback", "operation-rollback", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-rollback", "127.77.0.10")
	migration := helperApprovalPlansMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply helper approval migration: %v", err)
	}
	planID := insertHelperApprovalPlan(t, databaseConnection, helperApprovalPlanFixture{
		OperationID:            "operation-rollback",
		OperationRevision:      2,
		ProjectID:              "project-rollback",
		Mutation:               "release_loopback_identity",
		LeaseState:             "active",
		Kind:                   "primary",
		Address:                "127.77.0.10",
		LoopbackAddressLeaseID: &leaseID,
	})
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO helper_approval_plan_socket_requirements
		(helper_approval_plan_id, transport, port) VALUES (?, 'tcp4', 3306)`, planID)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback helper approval migration: %v", err)
	}
	for _, table := range []string{"helper_approval_plan_socket_requirements", "helper_approval_plans"} {
		if databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("rollback retained %s", table)
		}
	}
	for table, index := range map[string]string{
		"operations":              "operations_approval_project_revision_idx",
		"loopback_address_leases": "loopback_address_leases_approval_identity_idx",
	} {
		if databaseConnection.Migrator().HasIndex(table, index) {
			t.Fatalf("rollback retained %s on %s", index, table)
		}
	}

	assertProjectionCount(t, databaseConnection, "projects", 1)
	assertProjectionCount(t, databaseConnection, "operations", 1)
	assertProjectionCount(t, databaseConnection, "network_state", 1)
	assertProjectionCount(t, databaseConnection, "network_pool_candidates", 1)
	assertProjectionCount(t, databaseConnection, "loopback_address_leases", 1)
	var operation struct {
		State    string
		Revision int
	}
	if err := databaseConnection.Raw(
		"SELECT state, revision FROM operations WHERE id = 'operation-rollback'",
	).Scan(&operation).Error; err != nil {
		t.Fatalf("read operation after helper approval rollback: %v", err)
	}
	if operation.State != "requires_approval" || operation.Revision != 2 {
		t.Fatalf("operation after helper approval rollback = %#v, want requires_approval revision 2", operation)
	}
	assertHelperApprovalForeignKeys(t, databaseConnection)
}

// insertHelperApprovalLease creates an exact active lease fixture for composite approval-authority probes.
func insertHelperApprovalLease(
	t *testing.T,
	databaseConnection *gorm.DB,
	projectID string,
	kind string,
	secondaryID string,
	address string,
	ownershipInstallationID string,
	ownershipGeneration int,
) int {
	t.Helper()
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO loopback_address_leases
		(network_state_id, project_id, source_project_id, kind, secondary_id, address, state,
		 lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at)
		VALUES (1, ?, ?, ?, ?, ?, 'leased', 1, ?, ?, 'verified ensure', '2026-07-18T12:00:00Z')`,
		projectID,
		projectID,
		kind,
		secondaryID,
		address,
		ownershipInstallationID,
		ownershipGeneration,
	)
	var id int
	if err := databaseConnection.Raw(
		"SELECT id FROM loopback_address_leases WHERE project_id = ? AND address = ?",
		projectID,
		address,
	).Scan(&id).Error; err != nil {
		t.Fatalf("read helper approval lease identity: %v", err)
	}
	if id == 0 {
		t.Fatal("helper approval lease identity is zero")
	}
	return id
}

// helperApprovalPlanFixture is the direct SQL shape used to probe migration constraints.
type helperApprovalPlanFixture struct {
	OperationID             string
	OperationRevision       int
	ProjectID               string
	Mutation                string
	LeaseState              string
	Kind                    string
	SecondaryID             string
	Address                 string
	OwnershipInstallationID string
	OwnershipGeneration     int
	LoopbackAddressLeaseID  *int
}

// newHelperApprovalMigrationHarness opens the migrated production schema for one focused test.
func newHelperApprovalMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() {
		closeOperationMigrationDatabase(t, connections)
	})
	applyNetworkPersistenceMigrations(t, databaseConnection)
	if err := helperApprovalPlansMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply helper approval migration: %v", err)
	}
	return databaseConnection
}

// helperApprovalPlansMigration finds the production approval-plan migration through Harbor's embedded registry.
func helperApprovalPlansMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == helperApprovalPlansMigrationName {
			return migration
		}
	}
	t.Fatalf("helper approval migration %q is not registered", helperApprovalPlansMigrationName)
	return nil
}

// seedHelperApprovalOwner creates one project-bound operation already waiting at its durable approval revision.
func seedHelperApprovalOwner(
	t *testing.T,
	databaseConnection *gorm.DB,
	projectID string,
	operationID string,
	projectRevision int,
	operationRevision int,
) {
	t.Helper()
	insertProjectionProject(t, databaseConnection, projectID, "/work/"+projectID, projectID, projectRevision)
	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	if err := insertMigrationOperation(
		databaseConnection,
		operationID,
		"intent-"+operationID,
		"requires_approval",
		requestedAt,
		&startedAt,
		nil,
		nil,
		operationRevision,
	); err != nil {
		t.Fatalf("insert helper approval operation: %v", err)
	}
	if err := databaseConnection.Exec(
		"UPDATE operations SET project_id = ?, kind = 'project.start' WHERE id = ?",
		projectID,
		operationID,
	).Error; err != nil {
		t.Fatalf("bind helper approval operation to project: %v", err)
	}
}

// insertHelperApprovalPlan inserts one valid fixture and returns its durable row identity.
func insertHelperApprovalPlan(t *testing.T, databaseConnection *gorm.DB, fixture helperApprovalPlanFixture) int {
	t.Helper()
	fixture = defaultHelperApprovalPlanFixture(fixture)
	if err := executeHelperApprovalPlanInsert(databaseConnection, fixture); err != nil {
		t.Fatalf("insert helper approval plan: %v", err)
	}
	var id int
	if err := databaseConnection.Raw(`SELECT id FROM helper_approval_plans
		WHERE operation_id = ? AND project_id = ? AND secondary_id = ?`,
		fixture.OperationID,
		fixture.ProjectID,
		fixture.SecondaryID,
	).Scan(&id).Error; err != nil {
		t.Fatalf("read helper approval plan identity: %v", err)
	}
	if id == 0 {
		t.Fatal("helper approval plan identity is zero")
	}
	return id
}

// assertHelperApprovalPlanInsertFails verifies one malformed durable authority cannot enter the table.
func assertHelperApprovalPlanInsertFails(t *testing.T, databaseConnection *gorm.DB, fixture helperApprovalPlanFixture) {
	t.Helper()
	fixture = defaultHelperApprovalPlanFixture(fixture)
	if err := executeHelperApprovalPlanInsert(databaseConnection, fixture); err == nil {
		t.Fatalf("helper approval plan unexpectedly accepted: %#v", fixture)
	}
}

// executeHelperApprovalPlanInsert keeps valid and invalid probes on the exact same insert surface.
func executeHelperApprovalPlanInsert(databaseConnection *gorm.DB, fixture helperApprovalPlanFixture) error {
	return databaseConnection.Exec(`INSERT INTO helper_approval_plans
		(operation_id, operation_revision, network_state_id, mutation, lease_state,
		 project_id, kind, secondary_id, address, ownership_installation_id,
		 ownership_generation, loopback_address_lease_id)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fixture.OperationID,
		fixture.OperationRevision,
		fixture.Mutation,
		fixture.LeaseState,
		fixture.ProjectID,
		fixture.Kind,
		fixture.SecondaryID,
		fixture.Address,
		fixture.OwnershipInstallationID,
		fixture.OwnershipGeneration,
		fixture.LoopbackAddressLeaseID,
	).Error
}

// defaultHelperApprovalPlanFixture supplies only the canonical machine ownership defaults shared by every probe.
func defaultHelperApprovalPlanFixture(fixture helperApprovalPlanFixture) helperApprovalPlanFixture {
	if fixture.OwnershipInstallationID == "" {
		fixture.OwnershipInstallationID = "installation-a"
	}
	if fixture.OwnershipGeneration == 0 {
		fixture.OwnershipGeneration = 1
	}
	return fixture
}

// assertHelperApprovalForeignKeys proves no accepted fixture leaves a deferred ownership mismatch.
func assertHelperApprovalForeignKeys(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var violations []struct {
		Table  string
		RowID  int
		Parent string
		FKID   int
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("check helper approval foreign keys: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("helper approval foreign key violations = %#v", violations)
	}
}
