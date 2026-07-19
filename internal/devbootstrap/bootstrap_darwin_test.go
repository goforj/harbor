//go:build darwin

package devbootstrap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/harbor/internal/platform/helperpath"
	"golang.org/x/sys/unix"
)

const (
	darwinApplicationSupportDirectory           = "/Library/Application Support"
	darwinPrivilegedHelperToolsDirectory        = "/Library/PrivilegedHelperTools"
	darwinTestHelperPrefix                      = "com.goforj.harbor.devbootstrap-test-"
	darwinTestHelperContent                     = "Darwin helper"
	darwinTestHelperRandomBytes                 = 16
	darwinStickyAncestorMode             uint32 = 0o1755
)

// darwinTestParentState records whether this test owns removal authority over the standard helper parent.
type darwinTestParentState struct {
	identity os.FileInfo
	created  bool
}

// TestSecurePlatformCreatedAccessAcceptsAnAbsentACL prevents protected xattr removal from rejecting a clean new directory.
func TestSecurePlatformCreatedAccessAcceptsAnAbsentACL(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open clean Darwin test directory: %v", err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Errorf("close clean Darwin test directory: %v", err)
		}
	})

	if err := validatePlatformExtendedAccess(directory); err != nil {
		t.Fatalf("validate clean Darwin test directory extended access: %v", err)
	}
	if err := securePlatformCreatedAccess(directory); err != nil {
		t.Fatalf("secure clean Darwin test directory extended access: %v", err)
	}
}

// TestApplyPlatformPlanTraversesDarwinLibraryAncestors proves the native transaction admits the real standard macOS ancestors.
func TestApplyPlatformPlanTraversesDarwinLibraryAncestors(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("exact production ownership requires an already-root test process")
	}
	parentState := prepareDarwinTestHelperParent(t)
	assertDarwinTestHelperParent(t, parentState)
	helperDestination := newDarwinTestHelperDestination(t)
	if helperDestination == helperpath.Executable() {
		t.Fatalf("test helper destination unexpectedly equals production helper %q", helperDestination)
	}
	var installedIdentity os.FileInfo
	t.Cleanup(func() {
		cleanupDarwinTestHelper(t, helperDestination, installedIdentity)
	})

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
	writeExecutableTestFile(t, sourcePath, darwinTestHelperContent)
	paths := testMachinePaths(filepath.Join(base, "machine"))
	prepared, err := buildPlan(
		Config{HelperSource: sourcePath, UserID: 1, GroupID: 0},
		paths,
		helperDestination,
		"darwin",
	)
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}

	applyErr := applyPlatformPlan(prepared)
	identity, exists, inspectErr := inspectDarwinInstalledTestHelper(helperDestination, prepared)
	if inspectErr == nil && exists {
		installedIdentity = identity
	}
	if applyErr != nil {
		t.Fatalf("applyPlatformPlan() through Darwin Library ancestors error = %v", applyErr)
	}
	if inspectErr != nil {
		t.Fatalf("inspect installed Darwin test helper: %v", inspectErr)
	}
	if !exists {
		t.Fatal("installed Darwin test helper is absent")
	}
	assertPlannedFilesystemPolicy(t, prepared)
	assertDarwinTestHelperParent(t, parentState)
}

// prepareDarwinTestHelperParent retains preexistence and identity without modifying an existing standard directory.
func prepareDarwinTestHelperParent(t *testing.T) darwinTestParentState {
	t.Helper()
	identity, err := os.Lstat(darwinPrivilegedHelperToolsDirectory)
	if err == nil {
		return darwinTestParentState{identity: identity}
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect Darwin privileged helper parent: %v", err)
	}
	if err := unix.Mkdir(darwinPrivilegedHelperToolsDirectory, darwinStickyAncestorMode); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			t.Fatalf("create Darwin privileged helper parent: %v", err)
		}
		identity, err = os.Lstat(darwinPrivilegedHelperToolsDirectory)
		if err != nil {
			t.Fatalf("inspect concurrently created Darwin privileged helper parent: %v", err)
		}
		return darwinTestParentState{identity: identity}
	}
	identity, err = os.Lstat(darwinPrivilegedHelperToolsDirectory)
	if err != nil {
		t.Fatalf("inspect created Darwin privileged helper parent: %v", err)
	}
	state := darwinTestParentState{identity: identity, created: true}
	t.Cleanup(func() {
		cleanupDarwinTestHelperParent(t, state)
	})
	parent, err := os.Open(darwinPrivilegedHelperToolsDirectory)
	if err != nil {
		t.Fatalf("open created Darwin privileged helper parent: %v", err)
	}
	if err := unix.Fchown(int(parent.Fd()), 0, 0); err != nil {
		_ = parent.Close()
		t.Fatalf("assign created Darwin privileged helper parent ownership: %v", err)
	}
	if err := unix.Fchmod(int(parent.Fd()), darwinStickyAncestorMode); err != nil {
		_ = parent.Close()
		t.Fatalf("set created Darwin privileged helper parent mode: %v", err)
	}
	if err := securePlatformCreatedAccess(parent); err != nil {
		_ = parent.Close()
		t.Fatalf("secure created Darwin privileged helper parent extended access: %v", err)
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		t.Fatalf("persist created Darwin privileged helper parent: %v", err)
	}
	return state
}

