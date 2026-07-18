// App-owned command registration. EDIT THIS FILE.
// Add command fields here, or use `forj make:command`.

package app

import (
	"github.com/goforj/harbor/internal/cmd"
)

// Commands wires application-specific commands into the CLI.
// Keep command implementations in the package that owns the workflow.
type Commands struct {
	AddCmd        cmd.AddCmd        `cmd:""`
	ResourcesCmd  cmd.ResourcesCmd  `cmd:""`
	AboutCmd      cmd.AboutCmd      `cmd:""`
	HelloWorldCmd cmd.HelloWorldCmd `cmd:""`
	DaemonCmd     cmd.DaemonCmd     `cmd:""`
}

// NewCommands creates a new Commands instance with the given commands.
func NewCommands(
	addCmd *cmd.AddCmd,
	resourcesCmd *cmd.ResourcesCmd,
	aboutCmd *cmd.AboutCmd,
	helloWorldCmd *cmd.HelloWorldCmd,
	daemonCmd *cmd.DaemonCmd,
) *Commands {
	return &Commands{
		AddCmd:        *addCmd,
		ResourcesCmd:  *resourcesCmd,
		AboutCmd:      *aboutCmd,
		HelloWorldCmd: *helloWorldCmd,
		DaemonCmd:     *daemonCmd,
	}
}
