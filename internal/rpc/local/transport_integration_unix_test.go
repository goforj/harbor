//go:build darwin || linux

package local_test

import (
	"testing"

	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/rpc/local"
)

// TestDefaultUnixTransportUsesRuntimeDiscovery verifies the public API joins path resolution, locking, and peer admission.
func TestDefaultUnixTransportUsesRuntimeDiscovery(t *testing.T) {
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	t.Setenv("TMPDIR", runtimeRoot)

	lock, err := daemon.AcquireProcessLock()
	if err != nil {
		t.Fatalf("acquire daemon authority: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	listener, err := local.Listen()
	if err != nil {
		t.Fatalf("listen on discovered Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		accepted <- err
	}()

	connection, err := local.Dial(nil)
	if err != nil {
		t.Fatalf("dial discovered Unix endpoint: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close discovered Unix connection: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("accept discovered Unix connection: %v", err)
	}
}
