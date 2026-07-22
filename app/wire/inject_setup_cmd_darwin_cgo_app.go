//go:build darwin && cgo

package wire

import "github.com/goforj/harbor/internal/cmd"

// provideSetupCmd assembles complete setup only where trusted-ingress authority is available.
func provideSetupCmd(
	client *cmd.DaemonClient,
	setup cmd.NetworkSetupApprovalRunner,
	resolver cmd.NetworkResolverSetupApprovalRunner,
	dataPlane cmd.NetworkDataPlaneSetupApprovalRunner,
) *cmd.SetupCmd {
	return cmd.NewFullSetupCmd(client, setup, resolver, dataPlane)
}
