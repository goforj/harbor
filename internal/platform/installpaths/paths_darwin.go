//go:build darwin

package installpaths

// platformMachineRoot follows Harbor's reviewed macOS package layout.
func platformMachineRoot() (string, error) {
	return "/Library/Application Support/GoForj/Harbor", nil
}
