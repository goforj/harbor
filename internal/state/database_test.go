package state

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/inspects"
)

// TestConfigureDatabaseUsesUserDataPath verifies the default named connection cannot inherit a checkout-relative root database.
func TestConfigureDatabaseUsesUserDataPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "harbor.db")
	environment := map[string]string{}
	prepared := ""

	got, err := configureDatabase(testDatabaseConfiguration(environment, func() (string, error) {
		return want, nil
	}, func(path string) error {
		prepared = path
		return nil
	}))
	if err != nil {
		t.Fatalf("configure database: %v", err)
	}
	if got != want || prepared != want {
		t.Fatalf("configured path = %q, prepared %q, want %q", got, prepared, want)
	}
	if environment[databaseDriverKey] != databaseDriver {
		t.Fatalf("driver = %q, want %q", environment[databaseDriverKey], databaseDriver)
	}
	if !strings.HasPrefix(environment[databaseDSNKey], want+"?") {
		t.Fatalf("DSN = %q, want path prefix %q", environment[databaseDSNKey], want)
	}
	if environment[databaseMaxOpenKey] != databasePoolSize || environment[databaseMaxIdleKey] != databasePoolSize {
		t.Fatalf("pool limits = open %q idle %q, want %q", environment[databaseMaxOpenKey], environment[databaseMaxIdleKey], databasePoolSize)
	}
}

// TestConfigureDatabasePreservesExplicitNamedPath verifies operators can relocate state without falling back to root DB settings.
func TestConfigureDatabasePreservesExplicitNamedPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom.db")
	environment := map[string]string{
		databasePathKey: want,
		"DB_DSN":        filepath.Join(t.TempDir(), "wrong.db"),
	}

	got, err := configureDatabase(testDatabaseConfiguration(environment, func() (string, error) {
		return "", errors.New("default path should not be resolved")
	}, func(path string) error {
		if path != want {
			t.Fatalf("prepared path = %q, want %q", path, want)
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("configure database: %v", err)
	}
	if got != want || !strings.HasPrefix(environment[databaseDSNKey], want+"?") {
		t.Fatalf("configured path = %q, DSN %q, want %q", got, environment[databaseDSNKey], want)
	}
}

// TestConfigureDatabasePrefersExplicitDSN verifies the most specific GoForj setting retains normal precedence.
func TestConfigureDatabasePrefersExplicitDSN(t *testing.T) {
	want := filepath.Join(t.TempDir(), "explicit.db")
	environment := map[string]string{
		databaseDSNKey:    want,
		databaseSQLiteKey: filepath.Join(t.TempDir(), "sqlite.db"),
		databasePathKey:   filepath.Join(t.TempDir(), "database.db"),
	}

	got, err := configureDatabase(testDatabaseConfiguration(environment, func() (string, error) {
		return "", errors.New("default path should not be resolved")
	}, func(string) error { return nil }))
	if err != nil {
		t.Fatalf("configure database: %v", err)
	}
	if got != want {
		t.Fatalf("configured path = %q, want %q", got, want)
	}
}

// TestConfigureDatabaseRejectsAnotherDriver verifies daemon state cannot silently move onto the application's root database.
func TestConfigureDatabaseRejectsAnotherDriver(t *testing.T) {
	environment := map[string]string{databaseDriverKey: "postgres"}
	_, err := configureDatabase(testDatabaseConfiguration(environment, func() (string, error) {
		return filepath.Join(t.TempDir(), "harbor.db"), nil
	}, func(string) error { return nil }))
	if err == nil || !strings.Contains(err.Error(), "requires SQLite") {
		t.Fatalf("error = %v, want SQLite driver error", err)
	}
}

// TestHarborSQLiteDSNEnforcesConnectionPolicy verifies custom options cannot disable required journal guarantees.
func TestHarborSQLiteDSNEnforcesConnectionPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbor.db")
	dsn, err := harborSQLiteDSN(path + "?_pragma=foreign_keys(0)&cache=private&_txlock=deferred")
	if err != nil {
		t.Fatalf("build Harbor SQLite DSN: %v", err)
	}
	databasePath, rawQuery, found := strings.Cut(dsn, "?")
	if !found || databasePath != path {
		t.Fatalf("DSN path = %q, want %q", databasePath, path)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse DSN query: %v", err)
	}
	if values.Get("_txlock") != "immediate" || values.Get("cache") != "private" {
		t.Fatalf("DSN options = %#v, want immediate transaction and preserved cache option", values)
	}
	wantPragmas := []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)"}
	if got := values["_pragma"]; !reflect.DeepEqual(got, wantPragmas) {
		t.Fatalf("DSN pragmas = %#v, want %#v", got, wantPragmas)
	}
}

