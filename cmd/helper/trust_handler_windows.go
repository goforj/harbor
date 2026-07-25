//go:build windows

package main

import (
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/trusthandler"
	"github.com/goforj/harbor/internal/platform/trust"
)

// openPlatformTrustHandler binds Windows trust operations to the interactive account's CurrentUser Root store.
func openPlatformTrustHandler() (closingTrustHandler, error) {
	adapter, err := trust.New()
	if err != nil {
		return helper.UnavailableTrustHandler{}, nil
	}
	return trusthandler.New(adapter), nil
}

// openPlatformAdministratorTrustHandler binds elevated Windows trust operations to the machine Root registry store.
func openPlatformAdministratorTrustHandler() (closingTrustHandler, error) {
	adapter, err := trust.NewMachine()
	if err != nil {
		return helper.UnavailableTrustHandler{}, nil
	}
	return trusthandler.New(adapter), nil
}
