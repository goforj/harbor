//go:build darwin || linux

package replaystore

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// replayUnixFileInfo overrides selected stat evidence so failure branches remain deterministic without elevated chown access.
type replayUnixFileInfo struct {
	os.FileInfo
	mode   os.FileMode
	system any
}

// Mode returns the metadata mode selected by the test case.
func (info replayUnixFileInfo) Mode() os.FileMode {
	return info.mode
}

// Sys returns the native metadata selected by the test case.
func (info replayUnixFileInfo) Sys() any {
	return info.system
}

// TestUnixReplayValidationAcceptsExactPrivateObjects proves the reviewed directory and file policies pass handle validation.
func TestUnixReplayValidationAcceptsExactPrivateObjects(t *testing.T) {
	directory, directoryFile, directoryInfo, file, fileInfo := replayUnixObjects(t)
	owner := uint32(os.Geteuid())
	if err := validatePlatformDirectory(directory, directoryInfo, owner); err != nil {
		t.Fatalf("validatePlatformDirectory() error = %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("open replay root: %v", err)
	}
	if err := validatePlatformRoot(root, owner); err != nil {
		_ = root.Close()
		t.Fatalf("validatePlatformRoot() error = %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close replay root: %v", err)
	}
	if err := validateUnixReplayObject(directoryFile, true, owner); err != nil {
		t.Fatalf("validateUnixReplayObject(directory) error = %v", err)
	}
	if err := validatePlatformFile(file, fileInfo, owner); err != nil {
		t.Fatalf("validatePlatformFile() error = %v", err)
	}
}

// TestUnixReplayValidationRejectsClosedHandles proves handle-level metadata cannot be substituted with a stale stat result.
func TestUnixReplayValidationRejectsClosedHandles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "replays")
	if err := os.Mkdir(directory, unixReplayDirectoryMode); err != nil {
		t.Fatalf("create replay directory: %v", err)
	}
	file, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open replay directory: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close replay directory: %v", err)
	}
	if err := validateUnixReplayObject(file, true, uint32(os.Geteuid())); err == nil {
		t.Fatal("validateUnixReplayObject() accepted a closed handle")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("open replay root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close replay root: %v", err)
	}
	if err := validatePlatformRoot(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("validatePlatformRoot() accepted a closed root")
	}
}

// TestUnixReplayValidationRejectsMetadataAmbiguity directly covers type, permission, special-bit, owner, and native-stat failures.
func TestUnixReplayValidationRejectsMetadataAmbiguity(t *testing.T) {
	_, _, directoryInfo, _, fileInfo := replayUnixObjects(t)
	owner := uint32(os.Geteuid())
	directoryStatus := replayUnixStatus(t, directoryInfo)
	fileStatus := replayUnixStatus(t, fileInfo)
	wrongOwnerStatus := *fileStatus
	wrongOwnerStatus.Uid = owner ^ 1
	tests := []struct {
		name      string
		info      os.FileInfo
		directory bool
	}{
		{name: "file as directory", info: fileInfo, directory: true},
		{name: "directory as file", info: directoryInfo},
		{
			name:      "directory group access",
			info:      replayUnixFileInfo{FileInfo: directoryInfo, mode: os.ModeDir | 0o710, system: directoryStatus},
			directory: true,
		},
		{
			name: "file group access",
			info: replayUnixFileInfo{FileInfo: fileInfo, mode: 0o640, system: fileStatus},
		},
		{
			name:      "sticky directory",
			info:      replayUnixFileInfo{FileInfo: directoryInfo, mode: os.ModeDir | os.ModeSticky | unixReplayDirectoryMode, system: directoryStatus},
			directory: true,
		},
		{
			name: "setuid file",
			info: replayUnixFileInfo{FileInfo: fileInfo, mode: os.ModeSetuid | unixReplayFileMode, system: fileStatus},
		},
		{
			name: "wrong owner",
			info: replayUnixFileInfo{FileInfo: fileInfo, mode: fileInfo.Mode(), system: &wrongOwnerStatus},
		},
		{
			name: "missing native metadata",
			info: replayUnixFileInfo{FileInfo: fileInfo, mode: fileInfo.Mode()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateUnixReplayMetadata(test.info, test.directory, owner); err == nil {
				t.Fatal("validateUnixReplayMetadata() accepted unsafe metadata")
			}
		})
	}
}

// TestUnixReplayValidationRequiresOneFileLink proves another durable name invalidates a replay tombstone.
func TestUnixReplayValidationRequiresOneFileLink(t *testing.T) {
	directory, _, _, file, _ := replayUnixObjects(t)
	path := filepath.Join(directory, "tombstone")
	if err := os.Link(path, filepath.Join(directory, "second-name")); err != nil {
		t.Fatalf("create hard link: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat hard-linked replay file: %v", err)
	}
	if err := validatePlatformFile(file, info, uint32(os.Geteuid())); err == nil {
		t.Fatal("validatePlatformFile() accepted a hard-linked replay file")
	}
}

// TestOpenRequiresUnixRootOwnership proves exported replay storage cannot adopt an interactive user's private directory.
func TestOpenRequiresUnixRootOwnership(t *testing.T) {
	directory, _, directoryInfo, _, _ := replayUnixObjects(t)
	if os.Geteuid() == int(privilegedReplayOwnerID) {
		status := replayUnixStatus(t, directoryInfo)
		status.Uid = privilegedReplayOwnerID + 1
		unsafeInfo := replayUnixFileInfo{FileInfo: directoryInfo, mode: directoryInfo.Mode(), system: status}
		if err := validatePlatformDirectory(directory, unsafeInfo, privilegedReplayOwnerID); err == nil {
			t.Fatal("validatePlatformDirectory() accepted a non-root owner")
		}
		return
	}
	store, err := Open(directory)
	if store != nil {
		_ = store.Close()
		t.Fatal("Open() returned a store for a non-root-owned directory")
	}
	if err == nil || !strings.Contains(err.Error(), "want UID 0") {
		t.Fatalf("Open() error = %v, want root-owner rejection", err)
	}
}

// replayUnixObjects creates exact private fixtures and leaves their handles open for the calling test.
func replayUnixObjects(t *testing.T) (string, *os.File, os.FileInfo, *os.File, os.FileInfo) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "replays")
	if err := os.Mkdir(directory, unixReplayDirectoryMode); err != nil {
		t.Fatalf("create replay directory: %v", err)
	}
	path := filepath.Join(directory, "tombstone")
	if err := os.WriteFile(path, []byte("replay"), unixReplayFileMode); err != nil {
		t.Fatalf("create replay file: %v", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open replay directory: %v", err)
	}
	t.Cleanup(func() { _ = directoryFile.Close() })
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open replay file: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	directoryInfo, err := directoryFile.Stat()
	if err != nil {
		t.Fatalf("stat replay directory: %v", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		t.Fatalf("stat replay file: %v", err)
	}
	return directory, directoryFile, directoryInfo, file, fileInfo
}

// replayUnixStatus extracts a private copy of native stat evidence for one fixture.
func replayUnixStatus(t *testing.T, info os.FileInfo) *syscall.Stat_t {
	t.Helper()
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("fixture did not expose Unix stat metadata")
	}
	copy := *status
	return &copy
}
