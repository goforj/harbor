//go:build phase1acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/platform/runtimepath"
	"github.com/goforj/harbor/internal/rpc/local"
	"golang.org/x/sys/windows"
)

// phase1PlatformEnvironment confines Local AppData and home resolution to one disposable user-scoped root.
func phase1PlatformEnvironment(t *testing.T, root string) map[string]string {
	t.Helper()

	home := filepath.Join(root, "home")
	localAppData := filepath.Join(root, "local-app-data")
	temporary := filepath.Join(root, "tmp")
	for _, directory := range []string{home, localAppData, temporary} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create phase 1 Windows directory: %v", err)
		}
	}
	return map[string]string{
		"HOME":         home,
		"USERPROFILE":  home,
		"LOCALAPPDATA": localAppData,
		"TEMP":         temporary,
		"TMP":          temporary,
	}
}

// phase1AssertEndpointUnavailable proves the production SID-scoped pipe does not admit local clients.
func phase1AssertEndpointUnavailable(t *testing.T, endpoint string) {
	t.Helper()

	expected, err := runtimepath.PipePath()
	if err != nil {
		t.Fatalf("resolve phase 1 Windows endpoint during cleanup: %v", err)
	}
	if endpoint == "" || endpoint != expected {
		t.Fatalf("phase 1 Windows endpoint = %q, want %q", endpoint, expected)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	connection, err := local.Dial(ctx)
	if err == nil {
		closeErr := connection.Close()
		t.Fatalf("local IPC named pipe %q still accepts authenticated clients: close: %v", endpoint, closeErr)
	}
	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		t.Fatalf("probe unavailable local IPC named pipe %q: %v, want ERROR_FILE_NOT_FOUND", endpoint, err)
	}
}

// phase1EndpointPath returns the same production SID-scoped named pipe used by harbord and its clients.
func phase1EndpointPath() (string, error) {
	return runtimepath.PipePath()
}

// phase1TemporaryRoot creates the disposable Local AppData and runtime boundary for one acceptance run.
func phase1TemporaryRoot(t *testing.T) string {
	t.Helper()

	root, err := os.MkdirTemp("", "harbor-p1-")
	if err != nil {
		t.Fatalf("create phase 1 sandbox: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove phase 1 sandbox: %v", err)
		}
	})
	return root
}
