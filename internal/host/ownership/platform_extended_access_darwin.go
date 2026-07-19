//go:build darwin

package ownership

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const darwinFileSecurityNoACL = ^uint32(0)

type darwinFileSecurityHeader struct {
	magic      uint32
	owner      [16]byte
	group      [16]byte
	entryCount uint32
	flags      uint32
}

const darwinFileSecurityHeaderSize = unsafe.Sizeof(darwinFileSecurityHeader{})

var inspectDarwinExtendedAccess = inspectDarwinAccessControlList

// validatePlatformExtendedAccess rejects macOS ACLs because they can grant access beyond Unix mode bits.
func validatePlatformExtendedAccess(file *os.File) error {
	present, err := inspectDarwinExtendedAccess(int(file.Fd()))
	runtime.KeepAlive(file)
	if err != nil {
		return fmt.Errorf("inspect machine ownership macOS access control list: %w", err)
	}
	if present {
		return fmt.Errorf("machine ownership path has a macOS access control list")
	}
	return nil
}

// inspectDarwinAccessControlList asks the kernel for ACL metadata without reading macOS's protected system xattr directly.
func inspectDarwinAccessControlList(fd int) (bool, error) {
	var header darwinFileSecurityHeader
	var status unix.Stat_t
	size := darwinFileSecurityHeaderSize
	_, _, errno := unix.Syscall6(
		unix.SYS_FSTAT64_EXTENDED,
		uintptr(fd),
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	)
	runtime.KeepAlive(&status)
	runtime.KeepAlive(&header)
	runtime.KeepAlive(&size)
	if errno != 0 {
		return false, errno
	}
	return darwinFileSecurityHasACL(header, size)
}

// darwinFileSecurityHasACL needs only the fixed header because any larger required buffer proves ACL entries exist.
func darwinFileSecurityHasACL(header darwinFileSecurityHeader, size uintptr) (bool, error) {
	if size == 0 {
		return false, nil
	}
	if size < darwinFileSecurityHeaderSize {
		return false, fmt.Errorf("macOS file security metadata contains %d bytes, want zero or at least %d", size, darwinFileSecurityHeaderSize)
	}
	if size > darwinFileSecurityHeaderSize {
		return true, nil
	}
	return header.entryCount != darwinFileSecurityNoACL, nil
}
