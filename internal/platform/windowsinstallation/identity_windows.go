//go:build windows

package windowsinstallation

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

// fileIdentity preserves modern native volume and 128-bit file identity across the guarded deletion reopen.
type fileIdentity struct {
	volume uint64
	id     [16]byte
}

// identifyFile refuses filesystems that cannot provide the complete persistent object identity used for cleanup.
func identifyFile(handle windows.Handle) (fileIdentity, error) {
	var identity fileIdentity
	err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity)))
	return identity, err
}
