//go:build windows

package ticketspool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWindowsPendingDescriptorRequiresExactBoundary covers every owner, protection, entry, mask, flag, and principal dimension.
func TestWindowsPendingDescriptorRequiresExactBoundary(t *testing.T) {
	owner, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("resolve current user: %v", err)
	}
	user := owner.String()
	other := "S-1-1-0"
	fileEntries := "(A;;FA;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")"
	directoryEntries := "(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;" + windowsAdministratorsSID + ")(A;OICI;FA;;;" + windowsSystemSID + ")"
	tests := []struct {
		name      string
		sddl      string
		directory bool
		want      string
	}{
		{name: "valid file", sddl: "O:" + user + "D:P" + fileEntries},
		{name: "valid directory", sddl: "O:" + user + "D:P" + directoryEntries, directory: true},
		{name: "unprotected", sddl: "O:" + user + "D:" + fileEntries, want: "not protected"},
		{name: "wrong owner", sddl: "O:" + windowsSystemSID + "D:P" + fileEntries, want: "owner is"},
		{name: "missing principal", sddl: "O:" + user + "D:P(A;;FA;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")", want: "want 3"},
		{name: "additional principal", sddl: "O:" + user + "D:P" + fileEntries + "(A;;FA;;;" + other + ")", want: "want 3"},
		{name: "deny entry", sddl: "O:" + user + "D:P(D;;FA;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")", want: "not an allow"},
		{name: "generic all", sddl: "O:" + user + "D:P(A;;GA;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")", want: "access mask"},
		{name: "weakened access", sddl: "O:" + user + "D:P(A;;FR;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")", want: "access mask"},
		{name: "file inheritance flags", sddl: "O:" + user + "D:P" + directoryEntries, want: "flags"},
		{name: "directory missing inheritance flags", sddl: "O:" + user + "D:P" + fileEntries, directory: true, want: "flags"},
		{name: "unexpected principal", sddl: "O:" + user + "D:P(A;;FA;;;" + user + ")(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + other + ")", want: "unexpected or duplicate"},
		{name: "duplicate principal", sddl: "O:" + user + "D:P(A;;FA;;;" + user + ")(A;;FA;;;" + user + ")(A;;FA;;;" + windowsSystemSID + ")", want: "unexpected or duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatalf("build descriptor: %v", err)
			}
			err = validateWindowsDescriptor(descriptor, test.directory)
			if test.want == "" && err != nil {
				t.Fatalf("validateWindowsDescriptor() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("validateWindowsDescriptor() error = %v, want %q", err, test.want)
			}
		})
	}

	nullDACL, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatalf("create null-DACL descriptor: %v", err)
	}
	if err := nullDACL.SetOwner(owner, false); err != nil {
		t.Fatalf("set null-DACL owner: %v", err)
	}
	if err := nullDACL.SetDACL(nil, true, false); err != nil {
		t.Fatalf("set null DACL: %v", err)
	}
	if err := nullDACL.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		t.Fatalf("protect null DACL: %v", err)
	}
	if err := validateWindowsDescriptor(nullDACL, false); err == nil || !strings.Contains(err.Error(), "want 3") {
		t.Fatalf("validateWindowsDescriptor(null DACL) error = %v", err)
	}
	absentDACL, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatalf("create absent-DACL descriptor: %v", err)
	}
	if err := absentDACL.SetOwner(owner, false); err != nil {
		t.Fatalf("set absent-DACL owner: %v", err)
	}
	if err := absentDACL.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		t.Fatalf("protect absent DACL: %v", err)
	}
	if err := validateWindowsDescriptor(absentDACL, false); err == nil || !strings.Contains(err.Error(), "access list") {
		t.Fatalf("validateWindowsDescriptor(absent DACL) error = %v", err)
	}
}

// prepareTestDirectory creates the exact protected pending boundary in the Windows creation syscall.
func prepareTestDirectory(t *testing.T, path string) {
	t.Helper()
	descriptor, _, err := windowsPendingDescriptor(true)
	if err != nil {
		t.Fatalf("build private test descriptor: %v", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode private test directory: %v", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	err = windows.CreateDirectory(pathPointer, attributes)
	runtime.KeepAlive(descriptor)
	if err != nil {
		t.Fatalf("create private test directory: %v", err)
	}
}

// makeTestDirectoryUnsafe creates an inherited boundary that cannot satisfy the exact protected DACL.
func makeTestDirectoryUnsafe(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("create unsafe test directory: %v", err)
	}
}

// assertTestDirectoryUnsafe verifies a failed Open did not replace the broad inherited Windows policy.
func assertTestDirectoryUnsafe(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open unsafe test directory: %v", err)
	}
	validationErr := validatePlatformObject(file, nil, true)
	closeErr := file.Close()
	if validationErr == nil {
		t.Fatal("Open repaired the unsafe Windows directory policy")
	}
	if closeErr != nil {
		t.Fatalf("close unsafe test directory: %v", closeErr)
	}
}

// assertPrivateRegularFile verifies the final file retains the exact protected Windows policy.
func assertPrivateRegularFile(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open published file: %v", err)
	}
	validationErr := validatePlatformObject(file, nil, false)
	closeErr := file.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		t.Fatalf("validate published file: %v", err)
	}
}

// makeOpenedTestDirectoryUnsafe replaces the protected DACL and returns its exact restoration.
func makeOpenedTestDirectoryUnsafe(t *testing.T, path string) func() {
	t.Helper()
	owner, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("resolve current user: %v", err)
	}
	broad, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:(A;OICI;FA;;;%s)", owner.String(), owner.String()))
	if err != nil {
		t.Fatalf("build unsafe directory descriptor: %v", err)
	}
	setWindowsTestDescriptor(t, path, broad, false)
	return func() {
		exact, _, err := windowsPendingDescriptor(true)
		if err != nil {
			t.Fatalf("build restored directory descriptor: %v", err)
		}
		setWindowsTestDescriptor(t, path, exact, true)
	}
}

// makeStagedFileUnsafe removes the protected exact DACL from one staged file.
func makeStagedFileUnsafe(t *testing.T, root *os.Root, name string) {
	t.Helper()
	owner, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("resolve current user: %v", err)
	}
	broad, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:(A;;FA;;;%s)", owner.String(), owner.String()))
	if err != nil {
		t.Fatalf("build unsafe file descriptor: %v", err)
	}
	setWindowsTestDescriptor(t, filepath.Join(root.Name(), name), broad, false)
}

// setWindowsTestDescriptor applies one deliberate test policy without changing production creation code.
func setWindowsTestDescriptor(t *testing.T, path string, descriptor *windows.SECURITY_DESCRIPTOR, protected bool) {
	t.Helper()
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read test DACL: %v", err)
	}
	securityInformation := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		securityInformation = windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInformation, nil, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	if err != nil {
		t.Fatalf("apply test DACL: %v", err)
	}
}

// writePrivateFile creates one fixture through the same exact Windows creation boundary as production.
func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	directoryPath := filepath.Dir(path)
	root, err := os.OpenRoot(directoryPath)
	if err != nil {
		t.Fatalf("open private fixture root: %v", err)
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		t.Fatalf("open private fixture directory: %v", err)
	}
	defer func() { _ = directory.Close() }()
	file, err := createPlatformFile(root, directory, directoryPath, filepath.Base(path))
	if err != nil {
		t.Fatalf("create private fixture: %v", err)
	}
	if err := writeAll(file, content); err != nil {
		_ = file.Close()
		t.Fatalf("write private fixture: %v", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync private fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close private fixture: %v", err)
	}
}
