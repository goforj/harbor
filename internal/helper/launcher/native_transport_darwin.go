//go:build darwin

package launcher

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// inspectInstalledDarwinHelper uses lstat so a redirected helper path can never pass preflight.
func inspectInstalledDarwinHelper(path string) (darwinHelperMetadata, error) {
	if path == "" {
		return darwinHelperMetadata{}, errors.New("installed Darwin helper path is empty")
	}
	information, err := os.Lstat(path)
	if err != nil {
		return darwinHelperMetadata{}, err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		return darwinHelperMetadata{}, fmt.Errorf("inspect installed Darwin helper: native file status is unavailable")
	}
	return darwinHelperMetadata{
		mode:      information.Mode(),
		ownerUID:  status.Uid,
		linkCount: uint64(status.Nlink),
	}, nil
}
