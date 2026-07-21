// App-owned Wire injector. EDIT THIS FILE.
// Add application command providers here, or use `forj make:command`.

package wire

import (
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/wire"
)

// appCommandSet is a wire set that provides app-owned command providers.
var appCommandSet = wire.NewSet(
	cmd.NewHelloWorldCmd,
	cmd.NewResourcesCmd,
	cmd.NewDaemonClient,
	cmd.NewAddCmd,
	cmd.NewRemoveCmd,
	cmd.NewStartCmd,
	cmd.NewStopCmd,
	cmd.NewRestartCmd,
	cmd.NewOpenCmd,
	cmd.NewProjectStatusCmd,
	provideNetworkSetupApprovalRunner,
	cmd.NewSetupCmd,
	cmd.NewDaemonStatusCmd,
	cmd.NewDaemonStopCmd,
	cmd.NewDaemonSnapshotCmd,
	cmd.NewDaemonCmd,
)
