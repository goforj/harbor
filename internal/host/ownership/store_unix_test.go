//go:build darwin || linux

package ownership

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestStoreRejectsSpecialRecordAndLockFiles proves FIFOs cannot block or redirect protected state operations.
func TestStoreRejectsSpecialRecordAndLockFiles(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			path := filepath.Join(directory, "owner.json")
			if err := syscall.Mkfifo(path+suffix, 0o600); err != nil {
				t.Fatalf("syscall.Mkfifo() error = %v", err)
			}
			if _, err := NewStore(path); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("NewStore() FIFO error = %v, want ErrUnsafePath", err)
			}
			if info, err := os.Lstat(path + suffix); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("os.Lstat() FIFO = %#v, %v", info, err)
			}
		})
	}
}

// TestUnixStoreRejectsWritableParent prevents another local identity from replacing the fixed record or lock path.
func TestUnixStoreRejectsWritableParent(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	_, err := NewStore(filepath.Join(directory, "owner.json"))
	if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "want exactly 0700") {
		t.Fatalf("NewStore() writable parent error = %v, want ErrUnsafePath mode failure", err)
	}
}

// TestUnixStoreRequiresExactProtectedModes excludes every unreviewed permission and special-bit combination.
func TestUnixStoreRequiresExactProtectedModes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		suffix string
		mode   os.FileMode
	}{
		{name: "group-writable record", mode: 0o620},
		{name: "world-writable record", mode: 0o602},
		{name: "group-readable record", mode: 0o640},
		{name: "world-readable record", mode: 0o604},
		{name: "owner-read-only record", mode: 0o400},
		{name: "setuid record", mode: os.ModeSetuid | 0o600},
		{name: "setgid record", mode: os.ModeSetgid | 0o600},
		{name: "sticky record", mode: os.ModeSticky | 0o600},
		{name: "group-writable lock", suffix: ".lock", mode: 0o620},
		{name: "world-writable lock", suffix: ".lock", mode: 0o602},
		{name: "group-readable lock", suffix: ".lock", mode: 0o640},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			path := filepath.Join(directory, "owner.json")
			if err := os.WriteFile(path+test.suffix, nil, test.mode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			if err := os.Chmod(path+test.suffix, test.mode); err != nil {
				t.Fatalf("os.Chmod() error = %v", err)
			}
			_, err := NewStore(path)
			if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "want exactly 0600") {
				t.Fatalf("NewStore() writable file error = %v, want ErrUnsafePath mode failure", err)
			}
		})
	}
}

// TestUnixStoreRequiresExactDirectoryMode prevents read, traversal, and special-bit access outside the owner identity.
func TestUnixStoreRequiresExactDirectoryMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{
		0o750,
		0o701,
		0o600,
		os.ModeSetuid | 0o700,
		os.ModeSetgid | 0o700,
		os.ModeSticky | 0o700,
	} {
		mode := mode
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			if err := os.Chmod(directory, mode); err != nil {
				t.Fatalf("os.Chmod() error = %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
			_, err := NewStore(filepath.Join(directory, "owner.json"))
			if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "want exactly 0700") {
				t.Fatalf("NewStore() mode %s error = %v, want exact-mode rejection", mode, err)
			}
		})
	}
}

// TestUnixStoreRejectsHardLinkedExistingFiles removes external mutation aliases from record and lock ownership.
func TestUnixStoreRejectsHardLinkedExistingFiles(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			path := filepath.Join(directory, "owner.json")
			if err := os.WriteFile(path+suffix, nil, privateFileMode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			if err := os.Link(path+suffix, filepath.Join(directory, "alias")); err != nil {
				t.Fatalf("os.Link() error = %v", err)
			}
			_, err := NewStore(path)
			if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("NewStore() hard-link error = %v, want ErrUnsafePath hard-link failure", err)
			}
		})
	}
}

// TestUnixOwnerValidationRejectsForeignIdentity proves ownership comparison is against the elevated helper user.
func TestUnixOwnerValidationRejectsForeignIdentity(t *testing.T) {
	t.Parallel()
	foreign := uint32(os.Geteuid() + 1)
	if err := validateUnixOwnerID(foreign, "file"); err == nil || !strings.Contains(err.Error(), "elevated helper") {
		t.Fatalf("validateUnixOwnerID() error = %v, want elevated-helper ownership failure", err)
	}
}

// TestStoreCreatesOwnerOnlyFiles keeps both durable state and its coordination boundary private by default.
func TestStoreCreatesOwnerOnlyFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Claim(nil, testRecord()); err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	for _, candidate := range []string{path, path + ".lock"} {
		info, err := os.Lstat(candidate)
		if err != nil {
			t.Fatalf("os.Lstat(%q) error = %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != privateFileMode {
			t.Errorf("mode for %q = %04o, want %04o", candidate, got, privateFileMode)
		}
	}
}
