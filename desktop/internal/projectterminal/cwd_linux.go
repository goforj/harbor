//go:build linux

package projectterminal

import (
	"fmt"
	"os"
)

// verifyProcessDirectory proves the child entered the exact pinned checkout inode.
func verifyProcessDirectory(pid int, directory *os.File) error {
	expected, err := directory.Stat()
	if err != nil {
		return fmt.Errorf("inspect pinned project directory: %w", err)
	}
	observed, err := os.Stat(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return fmt.Errorf("inspect login shell working directory: %w", err)
	}
	if !os.SameFile(expected, observed) {
		return fmt.Errorf("login shell working directory differs from the pinned project directory")
	}
	return nil
}
