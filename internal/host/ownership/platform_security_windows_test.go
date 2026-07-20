//go:build windows

package ownership

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareTestStoreDirectory gives a temporary test directory the reviewed descriptor without invalidating retained roots.
func prepareTestStoreDirectory(t *testing.T, directory string) {
	t.Helper()
	if info, err := os.Stat(directory); err == nil {
		if validatePlatformDirectory(directory, info) == nil {
			return
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatalf("os.ReadDir(%q) error = %v", directory, readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("cannot replace unprotected non-empty test directory %q", directory)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) error = %v", directory, err)
	}
	descriptor, _, err := windowsOwnershipDescriptor(true)
	if err != nil {
		t.Fatalf("windowsOwnershipDescriptor() error = %v", err)
	}
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatalf("windows.UTF16PtrFromString(%q) error = %v", directory, err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatalf("os.Remove(%q) error = %v", directory, err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(path, attributes); err != nil {
		t.Fatalf("windows.CreateDirectory(%q) error = %v", directory, err)
	}
	runtime.KeepAlive(descriptor)
}

// TestWindowsStoreCreatesProtectedBoundary verifies the parent, lock, and published record use the exact reviewed DACL.
func TestWindowsStoreCreatesProtectedBoundary(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	path := filepath.Join(directory, "owner.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer store.Close()
	if _, err := store.Claim(context.Background(), testRecord()); err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatalf("os.Lstat(directory) error = %v", err)
	}
	if err := validatePlatformDirectory(directory, info); err != nil {
		t.Fatalf("validatePlatformDirectory() error = %v", err)
	}
	for _, candidate := range []string{path, path + ".lock"} {
		file, err := os.Open(candidate)
		if err != nil {
			t.Fatalf("os.Open(%q) error = %v", candidate, err)
		}
		info, statErr := file.Stat()
		validateErr := validatePlatformFile(file, info, false)
		closeErr := file.Close()
		if err := errors.Join(statErr, validateErr, closeErr); err != nil {
			t.Fatalf("validate protected file %q: %v", candidate, err)
		}
	}
}

// TestWindowsClaimRetryConfirmsAppliedRename proves replay cannot grant authority after an unconfirmed rename flush.
func TestWindowsClaimRetryConfirmsAppliedRename(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	path := filepath.Join(directory, "owner.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer store.Close()

	originalFlush := flushOwnershipFile
	flushCalls := 0
	flushOwnershipFile = func(handle windows.Handle) error {
		flushCalls++
		if flushCalls == 1 {
			return windows.ERROR_WRITE_FAULT
		}
		return originalFlush(handle)
	}
	defer func() { flushOwnershipFile = originalFlush }()

	if _, err := store.Claim(context.Background(), testRecord()); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Claim() unconfirmed error = %v, want ErrDurabilityUncertain", err)
	}
	observed, err := store.Observe(context.Background())
	if err != nil || !observed.Exists {
		t.Fatalf("Store.Observe() after applied rename = %#v, %v, want existing", observed, err)
	}
	replayed, err := store.Claim(context.Background(), testRecord())
	if err != nil {
		t.Fatalf("Store.Claim() confirmed retry error = %v", err)
	}
	if replayed != observed {
		t.Fatalf("Store.Claim() confirmed retry = %#v, want %#v", replayed, observed)
	}
	if flushCalls != 2 {
		t.Fatalf("flushOwnershipFile calls = %d, want rename failure plus retry confirmation", flushCalls)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != "owner.json" || entries[1].Name() != "owner.json.lock" {
		t.Fatalf("store entries = %#v, want only active record and lock", entries)
	}
}

// TestWindowsUpgradeRetryConfirmsAppliedReplacement proves overwrite and replay share the reviewed write-through flush boundary.
func TestWindowsUpgradeRetryConfirmsAppliedReplacement(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	path := filepath.Join(directory, "owner.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer store.Close()
	target := testNetworkPolicyRecord()
	source := target
	source.SchemaVersion = IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	claimed, err := store.Claim(context.Background(), source)
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}

	originalFlush := flushOwnershipFile
	flushCalls := 0
	flushOwnershipFile = func(handle windows.Handle) error {
		flushCalls++
		if flushCalls == 1 {
			return windows.ERROR_WRITE_FAULT
		}
		return originalFlush(handle)
	}
	defer func() { flushOwnershipFile = originalFlush }()

	if _, err := store.Upgrade(context.Background(), claimed.Fingerprint, target); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Upgrade() unconfirmed error = %v, want ErrDurabilityUncertain", err)
	}
	observed, err := store.Observe(context.Background())
	if err != nil || observed.Record != target {
		t.Fatalf("Store.Observe() after applied replacement = %#v, %v, want target", observed, err)
	}
	replayed, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil || replayed != observed {
		t.Fatalf("Store.Upgrade() confirmed retry = %#v, %v, want %#v", replayed, err, observed)
	}
	if flushCalls != 2 {
		t.Fatalf("flushOwnershipFile calls = %d, want replacement failure plus retry confirmation", flushCalls)
	}
	assertOwnershipEntries(t, path, true)
}

// TestWindowsNewStoreRejectsUnprotectedParent proves inherited grants cannot authorize machine-global mutation.
func TestWindowsNewStoreRejectsUnprotectedParent(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	sddl := "D:(A;OICI;FA;;;" + windowsAdministratorsSID + ")(A;OICI;FA;;;" + windowsSystemSID + ")"
	setWindowsTestDACL(t, directory, sddl, false)
	_, err := NewStore(filepath.Join(directory, "owner.json"))
	if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "not protected") {
		t.Fatalf("NewStore() unprotected parent error = %v, want ErrUnsafePath protected-DACL failure", err)
	}
}

// TestWindowsOwnershipDescriptorExcludesInteractiveSID proves UAC's filtered token cannot rewrite privileged state.
func TestWindowsOwnershipDescriptorExcludesInteractiveSID(t *testing.T) {
	descriptor, owner, err := windowsOwnershipDescriptor(true)
	if err != nil {
		t.Fatalf("windowsOwnershipDescriptor() error = %v", err)
	}
	if owner.String() != windowsAdministratorsSID {
		t.Fatalf("windowsOwnershipDescriptor() owner = %q, want %q", owner.String(), windowsAdministratorsSID)
	}
	if err := validateWindowsDACL(descriptor, windowsAdministratorsSID, true); err != nil {
		t.Fatalf("validateWindowsDACL() error = %v", err)
	}

	interactive := "S-1-5-21-100-200-300-1001"
	tampered, err := windows.SecurityDescriptorFromString(
		"O:" + windowsAdministratorsSID + "D:P(A;OICI;FA;;;" + interactive + ")(A;OICI;FA;;;" + windowsSystemSID + ")",
	)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString() error = %v", err)
	}
	if err := validateWindowsDACL(tampered, windowsAdministratorsSID, true); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("validateWindowsDACL() interactive grant error = %v", err)
	}
}

