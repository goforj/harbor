//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"path/filepath"
)

// fixtureTemporaryRoot binds Windows fixture approval to the current OS-resolved account's ordinary Temp directory.
func fixtureTemporaryRoot() (string, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Join(local, "Temp"))
}
