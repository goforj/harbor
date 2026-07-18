// App-owned command registration. EDIT THIS FILE.
// Add command fields here, or use `forj make:command`.

package harbordapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/daemon"
)

// foregroundRunner keeps command policy independent from operating-system daemon boundaries in tests.
type foregroundRunner interface {
	Run(context.Context) error
}

// Commands wires application-specific commands into the CLI.
// Keep command implementations in the package that owns the workflow.
type Commands struct {
	Foreground    bool              `help:"Run the Harbor daemon in the foreground"`
	ResourcesCmd  cmd.ResourcesCmd  `cmd:""`
	AboutCmd      cmd.AboutCmd      `cmd:""`
	HelloWorldCmd cmd.HelloWorldCmd `cmd:""`

	runner foregroundRunner
}

// NewCommands creates a new Commands instance with the given commands.
func NewCommands(
	resourcesCmd *cmd.ResourcesCmd,
	aboutCmd *cmd.AboutCmd,
	helloWorldCmd *cmd.HelloWorldCmd,
	runner *daemon.Runner,
) *Commands {
	return newCommands(resourcesCmd, aboutCmd, helloWorldCmd, runner)
}

// newCommands accepts the narrow daemon surface so command tests never acquire process authority.
func newCommands(
	resourcesCmd *cmd.ResourcesCmd,
	aboutCmd *cmd.AboutCmd,
	helloWorldCmd *cmd.HelloWorldCmd,
	runner foregroundRunner,
) *Commands {
	return &Commands{
		ResourcesCmd:  *resourcesCmd,
		AboutCmd:      *aboutCmd,
		HelloWorldCmd: *helloWorldCmd,
		runner:        runner,
	}
}

// Validate reserves foreground mode for the command root so maintenance commands cannot start the daemon accidentally.
func (commands *Commands) Validate(parseContext *kong.Context) error {
	if !commands.Foreground {
		return nil
	}
	if parseContext == nil {
		return errors.New("validate harbord foreground mode: parse context is required")
	}
	if command := parseContext.Command(); command != "" {
		return fmt.Errorf("--foreground cannot be combined with subcommand %q", command)
	}

	return nil
}

// Run holds the foreground process only when the explicit root flag selected daemon mode.
func (commands *Commands) Run(ctx context.Context) error {
	if !commands.Foreground {
		return nil
	}

	return commands.runner.Run(ctx)
}
