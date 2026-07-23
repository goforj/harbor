// App-owned daemon assembly. EDIT THIS FILE.

package wire

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforjruntime"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/helper/ticketkey"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/migrations"
)

// runtimeCloseCoordinationMargin reserves outer-runner time for scheduling and certificate-store cleanup.
const runtimeCloseCoordinationMargin = 5 * time.Second

const (
	sourceDevelopmentAppEnvironment           = "FORJ_APP"
	sourceDevelopmentCommandPrefixEnvironment = "FORJ_COMMAND_PREFIX"
	sourceDevelopmentApp                      = "harbord"
	sourceDevelopmentCommandPrefix            = "forj harbord"
	sourceDevelopmentHandoffTimeout           = 2 * time.Second
)

// projectUnregisterIssuerOpener isolates default issuer stores while retaining daemon-owned ownership authority.
type projectUnregisterIssuerOpener func(ticketissuer.PlanSource, *state.MachineOwnershipProjectionSource) (reconcile.TicketIssuer, error)

// networkSetupSigningKeyOpener retains first-run key creation behind an approved setup intent.
type networkSetupSigningKeyOpener func() (reconcile.SigningKeyStore, error)

// networkSetupPoolIssuerOpener retains capability-store access behind an explicit approval preparation.
type networkSetupPoolIssuerOpener func(ticketissuer.PoolPlanSource) (reconcile.PoolIssuer, error)

// networkResolverSetupIssuerOpener retains resolver capability stores behind an explicit approval preparation.
type networkResolverSetupIssuerOpener func(
	*state.NetworkResolverSetupPlanSource,
	*state.MachineOwnershipProjectionSource,
	reconcile.NetworkResolverSetupResolverObserver,
) (reconcile.NetworkResolverSetupIssuer, error)

// projectLifecycleRuntime joins managed project processes before releasing shared network infrastructure.
type projectLifecycleRuntime struct {
	daemon.Runtime
	lifecycle           projectLifecycleAuthority
	release             networkReleaseArmedReader
	networkRelease      networkReleaseCapability
	postRuntimeRecovery func(context.Context) error
	done                chan struct{}
	doneOnce            sync.Once
	mutex               sync.Mutex
	err                 error
}

// projectLifecycleAuthority is the process lifetime composed into daemon runtime ownership.
type projectLifecycleAuthority interface {
	Resume(context.Context) error
	Close(context.Context) error
	Done() <-chan struct{}
	Err() error
}

// networkReleaseArmedReader reports whether startup is retaining only the release control anchor.
type networkReleaseArmedReader interface {
	NetworkReleaseArmed() bool
}

// networkReleaseRecoveryAuthority discovers and arms a durable global network release before startup recovery.
type networkReleaseRecoveryAuthority interface {
	ArmNetworkRelease(context.Context) (bool, error)
}

// projectUnregisterRecoveryAuthority limits daemon startup to interrupted project-removal reconciliation.
type projectUnregisterRecoveryAuthority interface {
	// Recover settles interrupted project-removal authority before other startup work.
	Recover(context.Context) error
}

// projectLifecycleRecoveryAuthority settles durable process lifecycle work before runtime startup.
type projectLifecycleRecoveryAuthority interface {
	// Recover settles durable process-lifecycle work without dispatching queued starts.
	Recover(context.Context) error
}

// projectLifecycleEndpointReconciler backfills full-stage endpoints after network runtime startup.
type projectLifecycleEndpointReconciler interface {
	// ReconcileFullStageDefaultHTTPEndpoints backfills publishable routes after process recovery is safe.
	ReconcileFullStageDefaultHTTPEndpoints(context.Context) (state.NetworkRecord, error)
}

// networkDataPlaneSetupRecoveryAuthority resumes only a durably staged trusted-ingress activation.
type networkDataPlaneSetupRecoveryAuthority interface {
	Recover(context.Context, domain.OperationID) (state.OperationRecord, error)
}

