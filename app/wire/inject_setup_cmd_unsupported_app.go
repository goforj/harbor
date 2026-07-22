//go:build !darwin || !cgo

package wire

import "github.com/goforj/harbor/internal/cmd"

// provideSetupCmd preserves the supported pool-only setup boundary until trusted-ingress authority is available.
func provideSetupCmd(
	client *cmd.DaemonClient,
	setup cmd.NetworkSetupApprovalRunner,
	_ cmd.NetworkResolverSetupApprovalRunner,
	_ cmd.NetworkDataPlaneSetupApprovalRunner,
) *cmd.SetupCmd {
	return cmd.NewSetupCmd(client, setup)
}
