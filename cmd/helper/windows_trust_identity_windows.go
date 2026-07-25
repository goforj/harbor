//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// validateWindowsCurrentUserTrustIdentity binds CurrentUser Root resolution to the authenticated ticket requester.
func validateWindowsCurrentUserTrustIdentity(requester string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read Windows helper token user: %w", err)
	}
	actual := user.User.Sid.String()
	if actual != requester {
		return fmt.Errorf("Windows helper token user is %q, want requester %q", actual, requester)
	}
	return nil
}
