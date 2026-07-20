//go:build !darwin

package main

import "github.com/goforj/harbor/internal/helper"

// newPlatformResolverHandler keeps resolver effects unavailable outside the implemented Darwin backend.
func newPlatformResolverHandler() helper.ResolverHandler {
	return helper.UnavailableResolverHandler{}
}
