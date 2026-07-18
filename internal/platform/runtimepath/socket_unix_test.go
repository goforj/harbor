//go:build darwin || linux

package runtimepath

import "testing"

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
