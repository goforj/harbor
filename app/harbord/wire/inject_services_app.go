// App-owned Wire injector. EDIT THIS FILE.
// Add app-wide application service providers here when they do not belong to a narrower injector.

package wire

import (
	"github.com/goforj/wire"

	"github.com/goforj/harbor/app/harbord"
	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/runtime"
	"github.com/goforj/harbor/internal/state"
)

// appSet is a wire set that provides application-level services and dependencies.
var appSet = wire.NewSet(
	harbordapp.NewLifecycleRegistry,
	daemon.NewShutdown,
	runtime.NewTimeouts,
	state.NewMutationCoordinator,
	state.NewOperationJournal,
	state.NewHelperApprovalPlanSource,
	state.NewNetworkSetupPlanSource,
	state.NewNetworkResolverSetupPlanSource,
	state.NewMachineOwnershipProjectionSource,
	state.NewStore,
	harbordruntime.NewController,
	provideProjectProcessSupervisor,
	reconcile.NewProjectLifecycleCoordinator,
	provideProjectUnregisterCoordinator,
	provideNetworkSetupCoordinator,
	provideNetworkResolverObserver,
	provideNetworkResolverSetupCoordinator,
	provideNetworkDataPlaneSetupCapability,
	provideNetworkReleaseCapability,
	authority.NewAuthority,
	provideControlServer,
	provideHarbordReadiness,
	provideDaemonRunner,
	wire.Bind(new(daemon.ConnectionServer), new(*control.Server)),
	wire.Bind(new(reconcile.ProjectRouteReconciler), new(*harbordruntime.Controller)),
)
