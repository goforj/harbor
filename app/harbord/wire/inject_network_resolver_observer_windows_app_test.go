//go:build windows

package wire

import (
	"testing"

	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestProvideNetworkResolverObserverUsesWindowsAdapter proves daemon confirmation links the production NRPT backend.
func TestProvideNetworkResolverObserverUsesWindowsAdapter(t *testing.T) {
	observer := provideNetworkResolverObserver()
	if _, ok := observer.(*resolver.Adapter); !ok {
		t.Fatalf("provideNetworkResolverObserver() = %T, want *resolver.Adapter", observer)
	}
}
