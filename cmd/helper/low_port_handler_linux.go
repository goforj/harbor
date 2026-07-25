//go:build linux

package main

import (
	"github.com/goforj/harbor/internal/helper/lowporthandler"
	"github.com/goforj/harbor/internal/platform/lowport"
)

// openPlatformLowPortHandler binds Linux helper execution to Harbor's fixed Ubuntu nftables contract.
func openPlatformLowPortHandler() (closingLowPortHandler, error) {
	adapter, err := lowport.New()
	if err != nil {
		return nil, err
	}
	return lowporthandler.New(adapter), nil
}
