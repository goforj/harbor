//go:build darwin || linux

package projectdiscovery

import (
	"os"

	"golang.org/x/sys/unix"
)

// openMetadataFile uses nonblocking open so a path swap to a FIFO cannot stall daemon discovery.
func openMetadataFile(filename string) (*os.File, error) {
	descriptor, err := unix.Open(filename, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), filename), nil
}
