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

	temporaryParent, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatalf("resolve phase 1 temporary parent: %v", err)
	}
	root, err := os.MkdirTemp(
		filepath.Clean(temporaryParent),
		fmt.Sprintf("harbor-p1-%d-", os.Getpid()),
	)
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

// TestPhase1TemporaryRootReturnsCanonicalPath keeps descriptor-safe production paths valid on platforms where /tmp is a system symlink.
func TestPhase1TemporaryRootReturnsCanonicalPath(t *testing.T) {
	root := phase1TemporaryRoot(t)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve phase 1 sandbox: %v", err)
	}
	if root != filepath.Clean(canonicalRoot) {
		t.Fatalf("phase 1 sandbox = %q, want canonical path %q", root, canonicalRoot)
	}
}

// TestPhase1TemporaryRootCleanupRejectsReplacement protects unrelated contents when a sandbox leaf is swapped before cleanup.
func TestPhase1TemporaryRootCleanupRejectsReplacement(t *testing.T) {
	sentinel := t.TempDir()
	sentinelFile := filepath.Join(sentinel, "keep")
	if err := os.WriteFile(sentinelFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	var root string
	t.Run("sandbox", func(t *testing.T) {
		root = phase1TemporaryRoot(t)
		if err := os.Remove(root); err != nil {
			t.Fatalf("remove sandbox leaf: %v", err)
		}
		if err := os.Symlink(sentinel, root); err != nil {
			t.Fatalf("replace sandbox leaf: %v", err)
		}
	})
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("sandbox replacement remains after cleanup: %v", err)
	}
	contents, err := os.ReadFile(sentinelFile)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel = %q, %v, want preserved", contents, err)
	}
}
