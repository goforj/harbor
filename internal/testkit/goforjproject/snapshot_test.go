package goforjproject

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCaptureSnapshotRecordsDirectCheckoutEntries verifies regular content, modes, and links remain independently observable.
func TestCaptureSnapshotRecordsDirectCheckoutEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("set checkout root mode: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "application.txt"), []byte("orders"), 0o640); err != nil {
		t.Fatalf("write application file: %v", err)
	}
	createSnapshotSymlink(t, "nested/application.txt", filepath.Join(root, "application-link"))

	snapshot, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("CaptureSnapshot() error = %v", err)
	}
	entries := snapshotEntriesByPath(snapshot)
	if entries["."].Type != SnapshotEntryDirectory || entries["."].Permissions != 0o700 {
		t.Fatalf("root entry = %#v", entries["."])
	}
	if entries["nested"].Type != SnapshotEntryDirectory || entries["nested"].Permissions != 0o750 {
		t.Fatalf("nested entry = %#v", entries["nested"])
	}
	file := entries[filepath.Join("nested", "application.txt")]
	if file.Type != SnapshotEntryRegularFile || file.Permissions != 0o640 || file.SHA256 == ([32]byte{}) {
		t.Fatalf("file entry = %#v", file)
	}
	link := entries["application-link"]
	if link.Type != SnapshotEntrySymbolicLink || link.LinkTarget != "nested/application.txt" || link.SHA256 != ([32]byte{}) {
		t.Fatalf("link entry = %#v", link)
	}
}

// TestSnapshotDiffDetectsHarborHostEnvironmentLifecycle verifies temporary host overlays leave an exact checkout snapshot after removal.
func TestSnapshotDiffDetectsHarborHostEnvironmentLifecycle(t *testing.T) {
	root := t.TempDir()
	before, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture empty checkout: %v", err)
	}
	hostEnvironment := filepath.Join(root, ".env.host")
	if err := os.WriteFile(hostEnvironment, []byte("# harbor managed: begin\nIP_ADDRESS=127.77.0.11\n# harbor managed: end\n"), 0o600); err != nil {
		t.Fatalf("create Harbor host environment: %v", err)
	}
	during, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture checkout with host environment: %v", err)
	}
	if difference := before.Diff(during); !strings.Contains(difference, "added .env.host") {
		t.Fatalf("creation difference = %q", difference)
	}
	if err := os.Remove(hostEnvironment); err != nil {
		t.Fatalf("remove Harbor host environment: %v", err)
	}
	after, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture restored checkout: %v", err)
	}
	AssertSnapshotEqual(t, before, after)
}

// TestSnapshotDiffReportsHostEnvironmentContentAndModeDrift verifies cleanup assertions expose distinct file mutations.
func TestSnapshotDiffReportsHostEnvironmentContentAndModeDrift(t *testing.T) {
	root := t.TempDir()
	hostEnvironment := filepath.Join(root, ".env.host")
	if err := os.WriteFile(hostEnvironment, []byte("DB_HOST=127.0.0.1\n"), 0o640); err != nil {
		t.Fatalf("write project host environment: %v", err)
	}
	before, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture project host environment: %v", err)
	}
	if err := os.WriteFile(hostEnvironment, []byte("DB_HOST=127.77.0.11\n"), 0o640); err != nil {
		t.Fatalf("change project host environment: %v", err)
	}
	contentChanged, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture changed host environment: %v", err)
	}
	if difference := before.Diff(contentChanged); !strings.Contains(difference, "changed .env.host") || !strings.Contains(difference, "sha256=") {
		t.Fatalf("content difference = %q", difference)
	}
	if err := os.Chmod(hostEnvironment, 0o600); err != nil {
		t.Fatalf("change project host environment mode: %v", err)
	}
	modeChanged, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture mode-changed host environment: %v", err)
	}
	if difference := contentChanged.Diff(modeChanged); !strings.Contains(difference, "changed .env.host") || !strings.Contains(difference, "mode=") {
		t.Fatalf("mode difference = %q", difference)
	}
}

// TestSnapshotDiffReportsSymbolicLinkTargetDrift verifies link targets are recorded without reading their targets.
func TestSnapshotDiffReportsSymbolicLinkTargetDrift(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, ".env.host")
	createSnapshotSymlink(t, "first-target", link)
	before, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture first symbolic link: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove first symbolic link: %v", err)
	}
	createSnapshotSymlink(t, "second-target", link)
	after, err := CaptureSnapshot(root)
	if err != nil {
		t.Fatalf("capture second symbolic link: %v", err)
	}
	if difference := before.Diff(after); !strings.Contains(difference, "changed .env.host") || !strings.Contains(difference, "first-target") || !strings.Contains(difference, "second-target") {
		t.Fatalf("symbolic link difference = %q", difference)
	}
}

// createSnapshotSymlink skips platforms where test-process policy does not permit symbolic-link creation.
func createSnapshotSymlink(t *testing.T, target string, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("create symbolic link: %v", err)
		}
		t.Fatalf("create symbolic link: %v", err)
	}
}

// snapshotEntriesByPath indexes one snapshot so individual assertions stay focused on one direct path.
func snapshotEntriesByPath(snapshot Snapshot) map[string]SnapshotEntry {
	entries := make(map[string]SnapshotEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	return entries
}
