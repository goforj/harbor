//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestProjectRemovalIntentWindowsObjectsUseTokenUser verifies every durable object keeps one identity across UAC contexts.
func TestProjectRemovalIntentWindowsObjectsUseTokenUser(t *testing.T) {
	base := t.TempDir()
	journal := newProjectRemovalIntentJournalFixture(base)
	if _, err := journal.LoadOrCreate(context.Background(), "project-orders", "intent-first", nil); err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	root := filepath.Join(base, projectRemovalIntentDirectory)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		validateErr := validateProjectRemovalIntentObject(file, entry.IsDir())
		closeErr := file.Close()
		if err := errors.Join(validateErr, closeErr); err != nil {
			return fmt.Errorf("validate %q: %w", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestProjectRemovalIntentWindowsMigratesNarrowLegacyPolicy verifies prior TokenOwner objects become TokenUser-owned once.
func TestProjectRemovalIntentWindowsMigratesNarrowLegacyPolicy(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, projectRemovalIntentDirectory)
	if err := os.Mkdir(root, projectRemovalIntentDirectoryMode); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	user, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		t.Fatalf("currentProjectRemovalIntentWindowsUserSID() error = %v", err)
	}
	owner, err := currentProjectRemovalIntentWindowsOwnerSID()
	if err != nil {
		t.Fatalf("currentProjectRemovalIntentWindowsOwnerSID() error = %v", err)
	}
	setProjectRemovalIntentLegacyDACL(t, root, true, []string{
		user.String(),
		owner.String(),
		projectRemovalIntentWindowsSystemSID,
		projectRemovalIntentWindowsAdministratorsSID,
	})

	journal := newProjectRemovalIntentJournalFixture(base)
	if _, err := journal.LoadOrCreate(context.Background(), "project-orders", "intent-first", nil); err != nil {
		t.Fatalf("LoadOrCreate(legacy) error = %v", err)
	}
	file, err := os.Open(root)
	if err != nil {
		t.Fatalf("os.Open(root) error = %v", err)
	}
	defer file.Close()
	if err := validateProjectRemovalIntentObject(file, true); err != nil {
		t.Fatalf("validateProjectRemovalIntentObject(migrated) error = %v", err)
	}
}

// TestProjectRemovalIntentWindowsRejectsBroadLegacyPolicy verifies migration never blesses an unexpected principal.
func TestProjectRemovalIntentWindowsRejectsBroadLegacyPolicy(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, projectRemovalIntentDirectory)
	if err := os.Mkdir(root, projectRemovalIntentDirectoryMode); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	user, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		t.Fatalf("currentProjectRemovalIntentWindowsUserSID() error = %v", err)
	}
	setProjectRemovalIntentLegacyDACL(t, root, true, []string{user.String(), "S-1-1-0"})

	journal := newProjectRemovalIntentJournalFixture(base)
	_, err = journal.LoadOrCreate(context.Background(), "project-orders", "intent-first", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be migrated safely") || !strings.Contains(err.Error(), "remove Harbor's project-removal-intents directory") {
		t.Fatalf("LoadOrCreate(broad legacy) error = %v, want actionable migration rejection", err)
	}
	if _, err := os.Lstat(filepath.Join(root, projectRemovalIntentLockFilename)); !os.IsNotExist(err) {
		t.Fatalf("legacy rejection lock error = %v, want no mutation", err)
	}
}

// TestProjectRemovalIntentWindowsSecurityHandleRejectsIdentityMismatch proves migration cannot follow a swapped path.
func TestProjectRemovalIntentWindowsSecurityHandleRejectsIdentityMismatch(t *testing.T) {
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original")
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(originalPath, []byte("original"), projectRemovalIntentFileMode); err != nil {
		t.Fatalf("os.WriteFile(original) error = %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), projectRemovalIntentFileMode); err != nil {
		t.Fatalf("os.WriteFile(other) error = %v", err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatalf("os.Open(original) error = %v", err)
	}
	defer original.Close()

	handle, err := reopenProjectRemovalIntentWindowsSecurityHandleWith(original, false, func(
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
	if handle != windows.InvalidHandle {
		windows.CloseHandle(handle)
		t.Fatalf("reopenProjectRemovalIntentWindowsSecurityHandleWith() handle = %d, want invalid", handle)
	}
	if err == nil || !strings.Contains(err.Error(), "changed before security update") {
		t.Fatalf("reopenProjectRemovalIntentWindowsSecurityHandleWith() error = %v", err)
	}
}

// setProjectRemovalIntentLegacyDACL applies one unprotected legacy-shaped policy without changing its default owner.
func setProjectRemovalIntentLegacyDACL(t *testing.T, path string, directory bool, principals []string) {
	t.Helper()
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	unique := make(map[string]bool, len(principals))
	var sddl strings.Builder
	sddl.WriteString("D:")
	for _, principal := range principals {
		if unique[principal] {
			continue
		}
		unique[principal] = true
		_, _ = fmt.Fprintf(&sddl, "(A;%s;FA;;;%s)", inheritance, principal)
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl.String())
	if err != nil {
		t.Fatalf("windows.SecurityDescriptorFromString() error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("SecurityDescriptor.DACL() error = %v", err)
	}
	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		t.Fatalf("windows.SetNamedSecurityInfo() error = %v", err)
	}
}
