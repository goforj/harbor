//go:build darwin || linux

package devbootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestApplyPlatformPlanIsIdempotentAndPreservesRuntimeData exercises the complete native transaction when tests already run as root.
func TestApplyPlatformPlanIsIdempotentAndPreservesRuntimeData(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("exact production ownership requires an already-root test process")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve root home: %v", err)
	}
	base, err := os.MkdirTemp(home, ".harbor-devbootstrap-test-")
	if err != nil {
		t.Fatalf("create protected test root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove protected test root: %v", err)
		}
	})
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatalf("secure protected test root: %v", err)
	}

	sourcePath := filepath.Join(base, "built-harbor-helper")
	writeExecutableTestFile(t, sourcePath, "first helper")
	paths := testMachinePaths(filepath.Join(base, "machine"))
	prepared, err := buildPlan(
		Config{HelperSource: sourcePath, UserID: 1, GroupID: 0},
		paths,
		filepath.Join(base, "libexec", "harbor-helper"),
		runtime.GOOS,
	)
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	if err := applyPlatformPlan(prepared); err != nil {
		t.Fatalf("first applyPlatformPlan() error = %v", err)
	}
	assertPlannedFilesystemPolicy(t, prepared)

	runtimeData := map[string]string{
		paths.OwnershipPath:                             "owned machine record",
		paths.HostProjectionPath:                        "projected host record",
		filepath.Join(paths.PendingDirectory, "ticket"): "pending ticket",
		filepath.Join(paths.ClaimsDirectory, "claim"):   "claimed ticket",
		filepath.Join(paths.ReplayDirectory, "nonce"):   "replay tombstone",
	}
	for path, content := range runtimeData {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write runtime fixture %q: %v", path, err)
		}
	}
	writeExecutableTestFile(t, sourcePath, "second helper")
	if err := applyPlatformPlan(prepared); err != nil {
		t.Fatalf("second applyPlatformPlan() error = %v", err)
	}
	assertPlannedFilesystemPolicy(t, prepared)
	for path, content := range runtimeData {
		assertTestFileContent(t, path, content)
	}
	assertTestFileContent(t, prepared.helperDestination, "second helper")
	assertNoHelperStagingEntries(t, filepath.Dir(prepared.helperDestination))
}

// TestInstallHelperAtomicallyCreatesAndReplacesValidObjects covers native staging without requiring privileged ownership.
func TestInstallHelperAtomicallyCreatesAndReplacesValidObjects(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "destination")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatalf("create helper parent: %v", err)
	}
	sourcePath := filepath.Join(root, "source-helper")
	writeExecutableTestFile(t, sourcePath, "first")
	prepared := nativeTestPlan(filepath.Join(parentPath, "harbor-helper"), sourcePath)
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatalf("open helper parent: %v", err)
	}
	defer parent.Close()

	installTestHelper(t, parent, prepared)
	first, err := os.Lstat(prepared.helperDestination)
	if err != nil {
		t.Fatalf("stat first helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentPath, "runtime-data"), []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write sibling runtime data: %v", err)
	}
	writeExecutableTestFile(t, sourcePath, "second")
	installTestHelper(t, parent, prepared)
	second, err := os.Lstat(prepared.helperDestination)
	if err != nil {
		t.Fatalf("stat second helper: %v", err)
	}
	if os.SameFile(first, second) {
		t.Fatal("helper replacement retained the old inode instead of using an atomic staged object")
	}
	assertTestFileContent(t, prepared.helperDestination, "second")
	assertTestFileContent(t, filepath.Join(parentPath, "runtime-data"), "preserve")
	assertNoHelperStagingEntries(t, parentPath)
}

