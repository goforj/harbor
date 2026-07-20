//go:build !darwin && !linux && !windows

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformResolverHandler keeps resolver effects unavailable outside implemented platform backends.
func openPlatformResolverHandler() (closingResolverHandler, error) {
	return helper.UnavailableResolverHandler{}, nil
}
