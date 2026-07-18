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
	cmd.NewDaemonStatusCmd,
	cmd.NewDaemonSnapshotCmd,
	cmd.NewDaemonCmd,
)
