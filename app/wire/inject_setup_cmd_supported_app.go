//go:build (darwin && cgo) || linux || windows

package wire

import "github.com/goforj/harbor/internal/cmd"

// provideSetupCmd assembles complete setup on every platform with reviewed trusted-ingress authority.
func provideSetupCmd(
	client *cmd.DaemonClient,
	setup cmd.NetworkSetupApprovalRunner,
	resolver cmd.NetworkResolverSetupApprovalRunner,
	dataPlane cmd.NetworkDataPlaneSetupApprovalRunner,
) *cmd.SetupCmd {
	return cmd.NewFullSetupCmd(client, setup, resolver, dataPlane)
}
