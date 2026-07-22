//go:build !darwin && !linux && !windows

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformTrustHandler keeps trust effects unavailable outside the reviewed platform set.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}

// openPlatformAdministratorTrustHandler keeps administrator trust effects unavailable outside the reviewed platform set.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}
