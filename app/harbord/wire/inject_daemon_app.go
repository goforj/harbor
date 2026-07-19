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
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/migrations"
)

const runtimeCloseCoordinationMargin = 5 * time.Second

// projectUnregisterIssuerOpener isolates the machine-global store boundary so assembly tests can prove it stays lazy.
type projectUnregisterIssuerOpener func(ticketissuer.PlanSource) (reconcile.TicketIssuer, error)

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

// provideProjectUnregisterCoordinator assembles restart recovery while retaining helper stores behind a lazy factory.
func provideProjectUnregisterCoordinator(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.HelperApprovalPlanSource,
	runtimeController *harbordruntime.Controller,
) (*reconcile.ProjectUnregisterCoordinator, error) {
	return provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		runtimeController,
		func(plans ticketissuer.PlanSource) (reconcile.TicketIssuer, error) {
			return ticketissuer.OpenDefault(plans)
		},
	)
}

// provideProjectUnregisterCoordinatorWithIssuerOpener keeps the machine-global boundary injectable without making it process-global.
func provideProjectUnregisterCoordinatorWithIssuerOpener(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.HelperApprovalPlanSource,
	runtimeController *harbordruntime.Controller,
	openIssuer projectUnregisterIssuerOpener,
) (*reconcile.ProjectUnregisterCoordinator, error) {
	if store == nil {
		return nil, errors.New("create project unregister coordinator: state store is required")
	}
	if operations == nil {
		return nil, errors.New("create project unregister coordinator: operation journal is required")
	}
	if plans == nil {
		return nil, errors.New("create project unregister coordinator: approval plan source is required")
	}
	if runtimeController == nil {
		return nil, errors.New("create project unregister coordinator: runtime controller is required")
	}
	if openIssuer == nil {
		return nil, errors.New("create project unregister coordinator: ticket issuer opener is required")
	}
	issuers := func() (reconcile.TicketIssuer, error) {
		return openIssuer(plans)
	}
	return reconcile.NewProjectUnregisterCoordinator(
		store,
		operations,
		plans,
		loopback.New(),
		runtimeController,
		issuers,
		helper.SystemClock{},
	), nil
}

// provideDaemonRunner assembles singleton authority around the complete control and network lifetime.
func provideDaemonRunner(
	server daemon.ConnectionServer,
	readiness daemon.ReadinessCheck,
	runtimeController *harbordruntime.Controller,
	coordinator *reconcile.ProjectUnregisterCoordinator,
) (*daemon.Runner, error) {
	if coordinator == nil {
		return nil, errors.New("create daemon runner: project unregister coordinator is required")
	}
	return daemon.NewRunner(daemon.RunnerConfig{
		Server:              server,
		Readiness:           readiness,
		Recovery:            coordinator.Recover,
		Runtime:             runtimeController,
		RuntimeCloseTimeout: daemonRuntimeCloseTimeout(runtimeController),
	})
}

// daemonRuntimeCloseTimeout leaves scheduling and certificate-store closure outside the controller's child budget.
func daemonRuntimeCloseTimeout(runtimeController *harbordruntime.Controller) time.Duration {
	return runtimeController.ShutdownTimeout() + runtimeCloseCoordinationMargin
}