// TestInspectHelperDestinationRejectsUnsafeExistingObjects proves replacement is restricted to a Harbor-shaped direct file.
func TestInspectHelperDestinationRejectsUnsafeExistingObjects(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string)
	}{
		{name: "symlink", create: func(t *testing.T, destination string) {
			writeExecutableTestFile(t, destination+"-target", "target")
			if err := os.Symlink(destination+"-target", destination); err != nil {
				t.Fatalf("create helper symlink: %v", err)
			}
		}},
		{name: "directory", create: func(t *testing.T, destination string) {
			if err := os.Mkdir(destination, 0o755); err != nil {
				t.Fatalf("create helper directory: %v", err)
			}
		}},
		{name: "wrong mode", create: func(t *testing.T, destination string) {
			if err := os.WriteFile(destination, []byte("helper"), 0o777); err != nil {
				t.Fatalf("create broad helper: %v", err)
			}
			if err := os.Chmod(destination, 0o777); err != nil {
				t.Fatalf("broaden helper mode: %v", err)
			}
		}},
		{name: "hard link", create: func(t *testing.T, destination string) {
			backing := destination + "-backing"
			writeExecutableTestFile(t, backing, "helper")
			if err := os.Link(backing, destination); err != nil {
				t.Fatalf("create helper hard link: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentPath := t.TempDir()
			destination := filepath.Join(parentPath, "harbor-helper")
			test.create(t, destination)
			parent, err := os.Open(parentPath)
			if err != nil {
				t.Fatalf("open helper parent: %v", err)
			}
			defer parent.Close()
			prepared := nativeTestPlan(destination, filepath.Join(parentPath, "source"))
			if _, err := inspectHelperDestination(parent, prepared); !errors.Is(err, ErrUnsafeObject) {
				t.Fatalf("inspectHelperDestination() error = %v, want ErrUnsafeObject", err)
			}
		})
	}
}

// TestValidateHelperStatusRequiresExactGroup keeps every installed helper at the complete planned ownership tuple.
func TestValidateHelperStatusRequiresExactGroup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "harbor-helper")
	writeExecutableTestFile(t, path, "helper")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open helper: %v", err)
	}
	defer file.Close()
	status, err := fileStatus(file)
	if err != nil {
		t.Fatalf("inspect helper: %v", err)
	}
	prepared := nativeTestPlan(path, path)
	prepared.helperGID = uint32(status.Gid) + 1
	if err := validateHelperStatus(status, prepared); err == nil || !strings.Contains(err.Error(), "owner GID") {
		t.Fatalf("validateHelperStatus() error = %v, want owner GID rejection", err)
	}
}

// TestOpenHelperSourceRejectsIndirectAndLinkedObjects keeps staging input bound to one direct build artifact.
func TestOpenHelperSourceRejectsIndirectAndLinkedObjects(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "harbor-helper")
	writeExecutableTestFile(t, source, "helper")
	opened, _, err := openHelperSource(source)
	if err != nil {
		t.Fatalf("openHelperSource() direct error = %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close direct helper source: %v", err)
	}

	symlink := filepath.Join(root, "helper-link")
	if err := os.Symlink(source, symlink); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	if opened, _, err := openHelperSource(symlink); err == nil {
		_ = opened.Close()
		t.Fatal("openHelperSource() accepted a symlink")
	}

	hardLink := filepath.Join(root, "helper-hard-link")
	if err := os.Link(source, hardLink); err != nil {
		t.Fatalf("create source hard link: %v", err)
	}
	if opened, _, err := openHelperSource(source); err == nil {
		_ = opened.Close()
		t.Fatal("openHelperSource() accepted a multiply linked source")
	}
}

// TestTopologyAdmissionRejectsUnsafePreexistingObjectsWithoutMutation covers exact mode and no-follow checks before creation begins.
func TestTopologyAdmissionRejectsUnsafePreexistingObjectsWithoutMutation(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatalf("open topology parent: %v", err)
	}
	defer parent.Close()
	requirement := directoryPlan{mode: 0o700, uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())}

	wrongModePath := filepath.Join(root, "state")
	if err := os.Mkdir(wrongModePath, 0o755); err != nil {
		t.Fatalf("create wrong-mode topology directory: %v", err)
	}
	if err := os.Chmod(wrongModePath, 0o755); err != nil {
		t.Fatalf("set wrong topology mode: %v", err)
	}
	wrongMode, err := openDirectDirectory(parent, wrongModePath, "state")
	if err != nil {
		t.Fatalf("open wrong-mode topology directory: %v", err)
	}
	if err := validateExactDirectory(wrongMode, wrongModePath, requirement); !errors.Is(err, ErrUnsafeObject) {
		_ = wrongMode.Close()
		t.Fatalf("validateExactDirectory() error = %v, want ErrUnsafeObject", err)
	}
	if err := wrongMode.Close(); err != nil {
		t.Fatalf("close wrong-mode topology directory: %v", err)
	}

	symlinkPath := filepath.Join(root, "tickets")
	if err := os.Symlink(wrongModePath, symlinkPath); err != nil {
		t.Fatalf("create topology symlink: %v", err)
	}
	if opened, err := openDirectDirectory(parent, symlinkPath, "tickets"); err == nil {
		_ = opened.Close()
		t.Fatal("openDirectDirectory() followed a topology symlink")
	}
	if _, err := os.Lstat(filepath.Join(root, "created-after-validation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("topology validation created a later object: %v", err)
	}
}

