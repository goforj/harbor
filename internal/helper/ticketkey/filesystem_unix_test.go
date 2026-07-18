//go:build darwin || linux

package ticketkey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestUnixStoreCreatesOwnerOnlyTree verifies private modes exist from the root through the immutable key file.
func TestUnixStoreCreatesOwnerOnlyTree(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	store := mustStore(t, directory)
	defer store.Close()
	if _, err := store.LoadOrCreate(context.Background()); err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(privateFileMode)
		if entry.IsDir() {
			want = privateDirectoryMode
		}
		if info.Mode().Perm() != want {
			return errors.New(path + " has mode " + info.Mode().Perm().String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("private tree validation: %v", err)
	}
}

// TestUnixStoreRejectsPermissiveRoot verifies startup does not silently repair exposed key storage.
func TestUnixStoreRejectsPermissiveRoot(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Open(permissive root) error = %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("root mode = %04o, want original 0755", info.Mode().Perm())
	}
}

// TestUnixStoreRejectsSymlinksAndHardLinks verifies alternate paths cannot select or retain private key bytes.
func TestUnixStoreRejectsSymlinksAndHardLinks(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(target) error = %v", err)
		}
		link := filepath.Join(parent, "helper-ticket-key")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(root) error = %v", err)
		}
		if _, err := Open(link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("Open(symlink root) error = %v", err)
		}
	})

	t.Run("key symlink", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		if err := os.Mkdir(filepath.Join(directory, activeDirectory), privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(active) error = %v", err)
		}
		external := filepath.Join(t.TempDir(), "foreign.json")
		if err := os.WriteFile(external, []byte("{}"), privateFileMode); err != nil {
			t.Fatalf("WriteFile(external) error = %v", err)
		}
		if err := os.Symlink(external, filepath.Join(directory, activeDirectory, keyFilename)); err != nil {
			t.Fatalf("Symlink(key) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected entry") {
			t.Fatalf("LoadOrCreate(symlink key) error = %v", err)
		}
	})

	t.Run("hard-linked key", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		if _, err := store.LoadOrCreate(context.Background()); err != nil {
			t.Fatalf("LoadOrCreate() error = %v", err)
		}
		key := filepath.Join(directory, activeDirectory, keyFilename)
		if err := os.Link(key, filepath.Join(directory, "key-alias")); err != nil {
			t.Fatalf("Link() error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("LoadOrCreate(hard-linked key) error = %v", err)
		}
	})

	t.Run("active symlink", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		external := filepath.Join(t.TempDir(), "active")
		if err := os.Mkdir(external, privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(external active) error = %v", err)
		}
		if err := os.Symlink(external, filepath.Join(directory, activeDirectory)); err != nil {
			t.Fatalf("Symlink(active) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "direct object") {
			t.Fatalf("LoadOrCreate(symlink active) error = %v", err)
		}
	})
}

// TestUnixStoreRejectsNonRegularAndPermissiveState verifies special or exposed descendants never enter key decoding.
func TestUnixStoreRejectsNonRegularAndPermissiveState(t *testing.T) {
	t.Run("non-regular key", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		active := filepath.Join(directory, activeDirectory)
		if err := os.Mkdir(active, privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(active) error = %v", err)
		}
		if err := os.Mkdir(filepath.Join(active, keyFilename), privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(key) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected entry") {
			t.Fatalf("LoadOrCreate(non-regular key) error = %v", err)
		}
	})

	t.Run("permissive key", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		if _, err := store.LoadOrCreate(context.Background()); err != nil {
			t.Fatalf("LoadOrCreate() error = %v", err)
		}
		key := filepath.Join(directory, activeDirectory, keyFilename)
		if err := os.Chmod(key, 0o644); err != nil {
			t.Fatalf("Chmod(key) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("LoadOrCreate(permissive key) error = %v", err)
		}
	})

	t.Run("named pipe key", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		active := filepath.Join(directory, activeDirectory)
		if err := os.Mkdir(active, privateDirectoryMode); err != nil {
			t.Fatalf("Mkdir(active) error = %v", err)
		}
		if err := syscall.Mkfifo(filepath.Join(active, keyFilename), uint32(privateFileMode)); err != nil {
			t.Fatalf("Mkfifo(key) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("LoadOrCreate(named pipe key) error = %v", err)
		}
	})

	t.Run("permissive active directory", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "helper-ticket-key")
		store := mustStore(t, directory)
		defer store.Close()
		if _, err := store.LoadOrCreate(context.Background()); err != nil {
			t.Fatalf("LoadOrCreate() error = %v", err)
		}
		if err := os.Chmod(filepath.Join(directory, activeDirectory), 0o755); err != nil {
			t.Fatalf("Chmod(active) error = %v", err)
		}
		if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("LoadOrCreate(permissive active) error = %v", err)
		}
	})
}
