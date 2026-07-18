// App-owned daemon assembly. EDIT THIS FILE.

package wire

import (
	"context"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/migrations"
)

// provideControlServer binds durable Harbor authority to the authenticated product protocol.
func provideControlServer(controlAuthority *authority.Authority) (*control.Server, error) {
	return control.NewServer(control.ServerConfig{Authority: controlAuthority})
}

// provideHarbordReadiness defers migration inspection until the foreground runner owns daemon authority.
func provideHarbordReadiness(connections *database.Connections) daemon.ReadinessCheck {
	return func(ctx context.Context) error {
		return migrations.CheckHarbordReadiness(ctx, connections)
	}
}

// provideDaemonRunner assembles the production singleton lock, local transport, and control server.
func provideDaemonRunner(server daemon.ConnectionServer, readiness daemon.ReadinessCheck) (*daemon.Runner, error) {
	return daemon.NewRunner(daemon.RunnerConfig{
		Server:    server,
		Readiness: readiness,
	})
}