// TestConfiguredDatabaseAppliesSQLitePolicy verifies the generated named accessor receives Harbor's runtime connection guarantees.
func TestConfiguredDatabaseAppliesSQLitePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv(databaseDriverKey, databaseDriver)
	t.Setenv(databaseDSNKey, path)
	t.Setenv(databaseSQLiteKey, "")
	t.Setenv(databasePathKey, "")

	if _, err := ConfigureDatabase(); err != nil {
		t.Fatalf("configure database: %v", err)
	}
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	databaseConnection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open named database: %v", err)
	}

	var journalMode string
	var foreignKeys, busyTimeout, synchronous int
	for _, query := range []struct {
		statement string
		target    any
	}{
		{statement: "PRAGMA journal_mode", target: &journalMode},
		{statement: "PRAGMA foreign_keys", target: &foreignKeys},
		{statement: "PRAGMA busy_timeout", target: &busyTimeout},
		{statement: "PRAGMA synchronous", target: &synchronous},
	} {
		if err := databaseConnection.Raw(query.statement).Scan(query.target).Error; err != nil {
			t.Fatalf("query %s: %v", query.statement, err)
		}
	}
	if journalMode != "wal" || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 {
		t.Fatalf("SQLite policy = journal %q foreign %d busy %d synchronous %d", journalMode, foreignKeys, busyTimeout, synchronous)
	}
	sqlDatabase, err := databaseConnection.DB()
	if err != nil {
		t.Fatalf("resolve sql database: %v", err)
	}
	if got := sqlDatabase.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("maximum open connections = %d, want 1", got)
	}
}

// TestConfigureDatabaseReturnsResolutionError verifies startup fails before opening an ambiguous database path.
func TestConfigureDatabaseReturnsResolutionError(t *testing.T) {
	want := errors.New("user directory unavailable")
	_, err := configureDatabase(testDatabaseConfiguration(map[string]string{}, func() (string, error) {
		return "", want
	}, func(string) error { return nil }))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

// TestConfigureDatabaseReturnsPrepareError verifies unsafe or unavailable state directories stop startup.
func TestConfigureDatabaseReturnsPrepareError(t *testing.T) {
	want := errors.New("path rejected")
	_, err := configureDatabase(testDatabaseConfiguration(map[string]string{}, func() (string, error) {
		return filepath.Join(t.TempDir(), "harbor.db"), nil
	}, func(string) error { return want }))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// TestPrepareDatabasePathCreatesOwnerOnlyDirectory verifies GoForj never creates the final state directory with its generic mode.
func TestPrepareDatabasePathCreatesOwnerOnlyDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "harbor")
	if err := prepareDatabasePath(filepath.Join(directory, "harbor.db")); err != nil {
		t.Fatalf("prepare database path: %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o700 {
		t.Fatalf("data directory mode = %o, want 700", got)
	}
}

// TestPrepareDatabasePathPreservesExistingDirectoryMode verifies explicit database overrides cannot chmod a shared parent.
func TestPrepareDatabasePathPreservesExistingDirectoryMode(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("create existing directory: %v", err)
	}
	if err := prepareDatabasePath(filepath.Join(directory, "harbor.db")); err != nil {
		t.Fatalf("prepare database path: %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat existing directory: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o755 {
		t.Fatalf("existing directory mode = %o, want unchanged mode 755", got)
	}
}

// TestPrepareDatabasePathRejectsFileParent verifies an override cannot treat an existing file as a state directory.
func TestPrepareDatabasePathRejectsFileParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create parent file: %v", err)
	}
	if err := prepareDatabasePath(filepath.Join(path, "harbor.db")); err == nil {
		t.Fatal("prepareDatabasePath unexpectedly accepted a file parent")
	}
}

// TestPrepareDatabasePathRejectsRelativeAndEmptyPaths verifies daemon state never depends on its working directory.
func TestPrepareDatabasePathRejectsRelativeAndEmptyPaths(t *testing.T) {
	for _, path := range []string{"", filepath.Join("relative", "harbor.db")} {
		if err := prepareDatabasePath(path); err == nil {
			t.Fatalf("prepareDatabasePath(%q) unexpectedly succeeded", path)
		}
	}
}

// TestPrepareDatabasePathAcceptsMemoryDSNs keeps repository unit tests independent from a durable user directory.
func TestPrepareDatabasePathAcceptsMemoryDSNs(t *testing.T) {
	for _, path := range []string{":memory:", "file::memory:", ":memory:?cache=shared"} {
		if err := prepareDatabasePath(path); err != nil {
			t.Fatalf("prepareDatabasePath(%q): %v", path, err)
		}
	}
}

// testDatabaseConfiguration builds an isolated environment boundary for configuration tests.
func testDatabaseConfiguration(environment map[string]string, resolve func() (string, error), prepare func(string) error) databaseConfiguration {
	return databaseConfiguration{
		lookup: func(key string) (string, bool) {
			value, ok := environment[key]
			return value, ok
		},
		set: func(key string, value string) error {
			environment[key] = value
			return nil
		},
		resolvePath: resolve,
		preparePath: prepare,
	}
}
