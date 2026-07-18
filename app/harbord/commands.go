// App-owned command registration. EDIT THIS FILE.
// Add command fields here, or use `forj make:command`.

package harbordapp

import (
	"github.com/goforj/harbor/internal/cmd"
)

// Commands wires application-specific commands into the CLI.
// Keep command implementations in the package that owns the workflow.
type Commands struct {
	ResourcesCmd  cmd.ResourcesCmd  `cmd:""`
	AboutCmd      cmd.AboutCmd      `cmd:""`
	HelloWorldCmd cmd.HelloWorldCmd `cmd:""`
}

// NewCommands creates a new Commands instance with the given commands.
func NewCommands(
	resourcesCmd *cmd.ResourcesCmd,
	aboutCmd *cmd.AboutCmd,
	helloWorldCmd *cmd.HelloWorldCmd,
) *Commands {
	return &Commands{
		ResourcesCmd:  *resourcesCmd,
		AboutCmd:      *aboutCmd,
		HelloWorldCmd: *helloWorldCmd,
	}
}
