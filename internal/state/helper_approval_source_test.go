package state

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// helperApprovalSourceProductionFixture owns a production-schema release staged before a fresh source opens the named database.
type helperApprovalSourceProductionFixture struct {
	release     helperApprovalMutationFixture
	staged      ProjectNetworkReleaseApprovalResult
	source      *HelperApprovalPlanSource
	connection  *gorm.DB
	connections *database.Connections
}

// TestHelperApprovalPlanSourceResolvesProductionReleaseAfterRestart proves generated persistence and restart enumeration compose end to end.
func TestHelperApprovalPlanSourceResolvesProductionReleaseAfterRestart(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	outsideTransaction := make(map[string]bool)
	observed := make(map[string]int)
	callback := "harbor:test_helper_approval_source_production_transaction"
	if err := fixture.connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if !helperApprovalSourceAuthorityTable(tx.Statement.Table) {
			return
		}
		observed[tx.Statement.Table]++
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			outsideTransaction[tx.Statement.Table] = true
		}
	}); err != nil {
		t.Fatalf("register source transaction observer: %v", err)
	}
	t.Cleanup(func() { _ = fixture.connection.Callback().Query().Remove(callback) })

	requests, err := fixture.source.RequestsForOperation(context.Background(), fixture.staged.Operation.Operation.ID)
	if err != nil {
		t.Fatalf("RequestsForOperation() error = %v", err)
	}
	want := make([]ticketissuer.Request, 0, len(fixture.staged.Plans))
	for _, staged := range fixture.staged.Plans {
		want = append(want, ticketissuer.Request{
			OperationID: fixture.staged.Operation.Operation.ID,
			LeaseKey:    staged.Intent.Lease.Key,
		})
	}
	if !slices.Equal(requests, want) {
		t.Fatalf("RequestsForOperation() = %#v, want %#v", requests, want)
	}
	for index, request := range requests {
		plan, resolveErr := fixture.source.Resolve(context.Background(), request)
		if resolveErr != nil {
			t.Fatalf("Resolve(%d) error = %v", index, resolveErr)
		}
		if err := plan.Validate(); err != nil {
			t.Fatalf("Resolve(%d).Validate() error = %v", index, err)
		}
		staged := fixture.staged.Plans[index]
		if plan.OperationID != fixture.staged.Operation.Operation.ID ||
			plan.OperationRevision != fixture.staged.Operation.Revision ||
			plan.OperationState != domain.OperationRequiresApproval ||
			plan.Mutation != helper.OperationReleaseLoopbackIdentity ||
			plan.LeaseState != ticketissuer.LeaseActive ||
			plan.Lease != staged.Intent.Lease ||
			len(plan.Requirements) != 0 {
			t.Fatalf("Resolve(%d) = %#v, staged %#v", index, plan, staged)
		}
		if plan.Requirements == nil {
			t.Fatalf("Resolve(%d) returned nil requirements", index)
		}
	}
	for _, table := range []string{
		"helper_approval_plans",
		"helper_approval_plan_socket_requirements",
		"operations",
		"projects",
		"network_project_releases",
		"loopback_address_leases",
		"network_state",
	} {
		if observed[table] == 0 {
			t.Fatalf("source did not read authority table %q", table)
		}
		if outsideTransaction[table] {
			t.Fatalf("source read authority table %q outside its transaction", table)
		}
	}
}

