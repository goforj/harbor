package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/env/v2"
	"gorm.io/gorm"
)

func TestDefaultConnectionName(t *testing.T) {
	t.Setenv("DB_DEFAULT", "  Reporting ")
	if got := defaultConnectionName(); got != "reporting" {
		t.Fatalf("expected default connection name, got %q", got)
	}
	t.Setenv("DB_DEFAULT", "")
	if got := defaultConnectionName(); got != "default" {
		t.Fatalf("expected fallback default, got %q", got)
	}
}

func TestEnvKey(t *testing.T) {
	if got := envKey("default", "HOST"); got != "DB_HOST" {
		t.Fatalf("expected DB_HOST, got %q", got)
	}
	if got := envKey("analytics", "HOST"); got != "DB_ANALYTICS_HOST" {
		t.Fatalf("expected DB_ANALYTICS_HOST, got %q", got)
	}
}

func TestDBScope(t *testing.T) {
	if got := dbScope("default").Key("HOST"); got != "DB_HOST" {
		t.Fatalf("expected DB_HOST, got %q", got)
	}
	if got := dbScope("analytics").Key("HOST"); got != "DB_ANALYTICS_HOST" {
		t.Fatalf("expected DB_ANALYTICS_HOST, got %q", got)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("DB_ANALYTICS_HOST", "analytics-db")
	t.Setenv("DB_HOST", "main-db")
	if got := envOr("analytics", "HOST"); got != "analytics-db" {
		t.Fatalf("expected analytics override, got %q", got)
	}
	if got := envOr("missing", "HOST"); got != "main-db" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestDatabaseEnvOrUsesSQLiteSpecificPath(t *testing.T) {
	t.Setenv("DB_DATABASE", "db")
	t.Setenv("DB_SQLITE_DATABASE", "./_data/sqlite/app.db")
	if got := databaseEnvOr("default", "sqlite"); got != "./_data/sqlite/app.db" {
		t.Fatalf("expected sqlite database path, got %q", got)
	}
	if got := databaseEnvOr("default", "mysql"); got != "db" {
		t.Fatalf("expected default database for mysql, got %q", got)
	}
}

func TestDatabaseEnvOrFallsBackToDatabaseForSQLite(t *testing.T) {
	t.Setenv("DB_DATABASE", "./_data/sqlite/app.db")
	if got := databaseEnvOr("default", "sqlite"); got != "./_data/sqlite/app.db" {
		t.Fatalf("expected sqlite fallback database path, got %q", got)
	}
}

func TestDatabaseEnvOrUsesNamedSQLiteSpecificPath(t *testing.T) {
	t.Setenv("DB_DATABASE", "db")
	t.Setenv("DB_ANALYTICS_DATABASE", "analytics")
	t.Setenv("DB_ANALYTICS_SQLITE_DATABASE", "./_data/sqlite/analytics.db")
	if got := databaseEnvOr("analytics", "sqlite"); got != "./_data/sqlite/analytics.db" {
		t.Fatalf("expected named sqlite database path, got %q", got)
	}
}

func TestBuildDSNMySQL(t *testing.T) {
	t.Setenv("DB_ANALYTICS_HOST", "db")
	t.Setenv("DB_ANALYTICS_PORT", "3306")
	t.Setenv("DB_ANALYTICS_USERNAME", "user")
	t.Setenv("DB_ANALYTICS_PASSWORD", "pass")
	t.Setenv("DB_ANALYTICS_DATABASE", "analytics")

	got, err := buildDSN("analytics", "mysql")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	want := "user:pass@tcp(db:3306)/analytics?charset=utf8mb4&parseTime=True&loc=Local"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildDSNSQLiteDefaultsDatabasePath(t *testing.T) {
	t.Setenv("DB_DEFAULT", "default")
	t.Setenv("DB_DATABASE", "")
	got, err := buildDSN("default", "sqlite")
	if err != nil {
		t.Fatalf("expected default sqlite DSN, got %v", err)
	}
	if got != filepath.Join("_data", "sqlite", "app.db") {
		t.Fatalf("default sqlite DSN = %q", got)
	}
}

func TestBuildDSNNamedSQLiteDefaultsDatabasePath(t *testing.T) {
	t.Setenv("DB_DATABASE", "")
	t.Setenv("DB_SQLITE_DATABASE", "")
	t.Setenv("DB_ANALYTICS_DATABASE", "")
	t.Setenv("DB_ANALYTICS_SQLITE_DATABASE", "")
	got, err := buildDSN("analytics", "sqlite")
	if err != nil {
		t.Fatalf("expected named sqlite DSN, got %v", err)
	}
	if got != filepath.Join("_data", "sqlite", "analytics.db") {
		t.Fatalf("named sqlite DSN = %q", got)
	}
}

func TestGetIntEnv(t *testing.T) {
	t.Setenv("DB_ANALYTICS_MAX_IDLE_CONNECTIONS", "5")
	if got := getIntEnv("analytics", "MAX_IDLE_CONNECTIONS"); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}

	t.Setenv("DB_ANALYTICS_MAX_IDLE_CONNECTIONS", "")
	t.Setenv("DB_MAX_IDLE_CONNECTIONS", "7")
	if got := getIntEnv("analytics", "MAX_IDLE_CONNECTIONS", "DB_MAX_IDLE_CONNECTIONS"); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}

	t.Setenv("DB_MAX_IDLE_CONNECTIONS", "nope")
	if got := getIntEnv("analytics", "MAX_IDLE_CONNECTIONS", "DB_MAX_IDLE_CONNECTIONS"); got != 0 {
		t.Fatalf("expected invalid fallback to be 0, got %d", got)
	}
}

func TestDBScopeGetDuration(t *testing.T) {
	t.Setenv("DB_ANALYTICS_SLOW_QUERY_THRESHOLD", "350ms")
	if got := dbScope("analytics").GetDuration("SLOW_QUERY_THRESHOLD", ""); got != 350*time.Millisecond {
		t.Fatalf("expected 350ms, got %s", got)
	}

	t.Setenv("DB_ANALYTICS_SLOW_QUERY_THRESHOLD", "")
	t.Setenv("DB_SLOW_QUERY_THRESHOLD", "500ms")
	if got := dbScope("analytics").GetDuration("SLOW_QUERY_THRESHOLD", env.Get("DB_SLOW_QUERY_THRESHOLD", "")); got != 500*time.Millisecond {
		t.Fatalf("expected fallback 500ms, got %s", got)
	}

	t.Setenv("DB_SLOW_QUERY_THRESHOLD", "not-a-duration")
	if got := dbScope("analytics").GetDuration("SLOW_QUERY_THRESHOLD", env.Get("DB_SLOW_QUERY_THRESHOLD", "")); got != 0 {
		t.Fatalf("expected invalid fallback to be 0, got %s", got)
	}
}

func TestConnectionsCloseHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conns := &Connections{
		opened: map[string]*gorm.DB{
			"default": nil,
		},
	}

	err := conns.Close(ctx)
	if err == nil {
		t.Fatal("expected close to return context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestConnectionsCloseSkipsNilHandles(t *testing.T) {
	conns := &Connections{
		opened: map[string]*gorm.DB{
			"default": nil,
		},
	}

	if err := conns.Close(context.Background()); err != nil {
		t.Fatalf("expected nil-handle close to succeed, got %v", err)
	}
}
