//go:build windows

package machinepaths

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// knownFolderLookup keeps native Known Folder failures directly testable without consulting environment state.
type knownFolderLookup func(*windows.KNOWNFOLDERID, uint32) (string, error)

// environmentExpansion resolves expandable native Known Folder results before path validation.
type environmentExpansion func(string) (string, error)

// platformRoot resolves ProgramData through Windows' machine-global Known Folder contract.
func platformRoot() (string, error) {
	return platformRootFromKnownFolder(windows.KnownFolderPath)
}

// platformRootFromKnownFolder resolves the fixed native location without requiring optional installer topology to exist.
func platformRootFromKnownFolder(lookup knownFolderLookup) (string, error) {
	return platformRootFromNative(lookup, expandWindowsEnvironment)
}

// platformRootFromNative resolves and expands ProgramData through injectable native boundaries.
func platformRootFromNative(lookup knownFolderLookup, expand environmentExpansion) (string, error) {
	flags := uint32(windows.KF_FLAG_DONT_VERIFY)
	programData, err := lookup(windows.FOLDERID_ProgramData, flags)
	if err != nil {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: %w", err)
	}
	if programData == "" {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: path is empty")
	}
	programData, err = expand(programData)
	if err != nil {
		return "", fmt.Errorf("expand Windows ProgramData known folder: %w", err)
	}
	if !filepath.IsAbs(programData) {
		return "", fmt.Errorf("resolve Windows ProgramData known folder: path %q is not absolute", programData)
	}

	return filepath.Join(filepath.Clean(programData), "GoForj", "Harbor", "Privileged"), nil
}

// expandWindowsEnvironment uses the native expansion contract returned by Known Folder APIs.
func expandWindowsEnvironment(value string) (string, error) {
	source, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return "", fmt.Errorf("encode expandable path: %w", err)
	}
	required, err := windows.ExpandEnvironmentStrings(source, nil, 0)
	if err != nil {
		return "", fmt.Errorf("measure expandable path: %w", err)
	}
	if required == 0 || required > 32768 {
		return "", fmt.Errorf("expanded path size %d is invalid", required)
	}
	buffer := make([]uint16, required)
	written, err := windows.ExpandEnvironmentStrings(source, &buffer[0], required)
	if err != nil {
		return "", fmt.Errorf("expand path: %w", err)
	}
	if written == 0 || written > required {
		return "", fmt.Errorf("expanded path size changed from %d to %d", required, written)
	}
	return resolveWindowsSystemDrive(windows.UTF16ToString(buffer), windows.GetSystemWindowsDirectory)
}

// resolveWindowsSystemDrive handles the machine token returned when the caller environment omits SystemDrive.
func resolveWindowsSystemDrive(value string, windowsDirectory func() (string, error)) (string, error) {
	const token = "%SystemDrive%"
	if len(value) < len(token) || !strings.EqualFold(value[:len(token)], token) {
		return value, nil
	}
	directory, err := windowsDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve Windows system directory: %w", err)
	}
	volume := filepath.VolumeName(directory)
	if volume == "" || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("Windows system directory %q has no absolute volume", directory)
	}
	return volume + value[len(token):], nil
}
