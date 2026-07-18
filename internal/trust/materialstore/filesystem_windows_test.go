//go:build windows

package materialstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsSecureCreatedPathUsesObjectSpecificACEFlags verifies directories inherit private grants while files carry effective grants only.
func TestWindowsSecureCreatedPathUsesObjectSpecificACEFlags(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	if err := preparePlatformRoot(directory); err != nil {
		t.Fatalf("preparePlatformRoot() error = %v", err)
	}
	childDirectory := filepath.Join(directory, "generation")
	if err := os.Mkdir(childDirectory, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	childHandle, err := os.Open(childDirectory)
	if err != nil {
		t.Fatalf("Open(directory) error = %v", err)
	}
	if err := platformSecureCreatedFile(childHandle, true); err != nil {
		t.Fatalf("platformSecureCreatedFile(directory) error = %v", err)
	}
	if err := childHandle.Close(); err != nil {
		t.Fatalf("Close(directory) error = %v", err)
	}
	if err := validatePlatformPath(childDirectory, true); err != nil {
		t.Fatalf("validatePlatformPath(directory) error = %v", err)
	}
	privateKey := filepath.Join(childDirectory, privateKeyFilename)
	if err := os.WriteFile(privateKey, []byte("private"), privateFileMode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	keyHandle, err := os.Open(privateKey)
	if err != nil {
		t.Fatalf("Open(file) error = %v", err)
	}
	if err := platformSecureCreatedFile(keyHandle, false); err != nil {
		t.Fatalf("platformSecureCreatedFile(file) error = %v", err)
	}
	if err := keyHandle.Close(); err != nil {
		t.Fatalf("Close(file) error = %v", err)
	}
	if err := validatePlatformPath(privateKey, false); err != nil {
		t.Fatalf("validatePlatformPath(file) error = %v", err)
	}
}

// TestWindowsStoreCreatesProtectedTree verifies every root, manifest, generation, and key has the exact private DACL.
func TestWindowsStoreCreatesProtectedTree(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
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

// TestWindowsStoreRejectsUnprotectedDACL verifies inherited broad access cannot be accepted after tampering.
func TestWindowsStoreRejectsUnprotectedDACL(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	path := filepath.Join(directory, filepath.FromSlash(authorityDirectory))
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;WD)")
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString() error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("DACL() error = %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo() error = %v", err)
	}
	runtime.KeepAlive(descriptor)
	if err := validatePlatformPath(path, true); err == nil || !strings.Contains(err.Error(), "not protected") {
		t.Fatalf("validatePlatformPath(unprotected) error = %v", err)
	}
}

// TestWindowsStoreRejectsUnexpectedDACLPrincipal verifies private state cannot grant the Everyone SID.
func TestWindowsStoreRejectsUnexpectedDACLPrincipal(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	path := filepath.Join(directory, filepath.FromSlash(authorityDirectory))
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString() error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("DACL() error = %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo() error = %v", err)
	}
	runtime.KeepAlive(descriptor)
	if err := validatePlatformPath(path, true); err == nil || !strings.Contains(err.Error(), "want 2") {
		t.Fatalf("validatePlatformPath(Everyone) error = %v", err)
	}
}

// TestWindowsStoreRejectsHardLinkedPrivateKey verifies NTFS aliases cannot retain an external mutation path.
func TestWindowsStoreRejectsHardLinkedPrivateKey(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	key := filepath.Join(directory, filepath.FromSlash(authorityGenerations), authority.Material().Fingerprint, privateKeyFilename)
	alias := filepath.Join(directory, "key-alias")
	if err := os.Link(key, alias); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("LoadAuthority(hard-linked key) error = %v", err)
	}
}

// TestWindowsStoreRejectsInheritanceOnlyGrants verifies nominal principals still need effective access on the object.
func TestWindowsStoreRejectsInheritanceOnlyGrants(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	path := filepath.Join(directory, filepath.FromSlash(authorityDirectory))
	owner, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("currentWindowsUserSID() error = %v", err)
	}
	sddl := "D:P(A;OICIIO;FA;;;" + owner.String() + ")(A;OICIIO;FA;;;" + windowsSystemSID + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString() error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("DACL() error = %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("SetNamedSecurityInfo() error = %v", err)
	}
	runtime.KeepAlive(descriptor)
	if err := validatePlatformPath(path, true); err == nil || !strings.Contains(err.Error(), "flags") {
		t.Fatalf("validatePlatformPath(inherit-only) error = %v", err)
	}
}
