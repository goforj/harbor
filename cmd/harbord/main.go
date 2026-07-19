package main

import (
	"github.com/goforj/harbor/app/harbord"
	"github.com/goforj/harbor/app/harbord/wire"
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/console"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"

	"os"
)

func main() {
	developmentEnvironment := projectprocess.CaptureEnvironment()
	args := cmd.EffectiveLaunchArgs(os.Args[1:], false)
	cmd.ApplyLaunchApp("harbord")

	if err := cmd.LoadEnv(); err != nil {
		console.Fatalf("loading env: %v", err)
	}

	handled, err := cmd.DispatchPrebootCommand(args, &harbordapp.RootCmd{})
	if err != nil {
		console.Fatalf("%v", err)
	} else if handled {
		return
	}
	if _, err := state.ConfigureDatabase(); err != nil {
		console.Fatalf("configuring state database: %v", err)
	}
	application, err := wire.InitializeApplication(developmentEnvironment)
	if err != nil {
		console.Fatalf("initializing application: %v", err)
	}

	if err := application.Run(nil, args); err != nil {
		console.Fatalf("%v", err)
	}
}
