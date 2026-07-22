//go:build darwin || linux

package runtimepath

import "testing"

// TestOutputBrokerUnixSocketFilenameBudgetMatchesEndpointToken verifies Darwin reserves the exact compact broker filename.
func TestOutputBrokerUnixSocketFilenameBudgetMatchesEndpointToken(t *testing.T) {
	const outputBrokerSocketFilenameBytes = 44
	if maximumUnixSocketFilenameBytes != outputBrokerSocketFilenameBytes {
		t.Fatalf("output broker socket filename budget = %d, want %d", maximumUnixSocketFilenameBytes, outputBrokerSocketFilenameBytes)
	}
}

// TestSocketPathFitsPortableUnixLimit verifies path resolution reserves space for Harbor's endpoint.
func TestSocketPathFitsPortableUnixLimit(t *testing.T) {
	path, err := SocketPath()
	if err != nil {
		t.Fatalf("resolve daemon socket path: %v", err)
	}
	if len([]byte(path)) > maxPortableUnixSocketPathBytes {
		t.Fatalf("daemon socket path is %d bytes, want at most %d", len([]byte(path)), maxPortableUnixSocketPathBytes)
	}
}
