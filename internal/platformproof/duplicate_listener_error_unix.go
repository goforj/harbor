//go:build !windows

package platformproof

import (
	"errors"
	"syscall"
)

// isAddressInUseError recognizes only the native Unix bind-conflict sentinel.
func isAddressInUseError(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
