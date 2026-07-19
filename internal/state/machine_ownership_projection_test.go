package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const machineOwnershipProjectionTestMigrationName = "2026_07_19_150000_add_machine_ownership_network_policy_fingerprint"

const machineOwnershipProjectionTestVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// machineOwnershipProjectionFixture retains one restarted source and its production-schema named database.
type machineOwnershipProjectionFixture struct {
	source      *MachineOwnershipProjectionSource
	repository  *models.MachineOwnershipProjectionRepo
	database    *gorm.DB
	connections *database.Connections
	observation ownership.Observation
	confirmedAt time.Time
}

// TestMachineOwnershipProjectionRoundTripsThroughNamedRestart proves insertion and generated-repository reads survive a new harbord connection registry.
func TestMachineOwnershipProjectionRoundTripsThroughNamedRestart(t *testing.T) {
	fixture := newMachineOwnershipProjectionFixture(t, true)

	row, err := fixture.repository.ByID(machineOwnershipProjectionSingletonID)
	if err != nil {
		t.Fatalf("MachineOwnershipProjectionRepo.ByID() error = %v", err)
	}
	wantRow := machineOwnershipProjectionTestModel(fixture.observation, fixture.confirmedAt)
	if *row != wantRow {
		t.Fatalf("MachineOwnershipProjectionRepo.ByID() = %#v, want %#v", *row, wantRow)
	}

	observed, err := fixture.source.Observe(nil)
	if err != nil {
		t.Fatalf("Observe(nil) error = %v", err)
	}
	if observed != fixture.observation {
		t.Fatalf("Observe(nil) = %#v, want %#v", observed, fixture.observation)
	}

	var read ownership.Observation
	var confirmedAt time.Time
	err = fixture.database.Transaction(func(tx *gorm.DB) error {
		var readErr error
		read, confirmedAt, readErr = readMachineOwnershipProjectionInTransaction(tx)
		return readErr
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("readMachineOwnershipProjectionInTransaction() error = %v", err)
	}
	if read != fixture.observation || confirmedAt != fixture.confirmedAt {
		t.Fatalf("projection read = %#v at %s, want %#v at %s", read, confirmedAt, fixture.observation, fixture.confirmedAt)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if observed, err := fixture.source.Observe(ctx); !errors.Is(err, context.Canceled) || observed != (ownership.Observation{}) {
		t.Fatalf("Observe(cancelled) = %#v, %v, want context.Canceled", observed, err)
	}
}

// TestMachineOwnershipProjectionRoundTripsBothSchemas proves SQL NULL and policy-bound values survive insertion and observation.
func TestMachineOwnershipProjectionRoundTripsBothSchemas(t *testing.T) {
	for _, schemaVersion := range []uint32{
		ownership.IdentitySchemaVersion,
		ownership.NetworkPolicySchemaVersion,
	} {
		t.Run(fmt.Sprintf("schema-%d", schemaVersion), func(t *testing.T) {
			fixture := newMachineOwnershipProjectionFixture(t, false)
			observation := machineOwnershipProjectionTestObservationForSchema(t, schemaVersion)
			if schemaVersion == ownership.NetworkPolicySchemaVersion {
				machineOwnershipProjectionTestExec(
					t,
					fixture.database,
					"UPDATE network_state SET stage = ? WHERE id = ?",
					NetworkStageFull,
					networkStateSingletonID,
				)
			}
			if err := fixture.database.Transaction(func(tx *gorm.DB) error {
				return insertMachineOwnershipProjectionInTransaction(tx, observation, fixture.confirmedAt)
			}); err != nil {
				t.Fatalf("insert schema-%d projection: %v", schemaVersion, err)
			}

			row, err := fixture.repository.ByID(machineOwnershipProjectionSingletonID)
			if err != nil {
				t.Fatalf("read schema-%d projection row: %v", schemaVersion, err)
			}
			if !reflect.DeepEqual(*row, machineOwnershipProjectionTestModel(observation, fixture.confirmedAt)) {
				t.Fatalf("schema-%d projection row = %#v", schemaVersion, *row)
			}
			observed, err := fixture.source.Observe(context.Background())
			if err != nil || observed != observation {
				t.Fatalf("Observe() schema-%d = %#v, %v, want %#v", schemaVersion, observed, err, observation)
			}
		})
	}
}

// TestNewMachineOwnershipProjectionSourceRequiresRepository proves construction fails before retaining missing persistence authority.
func TestNewMachineOwnershipProjectionSourceRequiresRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewMachineOwnershipProjectionSource(nil) did not panic")
		}
	}()
	_ = NewMachineOwnershipProjectionSource(nil)
}

