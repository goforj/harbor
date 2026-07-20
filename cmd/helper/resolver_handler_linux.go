//go:build linux

package main

import (
	"github.com/goforj/harbor/internal/helper/resolverhandler"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// openPlatformResolverHandler binds Linux helper execution to fixed protected ownership and systemd-resolved paths.
func openPlatformResolverHandler() (closingResolverHandler, error) {
	return resolverhandler.OpenDefault(resolver.New())
}
