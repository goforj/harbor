package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/platform/userpaths"
)

const (
	// DatabaseConnection names the GoForj database connection owned by the harbord App.
	DatabaseConnection = "harbord"
	databaseDriverKey  = "DB_HARBORD_DRIVER"
	databaseDSNKey     = "DB_HARBORD_DSN"
	databaseSQLiteKey  = "DB_HARBORD_SQLITE_DATABASE"
	databasePathKey    = "DB_HARBORD_DATABASE"
	databaseDriver     = "sqlite"
)

// databaseConfiguration keeps process-wide environment mutation behind a testable boundary.
type databaseConfiguration struct {
	lookup      func(string) (string, bool)
	set         func(string, string) error
	resolvePath func() (string, error)
	preparePath func(string) error
}

// ConfigureDatabase points the named harbord connection at Harbor's per-user SQLite database.
func ConfigureDatabase() (string, error) {
	return configureDatabase(databaseConfiguration{
		lookup:      os.LookupEnv,
		set:         os.Setenv,
		resolvePath: userpaths.DatabasePath,
		preparePath: prepareDatabasePath,
	})
}

// configureDatabase preserves explicit named configuration while preventing root database settings from leaking into daemon state.
func configureDatabase(configuration databaseConfiguration) (string, error) {
	driver := strings.TrimSpace(environmentValue(configuration.lookup, databaseDriverKey))
	if driver != "" && driver != "sqlite" && driver != "sqlite3" {
		return "", fmt.Errorf("Harbor state requires SQLite, got database driver %q", driver)
	}

	path := firstEnvironmentValue(configuration.lookup, databaseDSNKey, databaseSQLiteKey, databasePathKey)
	if path == "" {
		var err error
		path, err = configuration.resolvePath()
		if err != nil {
			return "", fmt.Errorf("resolve Harbor database path: %w", err)
		}
	}
	path = strings.TrimSpace(path)
	if err := configuration.preparePath(path); err != nil {
		return "", err
	}
	if err := configuration.set(databaseDriverKey, databaseDriver); err != nil {
		return "", fmt.Errorf("configure Harbor database driver: %w", err)
	}
	if err := configuration.set(databaseDSNKey, path); err != nil {
		return "", fmt.Errorf("configure Harbor database path: %w", err)
	}

	return path, nil
}

// prepareDatabasePath creates Harbor's final data directory with owner-only Unix permissions before GoForj opens SQLite.
func prepareDatabasePath(path string) error {
	if path == ":memory:" || path == "file::memory:" {
		return nil
	}
	if path == "" {
		return fmt.Errorf("Harbor database path must not be empty")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("Harbor database path %q must be absolute", path)
	}

	directory := filepath.Dir(filepath.Clean(path))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Harbor data directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure Harbor data directory: %w", err)
	}
	return nil
}

// firstEnvironmentValue returns the first non-empty named connection setting in GoForj precedence order.
func firstEnvironmentValue(lookup func(string) (string, bool), keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(environmentValue(lookup, key)); value != "" {
			return value
		}
	}
	return ""
}

// environmentValue normalizes lookup's absent-value shape for configuration resolution.
func environmentValue(lookup func(string) (string, bool), key string) string {
	value, _ := lookup(key)
	return value
}
