//go:build windows

package platformproof

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddressInUseError recognizes only the Winsock bind-conflict sentinel.
func isAddressInUseError(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
