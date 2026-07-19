//go:build phase1acceptance && (darwin || linux)

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

// phase1PlatformEnvironment confines standard per-user paths to one short-lived owner-private root.
func phase1PlatformEnvironment(t *testing.T, root string) map[string]string {
	t.Helper()

	home := filepath.Join(root, "home")
	temporary := filepath.Join(root, "tmp")
	for _, directory := range []string{home, temporary} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create phase 1 platform directory: %v", err)
		}
	}

	environment := map[string]string{
		"HOME":   home,
		"TMPDIR": temporary,
	}
	if runtime.GOOS == "linux" {
		dataHome := filepath.Join(root, "data")
		runtimeDirectory := filepath.Join(root, "run")
		for _, directory := range []string{dataHome, runtimeDirectory} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatalf("create phase 1 Linux directory: %v", err)
			}
		}
		environment["XDG_DATA_HOME"] = dataHome
		environment["XDG_RUNTIME_DIR"] = runtimeDirectory
	}
	return environment
}

// phase1AssertEndpointUnavailable proves no filesystem IPC endpoint can accept a daemon client.
func phase1AssertEndpointUnavailable(t *testing.T, endpoint string) {
	t.Helper()

	if endpoint == "" {
		t.Fatal("phase 1 Unix endpoint path is empty")
	}
	if _, err := os.Lstat(endpoint); !os.IsNotExist(err) {
		t.Fatalf("local IPC endpoint is still present: %v", err)
	}
}

// phase1EndpointPath returns the Unix socket selected through Harbor's production runtime-path policy.
func phase1EndpointPath() (string, error) {
	return runtimepath.SocketPath()
}

// phase1TemporaryRoot creates a short path because macOS has the smallest supported Unix socket limit.
func phase1TemporaryRoot(t *testing.T) string {
	t.Helper()

	root, err := os.MkdirTemp("/tmp", fmt.Sprintf("harbor-p1-%d-", os.Getpid()))
	if err != nil {
		t.Fatalf("create phase 1 sandbox: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatalf("secure phase 1 sandbox: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove phase 1 sandbox: %v", err)
		}
	})
	return root
}