// TestHelperApprovalPlanSourceRejectsInvalidMissingAndCancelledRequests proves recovery inputs cannot widen durable authority.
func TestHelperApprovalPlanSourceRejectsInvalidMissingAndCancelledRequests(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	operationID := fixture.staged.Operation.Operation.ID
	if _, err := fixture.source.RequestsForOperation(context.Background(), ""); err == nil {
		t.Fatal("RequestsForOperation() accepted an empty operation ID")
	}
	if _, err := fixture.source.RequestsForOperation(context.Background(), "operation-missing"); err == nil ||
		!strings.Contains(err.Error(), "were not found") {
		t.Fatalf("missing RequestsForOperation() error = %v", err)
	}
	if _, err := fixture.source.Resolve(context.Background(), ticketissuer.Request{}); err == nil {
		t.Fatal("Resolve() accepted an empty request")
	}
	missingKey, err := identity.NewSecondaryKey(fixture.staged.Operation.Operation.ProjectID, "missing-service")
	if err != nil {
		t.Fatalf("create missing request key: %v", err)
	}
	if _, err := fixture.source.Resolve(context.Background(), ticketissuer.Request{
		OperationID: operationID,
		LeaseKey:    missingKey,
	}); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing Resolve() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.source.RequestsForOperation(ctx, operationID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RequestsForOperation() error = %v", err)
	}
	if _, err := fixture.source.Resolve(ctx, ticketissuer.Request{
		OperationID: operationID,
		LeaseKey:    fixture.staged.Plans[0].Intent.Lease.Key,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Resolve() error = %v", err)
	}
}

// TestHelperApprovalPlanSourceFailsClosedOnIncompleteOrAlteredReleaseAuthority covers complete-set and owner invariants.
func TestHelperApprovalPlanSourceFailsClosedOnIncompleteOrAlteredReleaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, helperApprovalSourceProductionFixture)
		want   string
	}{
		{
			name: "subset",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "DELETE FROM helper_approval_plans WHERE id = (SELECT max(id) FROM helper_approval_plans)")
			},
			want: "differ from the exact project release set",
		},
		{
			name: "superset",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				var extra models.HelperApprovalPlan
				if err := fixture.release.database.Order("id ASC").First(&extra).Error; err != nil {
					t.Fatalf("read approval plan: %v", err)
				}
				extra.Id = 0
				extra.Kind = string(identity.LeaseKindSecondary)
				extra.SecondaryId = "unused-service"
				extra.Address = "127.77.0.15"
				extra.Mutation = string(helper.OperationEnsureLoopbackIdentity)
				extra.LeaseState = string(ticketissuer.LeasePending)
				extra.LoopbackAddressLeaseId = null.Int{}
				if err := fixture.release.database.Create(&extra).Error; err != nil {
					t.Fatalf("create extra approval plan: %v", err)
				}
			},
			want: "differ from the exact project release set",
		},
		{
			name: "pending ensure",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(
					t,
					fixture.release.database,
					"UPDATE helper_approval_plans SET mutation = 'ensure_loopback_identity', lease_state = 'pending', loopback_address_lease_id = NULL WHERE id = (SELECT min(id) FROM helper_approval_plans)",
				)
			},
			want: "differs from the exact project release effect",
		},
		{
			name: "release requirements",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				var plan models.HelperApprovalPlan
				if err := fixture.release.database.Order("id ASC").First(&plan).Error; err != nil {
					t.Fatalf("read approval plan: %v", err)
				}
				if err := fixture.release.database.Create(&models.HelperApprovalPlanSocketRequirement{
					HelperApprovalPlanId: plan.Id,
					Transport:            "tcp4",
					Port:                 443,
				}).Error; err != nil {
					t.Fatalf("create release requirement: %v", err)
				}
			},
			want: "differs from the exact project release effect",
		},
		{
			name: "missing marker",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "DELETE FROM network_project_releases")
			},
			want: "was not started",
		},
		{
			name: "wrong operation kind",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "UPDATE operations SET kind = 'project.refresh' WHERE id = 'operation-release-alpha'")
			},
			want: "requires operation kind",
		},
		{
			name: "operation not awaiting approval",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "UPDATE operations SET state = 'running' WHERE id = 'operation-release-alpha'")
			},
			want: "operation state is \"running\"",
		},
		{
			name: "missing operation",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = OFF")
				helperApprovalSourceExec(t, fixture.release.database, "DELETE FROM operations WHERE id = 'operation-release-alpha'")
				helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = ON")
			},
			want: "operation has 0 rows",
		},
		{
			name: "missing requirement storage",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "DROP TABLE helper_approval_plan_socket_requirements")
			},
			want: "read helper approval socket requirements",
		},
		{
			name: "missing project",
			mutate: func(t *testing.T, fixture helperApprovalSourceProductionFixture) {
				helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = OFF")
				helperApprovalSourceExec(t, fixture.release.database, "DELETE FROM projects WHERE project_id = 'project-alpha'")
				helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = ON")
			},
			want: "project",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalSourceProductionFixture(t)
			test.mutate(t, fixture)
			_, err := fixture.source.RequestsForOperation(context.Background(), fixture.staged.Operation.Operation.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RequestsForOperation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestHelperApprovalPlanSourceResolveRejectsCorruptOperationSet proves individual resolution validates the whole operation first.
func TestHelperApprovalPlanSourceResolveRejectsCorruptOperationSet(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	helperApprovalSourceExec(t, fixture.release.database, "DELETE FROM helper_approval_plans WHERE id = (SELECT max(id) FROM helper_approval_plans)")
	_, err := fixture.source.Resolve(context.Background(), ticketissuer.Request{
		OperationID: fixture.staged.Operation.Operation.ID,
		LeaseKey:    fixture.staged.Plans[0].Intent.Lease.Key,
	})
	if err == nil || !strings.Contains(err.Error(), "differ from the exact project release set") {
		t.Fatalf("Resolve() corrupt operation error = %v", err)
	}
}

// TestHelperApprovalPlanSourceRejectsMisdirectedActiveLeaseReference proves a valid release intent cannot borrow another lease row ID.
func TestHelperApprovalPlanSourceRejectsMisdirectedActiveLeaseReference(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	helperApprovalSourceWeakenPlanSchema(t, fixture.release.database)
	helperApprovalSourceExec(
		t,
		fixture.release.database,
		`UPDATE helper_approval_plans
		 SET loopback_address_lease_id = (SELECT loopback_address_lease_id FROM helper_approval_plans ORDER BY id DESC LIMIT 1)
		 WHERE id = (SELECT id FROM helper_approval_plans ORDER BY id ASC LIMIT 1)`,
	)
	_, err := fixture.source.RequestsForOperation(context.Background(), fixture.staged.Operation.Operation.ID)
	if err == nil || !strings.Contains(err.Error(), "active lease has 2 rows") {
		t.Fatalf("misdirected active lease error = %v", err)
	}
}

// TestHelperApprovalPlanSourceReportsPlanStorageFailure proves the bounded plan query never masks missing storage.
func TestHelperApprovalPlanSourceReportsPlanStorageFailure(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = OFF")
	helperApprovalSourceExec(t, fixture.release.database, "DROP TABLE helper_approval_plan_socket_requirements")
	helperApprovalSourceExec(t, fixture.release.database, "DROP TABLE helper_approval_plans")
	helperApprovalSourceExec(t, fixture.release.database, "PRAGMA foreign_keys = ON")
	_, err := fixture.source.RequestsForOperation(context.Background(), fixture.staged.Operation.Operation.ID)
	if err == nil || !strings.Contains(err.Error(), "read helper approval plan") {
		t.Fatalf("missing plan storage error = %v", err)
	}
}

// TestValidateHelperApprovalActiveLeaseRejectsCorruptRows proves exact lease identity remains independently fail closed.
func TestValidateHelperApprovalActiveLeaseRejectsCorruptRows(t *testing.T) {
	tests := []struct {
		name       string
		mutatePlan func(*models.HelperApprovalPlan)
		mutateRow  func(*testing.T, *gorm.DB, models.HelperApprovalPlan)
		want       string
	}{
		{
			name: "missing lease identity",
			mutatePlan: func(plan *models.HelperApprovalPlan) {
				plan.LoopbackAddressLeaseId = null.Int{}
			},
			want: "requires a positive lease ID",
		},
		{
			name: "missing lease row",
			mutateRow: func(t *testing.T, connection *gorm.DB, plan models.HelperApprovalPlan) {
				helperApprovalSourceExec(t, connection, "PRAGMA foreign_keys = OFF")
				helperApprovalSourceExec(t, connection, "DELETE FROM loopback_address_leases WHERE id = ?", plan.LoopbackAddressLeaseId.Int64)
				helperApprovalSourceExec(t, connection, "PRAGMA foreign_keys = ON")
			},
			want: "active lease has 0 rows",
		},
		{
			name: "ambiguous lease row",
			mutateRow: func(t *testing.T, connection *gorm.DB, plan models.HelperApprovalPlan) {
				helperApprovalSourceWeakenLeaseSchema(t, connection)
				var duplicate models.LoopbackAddressLease
				if err := connection.First(&duplicate, plan.LoopbackAddressLeaseId.Int64).Error; err != nil {
					t.Fatalf("read active lease: %v", err)
				}
				duplicate.Id += 1000
				if err := connection.Create(&duplicate).Error; err != nil {
					t.Fatalf("create duplicate active lease: %v", err)
				}
			},
			want: "active lease has 2 rows",
		},
		{
			name: "lease differs",
			mutatePlan: func(plan *models.HelperApprovalPlan) {
				plan.OwnershipGeneration++
			},
			want: "differs from the exact approval plan",
		},
		{
			name: "invalid lease generation",
			mutateRow: func(t *testing.T, connection *gorm.DB, _ models.HelperApprovalPlan) {
				helperApprovalSourceWeakenLeaseSchema(t, connection)
				helperApprovalSourceExec(t, connection, "UPDATE loopback_address_leases SET lease_generation = 0")
			},
			want: "active lease generation must be positive",
		},
		{
			name: "missing ensure evidence",
			mutateRow: func(t *testing.T, connection *gorm.DB, _ models.HelperApprovalPlan) {
				helperApprovalSourceWeakenLeaseSchema(t, connection)
				helperApprovalSourceExec(t, connection, "UPDATE loopback_address_leases SET ensure_evidence = ''")
			},
			want: "ensure evidence is required",
		},
		{
			name: "missing leased time",
			mutateRow: func(t *testing.T, connection *gorm.DB, _ models.HelperApprovalPlan) {
				helperApprovalSourceWeakenLeaseSchema(t, connection)
				helperApprovalSourceExec(t, connection, "UPDATE loopback_address_leases SET leased_at = NULL")
			},
			want: "active lease time must not be zero",
		},
		{
			name: "release fields",
			mutateRow: func(t *testing.T, connection *gorm.DB, _ models.HelperApprovalPlan) {
				helperApprovalSourceWeakenLeaseSchema(t, connection)
				helperApprovalSourceExec(t, connection, "UPDATE loopback_address_leases SET release_generation = 999")
			},
			want: "contains release or quarantine fields",
		},
		{
			name: "lease read failure",
			mutateRow: func(t *testing.T, connection *gorm.DB, _ models.HelperApprovalPlan) {
				helperApprovalSourceExec(t, connection, "PRAGMA foreign_keys = OFF")
				helperApprovalSourceExec(t, connection, "DROP TABLE loopback_address_leases")
				helperApprovalSourceExec(t, connection, "PRAGMA foreign_keys = ON")
			},
			want: "read helper approval active lease",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalSourceProductionFixture(t)
			var plan models.HelperApprovalPlan
			if err := fixture.release.database.Order("id ASC").First(&plan).Error; err != nil {
				t.Fatalf("read approval plan: %v", err)
			}
			if test.mutatePlan != nil {
				test.mutatePlan(&plan)
			}
			if test.mutateRow != nil {
				test.mutateRow(t, fixture.release.database, plan)
			}
			err := validateHelperApprovalActiveLease(fixture.connection, plan)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateHelperApprovalActiveLease() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestHelperApprovalPlanSourceRejectsWeakenedSchemaCollisions proves migration constraints are defense in depth, not source assumptions.
func TestHelperApprovalPlanSourceRejectsWeakenedSchemaCollisions(t *testing.T) {
	fixture := newHelperApprovalSourceProductionFixture(t)
	helperApprovalSourceWeakenPlanSchema(t, fixture.release.database)
	var collision models.HelperApprovalPlan
	if err := fixture.release.database.Order("id ASC").First(&collision).Error; err != nil {
		t.Fatalf("read approval plan: %v", err)
	}
	collision.Id += 1000
	collision.OperationId = "operation-untrusted"
	if err := fixture.release.database.Create(&collision).Error; err != nil {
		t.Fatalf("create weakened-schema collision: %v", err)
	}
	_, err := fixture.source.RequestsForOperation(context.Background(), fixture.staged.Operation.Operation.ID)
	if err == nil || !strings.Contains(err.Error(), "same lease key or address") {
		t.Fatalf("weakened-schema collision error = %v", err)
	}
}

// TestHelperApprovalPlanSourceRequiresNamedDatabase proves connection failures cannot fall back to another database.
func TestHelperApprovalPlanSourceRequiresNamedDatabase(t *testing.T) {
	t.Setenv("DB_HARBORD_DRIVER", "not-built")
	t.Setenv("DB_HARBORD_DSN", "unused")
	connections := database.NewConnections(inspects.NewManager())
	key, err := identity.NewPrimaryKey("project-alpha")
	if err != nil {
		t.Fatal(err)
	}
	source := NewHelperApprovalPlanSource(models.NewHelperApprovalPlanRepo(connections))
	_, err = source.Resolve(context.Background(), ticketissuer.Request{
		OperationID: "operation-approval",
		LeaseKey:    key,
	})
	if err == nil || !strings.Contains(err.Error(), "open helper approval plans") {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := source.RequestsForOperation(context.Background(), "operation-approval"); err == nil ||
		!strings.Contains(err.Error(), "open helper approval plans") {
		t.Fatalf("RequestsForOperation() error = %v", err)
	}
}

// TestNewHelperApprovalPlanSourceRequiresRepository proves construction fails before retaining missing persistence authority.
func TestNewHelperApprovalPlanSourceRequiresRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHelperApprovalPlanSource(nil) did not panic")
		}
	}()
	_ = NewHelperApprovalPlanSource(nil)
}

// newHelperApprovalSourceProductionFixture opens a new named connection after staging through production migrations and Store APIs.
func newHelperApprovalSourceProductionFixture(t *testing.T) helperApprovalSourceProductionFixture {
	t.Helper()
	release := newHelperApprovalMutationFixture(t)
	staged := mustStageProjectNetworkReleaseApproval(t, release)
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close restarted approval source database: %v", err)
		}
	})
	source := NewHelperApprovalPlanSource(models.NewHelperApprovalPlanRepo(connections))
	connection, err := source.plans.Builder()
	if err != nil {
		t.Fatalf("open restarted approval source database: %v", err)
	}
	return helperApprovalSourceProductionFixture{
		release:     release,
		staged:      staged,
		source:      source,
		connection:  connection,
		connections: connections,
	}
}

