//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// connectionRefused admits only native Windows refusal as proof that the exact loopback listener is absent.
func connectionRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}
