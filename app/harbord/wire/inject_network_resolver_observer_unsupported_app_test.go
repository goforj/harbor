//go:build !darwin && !linux

package wire

import (
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestProvideNetworkResolverObserverRejectsUnsupportedPlatform preserves the optional setup boundary outside implemented daemon backends.
func TestProvideNetworkResolverObserverRejectsUnsupportedPlatform(t *testing.T) {
	observation, err := provideNetworkResolverObserver().Observe(t.Context(), resolver.Request{})
	if err == nil || !strings.Contains(err.Error(), runtime.GOOS) {
		t.Fatalf("Observe() error = %v, want unsupported %s error", err, runtime.GOOS)
	}
	if observation.Request != (resolver.Request{}) || observation.Complete || observation.Truncated || len(observation.Rules) != 0 {
		t.Fatalf("Observe() = %#v, want no unsupported-platform facts", observation)
	}
}
