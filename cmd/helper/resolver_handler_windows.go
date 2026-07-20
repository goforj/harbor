//go:build windows

package main

import (
	"github.com/goforj/harbor/internal/helper/resolverhandler"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// openPlatformResolverHandler binds Windows helper execution to the fixed NRPT PowerShell authority.
func openPlatformResolverHandler() (closingResolverHandler, error) {
	return resolverhandler.OpenDefault(resolver.New())
}
