//go:build darwin

package main

import (
	"testing"

	"github.com/goforj/harbor/internal/helper/lowporthandler"
)

// TestDarwinLowPortAdapterComposition proves the privileged helper links the reviewed launchd adapter and handler.
func TestDarwinLowPortAdapterComposition(t *testing.T) {
	handler, err := openPlatformLowPortHandler()
	if err != nil {
		t.Fatalf("openPlatformLowPortHandler() error = %v", err)
	}
	if _, ok := handler.(*lowporthandler.Handler); !ok {
		t.Fatalf("openPlatformLowPortHandler() type = %T, want *lowporthandler.Handler", handler)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("handler.Close() error = %v", err)
	}
}
