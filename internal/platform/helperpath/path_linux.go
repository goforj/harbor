//go:build linux

package helperpath

const linuxExecutable = "/usr/libexec/harbor-helper"

// platformExecutable keeps privileged execution independent of PATH and caller configuration.
func platformExecutable() string {
	return linuxExecutable
}
