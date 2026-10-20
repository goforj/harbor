//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// connectionRefused accepts only the native refusal that proves the old listener is absent.
func connectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
