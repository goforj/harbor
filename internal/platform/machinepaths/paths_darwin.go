//go:build darwin

package machinepaths

const darwinPrivilegedRoot = "/Library/Application Support/GoForj/Harbor/Privileged"

// platformRoot returns the fixed directory provisioned by Harbor's macOS installer.
func platformRoot() (string, error) {
	return darwinPrivilegedRoot, nil
}
