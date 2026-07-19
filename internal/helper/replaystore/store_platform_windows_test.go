//go:build windows

package replaystore

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// preparePlatformTestDirectory applies the machine-global boundary required by the Windows store.
func preparePlatformTestDirectory(t *testing.T, directory string) {
	t.Helper()
	descriptor, owner, err := replayMachineDescriptor(true)
	if err != nil {
		t.Fatalf("replayMachineDescriptor(directory) error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("SecurityDescriptor.DACL() error = %v", err)
	}
	err = windows.SetNamedSecurityInfo(
		directory,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		t.Fatalf("windows.SetNamedSecurityInfo() error = %v", err)
	}
}

// replayTestOwnerID is ignored because Windows validates machine principals instead of Unix owner IDs.
func replayTestOwnerID() uint32 {
	return privilegedReplayOwnerID
}
