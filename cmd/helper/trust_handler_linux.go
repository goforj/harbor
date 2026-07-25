//go:build linux

package main

import (
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/trusthandler"
	"github.com/goforj/harbor/internal/platform/trust"
)

// openPlatformTrustHandler binds Linux trust operations to Ubuntu's fixed system CA source and bundle refresh.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	adapter, err := trust.New()
	if err != nil {
		return helper.UnavailableTrustHandler{}, nil
	}
	return trusthandler.New(adapter), nil
}

// openPlatformAdministratorTrustHandler shares the same machine-global Ubuntu trust boundary.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	return openPlatformTrustHandler()
}
