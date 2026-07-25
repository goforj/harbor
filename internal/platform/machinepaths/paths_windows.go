//go:build windows

package machinepaths

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// knownFolderLookup keeps native Known Folder failures directly testable without consulting environment state.
type knownFolderLookup func(*windows.KNOWNFOLDERID, uint32) (string, error)

// platformRoot resolves ProgramData through Windows' machine-global Known Folder contract.
func platformRoot() (string, error) {
	return platformRootFromKnownFolder(windows.KnownFolderPath)
}

// platformRootFromKnownFolder resolves the fixed native location without requiring optional installer topology to exist.
func platformRootFromKnownFolder(lookup knownFolderLookup) (string, error) {
	flags := uint32(windows.KF_FLAG_DONT_VERIFY | windows.KF_FLAG_DONT_UNEXPAND)
	programData, err := lookup(windows.FOLDERID_ProgramData, flags)
	if err != nil {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: %w", err)
	}
	if programData == "" {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: path is empty")
	}
	if !filepath.IsAbs(programData) {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: path %q is not absolute", programData)
	}

	return filepath.Join(filepath.Clean(programData), "GoForj", "Harbor", "Privileged"), nil
}
