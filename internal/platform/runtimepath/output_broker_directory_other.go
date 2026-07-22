//go:build !darwin && !linux && !windows

package runtimepath

// outputBrokerDirectory keeps unsupported platforms on their existing runtime directory selection.
func outputBrokerDirectory(_ temporaryDirectoryLookup) (string, error) {
	return Directory()
}