// TestMachineOwnershipProjectionSourceFailsClosedOnDurableMismatch covers singleton absence, collisions, and network-root drift.
func TestMachineOwnershipProjectionSourceFailsClosedOnDurableMismatch(t *testing.T) {
	tests := []struct {
		name           string
		seedProjection bool
		mutate         func(*testing.T, *machineOwnershipProjectionFixture)
		want           string
	}{
		{name: "absent projection", want: "found 0 rows, expected 1"},
		{name: "duplicate projection", seedProjection: true, mutate: func(t *testing.T, fixture *machineOwnershipProjectionFixture) {
			weakenMachineOwnershipProjectionSchema(t, fixture.database)
			duplicate := machineOwnershipProjectionTestModel(fixture.observation, fixture.confirmedAt)
			duplicate.Id = 2
			if err := fixture.database.Create(&duplicate).Error; err != nil {
				t.Fatalf("create duplicate projection: %v", err)
			}
		}, want: "found 2 rows, expected 1"},
		{name: "absent network root", seedProjection: true, mutate: func(t *testing.T, fixture *machineOwnershipProjectionFixture) {
			machineOwnershipProjectionTestExec(t, fixture.database, "PRAGMA foreign_keys = OFF")
			machineOwnershipProjectionTestExec(t, fixture.database, "DELETE FROM network_state")
			machineOwnershipProjectionTestExec(t, fixture.database, "PRAGMA foreign_keys = ON")
		}, want: "network root has 0 rows, expected 1"},
		{name: "mismatched network root", seedProjection: true, mutate: func(t *testing.T, fixture *machineOwnershipProjectionFixture) {
			machineOwnershipProjectionTestExec(t, fixture.database, "UPDATE network_state SET installation_id = 'installation-b'")
		}, want: "projected ownership differs from the durable network root"},
		{name: "schema one with full root", seedProjection: true, mutate: func(t *testing.T, fixture *machineOwnershipProjectionFixture) {
			machineOwnershipProjectionTestExec(t, fixture.database, "UPDATE network_state SET stage = 'full'")
		}, want: "full-stage network retains schema-1 ownership"},
		{name: "schema two with identity root", seedProjection: true, mutate: func(t *testing.T, fixture *machineOwnershipProjectionFixture) {
			target := machineOwnershipProjectionTestObservationForSchema(t, ownership.NetworkPolicySchemaVersion)
			if err := fixture.database.Model(&models.MachineOwnershipProjection{}).
				Where("id = ?", machineOwnershipProjectionSingletonID).
				Updates(map[string]any{
					"ownership_schema_version":   int(ownership.NetworkPolicySchemaVersion),
					"network_policy_fingerprint": target.Record.NetworkPolicyFingerprint,
					"record_fingerprint":         target.Fingerprint,
				}).Error; err != nil {
				t.Fatalf("seed schema-two identity projection: %v", err)
			}
		}, want: "identity-stage network retains schema-2 ownership"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMachineOwnershipProjectionFixture(t, test.seedProjection)
			if test.mutate != nil {
				test.mutate(t, &fixture)
			}
			observed, err := fixture.source.Observe(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Observe() = %#v, %v, want %q", observed, err, test.want)
			}
			if observed != (ownership.Observation{}) {
				t.Fatalf("failed Observe() returned authority %#v", observed)
			}
		})
	}
}

