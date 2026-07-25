//go:build !windows

package main

import "fmt"

// validateWindowsTrustRequesterIdentity keeps the Windows-only identity boundary unavailable on other platforms.
func validateWindowsTrustRequesterIdentity(string) error {
	return fmt.Errorf("Windows trust requester identity validation is unavailable")
}
