//go:build darwin

package main

import (
	"github.com/goforj/harbor/internal/helper/lowporthandler"
	"github.com/goforj/harbor/internal/platform/lowport"
)

// openPlatformLowPortHandler binds Darwin helper execution to the fixed Harbor launchd service contract.
func openPlatformLowPortHandler() (closingLowPortHandler, error) {
	adapter, err := lowport.New()
	if err != nil {
		return nil, err
	}
	return lowporthandler.New(adapter), nil
}
