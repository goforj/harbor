// App-owned daemon assembly. EDIT THIS FILE.

package wire

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/migrations"
)

const runtimeCloseCoordinationMargin = 5 * time.Second

// projectUnregisterIssuerOpener isolates default issuer stores while retaining daemon-owned ownership authority.
type projectUnregisterIssuerOpener func(ticketissuer.PlanSource, *state.MachineOwnershipProjectionSource) (reconcile.TicketIssuer, error)

// projectLifecycleRuntime joins managed project processes before releasing shared network infrastructure.
type projectLifecycleRuntime struct {
	daemon.Runtime
	lifecycle projectLifecycleAuthority
	done      chan struct{}
	doneOnce  sync.Once
	mutex     sync.Mutex
	err       error
}

// projectLifecycleAuthority is the process lifetime composed into daemon runtime ownership.
type projectLifecycleAuthority interface {
	Close(context.Context) error
	Done() <-chan struct{}
	Err() error
}

// newProjectLifecycleRuntime creates one completion signal spanning both network and process authority.
func newProjectLifecycleRuntime(runtime daemon.Runtime, lifecycle projectLifecycleAuthority) *projectLifecycleRuntime {
	return &projectLifecycleRuntime{Runtime: runtime, lifecycle: lifecycle, done: make(chan struct{})}
}

// Start prevents a failed network startup from abandoning processes dispatched during durable recovery.
func (runtime *projectLifecycleRuntime) Start(ctx context.Context) error {
	if err := runtime.Runtime.Start(ctx); err != nil {
		closeErr := runtime.lifecycle.Close(context.Background())
		runtime.finish(errors.Join(err, closeErr))
		return errors.Join(err, closeErr)
	}
	go runtime.joinUnexpectedNetworkStop()
	return nil
}

// Done closes only after both network and managed-process authority are terminal.
func (runtime *projectLifecycleRuntime) Done() <-chan struct{} {
	return runtime.done
}

// Err joins terminal failures retained by both owned runtimes.
func (runtime *projectLifecycleRuntime) Err() error {
	runtime.mutex.Lock()
	err := runtime.err
	runtime.mutex.Unlock()
	return errors.Join(err, runtime.Runtime.Err(), runtime.lifecycle.Err())
}

// Close releases project process authority before the network runtime it depends on.
func (runtime *projectLifecycleRuntime) Close(ctx context.Context) error {
	lifecycleErr := runtime.lifecycle.Close(ctx)
	networkErr := runtime.Runtime.Close(ctx)
	err := errors.Join(lifecycleErr, networkErr)
	if signalClosed(runtime.lifecycle.Done()) && signalClosed(runtime.Runtime.Done()) {
		runtime.finish(err)
	}
	return err
}

// joinUnexpectedNetworkStop closes process authority before publishing composite runtime termination.
func (runtime *projectLifecycleRuntime) joinUnexpectedNetworkStop() {
	<-runtime.Runtime.Done()
	err := runtime.lifecycle.Close(context.Background())
	<-runtime.lifecycle.Done()
	runtime.finish(errors.Join(runtime.Runtime.Err(), err, runtime.lifecycle.Err()))
}

// finish publishes terminal composite ownership exactly once.
func (runtime *projectLifecycleRuntime) finish(err error) {
	runtime.mutex.Lock()
	runtime.err = errors.Join(runtime.err, err)
	runtime.mutex.Unlock()
	runtime.doneOnce.Do(func() { close(runtime.done) })
}

// signalClosed reports whether one owned completion signal is already terminal.
func signalClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// provideControlServer binds durable Harbor authority to the authenticated product protocol.
func provideControlServer(controlAuthority *authority.Authority, shutdown *daemon.Shutdown) (*control.Server, error) {
	if shutdown == nil {
		return nil, errors.New("create control server: daemon shutdown coordinator is required")
	}
	return control.NewServer(control.ServerConfig{
		Authority:       controlAuthority,
		RequestShutdown: shutdown.Request,
	})
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
	ownership *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
) (*reconcile.ProjectUnregisterCoordinator, error) {
	return provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		ownership,
		runtimeController,
		func(plans ticketissuer.PlanSource, ownership *state.MachineOwnershipProjectionSource) (reconcile.TicketIssuer, error) {
			return ticketissuer.OpenDefault(plans, ownership)
		},
	)
}

// provideProjectProcessSupervisor creates the one process-tree owner shared by all managed projects.
func provideProjectProcessSupervisor(environment projectprocess.Environment) *projectprocess.Supervisor {
	return projectprocess.New(projectprocess.Options{Environment: environment})
}

// provideProjectUnregisterCoordinatorWithIssuerOpener keeps default issuer storage injectable without making it process-global.
func provideProjectUnregisterCoordinatorWithIssuerOpener(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.HelperApprovalPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
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
	if ownership == nil {
		return nil, errors.New("create project unregister coordinator: machine ownership projection source is required")
	}
	if runtimeController == nil {
		return nil, errors.New("create project unregister coordinator: runtime controller is required")
	}
	if openIssuer == nil {
		return nil, errors.New("create project unregister coordinator: ticket issuer opener is required")
	}
	issuers := func() (reconcile.TicketIssuer, error) {
		return openIssuer(plans, ownership)
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
	lifecycle *reconcile.ProjectLifecycleCoordinator,
	shutdown *daemon.Shutdown,
) (*daemon.Runner, error) {
	if runtimeController == nil {
		return nil, errors.New("create daemon runner: runtime controller is required")
	}
	if coordinator == nil {
		return nil, errors.New("create daemon runner: project unregister coordinator is required")
	}
	if shutdown == nil {
		return nil, errors.New("create daemon runner: daemon shutdown coordinator is required")
	}
	if lifecycle == nil {
		return nil, errors.New("create daemon runner: project lifecycle coordinator is required")
	}
	recovery := func(ctx context.Context) error {
		if err := coordinator.Recover(ctx); err != nil {
			return err
		}
		return lifecycle.Recover(ctx)
	}
	return daemon.NewRunner(daemon.RunnerConfig{
		Server:              server,
		Readiness:           readiness,
		Recovery:            recovery,
		Runtime:             newProjectLifecycleRuntime(runtimeController, lifecycle),
		ShutdownRequested:   shutdown.Requested(),
		RuntimeCloseTimeout: daemonRuntimeCloseTimeout(runtimeController),
	})
}

// daemonRuntimeCloseTimeout leaves scheduling and certificate-store closure outside the controller's child budget.
func daemonRuntimeCloseTimeout(runtimeController *harbordruntime.Controller) time.Duration {
	return runtimeController.ShutdownTimeout() + runtimeCloseCoordinationMargin
}
