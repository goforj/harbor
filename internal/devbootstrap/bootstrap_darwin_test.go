//go:build darwin

package devbootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

const darwinApplicationSupportDirectory = "/Library/Application Support"

// TestApplyPlatformPlanTraversesDarwinLibraryAncestors proves the native transaction admits the real standard macOS ancestors.
func TestApplyPlatformPlanTraversesDarwinLibraryAncestors(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("exact production ownership requires an already-root test process")
	}

	base, err := os.MkdirTemp(darwinApplicationSupportDirectory, ".goforj-harbor-devbootstrap-test-")
	if err != nil {
		t.Fatalf("create isolated Darwin bootstrap root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove isolated Darwin bootstrap root: %v", err)
		}
	})

	sourcePath := filepath.Join(t.TempDir(), "built-harbor-helper")
	writeExecutableTestFile(t, sourcePath, "Darwin helper")
	paths := testMachinePaths(filepath.Join(base, "machine"))
	prepared, err := buildPlan(
		Config{HelperSource: sourcePath, UserID: 1, GroupID: 0},
		paths,
		filepath.Join(base, "helper", "com.goforj.harbor.test-helper"),
		"darwin",
	)
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}

	if err := applyPlatformPlan(prepared); err != nil {
		t.Fatalf("applyPlatformPlan() through Darwin Library ancestors error = %v", err)
	}
	assertPlannedFilesystemPolicy(t, prepared)
}
