//go:build wireinject
// +build wireinject

// GoForj Wire harness. Edit this file when customizing root assembly.
// Re-rendering can overwrite this file; review local changes before rendering over them.

package wire

import (
	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/wire"
)

// InitializeApplication initializes the application by providing all the dependencies.
func InitializeApplication(environment projectprocess.Environment) (App, error) {
	wire.Build(
		appSet,
		cmdSet,
		databaseSet,
		logger.ProvideAppLogger,
		managerSet,
		NewApplication,
	)

	return App{}, nil
}
