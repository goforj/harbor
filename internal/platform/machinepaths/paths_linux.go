//go:build linux

package machinepaths

const linuxPrivilegedRoot = "/var/lib/goforj/harbor"

// platformRoot returns the fixed directory provisioned by Harbor's Linux installer.
func platformRoot() (string, error) {
	return linuxPrivilegedRoot, nil
}
