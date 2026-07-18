//go:build windows

package runtimepath

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

const windowsPipePrefix = `\\.\pipe\goforj-harbor-`

// PipePath returns Harbor's named pipe path scoped to the current Windows user SID.
func PipePath() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current Windows user for Harbor pipe: %w", err)
	}

	return pipePathForUserID(user.User.Sid.String())
}

// pipePathForUserID keeps the canonical SID visible in endpoint discovery without accepting pipe syntax.
func pipePathForUserID(userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("build Harbor pipe path: user SID is empty")
	}
	if strings.ContainsAny(userID, `\/`) {
		return "", fmt.Errorf("build Harbor pipe path: user SID %q contains a path separator", userID)
	}
	sid, err := windows.StringToSid(userID)
	if err != nil {
		return "", fmt.Errorf("build Harbor pipe path: parse user SID %q: %w", userID, err)
	}

	return windowsPipePrefix + sid.String(), nil
}
