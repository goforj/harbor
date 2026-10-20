//go:build linux

package main

import (
	"os"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// resolveInstalledDaemon admits only the direct immutable executable owned by the Debian package.
func resolveInstalledDaemon() (string, error) {
	path := daemonservice.LinuxDaemonPath
	information, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if err := daemonservice.AdmitExecutable(information); err != nil {
		return "", err
	}
	return path, nil
}
