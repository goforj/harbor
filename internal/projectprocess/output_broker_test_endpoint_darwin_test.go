//go:build darwin

package projectprocess

import (
	"os"
	"testing"
)

// outputBrokerTestEndpointDirectory creates an owner-private short directory so direct Unix socket tests fit macOS sockaddr limits.
func outputBrokerTestEndpointDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "goforj-harbor-output-broker-test-")
	if err != nil {
		t.Fatalf("create short output broker endpoint directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove short output broker endpoint directory: %v", err)
		}
	})
	return directory
}
