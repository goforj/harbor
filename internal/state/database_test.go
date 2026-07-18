package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	if environment[databaseDSNKey] != want {
		t.Fatalf("DSN = %q, want %q", environment[databaseDSNKey], want)
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
	if got != want || environment[databaseDSNKey] != want {
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
	for _, path := range []string{":memory:", "file::memory:"} {
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
