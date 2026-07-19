//go:build darwin

package ticketspool

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/goforj/harbor/internal/platform/darwinacl"
	"golang.org/x/sys/unix"
)

// validatePlatformExtendedACL rejects macOS ACL entries that can grant access beyond the private mode bits.
func validatePlatformExtendedACL(file *os.File) error {
	present, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	if present {
		return fmt.Errorf("path has a macOS extended ACL")
	}
	return nil
}

// renamePlatformNoReplace commits one direct child without replacing an existing immutable reference.
func renamePlatformNoReplace(_ *os.Root, directory *os.File, _ *os.File, source string, destination string) (bool, error) {
	err := unix.RenameatxNp(
		int(directory.Fd()),
		source,
		int(directory.Fd()),
		destination,
		unix.RENAME_EXCL,
	)
	if err == unix.EEXIST {
		return false, fs.ErrExist
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
