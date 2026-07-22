//go:build !darwin

package projectprocess

import "testing"

// outputBrokerTestEndpointDirectory uses the ordinary test directory where Unix socket paths are not constrained by macOS sockaddr limits.
func outputBrokerTestEndpointDirectory(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
