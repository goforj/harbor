//go:build linux

package runtimepath

// outputBrokerDirectory keeps Linux's established runtime directory selection unchanged.
func outputBrokerDirectory(_ temporaryDirectoryLookup) (string, error) {
	return Directory()
}
