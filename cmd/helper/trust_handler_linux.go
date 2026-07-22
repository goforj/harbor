//go:build linux

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformTrustHandler keeps Linux trust effects unavailable until the tested distribution store adapter is installed.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}

// openPlatformAdministratorTrustHandler keeps administrator trust effects unavailable until the tested distribution store adapter is installed.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}
