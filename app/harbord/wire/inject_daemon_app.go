// App-owned daemon assembly. EDIT THIS FILE.

package wire

import (
	"context"
	"errors"
	"time"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/migrations"
)

const runtimeCloseCoordinationMargin = 5 * time.Second

// provideControlServer binds durable Harbor authority to the authenticated product protocol.
func provideControlServer(controlAuthority *authority.Authority) (*control.Server, error) {
	return control.NewServer(control.ServerConfig{Authority: controlAuthority})
}

// provideHarbordReadiness validates assembly while deferring migration inspection until daemon authority is owned.
func provideHarbordReadiness(connections *database.Connections) (daemon.ReadinessCheck, error) {
	if connections == nil {
		return nil, errors.New("create harbord readiness: database connections are required")
	}
	return func(ctx context.Context) error {
		return migrations.CheckHarbordReadiness(ctx, connections)
	}, nil
}

// provideDaemonRunner assembles singleton authority around the complete control and network lifetime.
func provideDaemonRunner(
	server daemon.ConnectionServer,
	readiness daemon.ReadinessCheck,
	runtimeController *harbordruntime.Controller,
) (*daemon.Runner, error) {
	return daemon.NewRunner(daemon.RunnerConfig{
		Server:              server,
		Readiness:           readiness,
		Runtime:             runtimeController,
		RuntimeCloseTimeout: daemonRuntimeCloseTimeout(runtimeController),
	})
}

// daemonRuntimeCloseTimeout leaves scheduling and certificate-store closure outside the controller's child budget.
func daemonRuntimeCloseTimeout(runtimeController *harbordruntime.Controller) time.Duration {
	return runtimeController.ShutdownTimeout() + runtimeCloseCoordinationMargin
}
