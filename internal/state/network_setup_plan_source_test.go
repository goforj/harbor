package state

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const networkSetupPlanSourceMigrationName = "2026_07_19_130000_create_network_setup_plans"

const networkSetupPlanSourceVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// networkSetupPlanSourceFixture retains one restarted source and its production-schema database.
type networkSetupPlanSourceFixture struct {
	source      *NetworkSetupPlanSource
	database    *gorm.DB
	connections *database.Connections
}

// TestNetworkSetupPlanSourceResolvesExactPoolAfterRestart proves every authority read shares one read-only database instant.
func TestNetworkSetupPlanSourceResolvesExactPoolAfterRestart(t *testing.T) {
	fixture := newNetworkSetupPlanSourceFixture(t)
	tables := []string{"operations", "network_setup_plans", "network_state"}
	readOrder := []string{}
	outsideTransaction := map[string]bool{}
	callback := "harbor:test_network_setup_plan_source_transaction"
	if err := fixture.database.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if !slices.Contains(tables, tx.Statement.Table) {
			return
		}
		readOrder = append(readOrder, tx.Statement.Table)
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			outsideTransaction[tx.Statement.Table] = true
		}
	}); err != nil {
		t.Fatalf("register setup source transaction observer: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Query().Remove(callback) })

	plan, err := fixture.source.Resolve(context.Background(), ticketissuer.PoolRequest{OperationID: "operation-setup"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Resolve().Validate() error = %v", err)
	}
	wantOwnership := networkSetupPlanSourceOwnership()
	if plan.OperationID != "operation-setup" || plan.OperationRevision != 1 ||
		plan.OperationState != domain.OperationRequiresApproval || plan.Mode != ticketissuer.PoolModeBootstrap ||
		plan.Ownership != wantOwnership {
		t.Fatalf("Resolve() = %#v, want exact durable operation, mode, and ownership", plan)
	}
	wantAddresses := make([]netip.Addr, 8)
	address := netip.MustParseAddr("127.77.0.8")
	for index := range wantAddresses {
		wantAddresses[index] = address
		address = address.Next()
	}
	if plan.Pool.Prefix() != netip.MustParsePrefix("127.77.0.8/29") ||
		!slices.Equal(plan.Pool.Candidates(), wantAddresses) {
		t.Fatalf("Resolve() pool = %s %#v, want complete canonical /29", plan.Pool.Prefix(), plan.Pool.Candidates())
	}
	if !slices.Equal(readOrder, tables) {
		t.Fatalf("Resolve() authority read order = %#v, want %#v", readOrder, tables)
	}
	for _, table := range tables {
		if outsideTransaction[table] {
			t.Fatalf("Resolve() read %s outside its read-only transaction", table)
		}
	}
}

// TestNetworkSetupPlanSourceRejectsEveryDurableMismatch verifies weakened schema cannot widen pool authority.
func TestNetworkSetupPlanSourceRejectsEveryDurableMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *networkSetupPlanSourceFixture)
		want   string
	}{
		{name: "missing operation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "DELETE FROM operations")
		}, want: "operation has 0 rows"},
		{name: "wrong operation kind", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE operations SET kind = 'host.setup'")
		}, want: "operation kind"},
		{name: "project scoped operation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE operations SET project_id = 'project-alpha'")
		}, want: "must not identify a project"},
		{name: "running operation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE operations SET state = 'running', phase = 'running'")
		}, want: `operation state is "running"`},
		{name: "cancelled operation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, `UPDATE operations
				SET state = 'cancelled', phase = 'cancelled', finished_at = ?`, networkSetupPlanSourceTime().Add(2*time.Second))
		}, want: `operation state is "cancelled"`},
		{name: "operation revision mismatch", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE operations SET revision = 2")
		}, want: "plan revision is 1, operation revision is 2"},
		{name: "operation revision overflow", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE operations SET revision = 9007199254740992")
		}, want: "operation revision is outside"},
		{name: "missing plan", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "DELETE FROM network_setup_plans")
		}, want: "singleton plan has 0 rows"},
		{name: "duplicate plan", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, `INSERT INTO network_setup_plans
				SELECT 2, operation_id, operation_revision, ownership_schema_version, installation_id,
				owner_identity, ownership_generation, loopback_pool_prefix, ticket_verifier_key
				FROM network_setup_plans`)
		}, want: "singleton plan has 2 rows"},
		{name: "wrong singleton ID", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET id = 2")
		}, want: "singleton ID is 2"},
		{name: "wrong plan operation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET operation_id = 'operation-other'")
		}, want: "operation ID does not match"},
		{name: "wrong plan revision", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET operation_revision = 2")
		}, want: "plan revision is 2"},
		{name: "plan revision overflow", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET operation_revision = 9007199254740992")
		}, want: "operation revision is outside"},
		{name: "ownership schema", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET ownership_schema_version = 2")
		}, want: "ownership schema version is 2"},
		{name: "installation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET installation_id = '-unsafe'")
		}, want: "installation ID"},
		{name: "owner", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET owner_identity = '0501'")
		}, want: "canonical unsigned UID"},
		{name: "ownership generation", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET ownership_generation = 2")
		}, want: "ownership generation is 2"},
		{name: "pool width", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET loopback_pool_prefix = '127.77.0.0/24'")
		}, want: "canonical IPv4-loopback /29"},
		{name: "pool alignment", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET loopback_pool_prefix = '127.77.0.9/29'")
		}, want: "not canonical"},
		{name: "verifier key", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			networkSetupPlanSourceExec(t, fixture.database, "UPDATE network_setup_plans SET ticket_verifier_key = 'bad'")
		}, want: "ticket verifier key"},
		{name: "network initialized", mutate: func(t *testing.T, fixture *networkSetupPlanSourceFixture) {
			at := networkSetupPlanSourceTime().Add(3 * time.Second)
			networkSetupPlanSourceExec(t, fixture.database, `INSERT INTO network_state
				(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length,
				dns_suffix, created_at, updated_at, revision)
				VALUES (1, 'identity', 'installation-a', 1, '127.77.0.8', 29, '.test', ?, ?, 2)`, at, at)
		}, want: "network state already exists"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkSetupPlanSourceFixture(t)
			networkSetupPlanSourceWeakenSchema(t, fixture.database)
			test.mutate(t, &fixture)

			_, err := fixture.source.Resolve(context.Background(), ticketissuer.PoolRequest{OperationID: "operation-setup"})
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want CorruptStateError containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkSetupPlanSourceRejectsInvalidAndCancelledRequests verifies request handling stops before durable reads.
func TestNetworkSetupPlanSourceRejectsInvalidAndCancelledRequests(t *testing.T) {
	fixture := newNetworkSetupPlanSourceFixture(t)
	if _, err := fixture.source.Resolve(context.Background(), ticketissuer.PoolRequest{}); err == nil {
		t.Fatal("Resolve() accepted an empty operation ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.source.Resolve(ctx, ticketissuer.PoolRequest{OperationID: "operation-setup"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Resolve() error = %v, want context.Canceled", err)
	}
}

// TestNewNetworkSetupPlanSourceRequiresRepository proves construction fails before retaining missing persistence authority.
func TestNewNetworkSetupPlanSourceRequiresRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewNetworkSetupPlanSource(nil) did not panic")
		}
	}()
	_ = NewNetworkSetupPlanSource(nil)
}

// newNetworkSetupPlanSourceFixture seeds production migrations, closes the writer, and reopens through the generated named repository.
func newNetworkSetupPlanSourceFixture(t *testing.T) networkSetupPlanSourceFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")
	if _, err := ConfigureDatabase(); err != nil {
		t.Fatalf("configure network setup source database: %v", err)
	}

	seedConnections := database.NewConnections(inspects.NewManager())
	seedDatabase, err := seedConnections.GetHarbord()
	if err != nil {
		t.Fatalf("open network setup source seed database: %v", err)
	}
	applyNetworkSetupPlanSourceMigrations(t, seedDatabase)
	seedNetworkSetupPlanSource(t, seedDatabase)
	if err := seedConnections.Close(context.Background()); err != nil {
		t.Fatalf("close network setup source seed database: %v", err)
	}

	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close restarted network setup source database: %v", err)
		}
	})
	source := NewNetworkSetupPlanSource(models.NewNetworkSetupPlanRepo(connections))
	databaseConnection, err := source.plans.Builder()
	if err != nil {
		t.Fatalf("open restarted network setup source database: %v", err)
	}
	return networkSetupPlanSourceFixture{source: source, database: databaseConnection, connections: connections}
}

// applyNetworkSetupPlanSourceMigrations applies the production SQLite stream through the setup-plan migration.
func applyNetworkSetupPlanSourceMigrations(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	selected := make([]migrations.Migration, 0)
	for _, migration := range migrations.GetMigrations() {
		if migration.App() == "harbord" && migration.Connection() == "default" &&
			(migration.Driver() == "" || migration.Driver() == "sqlite") {
			selected = append(selected, migration)
		}
	}
	sort.Slice(selected, func(left int, right int) bool { return selected[left].Name() < selected[right].Name() })
	found := false
	for _, migration := range selected {
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply network setup source migration %s: %v", migration.Name(), err)
		}
		if migration.Name() == networkSetupPlanSourceMigrationName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("network setup source migration %q is not registered", networkSetupPlanSourceMigrationName)
	}
}

// seedNetworkSetupPlanSource writes one valid approval owner and complete ownership record.
func seedNetworkSetupPlanSource(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	at := networkSetupPlanSourceTime()
	startedAt := at.Add(time.Second)
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES ('operation-setup', 'intent-setup', 'network.setup', NULL, 'requires_approval',
		'waiting for network setup approval', ?, ?, 1)`, at, startedAt).Error; err != nil {
		t.Fatalf("seed network setup operation: %v", err)
	}
	owner := networkSetupPlanSourceOwnership()
	if err := databaseConnection.Create(&models.NetworkSetupPlan{
		Id:                     1,
		OperationId:            "operation-setup",
		OperationRevision:      1,
		OwnershipSchemaVersion: int(owner.SchemaVersion),
		InstallationId:         owner.InstallationID,
		OwnerIdentity:          owner.OwnerIdentity,
		OwnershipGeneration:    int(owner.Generation),
		LoopbackPoolPrefix:     owner.LoopbackPoolPrefix,
		TicketVerifierKey:      owner.TicketVerifierKey,
	}).Error; err != nil {
		t.Fatalf("seed network setup plan: %v", err)
	}
}

