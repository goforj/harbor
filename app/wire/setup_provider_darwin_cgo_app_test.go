//go:build darwin && cgo

package wire

import (
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/cmd"
)

// TestProvideSetupCmdUsesFullSetupOnDarwin proves the shipping macOS build selects every network phase.
func TestProvideSetupCmdUsesFullSetupOnDarwin(t *testing.T) {
	client := cmd.NewDaemonClient()
	command := provideSetupCmd(
		client,
		provideNetworkSetupApprovalRunner(client),
		provideNetworkResolverSetupApprovalRunner(client),
		provideNetworkDataPlaneSetupApprovalRunner(client),
	)
	if !reflect.ValueOf(command).Elem().FieldByName("fullSetup").Bool() {
		t.Fatal("provideSetupCmd() selected pool-only setup on Darwin with cgo")
	}
}
