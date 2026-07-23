//go:build darwin || linux

package projectprocess

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// readManagedHostEnvironment reads a direct regular dotenv file without following a project-controlled link.
func readManagedHostEnvironment(path string) ([]byte, fs.FileMode, fs.FileInfo, bool, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil, false, nil
	}
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("open %q without following links: %w", path, err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, nil, false, fmt.Errorf("%q must be a direct regular file", path)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("read %q: %w", path, err)
	}
	return contents, info.Mode().Perm(), info, true, nil
}

// verifyManagedHostEnvironmentIdentity prevents a pathname replacement between inspection and publication.
func verifyManagedHostEnvironmentIdentity(path string, expected fs.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect %q: %w", path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return fmt.Errorf("%q changed after inspection", path)
	}
	return nil
}
