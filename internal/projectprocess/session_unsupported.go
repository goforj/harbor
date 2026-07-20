//go:build !darwin && !linux && !windows

package projectprocess

import (
	"fmt"
	"strings"
	"syscall"
)

// hostProcessBirthToken removes the version marker even though session settlement remains unsupported.
func hostProcessBirthToken(persistedBirthToken string) string {
	return strings.TrimPrefix(persistedBirthToken, ownedUnixSessionBirthTokenPrefix)
}

// observeOwnedProcessSession fails closed where Harbor cannot enumerate a Unix session exactly.
func observeOwnedProcessSession(sessionID int, expectedRootBirth string) (PriorProcessState, []struct{}, error) {
	return "", nil, fmt.Errorf("observe process %d session: session ownership is unsupported on this operating system", sessionID)
}

// signalOwnedProcessSession fails closed where Harbor cannot enumerate a Unix session exactly.
func signalOwnedProcessSession(sessionID int, expectedRootBirth string, signal syscall.Signal) (PriorProcessState, error) {
	return "", fmt.Errorf("signal process %d session: session ownership is unsupported on this operating system", sessionID)
}

// forceOwnedProcessSession fails closed where Harbor cannot enumerate a Unix session exactly.
func forceOwnedProcessSession(sessionID int, expectedRootBirth string) (PriorProcessState, error) {
	return "", fmt.Errorf("force process %d session: session ownership is unsupported on this operating system", sessionID)
}

// forceUnattachedProcessSession fails closed where Harbor cannot enumerate a Unix session exactly.
func forceUnattachedProcessSession(sessionID int) error {
	return fmt.Errorf("force unattached process %d session: session ownership is unsupported on this operating system", sessionID)
}
