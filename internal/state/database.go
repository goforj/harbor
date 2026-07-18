package state

import (
	"fmt"
	"net/url"
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
	databaseMaxOpenKey = "DB_HARBORD_MAX_OPEN_CONNECTIONS"
	databaseMaxIdleKey = "DB_HARBORD_MAX_IDLE_CONNECTIONS"
	databaseDriver     = "sqlite"
	databasePoolSize   = "1"
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
	dsn, err := harborSQLiteDSN(path)
	if err != nil {
		return "", err
	}
	if err := configuration.set(databaseDriverKey, databaseDriver); err != nil {
		return "", fmt.Errorf("configure Harbor database driver: %w", err)
	}
	if err := configuration.set(databaseDSNKey, dsn); err != nil {
		return "", fmt.Errorf("configure Harbor database path: %w", err)
	}
	if err := configuration.set(databaseMaxOpenKey, databasePoolSize); err != nil {
		return "", fmt.Errorf("configure Harbor database writer limit: %w", err)
	}
	if err := configuration.set(databaseMaxIdleKey, databasePoolSize); err != nil {
		return "", fmt.Errorf("configure Harbor database idle limit: %w", err)
	}

	databasePath, _, _ := strings.Cut(path, "?")
	return databasePath, nil
}

// harborSQLiteDSN applies the durability and concurrency policy to every connection the GoForj registry may open.
func harborSQLiteDSN(path string) (string, error) {
	databasePath, rawQuery, _ := strings.Cut(strings.TrimSpace(path), "?")
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse Harbor database options: %w", err)
	}
	values.Set("_txlock", "immediate")
	values.Del("_pragma")
	for _, pragma := range []string{
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
	} {
		values.Add("_pragma", pragma)
	}
	return databasePath + "?" + values.Encode(), nil
}

// prepareDatabasePath creates Harbor's final data directory with owner-only Unix permissions before GoForj opens SQLite.
func prepareDatabasePath(path string) error {
	databasePath, _, _ := strings.Cut(strings.TrimSpace(path), "?")
	if databasePath == ":memory:" || databasePath == "file::memory:" {
		return nil
	}
	if databasePath == "" {
		return fmt.Errorf("Harbor database path must not be empty")
	}
	if !filepath.IsAbs(databasePath) {
		return fmt.Errorf("Harbor database path %q must be absolute", databasePath)
	}

	directory := filepath.Dir(filepath.Clean(databasePath))
	exists, err := databaseDirectoryExists(directory)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create Harbor data directory parent: %w", err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create Harbor data directory: %w", err)
		}
		if _, validationErr := databaseDirectoryExists(directory); validationErr != nil {
			return validationErr
		}
	}
	return nil
}

// databaseDirectoryExists distinguishes a usable existing directory from an unsafe filesystem entry.
func databaseDirectoryExists(directory string) (bool, error) {
	info, err := os.Stat(directory)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Harbor data directory: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("Harbor data directory %q is not a directory", directory)
	}
	return true, nil
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
