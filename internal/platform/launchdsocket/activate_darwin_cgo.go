//go:build darwin && cgo

package launchdsocket

/*
#include <launch.h>
#include <stdlib.h>

static int harbor_launch_activate_socket(const char *name, int **descriptors, size_t *count) {
	return launch_activate_socket(name, descriptors, count);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// platformActivateSocket copies launchd's descriptor array into Go-owned files and frees the native allocation.
func platformActivateSocket(name string) ([]*os.File, error) {
	nativeName := C.CString(name)
	defer C.free(unsafe.Pointer(nativeName))

	var nativeDescriptors *C.int
	var nativeCount C.size_t
	status := C.harbor_launch_activate_socket(nativeName, &nativeDescriptors, &nativeCount)
	if nativeDescriptors != nil {
		defer C.free(unsafe.Pointer(nativeDescriptors))
	}
	descriptors, err := copyNativeDescriptors(nativeDescriptors, nativeCount)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, errors.Join(
			fmt.Errorf("launch_activate_socket %q: %w", name, syscall.Errno(uintptr(status))),
			closeNativeDescriptors(descriptors),
		)
	}

	files := make([]*os.File, 0, len(descriptors))
	for index, descriptor := range descriptors {
		if descriptor < 0 {
			return nil, errors.Join(
				fmt.Errorf("launch_activate_socket %q returned negative descriptor %d", name, descriptor),
				closeNativeFilesAndDescriptors(files, descriptors[index:]),
			)
		}
		file := os.NewFile(uintptr(descriptor), "launchd-"+name)
		if file == nil {
			return nil, errors.Join(
				fmt.Errorf("launch_activate_socket %q could not retain descriptor %d", name, descriptor),
				closeNativeFilesAndDescriptors(files, descriptors[index:]),
			)
		}
		files = append(files, file)
	}
	return files, nil
}

// copyNativeDescriptors bounds integer conversion before viewing launchd's allocated array.
func copyNativeDescriptors(native *C.int, count C.size_t) ([]int, error) {
	if native == nil {
		if count != 0 {
			return nil, fmt.Errorf("launch_activate_socket returned %d descriptors without storage", uint64(count))
		}
		return []int{}, nil
	}
	maximumInt := int(^uint(0) >> 1)
	if uint64(count) > uint64(maximumInt) {
		return nil, fmt.Errorf("launch_activate_socket descriptor count exceeds the platform integer range")
	}
	view := unsafe.Slice(native, int(count))
	descriptors := make([]int, len(view))
	for index, descriptor := range view {
		descriptors[index] = int(descriptor)
	}
	return descriptors, nil
}

// closeNativeDescriptors closes every raw descriptor not transferred to an os.File.
func closeNativeDescriptors(descriptors []int) error {
	var result error
	for _, descriptor := range descriptors {
		if descriptor < 0 {
			continue
		}
		if err := unix.Close(descriptor); err != nil {
			result = errors.Join(result, fmt.Errorf("close launchd descriptor %d: %w", descriptor, err))
		}
	}
	return result
}

// closeNativeFilesAndDescriptors closes transferred files before remaining raw descriptors can be reused.
func closeNativeFilesAndDescriptors(files []*os.File, remaining []int) error {
	var result error
	for _, file := range files {
		if err := file.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close retained launchd descriptor: %w", err))
		}
	}
	return errors.Join(result, closeNativeDescriptors(remaining))
}
