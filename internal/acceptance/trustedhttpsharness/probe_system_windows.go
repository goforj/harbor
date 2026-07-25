//go:build windows

package trustedhttpsharness

import (
	"os"
	"path/filepath"
)

// systemCurlPath returns the Windows inbox curl executable without consulting PATH.
func systemCurlPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("WINDIR")
	}
	if !filepath.IsAbs(root) {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "curl.exe")
}

// nativeCurlTLSArguments lets Schannel soft-fail only unavailable revocation data for Harbor's
// short-lived local certificates while retaining its chain, hostname, lifetime, and trust checks.
func nativeCurlTLSArguments() []string {
	return []string{"--ssl-revoke-best-effort"}
}

// nativeProbeEnvironment preserves only Windows identity and system-path inputs needed by Schannel.
func nativeProbeEnvironment() []string {
	environment := make([]string, 0, 5)
	for _, name := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "HOME"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
