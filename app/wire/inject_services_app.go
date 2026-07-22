// App-owned Wire injector. EDIT THIS FILE.
// Add app-wide application service providers here when they do not belong to a narrower injector.

package wire

import (
	"github.com/goforj/wire"

	"github.com/goforj/harbor/app"
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/runtime"
)

// provideNetworkResolverSetupApprovalRunner keeps resolver consent dormant until setup runs.
func provideNetworkResolverSetupApprovalRunner(client *cmd.DaemonClient) cmd.NetworkResolverSetupApprovalRunner {
	return networkresolverapproval.New(
		client,
		launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
	)
}

// provideNetworkDataPlaneSetupApprovalRunner keeps trusted-ingress consent dormant until setup runs.
func provideNetworkDataPlaneSetupApprovalRunner(client *cmd.DaemonClient) cmd.NetworkDataPlaneSetupApprovalRunner {
	return networkdataplaneapproval.New(
		client,
		launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
	)
}

// appSet is a wire set that provides application-level services and dependencies.
var appSet = wire.NewSet(
	app.NewLifecycleRegistry,
	runtime.NewTimeouts,
	provideNetworkSetupApprovalRunner,
	provideNetworkResolverSetupApprovalRunner,
	provideNetworkDataPlaneSetupApprovalRunner,
)
