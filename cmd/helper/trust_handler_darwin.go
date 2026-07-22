//go:build darwin

package main

import (
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/trusthandler"
	"github.com/goforj/harbor/internal/platform/trust"
)

// openPlatformTrustHandler binds Darwin trust operations to the current user's Security.framework store.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	adapter, err := trust.New()
	if err != nil {
		return helper.UnavailableTrustHandler{}, nil
	}
	return trusthandler.New(adapter), nil
}

// openPlatformAdministratorTrustHandler binds the administrator-selected dispatch seam to Darwin's reviewed trust adapter.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	return openPlatformTrustHandler()
}