// activeNetworkDataPlaneSetupOperationReader reads the one trusted-ingress setup operation eligible for recovery.
type activeNetworkDataPlaneSetupOperationReader interface {
	ActiveNetworkDataPlaneSetupOperation(context.Context) (state.NetworkDataPlaneSetupActiveOperation, bool, error)
}

// networkRuntimeActivator publishes a durable full network revision into the running network runtime.
type networkRuntimeActivator interface {
	ActivateNetwork(context.Context, domain.Sequence) error
}

// networkDataPlaneSetupCapability retains optional control and restart-recovery authority together.
type networkDataPlaneSetupCapability struct {
	authority *authority.NetworkDataPlaneSetupAuthority
	recovery  networkDataPlaneSetupRecoveryAuthority
}

// networkReleaseRecoveryCoordinator resumes a staged global network release after the cold anchor starts.
type networkReleaseRecoveryCoordinator interface {
	Recover(context.Context) error
}

// networkReleaseCapability retains optional control and restart-recovery authority together.
type networkReleaseCapability struct {
	authority *authority.NetworkReleaseAuthority
	recovery  networkReleaseRecoveryCoordinator
}

// networkResolverPolicyMigrationCapability retains optional legacy resolver-policy retirement authority.
type networkResolverPolicyMigrationCapability struct {
	authority *authority.NetworkResolverPolicyMigrationAuthority
}

// newProjectLifecycleRuntime creates one completion signal spanning both network and process authority.
func newProjectLifecycleRuntime(
	runtime daemon.Runtime,
	lifecycle projectLifecycleAuthority,
	release networkReleaseArmedReader,
	networkRelease networkReleaseCapability,
	postRuntimeRecovery func(context.Context) error,
) *projectLifecycleRuntime {
	if release == nil {
		panic("wire.newProjectLifecycleRuntime requires network release state")
	}
	if postRuntimeRecovery == nil {
		panic("wire.newProjectLifecycleRuntime requires post-runtime recovery")
	}
	return &projectLifecycleRuntime{
		Runtime:             runtime,
		lifecycle:           lifecycle,
		release:             release,
		networkRelease:      networkRelease,
		postRuntimeRecovery: postRuntimeRecovery,
		done:                make(chan struct{}),
	}
}

