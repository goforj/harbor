//go:build darwin || linux

package ownership

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewStoreRejectsMissingAndNondirectoryBoundaries covers fixed-path failures before a root handle exists.
func TestNewStoreRejectsMissingAndNondirectoryBoundaries(t *testing.T) {
	if _, err := NewStore(""); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("NewStore(empty) error = %v", err)
	}
	if _, err := NewStore(string(filepath.Separator)); err == nil || !strings.Contains(err.Error(), "does not name a file") {
		t.Fatalf("NewStore(root) error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing", "owner.json")
	if _, err := NewStore(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("NewStore(missing parent) error = %v, want not exist", err)
	}
	parent := filepath.Join(t.TempDir(), "parent-file")
	if err := os.WriteFile(parent, nil, privateFileMode); err != nil {
		t.Fatalf("os.WriteFile(parent) error = %v", err)
	}
	if _, err := NewStore(filepath.Join(parent, "owner.json")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("NewStore(file parent) error = %v, want ErrUnsafePath", err)
	}
}

// TestUnixStoreRejectsLockEntriesIntroducedAfterOpening covers special, inaccessible, and weakened lock paths.
func TestUnixStoreRejectsLockEntriesIntroducedAfterOpening(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{
			name: "special",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("os.Mkdir() error = %v", err)
				}
			},
			want: "not a direct regular file",
		},
		{
			name: "inaccessible",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, privateFileMode); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("os.Chmod() error = %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, privateFileMode) })
			},
			want: "",
		},
		{
			name: "weakened mode",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o640); err != nil {
					t.Fatalf("os.WriteFile() error = %v", err)
				}
				if err := os.Chmod(path, 0o640); err != nil {
					t.Fatalf("os.Chmod() error = %v", err)
				}
			},
			want: "not protected",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			test.setup(t, path+".lock")
			_, err := store.openLockFile()
			if err == nil || test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.openLockFile() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestUnixStoreRejectsRecordEntriesIntroducedAfterOpening covers inaccessible and weakened active records.
func TestUnixStoreRejectsRecordEntriesIntroducedAfterOpening(t *testing.T) {
	for _, mode := range []os.FileMode{0, 0o640} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			if err := os.WriteFile(path, nil, privateFileMode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("os.Chmod() error = %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, privateFileMode) })
			_, err := store.openRecordLocked()
			if err == nil {
				t.Fatal("Store.openRecordLocked() error = nil")
			}
		})
	}
}

// TestValidateOpenedEntryRejectsHandleAndPathChanges covers each independent retained-handle proof.
func TestValidateOpenedEntryRejectsHandleAndPathChanges(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *os.Root, string, *os.File)
		want  string
	}{
		{
			name: "closed handle",
			setup: func(t *testing.T, _ *os.Root, _ string, file *os.File) {
				if err := file.Close(); err != nil {
					t.Fatalf("File.Close() error = %v", err)
				}
			},
			want: "inspect opened",
		},
		{
			name: "removed path",
			setup: func(t *testing.T, root *os.Root, name string, _ *os.File) {
				if err := root.Remove(name); err != nil {
					t.Fatalf("Root.Remove() error = %v", err)
				}
			},
			want: "inspect opened",
		},
		{
			name: "replaced path",
			setup: func(t *testing.T, root *os.Root, name string, _ *os.File) {
				if err := root.Rename(name, name+".old"); err != nil {
					t.Fatalf("Root.Rename() error = %v", err)
				}
				replacement, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, privateFileMode)
				if err != nil {
					t.Fatalf("Root.OpenFile() error = %v", err)
				}
				if err := replacement.Close(); err != nil {
					t.Fatalf("replacement.Close() error = %v", err)
				}
			},
			want: "changed while opening",
		},
		{
			name: "weakened handle",
			setup: func(t *testing.T, _ *os.Root, _ string, file *os.File) {
				if err := file.Chmod(0o640); err != nil {
					t.Fatalf("File.Chmod() error = %v", err)
				}
			},
			want: "not protected",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatalf("os.OpenRoot() error = %v", err)
			}
			defer root.Close()
			file, err := createPlatformFile(root, directory, "entry")
			if err != nil {
				t.Fatalf("createPlatformFile() error = %v", err)
			}
			test.setup(t, root, "entry", file)
			err = validateOpenedEntry(root, "entry", file)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOpenedEntry() error = %v, want substring %q", err, test.want)
			}
			_ = file.Close()
		})
	}
}

// TestValidateOpenedEntryRejectsDirectoryHandle prevents object-type substitution at the retained name.
func TestValidateOpenedEntryRejectsDirectoryHandle(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	if err := os.Mkdir(filepath.Join(directory, "entry"), 0o700); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	defer root.Close()
	file, err := root.Open("entry")
	if err != nil {
		t.Fatalf("Root.Open() error = %v", err)
	}
	defer file.Close()
	if err := validateOpenedEntry(root, "entry", file); !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("validateOpenedEntry() error = %v, want object-type rejection", err)
	}
}

// TestReadBoundedRejectsClosedAndOversizedFiles exercises metadata failures before decoding.
func TestReadBoundedRejectsClosedAndOversizedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(path, []byte("oversized"), privateFileMode); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("os.Open() error = %v", err)
	}
	if _, err := readBounded(file, 1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBounded(oversized) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("File.Close() error = %v", err)
	}
	if _, err := readBounded(file, MaximumRecordBytes); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("readBounded(closed) error = %v", err)
	}
}

// TestUnixPlatformPrimitivesClassifyNativeFailures covers collision, missing source, closed root, and invalid lock handles.
func TestUnixPlatformPrimitivesClassifyNativeFailures(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	file, err := createPlatformFile(root, directory, "entry")
	if err != nil {
		t.Fatalf("createPlatformFile() error = %v", err)
	}
	if _, err := createPlatformFile(root, directory, "entry"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("createPlatformFile(existing) error = %v, want fs.ErrExist", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("File.Close() error = %v", err)
	}
	if applied, err := platformRenameNoReplace(root, directory, "missing", "destination"); applied || err == nil {
		t.Fatalf("platformRenameNoReplace(missing) = %t, %v, want failure", applied, err)
	}
	lock, err := os.Open(filepath.Join(directory, "entry"))
	if err != nil {
		t.Fatalf("os.Open(lock) error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("lock.Close() error = %v", err)
	}
	if err := acquirePlatformLock(context.Background(), lock); err == nil {
		t.Fatal("acquirePlatformLock(closed) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	validLock, err := os.Open(filepath.Join(directory, "entry"))
	if err != nil {
		t.Fatalf("os.Open(valid lock) error = %v", err)
	}
	defer validLock.Close()
	if err := acquirePlatformLock(canceled, validLock); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquirePlatformLock(canceled) error = %v, want context.Canceled", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("Root.Close() error = %v", err)
	}
	if err := platformConfirmEntry(root, directory, "entry"); err == nil {
		t.Fatal("platformConfirmEntry(closed root) error = nil")
	}
}
