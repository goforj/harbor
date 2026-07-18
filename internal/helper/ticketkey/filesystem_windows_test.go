//go:build windows

package ticketkey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWindowsStoreCreatesProtectedTree verifies every signing-key object has the exact private DACL.
func TestWindowsStoreCreatesProtectedTree(t *testing.T) {
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
		return validatePlatformPath(path, entry.IsDir())
	})
	if err != nil {
		t.Fatalf("validate private Windows tree: %v", err)
	}
}

// TestWindowsStoreRejectsHardLinkedKey verifies NTFS aliases cannot retain an external mutation path.
func TestWindowsStoreRejectsHardLinkedKey(t *testing.T) {
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
}
