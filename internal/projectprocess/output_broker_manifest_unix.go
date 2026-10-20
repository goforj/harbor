//go:build !windows

package projectprocess

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// createPrivateOutputBrokerManifest creates the ticket exclusively with owner-only Unix permissions.
func createPrivateOutputBrokerManifest(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

// readPrivateOutputBrokerManifest refuses symlinks and checks permissions on the retained object before reading bounded bytes.
func readPrivateOutputBrokerManifest(path string) ([]byte, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("output broker launch config must be a direct regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("output broker launch config must not be readable by group or other users")
	}
	return io.ReadAll(io.LimitReader(file, maximumOutputBrokerLaunchConfigBytes+1))
}
