package installpaths

import "path/filepath"

const (
	// ProductID is the immutable operating-system package identity for Harbor.
	ProductID = "com.goforj.harbor"
	// DaemonLaunchAgentLabel identifies Harbor's per-user macOS daemon service.
	DaemonLaunchAgentLabel = "com.goforj.harbor.daemon"
)

// MachineRoot returns the fixed machine-wide Harbor product root for the current platform.
func MachineRoot() (string, error) {
	return platformMachineRoot()
}

// DaemonLauncher returns the fixed executable used by the per-user daemon service.
func DaemonLauncher() (string, error) {
	root, err := MachineRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "daemon-launcher"), nil
}

// CurrentRelease returns the fixed indirection to the active complete Harbor release.
func CurrentRelease() (string, error) {
	root, err := MachineRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "current"), nil
}
