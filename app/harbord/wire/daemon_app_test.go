package wire

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/state"
)

// TestProvideHarbordReadinessIsLazy verifies assembly does not touch durable state before daemon authority is requested.
func TestProvideHarbordReadinessIsLazy(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath)
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close database connections: %v", err)
		}
	})

	readiness, err := provideHarbordReadiness(connections)
	if err != nil {
		t.Fatalf("provideHarbordReadiness() error = %v", err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database exists after readiness assembly: %v", err)
	}

	err = readiness(t.Context())
	if err == nil || !strings.Contains(err.Error(), "migrations are not ready") {
		t.Fatalf("readiness error = %v, want missing migration ledger", err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("database was not opened by readiness invocation: %v", err)
	}
}

// TestProvideHarbordReadinessRejectsMissingConnections verifies invalid assembly fails before foreground authority can be acquired.
func TestProvideHarbordReadinessRejectsMissingConnections(t *testing.T) {
	readiness, err := provideHarbordReadiness(nil)
	if err == nil || readiness != nil {
		t.Fatalf("provideHarbordReadiness(nil) = (%v, %v), want nil readiness and construction error", readiness, err)
	}
}

// TestInitializeApplicationWiresForegroundServices verifies Wire constructs the complete production daemon dependency graph lazily.
func TestInitializeApplicationWiresForegroundServices(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath)

	application, err := InitializeApplication()
	if err != nil {
		t.Fatalf("InitializeApplication() error = %v", err)
	}
	if application.RootCmd() == nil {
		t.Fatal("InitializeApplication() returned no root command")
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database exists after application assembly: %v", err)
	}

	parser, err := kong.New(application.RootCmd(), kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	if _, err := parser.Parse([]string{"--foreground", "about"}); err == nil || !strings.Contains(err.Error(), "--foreground cannot be combined") {
		t.Fatalf("production foreground conflict error = %v", err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("foreground parsing touched the database before daemon execution: %v", err)
	}
}

// TestDaemonProvidersRejectIncompleteAssembly verifies constructor validation remains at the owning production boundaries.
func TestDaemonProvidersRejectIncompleteAssembly(t *testing.T) {
	runtimeController, err := harbordruntime.NewController(new(state.Store))
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	if _, err := provideControlServer(nil); err == nil {
		t.Fatal("provideControlServer(nil) error = nil, want required authority error")
	}
	if _, err := provideDaemonRunner(nil, func(context.Context) error { return nil }, runtimeController); err == nil {
		t.Fatal("provideDaemonRunner(nil server) error = nil, want required server error")
	}
	if _, err := provideDaemonRunner(new(control.Server), nil, runtimeController); err == nil {
		t.Fatal("provideDaemonRunner(nil readiness) error = nil, want required readiness error")
	}
	if _, err := provideDaemonRunner(new(control.Server), func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("provideDaemonRunner(nil runtime) error = nil, want required runtime error")
	}
}

// TestDaemonRuntimeCloseTimeoutExceedsControllerBudget keeps outer authority beyond nested cleanup.
func TestDaemonRuntimeCloseTimeoutExceedsControllerBudget(t *testing.T) {
	runtimeController, err := harbordruntime.NewController(new(state.Store))
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}

	timeout := daemonRuntimeCloseTimeout(runtimeController)
	if timeout != runtimeController.ShutdownTimeout()+runtimeCloseCoordinationMargin {
		t.Fatalf("daemon runtime close timeout = %s, want controller budget plus %s", timeout, runtimeCloseCoordinationMargin)
	}
	if timeout <= runtimeController.ShutdownTimeout() {
		t.Fatalf("daemon runtime close timeout = %s, must exceed controller budget %s", timeout, runtimeController.ShutdownTimeout())
	}
}