// Start publishes network authority before dispatching process launches retained during durable recovery.
func (runtime *projectLifecycleRuntime) Start(ctx context.Context) error {
	if err := runtime.Runtime.Start(ctx); err != nil {
		closeErr := runtime.lifecycle.Close(context.Background())
		runtime.finish(errors.Join(err, closeErr))
		return errors.Join(err, closeErr)
	}
	if runtime.release.NetworkReleaseArmed() {
		if runtime.networkRelease.recovery == nil {
			err := errors.New("recover global network release: platform recovery authority is unavailable")
			closeErr := runtime.closeOwned(context.Background())
			joined := errors.Join(err, closeErr)
			runtime.finish(joined)
			return joined
		}
		if err := runtime.networkRelease.recovery.Recover(ctx); err != nil {
			closeErr := runtime.closeOwned(context.Background())
			joined := errors.Join(err, closeErr)
			runtime.finish(joined)
			return joined
		}
		go runtime.joinUnexpectedNetworkStop()
		return nil
	}
	if err := runtime.postRuntimeRecovery(ctx); err != nil {
		closeErr := runtime.closeOwned(context.Background())
		joined := errors.Join(err, closeErr)
		runtime.finish(joined)
		return joined
	}
	if err := runtime.lifecycle.Resume(ctx); err != nil {
		closeErr := runtime.closeOwned(context.Background())
		joined := errors.Join(err, closeErr)
		runtime.finish(joined)
		return joined
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
	err := runtime.closeOwned(ctx)
	if signalClosed(runtime.lifecycle.Done()) && signalClosed(runtime.Runtime.Done()) {
		runtime.finish(err)
	}
	return err
}

// closeOwned releases project processes before the network routes on which their readiness depends.
func (runtime *projectLifecycleRuntime) closeOwned(ctx context.Context) error {
	lifecycleErr := runtime.lifecycle.Close(ctx)
	networkErr := runtime.Runtime.Close(ctx)
	return errors.Join(lifecycleErr, networkErr)
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
func provideControlServer(
	controlAuthority *authority.Authority,
	networkDataPlaneSetup networkDataPlaneSetupCapability,
	networkRelease networkReleaseCapability,
	networkResolverPolicyMigration networkResolverPolicyMigrationCapability,
	shutdown *daemon.Shutdown,
	appLogger *logger.AppLogger,
) (*control.Server, error) {
	if shutdown == nil {
		return nil, errors.New("create control server: daemon shutdown coordinator is required")
	}
	if appLogger == nil {
		return nil, errors.New("create control server: application logger is required")
	}
	return control.NewServer(control.ServerConfig{
		Authority:                               controlAuthority,
		RequestShutdown:                         shutdown.Request,
		ObserveError:                            newControlErrorObserver(appLogger),
		ManagedAuthority:                        controlAuthority,
		NetworkDataPlaneSetupAuthority:          networkDataPlaneSetup.authority,
		NetworkReleaseAuthority:                 networkRelease.authority,
		NetworkReleaseApprovalAuthority:         networkRelease.authority,
		NetworkResolverPolicyMigrationAuthority: networkResolverPolicyMigration.authority,
		ProjectEnvironmentAuthority:             controlAuthority,
	})
}

// newControlErrorObserver retains substantive redacted causes while ignoring cancellation caused by connection retirement.
func newControlErrorObserver(appLogger *logger.AppLogger) control.ErrorObserver {
	return func(caller control.Caller, method string, err error) {
		if controlErrorIsCancellationOnly(err) {
			return
		}
		event := appLogger.Error()
		message := "Harbor control request failed"
		var prerequisite *ticketissuer.PoolPrerequisiteMissingError
		if errors.As(err, &prerequisite) {
			event = appLogger.Info()
			message = "Harbor control request requires privileged setup"
		}
		event.
			Err(err).
			Str("control_method", method).
			Str("peer_role", string(caller.Session.Role)).
			Str("peer_user_id", caller.Transport.UserID).
			Uint64("peer_process_id", uint64(caller.Transport.ProcessID)).
			Msg(message)
	}
}

// controlErrorIsCancellationOnly suppresses teardown fan-out without hiding a real failure joined to cancellation.
func controlErrorIsCancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	type joinedError interface {
		Unwrap() []error
	}
	if joined, ok := err.(joinedError); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !controlErrorIsCancellationOnly(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return controlErrorIsCancellationOnly(cause)
	}
	return err == context.Canceled
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
	options := projectprocess.Options{Environment: environment}
	// The broker is an additive artifact during development and packaging. Until it is installed beside
	// harbord, retaining direct pipes keeps ordinary project lifecycle behavior available.
	if launcher, err := projectprocess.NewSiblingOutputBrokerProcessLauncher(); err == nil {
		options.OutputBrokerLauncher = launcher
	}
	return projectprocess.New(options)
}

// provideGoForjProjectRuntime adapts the ordinary GoForj process supervisor to Harbor's project-neutral lifecycle boundary.
func provideGoForjProjectRuntime(supervisor *projectprocess.Supervisor) *goforjruntime.Runtime {
	return goforjruntime.New(supervisor)
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

// provideNetworkSetupCoordinator assembles machine setup without opening user stores or scanning host addresses.
func provideNetworkSetupCoordinator(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.NetworkSetupPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
) (*reconcile.NetworkSetupCoordinator, error) {
	return provideNetworkSetupCoordinatorWithOpeners(
		store,
		operations,
		plans,
		ownership,
		func() (reconcile.SigningKeyStore, error) {
			return ticketkey.OpenDefault()
		},
		func(plans ticketissuer.PoolPlanSource) (reconcile.PoolIssuer, error) {
			return ticketissuer.OpenDefaultPoolService(plans)
		},
	)
}

// provideNetworkSetupCoordinatorWithOpeners keeps every filesystem-backed authority lazy and testable.
func provideNetworkSetupCoordinatorWithOpeners(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.NetworkSetupPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
	openKeys networkSetupSigningKeyOpener,
	openIssuer networkSetupPoolIssuerOpener,
) (*reconcile.NetworkSetupCoordinator, error) {
	if store == nil {
		return nil, errors.New("create network setup coordinator: state store is required")
	}
	if operations == nil {
		return nil, errors.New("create network setup coordinator: operation journal is required")
	}
	if plans == nil {
		return nil, errors.New("create network setup coordinator: setup plan source is required")
	}
	if ownership == nil {
		return nil, errors.New("create network setup coordinator: confirmed ownership projection source is required")
	}
	if openKeys == nil {
		return nil, errors.New("create network setup coordinator: signing-key opener is required")
	}
	if openIssuer == nil {
		return nil, errors.New("create network setup coordinator: pool issuer opener is required")
	}
	keys := func() (reconcile.SigningKeyStore, error) {
		return openKeys()
	}
	issuers := func() (reconcile.PoolIssuer, error) {
		return openIssuer(plans)
	}
	return reconcile.NewNetworkSetupCoordinator(
		operations,
		plans,
		store,
		keys,
		ticketissuer.NewDefaultPoolSelector(),
		issuers,
		ownership,
		loopback.New(),
		helper.SystemClock{},
	), nil
}

// provideNetworkResolverSetupCoordinator assembles policy-bound resolver setup without opening capability stores.
func provideNetworkResolverSetupCoordinator(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.NetworkResolverSetupPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
	resolverObserver reconcile.NetworkResolverSetupResolverObserver,
) (*reconcile.NetworkResolverSetupCoordinator, error) {
	platform, err := reconcile.CurrentNetworkResolverSetupPlatform()
	if err != nil {
		return nil, fmt.Errorf("create network resolver setup coordinator: %w", err)
	}
	return provideNetworkResolverSetupCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		ownership,
		runtimeController,
		resolverObserver,
		platform,
		func(
			plans *state.NetworkResolverSetupPlanSource,
			ownership *state.MachineOwnershipProjectionSource,
			resolverObserver reconcile.NetworkResolverSetupResolverObserver,
		) (reconcile.NetworkResolverSetupIssuer, error) {
			return ticketissuer.OpenDefaultResolverService(plans, ownership, resolverObserver)
		},
	)
}

// provideNetworkResolverSetupCoordinatorWithIssuerOpener keeps capability stores dormant until a reviewed Prepare call.
func provideNetworkResolverSetupCoordinatorWithIssuerOpener(
	store *state.Store,
	operations *state.OperationJournal,
	plans *state.NetworkResolverSetupPlanSource,
	ownership *state.MachineOwnershipProjectionSource,
	runtimeController *harbordruntime.Controller,
	resolverObserver reconcile.NetworkResolverSetupResolverObserver,
	platform networkplan.Platform,
	openIssuer networkResolverSetupIssuerOpener,
) (*reconcile.NetworkResolverSetupCoordinator, error) {
	issuers := func() (reconcile.NetworkResolverSetupIssuer, error) {
		return openIssuer(plans, ownership, resolverObserver)
	}
	return reconcile.NewNetworkResolverSetupCoordinator(
		operations,
		store,
		plans,
		store,
		runtimeController,
		issuers,
		ownership,
		resolverObserver,
		platform,
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
	operations *state.OperationJournal,
	networkDataPlaneSetup networkDataPlaneSetupCapability,
	networkRelease networkReleaseCapability,
	shutdown *daemon.Shutdown,
	environment projectprocess.Environment,
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
	if operations == nil {
		return nil, errors.New("create daemon runner: operation journal is required")
	}
	recovery := func(ctx context.Context) error {
		return recoverDaemonState(ctx, runtimeController, coordinator, lifecycle)
	}
	postRuntimeRecovery := func(ctx context.Context) error {
		return recoverDaemonRuntimeState(ctx, operations, networkDataPlaneSetup.recovery, lifecycle, runtimeController)
	}
	return daemon.NewRunner(daemon.RunnerConfig{
		Server:              server,
		Readiness:           readiness,
		Recovery:            recovery,
		Runtime:             newProjectLifecycleRuntime(runtimeController, lifecycle, runtimeController, networkRelease, postRuntimeRecovery),
		ShutdownRequested:   shutdown.Requested(),
		RuntimeCloseTimeout: daemonRuntimeCloseTimeout(runtimeController),
		AuthorityContention: sourceDevelopmentDaemonHandoff(environment),
	})
}

// sourceDevelopmentDaemonHandoff enables same-user graceful handoff for Harbor's structured GoForj app runtime.
func sourceDevelopmentDaemonHandoff(environment projectprocess.Environment) daemon.AuthorityContentionHandler {
	if !sourceDevelopmentEnvironmentEnabled(environment) {
		return nil
	}
	client := cmd.NewDaemonClient()
	return func(ctx context.Context) {
		handoffContext, cancel := context.WithTimeout(ctx, sourceDevelopmentHandoffTimeout)
		defer cancel()
		_ = client.Stop(handoffContext)
	}
}

// sourceDevelopmentEnvironmentEnabled reads the pre-dotenv process snapshot so dotenv files cannot opt an ordinary launch into source-only behavior.
func sourceDevelopmentEnvironmentEnabled(environment projectprocess.Environment) bool {
	developmentApp := false
	developmentCommandPrefix := false
	for _, entry := range environment {
		switch entry {
		case sourceDevelopmentAppEnvironment + "=" + sourceDevelopmentApp:
			developmentApp = true
		case sourceDevelopmentCommandPrefixEnvironment + "=" + sourceDevelopmentCommandPrefix:
			developmentCommandPrefix = true
		}
	}
	return developmentApp && developmentCommandPrefix
}

// recoverDaemonState arms durable global release recovery before ordinary project recovery.
func recoverDaemonState(
	ctx context.Context,
	release networkReleaseRecoveryAuthority,
	unregister projectUnregisterRecoveryAuthority,
	lifecycle projectLifecycleRecoveryAuthority,
) error {
	armed, err := release.ArmNetworkRelease(ctx)
	if err != nil {
		return fmt.Errorf("arm global network release during daemon recovery: %w", err)
	}
	if armed {
		return nil
	}
	if err := unregister.Recover(ctx); err != nil {
		return err
	}
	if err := lifecycle.Recover(ctx); err != nil {
		return err
	}
	return nil
}

// recoverDaemonRuntimeState resumes staged trusted-ingress activation and publishes post-start endpoint changes.
func recoverDaemonRuntimeState(
	ctx context.Context,
	operations activeNetworkDataPlaneSetupOperationReader,
	setup networkDataPlaneSetupRecoveryAuthority,
	lifecycle projectLifecycleEndpointReconciler,
	runtime networkRuntimeActivator,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	active, found, err := operations.ActiveNetworkDataPlaneSetupOperation(ctx)
	if err != nil {
		return fmt.Errorf("read active network data-plane setup during daemon recovery: %w", err)
	}
	if found && active.Phase == state.NetworkDataPlaneSetupPhaseActivation {
		if setup == nil {
			// Non-Darwin daemon startup remains available until an interrupted Darwin activation needs its native authority.
			return errors.New("recover active network data-plane setup: platform recovery authority is unavailable")
		}
		if _, err := setup.Recover(ctx, active.Operation.Operation.ID); err != nil {
			return fmt.Errorf("recover active network data-plane setup: %w", err)
		}
	}
	final, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(ctx)
	if err != nil {
		return fmt.Errorf("reconcile full-stage default HTTP endpoints during daemon recovery: %w", err)
	}
	if final.Stage == state.NetworkStageFull {
		if err := runtime.ActivateNetwork(ctx, final.Revision); err != nil {
			return fmt.Errorf("activate full network after daemon recovery: %w", err)
		}
	}
	return nil
}

// daemonRuntimeCloseTimeout leaves scheduling and certificate-store closure outside the controller's child budget.
func daemonRuntimeCloseTimeout(runtimeController *harbordruntime.Controller) time.Duration {
	return runtimeController.ShutdownTimeout() + runtimeCloseCoordinationMargin
}
