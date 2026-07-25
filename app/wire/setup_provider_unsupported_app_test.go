//go:build (!darwin || !cgo) && !linux && !windows

package wire

import (
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/cmd"
)

// TestProvideSetupCmdRetainsPoolOnlySetupWithoutTrustedIngress preserves the unsupported-platform CLI boundary.
func TestProvideSetupCmdRetainsPoolOnlySetupWithoutTrustedIngress(t *testing.T) {
	client := cmd.NewDaemonClient()
	command := provideSetupCmd(
		client,
		provideNetworkSetupApprovalRunner(client),
		provideNetworkResolverSetupApprovalRunner(client),
		provideNetworkDataPlaneSetupApprovalRunner(client),
	)
	if reflect.ValueOf(command).Elem().FieldByName("fullSetup").Bool() {
		t.Fatal("provideSetupCmd() selected full setup without trusted-ingress authority")
	}
}
