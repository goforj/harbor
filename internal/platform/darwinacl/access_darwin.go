//go:build darwin

package darwinacl

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	fileSecurityACLProperty = 5
	fileSecurityRemoveACL   = 1
	fileSecurityNoACL       = ^uint32(0)
)

type fileSecurityHeader struct {
	magic      uint32
	owner      [16]byte
	group      [16]byte
	entryCount uint32
	flags      uint32
}

const fileSecurityHeaderSize = unsafe.Sizeof(fileSecurityHeader{})

var inspectAccessControlList = inspectNativeAccessControlList

// Present reports whether the opened object has a macOS access control list.
func Present(file *os.File) (bool, error) {
	present, err := inspectAccessControlList(int(file.Fd()))
	runtime.KeepAlive(file)
	if err != nil {
		return false, fmt.Errorf("inspect macOS access control list: %w", err)
	}
	return present, nil
}

// Remove clears the opened object's macOS access control list without resolving its path again.
func Remove(file *os.File) error {
	security, _, callErr := systemPointerCall(filesecInitTrampolineAddress, 0, 0, 0)
	if security == 0 {
		if callErr != 0 {
			return fmt.Errorf("initialize macOS file security: %w", callErr)
		}
		return fmt.Errorf("initialize macOS file security: %w", syscall.ENOMEM)
	}
	defer systemCall(filesecFreeTrampolineAddress, security, 0, 0)

	_, _, callErr = systemCall(
		filesecSetPropertyTrampolineAddress,
		security,
		fileSecurityACLProperty,
		fileSecurityRemoveACL,
	)
	if callErr != 0 {
		return fmt.Errorf("configure macOS ACL removal: %w", callErr)
	}
	_, _, callErr = systemCall(fchmodxTrampolineAddress, file.Fd(), security, 0)
	runtime.KeepAlive(file)
	if callErr != 0 {
		return fmt.Errorf("apply macOS ACL removal: %w", callErr)
	}
	return nil
}

// inspectNativeAccessControlList asks Darwin for file-security metadata instead of reading its protected xattr.
func inspectNativeAccessControlList(fd int) (bool, error) {
	var header fileSecurityHeader
	var status unix.Stat_t
	size := fileSecurityHeaderSize
	_, _, callErr := unix.Syscall6(
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
	if callErr != 0 {
		return false, callErr
	}
	return fileSecurityHasACL(header, size)
}

// fileSecurityHasACL classifies the fixed metadata header and a larger kernel-reported buffer requirement.
func fileSecurityHasACL(header fileSecurityHeader, size uintptr) (bool, error) {
	if size == 0 {
		return false, nil
	}
	if size < fileSecurityHeaderSize {
		return false, fmt.Errorf("macOS file security metadata contains %d bytes, want zero or at least %d", size, fileSecurityHeaderSize)
	}
	if size > fileSecurityHeaderSize {
		return true, nil
	}
	return header.entryCount != fileSecurityNoACL, nil
}

// systemCall invokes one integer-returning libSystem function with Darwin errno handling.
func systemCall(function uintptr, argument1 uintptr, argument2 uintptr, argument3 uintptr) (result1 uintptr, result2 uintptr, callErr syscall.Errno)

//go:linkname systemCall syscall.syscall

// systemPointerCall invokes one pointer-returning libSystem function with null-aware errno handling.
func systemPointerCall(function uintptr, argument1 uintptr, argument2 uintptr, argument3 uintptr) (result1 uintptr, result2 uintptr, callErr syscall.Errno)

//go:linkname systemPointerCall syscall.syscallPtr

var (
	filesecInitTrampolineAddress        uintptr
	filesecFreeTrampolineAddress        uintptr
	filesecSetPropertyTrampolineAddress uintptr
	fchmodxTrampolineAddress            uintptr
)

//go:cgo_import_dynamic libc_filesec_init filesec_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_filesec_free filesec_free "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_filesec_set_property filesec_set_property "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_fchmodx_np fchmodx_np "/usr/lib/libSystem.B.dylib"
