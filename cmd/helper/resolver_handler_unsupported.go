//go:build !darwin

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformResolverHandler keeps resolver effects unavailable outside the implemented Darwin backend.
func openPlatformResolverHandler() (closingResolverHandler, error) {
	return helper.UnavailableResolverHandler{}, nil
}
