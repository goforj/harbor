//go:build darwin

package launchdrelaypath

const darwinExecutable = "/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay"

// platformExecutable keeps the future launchd service bound to an installer-protected location.
func platformExecutable() string {
	return darwinExecutable
}