// TestMachineOwnershipProjectionFromModelValidatesEveryAuthorityDimension covers direct conversion before schema constraints can hide corrupt rows.
func TestMachineOwnershipProjectionFromModelValidatesEveryAuthorityDimension(t *testing.T) {
	observation := machineOwnershipProjectionTestObservation(t)
	confirmedAt := machineOwnershipProjectionTestTime()
	valid := machineOwnershipProjectionTestModel(observation, confirmedAt)
	converted, convertedAt, err := machineOwnershipProjectionFromModel(valid)
	if err != nil {
		t.Fatalf("machineOwnershipProjectionFromModel() error = %v", err)
	}
	if converted != observation || convertedAt != confirmedAt {
		t.Fatalf("machineOwnershipProjectionFromModel() = %#v at %s, want %#v at %s", converted, convertedAt, observation, confirmedAt)
	}
	schemaTwo := machineOwnershipProjectionTestObservationForSchema(t, ownership.NetworkPolicySchemaVersion)
	converted, convertedAt, err = machineOwnershipProjectionFromModel(
		machineOwnershipProjectionTestModel(schemaTwo, confirmedAt),
	)
	if err != nil || converted != schemaTwo || convertedAt != confirmedAt {
		t.Fatalf("schema-two conversion = %#v at %s, %v, want %#v", converted, convertedAt, err, schemaTwo)
	}

	tests := []struct {
		name   string
		mutate func(*models.MachineOwnershipProjection)
	}{
		{name: "projection singleton", mutate: func(row *models.MachineOwnershipProjection) { row.Id = 2 }},
		{name: "network singleton", mutate: func(row *models.MachineOwnershipProjection) { row.NetworkStateId = 2 }},
		{name: "ownership schema", mutate: func(row *models.MachineOwnershipProjection) {
			row.OwnershipSchemaVersion = int(ownership.NetworkPolicySchemaVersion + 1)
		}},
		{name: "ownership generation", mutate: func(row *models.MachineOwnershipProjection) { row.OwnershipGeneration = 0 }},
		{name: "ownership record", mutate: func(row *models.MachineOwnershipProjection) { row.InstallationId = "-unsafe" }},
		{name: "record fingerprint", mutate: func(row *models.MachineOwnershipProjection) { row.RecordFingerprint = strings.Repeat("0", 64) }},
		{name: "zero confirmation time", mutate: func(row *models.MachineOwnershipProjection) { row.ConfirmedAt = time.Time{} }},
		{name: "non-UTC confirmation time", mutate: func(row *models.MachineOwnershipProjection) {
			row.ConfirmedAt = time.Date(2026, time.July, 19, 16, 0, 0, 0, time.FixedZone("offset", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			converted, convertedAt, err := machineOwnershipProjectionFromModel(row)
			if err == nil || converted != (ownership.Observation{}) || !convertedAt.IsZero() {
				t.Fatalf("machineOwnershipProjectionFromModel() = %#v at %s, %v", converted, convertedAt, err)
			}
		})
	}
}

// TestMachineOwnershipProjectionFromModelEnforcesPolicyNullability rejects ambiguous schema encodings before fingerprint comparison.
func TestMachineOwnershipProjectionFromModelEnforcesPolicyNullability(t *testing.T) {
	confirmedAt := machineOwnershipProjectionTestTime()
	empty := ""
	schemaOne := machineOwnershipProjectionTestModel(
		machineOwnershipProjectionTestObservationForSchema(t, ownership.IdentitySchemaVersion),
		confirmedAt,
	)
	schemaOne.NetworkPolicyFingerprint = &empty
	if _, _, err := machineOwnershipProjectionFromModel(schemaOne); err == nil || !strings.Contains(err.Error(), "is not NULL") {
		t.Fatalf("schema-one non-NULL policy error = %v", err)
	}

	schemaTwo := machineOwnershipProjectionTestModel(
		machineOwnershipProjectionTestObservationForSchema(t, ownership.NetworkPolicySchemaVersion),
		confirmedAt,
	)
	schemaTwo.NetworkPolicyFingerprint = nil
	if _, _, err := machineOwnershipProjectionFromModel(schemaTwo); err == nil || !strings.Contains(err.Error(), "is NULL") {
		t.Fatalf("schema-two NULL policy error = %v", err)
	}
}

// TestInsertMachineOwnershipProjectionValidatesAndRollsBack proves invalid confirmation never leaves partial or duplicate authority.
func TestInsertMachineOwnershipProjectionValidatesAndRollsBack(t *testing.T) {
	fixture := newMachineOwnershipProjectionFixture(t, false)
	valid := fixture.observation
	confirmedAt := fixture.confirmedAt
	tests := []struct {
		name        string
		observation ownership.Observation
		confirmedAt time.Time
	}{
		{name: "absent authority", observation: ownership.Observation{}, confirmedAt: confirmedAt},
		{name: "mismatched fingerprint", observation: ownership.Observation{Exists: true, Record: valid.Record, Fingerprint: strings.Repeat("0", 64)}, confirmedAt: confirmedAt},
		{name: "zero confirmation time", observation: valid, confirmedAt: time.Time{}},
		{name: "non-UTC confirmation time", observation: valid, confirmedAt: time.Date(2026, time.July, 19, 16, 0, 0, 0, time.FixedZone("offset", 3600))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := fixture.database.Transaction(func(tx *gorm.DB) error {
				return insertMachineOwnershipProjectionInTransaction(tx, test.observation, test.confirmedAt)
			})
			if err == nil {
				t.Fatal("insertMachineOwnershipProjectionInTransaction() error = nil")
			}
			assertMachineOwnershipProjectionRowCount(t, fixture.database, 0)
		})
	}

	err := fixture.database.Transaction(func(tx *gorm.DB) error {
		if err := insertMachineOwnershipProjectionInTransaction(tx, valid, confirmedAt); err != nil {
			return err
		}
		return insertMachineOwnershipProjectionInTransaction(tx, valid, confirmedAt)
	})
	if err == nil {
		t.Fatal("duplicate insert transaction error = nil")
	}
	assertMachineOwnershipProjectionRowCount(t, fixture.database, 0)
}

// newMachineOwnershipProjectionFixture applies production migrations, optionally inserts confirmation, and reopens the named database.
func newMachineOwnershipProjectionFixture(t *testing.T, seedProjection bool) machineOwnershipProjectionFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")
	if _, err := ConfigureDatabase(); err != nil {
		t.Fatalf("configure machine ownership projection database: %v", err)
	}

	observation := machineOwnershipProjectionTestObservation(t)
	confirmedAt := machineOwnershipProjectionTestTime()
	seedConnections := database.NewConnections(inspects.NewManager())
	seedDatabase, err := seedConnections.GetHarbord()
	if err != nil {
		t.Fatalf("open machine ownership projection seed database: %v", err)
	}
	applyMachineOwnershipProjectionTestMigrations(t, seedDatabase)
	root := machineOwnershipProjectionTestNetworkRoot(observation.Record, confirmedAt)
	if err := seedDatabase.Create(&root).Error; err != nil {
		t.Fatalf("seed machine ownership projection network root: %v", err)
	}
	if seedProjection {
		if err := seedDatabase.Transaction(func(tx *gorm.DB) error {
			return insertMachineOwnershipProjectionInTransaction(tx, observation, confirmedAt)
		}); err != nil {
			t.Fatalf("seed machine ownership projection: %v", err)
		}
	}
	if err := seedConnections.Close(context.Background()); err != nil {
		t.Fatalf("close machine ownership projection seed database: %v", err)
	}

	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close restarted machine ownership projection database: %v", err)
		}
	})
	repository := models.NewMachineOwnershipProjectionRepo(connections)
	databaseConnection, err := repository.Builder()
	if err != nil {
		t.Fatalf("open restarted machine ownership projection database: %v", err)
	}
	return machineOwnershipProjectionFixture{
		source:      NewMachineOwnershipProjectionSource(repository),
		repository:  repository,
		database:    databaseConnection,
		connections: connections,
		observation: observation,
		confirmedAt: confirmedAt,
	}
}

