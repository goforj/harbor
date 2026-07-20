//go:build darwin

package main

import (
	"github.com/goforj/harbor/internal/helper/resolverhandler"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// openPlatformResolverHandler binds Darwin helper execution to fixed protected ownership and resolver paths.
func openPlatformResolverHandler() (closingResolverHandler, error) {
	return resolverhandler.OpenDefault(resolver.New())
}
