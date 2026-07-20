//go:build linux

package wire

import (
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/reconcile"
)

// provideNetworkResolverObserver binds daemon confirmation to the same reviewed Linux facts used by the helper.
func provideNetworkResolverObserver() reconcile.NetworkResolverSetupResolverObserver {
	return resolver.New()
}