// helperApprovalSourceAuthorityTable reports whether one observed query reads durable helper authority.
func helperApprovalSourceAuthorityTable(table string) bool {
	return slices.Contains([]string{
		"helper_approval_plans",
		"helper_approval_plan_socket_requirements",
		"operations",
		"operation_transitions",
		"projects",
		"project_apps",
		"project_services",
		"project_resources",
		"harbor_state",
		"network_state",
		"network_pool_candidates",
		"network_setup_evidence",
		"network_shared_listeners",
		"loopback_address_leases",
		"public_endpoint_leases",
		"network_project_releases",
	}, table)
}

// helperApprovalSourceWeakenPlanSchema removes migration guards so collision handling is exercised as application authority.
func helperApprovalSourceWeakenPlanSchema(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"PRAGMA foreign_keys = OFF",
		"DROP TABLE helper_approval_plan_socket_requirements",
		"ALTER TABLE helper_approval_plans RENAME TO helper_approval_plans_guarded",
		"CREATE TABLE helper_approval_plans AS SELECT * FROM helper_approval_plans_guarded",
		"DROP TABLE helper_approval_plans_guarded",
		`CREATE TABLE helper_approval_plan_socket_requirements (
			id INTEGER,
			helper_approval_plan_id INTEGER,
			transport TEXT,
			port INTEGER
		)`,
		"PRAGMA foreign_keys = ON",
	} {
		helperApprovalSourceExec(t, connection, statement)
	}
}

