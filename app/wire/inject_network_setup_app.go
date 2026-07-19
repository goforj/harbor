// App-owned network setup wiring. EDIT THIS FILE.

package wire

import (
	"github.com/goforj/harbor/internal/cmd"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/networksetupapproval"
)

// provideNetworkSetupApprovalRunner keeps native consent dormant until the setup command runs.
func provideNetworkSetupApprovalRunner(client *cmd.DaemonClient) cmd.NetworkSetupApprovalRunner {
	return networksetupapproval.New(
		client,
		launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
	)
}
