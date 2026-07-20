//go:build windows

package wire

import (
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/reconcile"
)

// provideNetworkResolverObserver binds Windows confirmation to the reviewed NRPT facts used by the helper.
func provideNetworkResolverObserver() reconcile.NetworkResolverSetupResolverObserver {
	return resolver.New()
}
