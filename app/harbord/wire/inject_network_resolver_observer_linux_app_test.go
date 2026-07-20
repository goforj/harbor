//go:build linux

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestProvideNetworkResolverObserverUsesLinuxAdapter proves daemon confirmation links the production Linux resolver backend.
func TestProvideNetworkResolverObserverUsesLinuxAdapter(t *testing.T) {
	observer := provideNetworkResolverObserver()
	if _, ok := observer.(*resolver.Adapter); !ok {
		t.Fatalf("provideNetworkResolverObserver() = %T, want *resolver.Adapter", observer)
	}
}
