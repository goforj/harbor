//go:build windows

// Package windowsfile provides identity-safe operations on open Windows filesystem objects.
package windowsfile

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	finalPathStartSize = 256
	finalPathLimit     = 32768
)

// fileIDInfo matches FILE_ID_INFO so ReFS identities retain all 128 file-ID bits.
type fileIDInfo struct {
	volumeSerialNumber uint64
	fileID             [16]byte
}

// FinalPath resolves an open object's current Win32 path for an identity-checked operation.
func FinalPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, finalPathStartSize)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		if length >= finalPathLimit {
			return "", fmt.Errorf("path exceeds %d UTF-16 code units", finalPathLimit-1)
		}
		buffer = make([]uint16, length+1)
	}
}

// SameObject reports whether both handles identify the same object on NTFS or ReFS.
func SameObject(first, second windows.Handle) (bool, error) {
	firstIdentity, err := objectIdentity(first)
	if err != nil {
		return false, fmt.Errorf("identify first Windows filesystem object: %w", err)
	}
	secondIdentity, err := objectIdentity(second)
	if err != nil {
		return false, fmt.Errorf("identify second Windows filesystem object: %w", err)
	}
	return firstIdentity == secondIdentity, nil
}

// objectIdentity fails closed when the filesystem cannot provide its full persistent object identity.
func objectIdentity(handle windows.Handle) (fileIDInfo, error) {
	var identity fileIDInfo
	err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&identity)),
		uint32(unsafe.Sizeof(identity)),
	)
	if err != nil {
		return fileIDInfo{}, err
	}
	return identity, nil
}