// TestUnixTransactionRollbackNeverDescendsIntoCreatedDirectories proves concurrent runtime data wins over cleanup.
func TestUnixTransactionRollbackNeverDescendsIntoCreatedDirectories(t *testing.T) {
	parentPath := t.TempDir()
	childPath := filepath.Join(parentPath, "created")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatalf("create transaction directory: %v", err)
	}
	dataPath := filepath.Join(childPath, "runtime-data")
	if err := os.WriteFile(dataPath, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write transaction runtime data: %v", err)
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatalf("open transaction parent: %v", err)
	}
	defer parent.Close()
	transaction := &unixTransaction{created: []createdDirectory{{parent: parent, name: "created", path: childPath}}}
	if err := transaction.rollback(); err == nil {
		t.Fatal("rollback() removed or ignored a non-empty transaction directory")
	}
	assertTestFileContent(t, dataPath, "preserve")
	if err := os.Remove(dataPath); err != nil {
		t.Fatalf("remove transaction runtime data: %v", err)
	}
	if err := transaction.rollback(); err != nil {
		t.Fatalf("rollback() empty directory error = %v", err)
	}
	if _, err := os.Lstat(childPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back directory stat error = %v, want not exist", err)
	}
}

// installTestHelper opens a fresh source snapshot and completes one native staged installation.
func installTestHelper(t *testing.T, parent *os.File, prepared plan) {
	t.Helper()
	source, snapshot, err := openHelperSource(prepared.helperSource)
	if err != nil {
		t.Fatalf("open helper source: %v", err)
	}
	defer source.Close()
	initial, err := inspectHelperDestination(parent, prepared)
	if err != nil {
		t.Fatalf("inspect helper destination: %v", err)
	}
	published, err := installHelper(source, snapshot, parent, initial, prepared)
	if err != nil || !published {
		t.Fatalf("installHelper() = published %t, error %v", published, err)
	}
}

// nativeTestPlan permits unprivileged tests to exercise native mechanics without weakening production plan construction.
func nativeTestPlan(destination string, source string) plan {
	return plan{
		helperSource:      source,
		helperDestination: destination,
		helperMode:        0o755,
		helperUID:         uint32(os.Geteuid()),
		helperGID:         uint32(os.Getegid()),
	}
}

// assertPlannedFilesystemPolicy verifies exact native metadata for every provisioned directory and helper.
func assertPlannedFilesystemPolicy(t *testing.T, prepared plan) {
	t.Helper()
	for _, directory := range prepared.directories {
		information, err := os.Lstat(directory.path)
		if err != nil {
			t.Fatalf("stat planned directory %q: %v", directory.path, err)
		}
		var status unix.Stat_t
		if err := unix.Lstat(directory.path, &status); err != nil {
			t.Fatalf("read planned directory %q native metadata: %v", directory.path, err)
		}
		if !information.IsDir() || statusModeSecurity(status) != directory.mode || uint32(status.Uid) != directory.uid || uint32(status.Gid) != directory.gid {
			t.Fatalf("planned directory %q metadata = %#v / %#v, want mode %04o owner %d:%d", directory.path, information, status, directory.mode, directory.uid, directory.gid)
		}
	}
	information, err := os.Lstat(prepared.helperDestination)
	if err != nil {
		t.Fatalf("stat planned helper: %v", err)
	}
	var status unix.Stat_t
	if err := unix.Lstat(prepared.helperDestination, &status); err != nil {
		t.Fatalf("read planned helper native metadata: %v", err)
	}
	if !information.Mode().IsRegular() || statusModeSecurity(status) != prepared.helperMode || uint32(status.Uid) != prepared.helperUID || uint64(status.Nlink) != 1 {
		t.Fatalf("planned helper metadata = %#v / %#v", information, status)
	}
}

// assertNoHelperStagingEntries proves successful and failed cleanup names do not remain visible.
func assertNoHelperStagingEntries(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read helper directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".harbor-helper.devbootstrap-") {
			t.Fatalf("helper staging entry %q remained", entry.Name())
		}
	}
}

// writeExecutableTestFile writes one direct executable source fixture with exact ordinary execute bits.
func writeExecutableTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable test file %q: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("set executable test file mode %q: %v", path, err)
	}
}

// assertTestFileContent verifies runtime and helper bytes without relying on metadata formatting.
func assertTestFileContent(t *testing.T, path string, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test file %q: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("test file %q content = %q, want %q", path, content, want)
	}
}