// TestWindowsOwnershipSecurityUpdateRequestsOnlyNecessaryAuthority verifies an existing machine owner never requires WRITE_OWNER.
func TestWindowsOwnershipSecurityUpdateRequestsOnlyNecessaryAuthority(t *testing.T) {
	administrators, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		t.Fatalf("windows.StringToSid(Administrators) error = %v", err)
	}
	interactive, err := windows.StringToSid("S-1-5-21-100-200-300-1001")
	if err != nil {
		t.Fatalf("windows.StringToSid(interactive) error = %v", err)
	}

	information, ownerAccess := windowsOwnershipSecurityUpdate(administrators, administrators)
	if information&windows.OWNER_SECURITY_INFORMATION != 0 || ownerAccess != 0 {
		t.Fatalf("existing owner update = (%#x, %#x), want no owner mutation", information, ownerAccess)
	}
	if information&windows.DACL_SECURITY_INFORMATION == 0 || information&windows.PROTECTED_DACL_SECURITY_INFORMATION == 0 {
		t.Fatalf("existing owner update information = %#x, want protected DACL mutation", information)
	}

	information, ownerAccess = windowsOwnershipSecurityUpdate(interactive, administrators)
	if information&windows.OWNER_SECURITY_INFORMATION == 0 || ownerAccess != windows.WRITE_OWNER {
		t.Fatalf("different owner update = (%#x, %#x), want owner mutation", information, ownerAccess)
	}
}