// assertDarwinTestHelperParent verifies the live standard ancestor remains the admitted root-owned object.
func assertDarwinTestHelperParent(t *testing.T, state darwinTestParentState) {
	t.Helper()
	current, err := os.Lstat(darwinPrivilegedHelperToolsDirectory)
	if err != nil {
		t.Fatalf("inspect live Darwin privileged helper parent: %v", err)
	}
	if !os.SameFile(state.identity, current) {
		t.Fatal("Darwin privileged helper parent changed identity during test")
	}
	var status unix.Stat_t
	if err := unix.Lstat(darwinPrivilegedHelperToolsDirectory, &status); err != nil {
		t.Fatalf("read Darwin privileged helper parent native metadata: %v", err)
	}
	mode := statusModeSecurity(status)
	if statusModeType(status) != unix.S_IFDIR || !validAncestorDirectoryPolicy(uint32(status.Uid), mode) {
		t.Fatalf("Darwin privileged helper parent metadata is %d/%04o, want an admitted root-owned ancestor", status.Uid, mode)
	}
	if state.created && mode != darwinStickyAncestorMode {
		t.Fatalf("created Darwin privileged helper parent mode is %04o, want %04o", mode, darwinStickyAncestorMode)
	}
	t.Logf("Darwin privileged helper parent preexisted=%t mode=%04o owner=%d:%d", !state.created, mode, status.Uid, status.Gid)
}

// newDarwinTestHelperDestination selects a collision-resistant name that cannot address Harbor's production helper.
func newDarwinTestHelperDestination(t *testing.T) string {
	t.Helper()
	for range 128 {
		randomBytes := make([]byte, darwinTestHelperRandomBytes)
		if _, err := rand.Read(randomBytes); err != nil {
			t.Fatalf("generate Darwin test helper name: %v", err)
		}
		path := filepath.Join(darwinPrivilegedHelperToolsDirectory, darwinTestHelperPrefix+hex.EncodeToString(randomBytes))
		if path == helperpath.Executable() {
			continue
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path
		} else if err != nil {
			t.Fatalf("inspect Darwin test helper destination %q: %v", path, err)
		}
	}
	t.Fatal("Darwin test helper names were exhausted")
	return ""
}

// inspectDarwinInstalledTestHelper admits cleanup only for the exact installed test bytes and metadata.
func inspectDarwinInstalledTestHelper(path string, prepared plan) (os.FileInfo, bool, error) {
	identity, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return nil, false, err
	}
	if err := validateHelperStatus(status, prepared); err != nil {
		return nil, true, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	if string(content) != darwinTestHelperContent {
		return nil, true, fmt.Errorf("installed Darwin test helper content is %q", content)
	}
	helper, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	accessErr := validatePlatformExtendedAccess(helper)
	closeErr := helper.Close()
	if err := errors.Join(accessErr, closeErr); err != nil {
		return nil, true, err
	}
	confirmed, err := os.Lstat(path)
	if err != nil {
		return nil, true, err
	}
	if !os.SameFile(identity, confirmed) {
		return nil, true, errors.New("installed Darwin test helper changed during metadata inspection")
	}
	return confirmed, true, nil
}

// cleanupDarwinTestHelper removes only the installed object identity admitted under this test's unique name.
func cleanupDarwinTestHelper(t *testing.T, path string, installed os.FileInfo) {
	t.Helper()
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("inspect Darwin test helper during cleanup: %v", err)
		return
	}
	if installed == nil || !os.SameFile(installed, current) {
		t.Errorf("preserve Darwin test helper %q because its installed identity is unavailable or changed", path)
		return
	}
	if err := os.Remove(path); err != nil {
		t.Errorf("remove exact Darwin test helper %q: %v", path, err)
	}
}

// cleanupDarwinTestHelperParent removes only an empty parent created by this test whose identity is unchanged.
func cleanupDarwinTestHelperParent(t *testing.T, state darwinTestParentState) {
	t.Helper()
	if !state.created {
		return
	}
	current, err := os.Lstat(darwinPrivilegedHelperToolsDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("inspect created Darwin privileged helper parent during cleanup: %v", err)
		return
	}
	if !os.SameFile(state.identity, current) {
		t.Errorf("preserve created Darwin privileged helper parent because its identity changed")
		return
	}
	entries, err := os.ReadDir(darwinPrivilegedHelperToolsDirectory)
	if err != nil {
		t.Errorf("read created Darwin privileged helper parent during cleanup: %v", err)
		return
	}
	if len(entries) != 0 {
		t.Logf("preserve created Darwin privileged helper parent because it contains %d entries", len(entries))
		return
	}
	if err := os.Remove(darwinPrivilegedHelperToolsDirectory); err != nil {
		t.Errorf("remove empty created Darwin privileged helper parent: %v", err)
	}
}
