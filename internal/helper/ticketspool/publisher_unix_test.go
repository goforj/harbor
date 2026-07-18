//go:build darwin || linux

package ticketspool

import (
	"os"
	"path/filepath"
	"testing"
)

// prepareTestDirectory creates the exact owner-private boundary production expects on Unix.
func prepareTestDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("create private test directory: %v", err)
	}
}

// makeTestDirectoryUnsafe creates a boundary that Open must reject without repairing.
func makeTestDirectoryUnsafe(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create unsafe test directory: %v", err)
	}
}

// assertTestDirectoryUnsafe verifies a failed Open left the deliberately broad Unix mode untouched.
func assertTestDirectoryUnsafe(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat unsafe test directory: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("Open repaired directory mode to %04o", info.Mode().Perm())
	}
}

// assertPrivateRegularFile verifies the durable owner-private Unix file boundary.
func assertPrivateRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat published file: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != privateFileMode {
		t.Fatalf("published mode = %v, want regular 0600", info.Mode())
	}
}

// makeOpenedTestDirectoryUnsafe broadens a retained directory and returns its exact restoration.
func makeOpenedTestDirectoryUnsafe(t *testing.T, path string) func() {
	t.Helper()
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("broaden pending permissions: %v", err)
	}
	return func() {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatalf("restore pending permissions: %v", err)
		}
	}
}

// makeStagedFileUnsafe broadens one staging file so reopened metadata validation must reject it.
func makeStagedFileUnsafe(t *testing.T, root *os.Root, name string) {
	t.Helper()
	if err := root.Chmod(name, 0o644); err != nil {
		t.Fatalf("broaden staged file permissions: %v", err)
	}
}

// writePrivateFile creates one exact owner-private Unix fixture.
func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, privateFileMode); err != nil {
		t.Fatalf("write private fixture: %v", err)
	}
}

// TestUnixRenameMissingSourceFails proves the native no-replace primitive does not report an unapplied name as success.
func TestUnixRenameMissingSourceFails(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("open native root: %v", err)
	}
	defer func() { _ = root.Close() }()
	openedDirectory, err := root.Open(".")
	if err != nil {
		t.Fatalf("open native directory: %v", err)
	}
	defer func() { _ = openedDirectory.Close() }()
	if applied, err := renamePlatformNoReplace(root, openedDirectory, nil, "missing", "final"); applied || err == nil {
		t.Fatalf("renamePlatformNoReplace() result = %t, %v, want unapplied failure", applied, err)
	}
}

// TestUnixHardLinkedStagingFileIsRejected proves link-count validation closes alternate-name mutation races.
func TestUnixHardLinkedStagingFileIsRejected(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	path := filepath.Join(directory, "staging")
	if err := os.WriteFile(path, []byte("ticket"), privateFileMode); err != nil {
		t.Fatalf("write staging fixture: %v", err)
	}
	if err := os.Link(path, filepath.Join(directory, "alternate")); err != nil {
		t.Fatalf("link staging fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open staging fixture: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("stat staging fixture: %v", err)
	}
	validationErr := validatePlatformObject(file, info, false)
	closeErr := file.Close()
	if validationErr == nil {
		t.Fatal("validatePlatformObject() accepted a hard-linked staging file")
	}
	if closeErr != nil {
		t.Fatalf("close staging fixture: %v", closeErr)
	}
}

// TestUnixObjectValidationRejectsTypeAndOwnerAmbiguity covers fail-closed metadata branches directly.
func TestUnixObjectValidationRejectsTypeAndOwnerAmbiguity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	filePath := filepath.Join(directory, "file")
	writePrivateFile(t, filePath, []byte("ticket"))
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat directory fixture: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file fixture: %v", err)
	}
	if err := validateUnixObject(fileInfo, true); err == nil {
		t.Fatal("validateUnixObject() accepted a file as a directory")
	}
	if err := validateUnixObject(directoryInfo, false); err == nil {
		t.Fatal("validateUnixObject() accepted a directory as a file")
	}
	if err := validateUnixObject(fileInfoWithoutSystem{FileInfo: fileInfo}, false); err == nil {
		t.Fatal("validateUnixObject() accepted metadata without an owner identity")
	}
}

// TestUnixObjectValidationRejectsSpecialBits proves owner-only permissions cannot conceal set-ID or sticky semantics.
func TestUnixObjectValidationRejectsSpecialBits(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	filePath := filepath.Join(directory, "file")
	writePrivateFile(t, filePath, []byte("ticket"))
	tests := []struct {
		name      string
		path      string
		mode      os.FileMode
		directory bool
	}{
		{name: "sticky directory", path: directory, mode: 0o700 | os.ModeSticky, directory: true},
		{name: "setgid directory", path: directory, mode: 0o700 | os.ModeSetgid, directory: true},
		{name: "setuid file", path: filePath, mode: privateFileMode | os.ModeSetuid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Chmod(test.path, test.mode); err != nil {
				t.Fatalf("apply special mode fixture: %v", err)
			}
			info, err := os.Stat(test.path)
			if err != nil {
				t.Fatalf("stat special mode fixture: %v", err)
			}
			if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 {
				t.Skip("filesystem did not retain the requested special mode")
			}
			if err := validateUnixObject(info, test.directory); err == nil {
				t.Fatal("validateUnixObject() accepted special mode bits")
			}
			baseMode := os.FileMode(privateFileMode)
			if test.directory {
				baseMode = 0o700
			}
			if err := os.Chmod(test.path, baseMode); err != nil {
				t.Fatalf("restore fixture mode: %v", err)
			}
		})
	}
}

// fileInfoWithoutSystem removes the native owner evidence from an otherwise valid fixture.
type fileInfoWithoutSystem struct {
	os.FileInfo
}

// Sys withholds platform metadata so owner validation must fail closed.
func (info fileInfoWithoutSystem) Sys() any {
	return nil
}