// TestWindowsSecurityDescriptorRequiresExactBoundary covers every descriptor dimension without path setup hiding failures.
func TestWindowsSecurityDescriptorRequiresExactBoundary(t *testing.T) {
	interactive := "S-1-5-21-100-200-300-1001"
	tests := []struct {
		name      string
		sddl      string
		directory bool
		want      string
	}{
		{
			name: "valid file",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
		},
		{
			name:      "valid directory",
			sddl:      "O:" + windowsAdministratorsSID + "D:P(A;OICI;FA;;;" + windowsAdministratorsSID + ")(A;OICI;FA;;;" + windowsSystemSID + ")",
			directory: true,
		},
		{
			name: "generic all is not expanded",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;GA;;;" + windowsAdministratorsSID + ")(A;;GA;;;" + windowsSystemSID + ")",
			want: "access mask",
		},
		{
			name: "weakened access",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FR;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "access mask",
		},
		{
			name: "interactive grant",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + interactive + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "unexpected",
		},
		{
			name: "additional grant",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")(A;;FA;;;" + interactive + ")",
			want: "want 2",
		},
		{
			name: "duplicate grant",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsAdministratorsSID + ")",
			want: "duplicate",
		},
		{
			name: "missing system grant",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")",
			want: "want 2",
		},
		{
			name: "deny entry",
			sddl: "O:" + windowsAdministratorsSID + "D:P(D;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "not an allow",
		},
		{
			name: "file inheritance flags",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;OICI;FA;;;" + windowsAdministratorsSID + ")(A;OICI;FA;;;" + windowsSystemSID + ")",
			want: "flags",
		},
		{
			name:      "directory missing inheritance flags",
			sddl:      "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			directory: true,
			want:      "flags",
		},
		{
			name: "unprotected",
			sddl: "O:" + windowsAdministratorsSID + "D:(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "not protected",
		},
		{
			name: "wrong owner",
			sddl: "O:" + windowsSystemSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "object owner",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatalf("windows.SecurityDescriptorFromString() error = %v", err)
			}
			err = validateWindowsSecurityDescriptor(descriptor, test.directory)
			if test.want == "" && err != nil {
				t.Fatalf("validateWindowsSecurityDescriptor() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateWindowsSecurityDescriptor() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestWindowsNewStoreRejectsUnexpectedDACLPrincipal prevents nominally protected files from granting another identity access.
func TestWindowsNewStoreRejectsUnexpectedDACLPrincipal(t *testing.T) {
	for _, suffix := range []string{"", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			path := filepath.Join(directory, "owner.json")
			if err := os.WriteFile(path+suffix, nil, privateFileMode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			setWindowsTestDACL(t, path+suffix, "D:P(A;;FA;;;WD)", true)
			_, err := NewStore(path)
			if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "want 2") {
				t.Fatalf("NewStore() unexpected-principal error = %v, want ErrUnsafePath exact-DACL failure", err)
			}
		})
	}
}

// TestWindowsNewStoreRejectsHardLinkedFiles removes external aliases from both persisted state and lock authority.
func TestWindowsNewStoreRejectsHardLinkedFiles(t *testing.T) {
	for _, suffix := range []string{"", ".lock"} {
		t.Run(suffix, func(t *testing.T) {
			directory := t.TempDir()
			prepareTestStoreDirectory(t, directory)
			path := filepath.Join(directory, "owner.json")
			file, err := os.OpenFile(path+suffix, os.O_CREATE|os.O_EXCL|os.O_RDWR, privateFileMode)
			if err != nil {
				t.Fatalf("os.OpenFile() error = %v", err)
			}
			secureErr := securePlatformFile(file, false)
			closeErr := file.Close()
			if err := errors.Join(secureErr, closeErr); err != nil {
				t.Fatalf("secure fixture file: %v", err)
			}
			if err := os.Link(path+suffix, filepath.Join(directory, "alias")); err != nil {
				t.Fatalf("os.Link() error = %v", err)
			}
			_, err = NewStore(path)
			if !errors.Is(err, ErrUnsafePath) || !strings.Contains(err.Error(), "hard links") {
				t.Fatalf("NewStore() hard-link error = %v, want ErrUnsafePath hard-link failure", err)
			}
		})
	}
}

// setWindowsTestDACL applies a deterministic tampered access list while preserving the current object owner.
func setWindowsTestDACL(t *testing.T, path string, sddl string, protected bool) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("windows.SecurityDescriptorFromString() error = %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("SecurityDescriptor.DACL() error = %v", err)
	}
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		information = windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
		t.Fatalf("windows.SetNamedSecurityInfo() error = %v", err)
	}
	runtime.KeepAlive(descriptor)
}