// applyMachineOwnershipProjectionTestMigrations applies the production SQLite stream through the projection migration.
func applyMachineOwnershipProjectionTestMigrations(t *testing.T, databaseConnection *gorm.DB) {
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
			t.Fatalf("apply machine ownership projection migration %s: %v", migration.Name(), err)
		}
		if migration.Name() == machineOwnershipProjectionTestMigrationName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("machine ownership projection migration %q is not registered", machineOwnershipProjectionTestMigrationName)
	}
}

// machineOwnershipProjectionTestObservation returns one exact helper-confirmed ownership record and fingerprint.
func machineOwnershipProjectionTestObservation(t *testing.T) ownership.Observation {
	t.Helper()
	return machineOwnershipProjectionTestObservationForSchema(t, ownership.IdentitySchemaVersion)
}

// machineOwnershipProjectionTestObservationForSchema returns exact helper confirmation for either supported schema.
func machineOwnershipProjectionTestObservationForSchema(
	t *testing.T,
	schemaVersion uint32,
) ownership.Observation {
	t.Helper()
	record := ownership.Record{
		SchemaVersion:      schemaVersion,
		InstallationID:     "installation-a",
		OwnerIdentity:      "501",
		Generation:         1,
		LoopbackPoolPrefix: "127.77.0.8/29",
		TicketVerifierKey:  machineOwnershipProjectionTestVerifierKey,
	}
	if schemaVersion == ownership.NetworkPolicySchemaVersion {
		record.NetworkPolicyFingerprint = strings.Repeat("a", 64)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint machine ownership projection record: %v", err)
	}
	return ownership.Observation{Exists: true, Record: record, Fingerprint: fingerprint}
}

