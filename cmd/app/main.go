package main

import (
	"github.com/goforj/harbor/app"
	"github.com/goforj/harbor/app/wire"
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/console"

	"os"
)

func main() {
	args := cmd.EffectiveLaunchArgs(os.Args[1:], false)
	cmd.ApplyLaunchApp("app")

	if err := cmd.LoadEnv(); err != nil {
		console.Fatalf("loading env: %v", err)
	}

	handled, err := cmd.DispatchPrebootCommand(args, &app.RootCmd{})
	if err != nil {
		console.Fatalf("%v", err)
	} else if handled {
		return
	}
	application, err := wire.InitializeApplication()
	if err != nil {
		console.Fatalf("initializing application: %v", err)
	}

	if err := application.Run(nil, args); err != nil {
		console.Fatalf("%v", err)
	}
}