// networkSetupPlanSourceOwnership returns the exact generation-one protected record expected after restart.
func networkSetupPlanSourceOwnership() ownership.Record {
	return ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     "installation-a",
		OwnerIdentity:      "501",
		Generation:         1,
		LoopbackPoolPrefix: "127.77.0.8/29",
		TicketVerifierKey:  networkSetupPlanSourceVerifierKey,
	}
}

// networkSetupPlanSourceWeakenSchema removes plan checks so every read-time corruption branch remains directly testable.
func networkSetupPlanSourceWeakenSchema(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, statement := range []string{
		"PRAGMA foreign_keys = OFF",
		"ALTER TABLE network_setup_plans RENAME TO network_setup_plans_guarded",
		"CREATE TABLE network_setup_plans AS SELECT * FROM network_setup_plans_guarded",
		"DROP TABLE network_setup_plans_guarded",
		"PRAGMA foreign_keys = ON",
	} {
		networkSetupPlanSourceExec(t, databaseConnection, statement)
	}
}

// networkSetupPlanSourceExec applies one focused fixture mutation or fails immediately.
func networkSetupPlanSourceExec(t *testing.T, databaseConnection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute network setup source fixture statement: %v", err)
	}
}

// networkSetupPlanSourceTime returns the stable UTC time shared by durable fixtures.
func networkSetupPlanSourceTime() time.Time {
	return time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
}
