//go:build windows

package helperpath

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

const windowsHelperDirectory = `GoForj\Harbor`

// windowsKnownFolderLookup keeps the machine installation resolver testable without environment fallbacks.
type windowsKnownFolderLookup func(*windows.KNOWNFOLDERID, uint32) (string, error)

// platformExecutable resolves the fixed machine installation without consulting environment state.
func platformExecutable() string {
	return windowsExecutableFromKnownFolder(windows.KnownFolderPath)
}

// windowsExecutableFromKnownFolder validates Program Files before deriving the installer-owned helper.
func windowsExecutableFromKnownFolder(lookup windowsKnownFolderLookup) string {
	programFiles, err := lookup(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil || programFiles == "" || !filepath.IsAbs(programFiles) {
		return ""
	}
	return filepath.Join(filepath.Clean(programFiles), windowsHelperDirectory, "harbor-helper.exe")
}
