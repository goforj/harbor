//go:build linux

package main

import (
	"testing"

	"github.com/goforj/harbor/internal/helper/lowporthandler"
)

// TestLinuxLowPortAdapterComposition proves production Linux wiring opens the reviewed nftables handler.
func TestLinuxLowPortAdapterComposition(t *testing.T) {
	handler, err := openPlatformLowPortHandler()
	if err != nil {
		t.Fatalf("openPlatformLowPortHandler() error = %v", err)
	}
	defer handler.Close()
	if _, ok := handler.(*lowporthandler.Handler); !ok {
		t.Fatalf("openPlatformLowPortHandler() type = %T, want *lowporthandler.Handler", handler)
	}
}
