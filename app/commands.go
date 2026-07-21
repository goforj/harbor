// App-owned command registration. EDIT THIS FILE.
// Add command fields here, or use `forj make:command`.

package app

import (
	"github.com/goforj/harbor/internal/cmd"
)

// Commands wires application-specific commands into the CLI.
// Keep command implementations in the package that owns the workflow.
type Commands struct {
	AddCmd        cmd.AddCmd           `cmd:""`
	RemoveCmd     cmd.RemoveCmd        `cmd:""`
	StartCmd      cmd.StartCmd         `cmd:""`
	StopCmd       cmd.StopCmd          `cmd:""`
	RestartCmd    cmd.RestartCmd       `cmd:""`
	OpenCmd       cmd.OpenCmd          `cmd:""`
	LogsCmd       cmd.LogsCmd          `cmd:""`
	StatusCmd     cmd.ProjectStatusCmd `cmd:""`
	SetupCmd      cmd.SetupCmd         `cmd:""`
	ResourcesCmd  cmd.ResourcesCmd     `cmd:""`
	AboutCmd      cmd.AboutCmd         `cmd:""`
	HelloWorldCmd cmd.HelloWorldCmd    `cmd:""`
	DaemonCmd     cmd.DaemonCmd        `cmd:""`
}

// NewCommands creates a new Commands instance with the given commands.
func NewCommands(
	addCmd *cmd.AddCmd,
	removeCmd *cmd.RemoveCmd,
	startCmd *cmd.StartCmd,
	stopCmd *cmd.StopCmd,
	restartCmd *cmd.RestartCmd,
	openCmd *cmd.OpenCmd,
	logsCmd *cmd.LogsCmd,
	statusCmd *cmd.ProjectStatusCmd,
	setupCmd *cmd.SetupCmd,
	resourcesCmd *cmd.ResourcesCmd,
	aboutCmd *cmd.AboutCmd,
	helloWorldCmd *cmd.HelloWorldCmd,
	daemonCmd *cmd.DaemonCmd,
) *Commands {
	return &Commands{
		AddCmd:        *addCmd,
		RemoveCmd:     *removeCmd,
		StartCmd:      *startCmd,
		StopCmd:       *stopCmd,
		RestartCmd:    *restartCmd,
		OpenCmd:       *openCmd,
		LogsCmd:       *logsCmd,
		StatusCmd:     *statusCmd,
		SetupCmd:      *setupCmd,
		ResourcesCmd:  *resourcesCmd,
		AboutCmd:      *aboutCmd,
		HelloWorldCmd: *helloWorldCmd,
		DaemonCmd:     *daemonCmd,
	}
}
