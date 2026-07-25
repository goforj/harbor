//go:build !windows

package trustedhttpsharness

import "os"

// systemCurlPath returns the fixed OS-provided curl used for native trust proof.
func systemCurlPath() string {
	return "/usr/bin/curl"
}

// nativeProbeEnvironment preserves only the user identity needed by the native trust backend.
func nativeProbeEnvironment() []string {
	environment := []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
	if home := os.Getenv("HOME"); home != "" {
		environment = append(environment, "HOME="+home)
	}
	return environment
}
