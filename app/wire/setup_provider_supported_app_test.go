//go:build (darwin && cgo) || linux || windows

package wire

import (
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/cmd"
)

// TestProvideSetupCmdUsesFullSetupOnSupportedPlatforms proves shipping builds select every network phase.
func TestProvideSetupCmdUsesFullSetupOnSupportedPlatforms(t *testing.T) {
	client := cmd.NewDaemonClient()
	command := provideSetupCmd(
		client,
		provideNetworkSetupApprovalRunner(client),
		provideNetworkResolverSetupApprovalRunner(client),
		provideNetworkDataPlaneSetupApprovalRunner(client),
	)
	if !reflect.ValueOf(command).Elem().FieldByName("fullSetup").Bool() {
		t.Fatal("provideSetupCmd() selected pool-only setup on a trusted-ingress platform")
	}
}
