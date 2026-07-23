//go:build !darwin && !linux

package projectprocess

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// readManagedHostEnvironment reads a regular dotenv file and verifies its identity before use on platforms without O_NOFOLLOW.
func readManagedHostEnvironment(path string) ([]byte, fs.FileMode, fs.FileInfo, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil, false, nil
	}
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, nil, false, fmt.Errorf("%q must be a direct regular file", path)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return nil, 0, nil, false, fmt.Errorf("reinspect %q: %w", path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, 0, nil, false, fmt.Errorf("%q changed during inspection", path)
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
