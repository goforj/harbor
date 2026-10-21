//go:build windows

// Package windowsjob keeps native job-information buffers visible to Go's pointer and stack-lifetime rules.
package windowsjob

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	queryInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryInformationJobObject")
	setInformation   = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetInformationJobObject")
)

// Query reads native accounting or limits without hiding a movable Go buffer behind a uintptr-typed wrapper.
func Query(job windows.Handle, class int32, information unsafe.Pointer, size uint32) error {
	result, _, err := queryInformation.Call(uintptr(job), uintptr(class), uintptr(information), uintptr(size), 0)
	if result == 0 {
		return err
	}
	return nil
}

// Set applies native limits while keeping the typed buffer live through the syscall boundary.
func Set(job windows.Handle, class uint32, information unsafe.Pointer, size uint32) error {
	result, _, err := setInformation.Call(uintptr(job), uintptr(class), uintptr(information), uintptr(size))
	if result == 0 {
		return err
	}
	return nil
}
