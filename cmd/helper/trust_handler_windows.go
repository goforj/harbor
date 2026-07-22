//go:build windows

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformTrustHandler keeps Windows trust effects unavailable until the tested CurrentUser\\Root adapter is installed.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}

// openPlatformAdministratorTrustHandler keeps administrator trust effects unavailable until a reviewed administrator store adapter is installed.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}
