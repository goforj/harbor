// App-owned Wire injector. EDIT THIS FILE.
// Add app-wide application service providers here when they do not belong to a narrower injector.

package wire

import (
	"github.com/goforj/wire"

	"github.com/goforj/harbor/app/harbord"
	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/runtime"
	"github.com/goforj/harbor/internal/state"
)

// appSet is a wire set that provides application-level services and dependencies.
var appSet = wire.NewSet(
	harbordapp.NewLifecycleRegistry,
	runtime.NewTimeouts,
	state.NewMutationCoordinator,
	state.NewOperationJournal,
	state.NewStore,
	authority.NewAuthority,
	provideControlServer,
	provideHarbordReadiness,
	provideDaemonRunner,
	wire.Bind(new(daemon.ConnectionServer), new(*control.Server)),
)
