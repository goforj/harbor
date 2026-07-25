//go:build !windows

package main

import "fmt"

// validateWindowsCurrentUserTrustIdentity keeps the Windows-only identity boundary unavailable on other platforms.
func validateWindowsCurrentUserTrustIdentity(string) error {
	return fmt.Errorf("Windows CurrentUser trust identity validation is unavailable")
}
