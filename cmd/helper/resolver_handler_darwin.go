//go:build darwin

package main

import (
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/resolverhandler"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// newPlatformResolverHandler binds Darwin helper execution to the fixed resolver-file backend.
func newPlatformResolverHandler() helper.ResolverHandler {
	return resolverhandler.New(resolver.New())
}
