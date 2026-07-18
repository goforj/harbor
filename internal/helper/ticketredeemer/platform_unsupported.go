//go:build !darwin && !linux && !windows

package ticketredeemer

import (
	"os"

	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// validatePlatformProcessAdmission rejects operating systems without a reviewed elevation proof.
func validatePlatformProcessAdmission() error { return machinepaths.ErrUnsupported }

// openPlatformRootDirectory rejects operating systems without a reviewed privileged topology.
func openPlatformRootDirectory(string) (*os.File, error) { return nil, machinepaths.ErrUnsupported }

// openPlatformDirectory rejects operating systems without a reviewed privileged topology.
func openPlatformDirectory(*os.File, string, string) (*os.File, error) {
	return nil, machinepaths.ErrUnsupported
}

// openPlatformFile rejects operating systems without a reviewed privileged topology.
func openPlatformFile(*os.File, string, string) (*os.File, error) {
	return nil, machinepaths.ErrUnsupported
}

// platformEntryExists rejects operating systems without a reviewed privileged topology.
func platformEntryExists(*os.File, string, string) (bool, error) {
	return false, machinepaths.ErrUnsupported
}

// validatePlatformGatewayDirectory rejects operating systems without a reviewed privileged topology.
func validatePlatformGatewayDirectory(*os.File, string) error { return machinepaths.ErrUnsupported }

// platformPendingIdentity rejects operating systems without a reviewed privileged topology.
func platformPendingIdentity(*os.File) (string, error) { return "", machinepaths.ErrUnsupported }

// validatePlatformMachineDirectory rejects operating systems without a reviewed privileged topology.
func validatePlatformMachineDirectory(*os.File) error { return machinepaths.ErrUnsupported }

// validatePlatformPendingFile rejects operating systems without a reviewed privileged topology.
func validatePlatformPendingFile(*os.File, string) error { return machinepaths.ErrUnsupported }

// validatePlatformMachineFile rejects operating systems without a reviewed privileged topology.
func validatePlatformMachineFile(*os.File) error { return machinepaths.ErrUnsupported }

// securePlatformClaim rejects operating systems without a reviewed privileged topology.
func securePlatformClaim(*os.File) error { return machinepaths.ErrUnsupported }

// syncPlatformDirectory rejects operating systems without a reviewed privileged topology.
func syncPlatformDirectory(*os.File) error { return machinepaths.ErrUnsupported }

// validatePlatformTopology rejects operating systems without a reviewed privileged topology.
func validatePlatformTopology(*os.File, *os.File, *os.File) error { return machinepaths.ErrUnsupported }

// renamePlatformNoReplace rejects operating systems without a reviewed privileged topology.
func renamePlatformNoReplace(*os.File, *os.File, *os.File, string, string) (bool, error) {
	return false, machinepaths.ErrUnsupported
}
