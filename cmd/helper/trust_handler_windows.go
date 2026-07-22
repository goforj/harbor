//go:build windows

package main

import "github.com/goforj/harbor/internal/helper"

// openPlatformTrustHandler keeps Windows trust effects unavailable until the tested CurrentUser\\Root adapter is installed.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	return helper.UnavailableTrustHandler{}, nil
}
