//go:build !windows || !release

package windowsinstallation

// CheckRuntimeRole preserves ordinary source development and other platforms' existing runtime admission.
func CheckRuntimeRole(string) error { return nil }
