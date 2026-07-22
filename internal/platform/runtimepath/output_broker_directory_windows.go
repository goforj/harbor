//go:build windows

package runtimepath

// outputBrokerDirectory keeps Windows's named-pipe runtime directory selection unchanged.
func outputBrokerDirectory(_ temporaryDirectoryLookup) (string, error) {
	return Directory()
}