// machineOwnershipProjectionTestNetworkRoot returns the durable singleton that exactly owns one projected record.
func machineOwnershipProjectionTestNetworkRoot(record ownership.Record, at time.Time) models.NetworkState {
	return models.NetworkState{
		Id:                  networkStateSingletonID,
		Stage:               string(NetworkStageIdentity),
		InstallationId:      record.InstallationID,
		OwnershipGeneration: int(record.Generation),
		PoolNetwork:         "127.77.0.8",
		PoolPrefixLength:    29,
		DnsSuffix:           ".test",
		CreatedAt:           at.Add(-time.Minute),
		UpdatedAt:           at,
		Revision:            1,
	}
}

// machineOwnershipProjectionTestModel returns the exact generated row implied by one valid confirmation.
func machineOwnershipProjectionTestModel(observation ownership.Observation, confirmedAt time.Time) models.MachineOwnershipProjection {
	return models.MachineOwnershipProjection{
		Id:                       machineOwnershipProjectionSingletonID,
		NetworkStateId:           networkStateSingletonID,
		OwnershipSchemaVersion:   int(observation.Record.SchemaVersion),
		InstallationId:           observation.Record.InstallationID,
		OwnerIdentity:            observation.Record.OwnerIdentity,
		OwnershipGeneration:      int(observation.Record.Generation),
		LoopbackPoolPrefix:       observation.Record.LoopbackPoolPrefix,
		NetworkPolicyFingerprint: machineOwnershipNetworkPolicyModelValue(observation.Record),
		TicketVerifierKey:        observation.Record.TicketVerifierKey,
		RecordFingerprint:        observation.Fingerprint,
		ConfirmedAt:              canonicalNetworkMutationTime(confirmedAt),
	}
}

// weakenMachineOwnershipProjectionSchema removes singleton guards so duplicate read handling remains directly testable.
func weakenMachineOwnershipProjectionSchema(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	statements := []string{
		"PRAGMA foreign_keys = OFF",
		"ALTER TABLE machine_ownership_projections RENAME TO machine_ownership_projections_guarded",
		`CREATE TABLE machine_ownership_projections (
			id INTEGER,
			network_state_id INTEGER,
			ownership_schema_version INTEGER,
			installation_id TEXT,
			owner_identity TEXT,
			ownership_generation INTEGER,
			loopback_pool_prefix TEXT,
			network_policy_fingerprint TEXT,
			ticket_verifier_key TEXT,
			record_fingerprint TEXT,
			confirmed_at DATETIME
		)`,
		`INSERT INTO machine_ownership_projections
			SELECT * FROM machine_ownership_projections_guarded`,
		"DROP TABLE machine_ownership_projections_guarded",
		"PRAGMA foreign_keys = ON",
	}
	for _, statement := range statements {
		machineOwnershipProjectionTestExec(t, databaseConnection, statement)
	}
}

// assertMachineOwnershipProjectionRowCount proves a failed transaction left no daemon authority behind.
func assertMachineOwnershipProjectionRowCount(t *testing.T, databaseConnection *gorm.DB, want int64) {
	t.Helper()
	var count int64
	if err := databaseConnection.Model(&models.MachineOwnershipProjection{}).Count(&count).Error; err != nil {
		t.Fatalf("count machine ownership projections: %v", err)
	}
	if count != want {
		t.Fatalf("machine ownership projection rows = %d, want %d", count, want)
	}
}

// machineOwnershipProjectionTestExec applies one focused durable corruption or fails immediately.
func machineOwnershipProjectionTestExec(t *testing.T, databaseConnection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute machine ownership projection fixture statement: %v", err)
	}
}

// machineOwnershipProjectionTestTime returns the stable UTC confirmation instant shared by fixtures.
func machineOwnershipProjectionTestTime() time.Time {
	return time.Date(2026, time.July, 19, 15, 0, 0, 0, time.UTC)
}
