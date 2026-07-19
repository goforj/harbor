//go:build windows

package ticketkey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsSecureCreatedObjectAppliesTokenUserOwner verifies default ownership cannot vary across elevation contexts.
func TestWindowsSecureCreatedObjectAppliesTokenUserOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ticket-keys")
	if err := preparePlatformRoot(root); err != nil {
		t.Fatalf("preparePlatformRoot() error = %v", err)
	}
	path := filepath.Join(root, "ticket-key")
	if err := os.WriteFile(path, nil, privateFileMode); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("os.Open() error = %v", err)
	}
	defer file.Close()
	beforeDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("windows.GetSecurityInfo() error = %v", err)
	}
	beforeOwner, _, err := beforeDescriptor.Owner()
	if err != nil {
		t.Fatalf("SecurityDescriptor.Owner() error = %v", err)
	}
	want, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("currentWindowsUserSID() error = %v", err)
	}
	if beforeOwner != nil && !beforeOwner.Equals(want) {
		t.Logf("default owner %q differs from TokenUser %q", beforeOwner.String(), want.String())
	}
	if err := platformSecureCreatedFile(file, false); err != nil {
		t.Fatalf("platformSecureCreatedFile() error = %v", err)
	}
	afterDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("windows.GetSecurityInfo(after) error = %v", err)
	}
	afterOwner, _, err := afterDescriptor.Owner()
	if err != nil {
		t.Fatalf("SecurityDescriptor.Owner(after) error = %v", err)
	}
	if afterOwner == nil || !afterOwner.Equals(want) {
		got := ""
		if afterOwner != nil {
			got = afterOwner.String()
		}
		t.Fatalf("secured object owner = %q, want TokenUser %q", got, want.String())
	}
}

// TestWindowsSecurityHandleRejectsReopenIdentityMismatch proves a path swap cannot redirect DACL protection.
func TestWindowsSecurityHandleRejectsReopenIdentityMismatch(t *testing.T) {
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original")
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(originalPath, []byte("original"), privateFileMode); err != nil {
		t.Fatalf("WriteFile(original) error = %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), privateFileMode); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatalf("Open(original) error = %v", err)
	}
	defer original.Close()

	securityHandle, err := reopenWindowsSecurityHandleWith(original, false, func(
		_ *uint16,
		access uint32,
		share uint32,
		attributes *windows.SecurityAttributes,
		creation uint32,
		flags uint32,
		template windows.Handle,
	) (windows.Handle, error) {
		otherPointer, pointerErr := windows.UTF16PtrFromString(otherPath)
		if pointerErr != nil {
			return windows.InvalidHandle, pointerErr
		}
		return windows.CreateFile(otherPointer, access, share, attributes, creation, flags, template)
	})
	if securityHandle != windows.InvalidHandle {
		windows.CloseHandle(securityHandle)
		t.Fatalf("reopenWindowsSecurityHandleWith() handle = %d, want invalid", securityHandle)
	}
	if err == nil || !strings.Contains(err.Error(), "changed before DACL protection") {
		t.Fatalf("reopenWindowsSecurityHandleWith() error = %v", err)
	}
}

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
