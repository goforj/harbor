//go:build !darwin

package wire

import (
	"context"
	"fmt"
	"runtime"

	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/reconcile"
)

// unavailableNetworkResolverObserver keeps daemon startup independent from resolver setup on unfinished platforms.
type unavailableNetworkResolverObserver struct{}

// Observe fails at the optional setup boundary while preserving the rest of the daemon on this platform.
func (unavailableNetworkResolverObserver) Observe(
	_ context.Context,
	_ resolver.Request,
) (resolver.Observation, error) {
	return resolver.Observation{}, fmt.Errorf("Harbor resolver setup is not implemented on %s", runtime.GOOS)
}

// provideNetworkResolverObserver keeps unsupported resolver authority dormant until setup is explicitly requested.
func provideNetworkResolverObserver() reconcile.NetworkResolverSetupResolverObserver {
	return unavailableNetworkResolverObserver{}
}