// helperApprovalSourceWeakenLeaseSchema removes lease checks so read-time corruption guards can be tested directly.
func helperApprovalSourceWeakenLeaseSchema(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"PRAGMA foreign_keys = OFF",
		"ALTER TABLE loopback_address_leases RENAME TO loopback_address_leases_guarded",
		`CREATE TABLE loopback_address_leases (
			id INTEGER,
			network_state_id INTEGER,
			project_id TEXT,
			source_project_id TEXT,
			kind TEXT,
			secondary_id TEXT,
			address TEXT,
			state TEXT,
			lease_generation INTEGER,
			ownership_installation_id TEXT,
			ownership_generation INTEGER,
			ensure_evidence TEXT,
			leased_at DATETIME,
			release_generation INTEGER,
			release_evidence TEXT,
			released_at DATETIME,
			quarantined_at DATETIME,
			reuse_after DATETIME,
			quarantine_reason TEXT
		)`,
		"INSERT INTO loopback_address_leases SELECT * FROM loopback_address_leases_guarded",
		"DROP TABLE loopback_address_leases_guarded",
		"PRAGMA foreign_keys = ON",
	} {
		helperApprovalSourceExec(t, connection, statement)
	}
}

// helperApprovalSourceExec applies one focused fixture mutation or fails immediately.
func helperApprovalSourceExec(t *testing.T, connection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := connection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute approval source fixture statement: %v", err)
	}
}
