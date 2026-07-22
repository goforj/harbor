//go:build !darwin || !cgo

package wire

import (
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/cmd"
)

// TestProvideSetupCmdRetainsPoolOnlySetupOutsideDarwin preserves the existing cross-platform CLI boundary.
func TestProvideSetupCmdRetainsPoolOnlySetupOutsideDarwin(t *testing.T) {
	client := cmd.NewDaemonClient()
	command := provideSetupCmd(
		client,
		provideNetworkSetupApprovalRunner(client),
		provideNetworkResolverSetupApprovalRunner(client),
		provideNetworkDataPlaneSetupApprovalRunner(client),
	)
	if reflect.ValueOf(command).Elem().FieldByName("fullSetup").Bool() {
		t.Fatal("provideSetupCmd() selected unsupported full setup outside Darwin with cgo")
	}
}
