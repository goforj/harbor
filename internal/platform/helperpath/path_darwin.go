//go:build darwin

package helperpath

const darwinExecutable = "/Library/PrivilegedHelperTools/com.goforj.harbor.helper"

// platformExecutable keeps privileged execution bound to the installer-protected helper location.
func platformExecutable() string {
	return darwinExecutable
}
