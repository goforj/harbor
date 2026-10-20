//go:build windows

package windowsinstallation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestIntegrationShortcutNamespaceDescriptor restricts shell deletion exceptions to read and deletion grants.
func TestIntegrationShortcutNamespaceDescriptor(t *testing.T) {
	for _, test := range []struct {
		name, sddl string
		allowed    bool
	}{
		{"readdelete", "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x1300e9;;;BU)", true},
		{"readonly", "O:BAD:P(A;;FA;;;BA)(A;;FR;;;BU)", true},
		{"systemowner", "O:SYD:P(A;;FA;;;SY)(A;;0x1300e9;;;BU)", true},
		{"foreignowner", "O:BUD:P(A;;FA;;;BA)(A;;FR;;;BU)", false},
		{"nullacl", "O:BA", false},
		{"deny", "O:BAD:P(D;;SD;;;BU)(A;;FA;;;BA)", false},
		{"addfile", "O:BAD:P(A;;FA;;;BA)(A;;0x2;;;BU)", false},
		{"adddirectory", "O:BAD:P(A;;FA;;;BA)(A;;0x4;;;BU)", false},
		{"writeea", "O:BAD:P(A;;FA;;;BA)(A;;0x10;;;BU)", false},
		{"writeattributes", "O:BAD:P(A;;FA;;;BA)(A;;0x100;;;BU)", false},
		{"writedacl", "O:BAD:P(A;;FA;;;BA)(A;;WD;;;BU)", false},
		{"writeowner", "O:BAD:P(A;;FA;;;BA)(A;;WO;;;BU)", false},
		{"genericwrite", "O:BAD:P(A;;FA;;;BA)(A;;GW;;;BU)", false},
		{"genericall", "O:BAD:P(A;;FA;;;BA)(A;;GA;;;BU)", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := integrationAdmitShortcutNamespace(descriptor); (err == nil) != test.allowed {
				t.Fatalf("namespace admission=%v want allowed=%v", err, test.allowed)
			}
		})
	}
}

// TestNativeIntegrationShortcutNamespaceBoundary proves shell parent deletion rights cannot replace retained owned children.
func TestNativeIntegrationShortcutNamespaceBoundary(t *testing.T) {
	root := integrationTestAnchor(t)
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	user, err := source.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	programs := filepath.Join(root, "Programs")
	integrationNamespaceCreateDirectory(t, programs, "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;;0x1300e9;;;"+user.User.Sid.String()+")")
	positive := filepath.Join(programs, "PositiveControl")
	integrationNamespaceCreateDirectory(t, positive, integrationShortcutDirectorySDDL)
	spec := integrationShortcutSpec{Programs: programs, Target: filepath.Join(root, "desktop-launcher.exe"), WorkingDirectory: root, Icon: filepath.Join(root, "harbor-desktop.exe")}
	shortcut, err := integrationCreateShortcut(spec)
	if err != nil {
		t.Fatal(err)
	}
	receipt := shortcut.Receipt
	defer shortcut.Close()
	restricted, err := restrictedRuntimeToken(source)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	// Keep every low operation on the impersonated thread, including native deletion and rename requests.
	func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.SetThreadToken(nil, impersonation); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := windows.RevertToSelf(); err != nil {
				t.Fatalf("restore high fixture thread: %v", err)
			}
		}()
		var actual windows.Token
		if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &actual); err != nil {
			t.Fatal(err)
		}
		facts, err := inspectRuntimeToken(actual)
		actual.Close()
		if err != nil {
			t.Fatal(err)
		}
		if facts.Elevation != 0 || facts.UserSID != user.User.Sid.String() {
			t.Fatalf("wrong effective namespace authority: %+v", facts)
		}
		t.Logf("effective restricted shortcut namespace token: %+v", facts)
		pointer, _ := windows.UTF16PtrFromString(positive)
		handle, err := windows.CreateFile(pointer, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatalf("parent DELETE_CHILD positive control could not open owned child: %v", err)
		}
		file := os.NewFile(uintptr(handle), positive)
		if err := retireIntegrationFile(file); err != nil {
			file.Close()
			t.Fatalf("parent DELETE_CHILD positive control could not delete owned child: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		pointer, _ = windows.UTF16PtrFromString(receipt.Directory.Path)
		handle, err = windows.CreateFile(pointer, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			windows.CloseHandle(handle)
			t.Error("retained shortcut directory admitted a DELETE handle")
		} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Errorf("retained directory expected sharing violation, got %v", err)
		}
		renamed, _ := windows.UTF16PtrFromString(filepath.Join(programs, "UnexpectedRename"))
		if err := windows.MoveFileEx(pointer, renamed, 0); err == nil {
			t.Error("low parent DELETE_CHILD renamed retained shortcut directory")
		} else if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("retained directory unexpected rename rejection: %v", err)
		}
	}()
	if _, err := os.Stat(positive); !os.IsNotExist(err) {
		t.Fatalf("positive control child survived deletion: %v", err)
	}
	if err := shortcut.Close(); err != nil {
		t.Fatal(err)
	}
	// A privileged replacement after release must still fail receipt identity admission.
	oldPath := filepath.Join(programs, "RetiredFixture")
	if err := os.Rename(receipt.Directory.Path, oldPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := integrationCreateShortcut(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	admitted, err := integrationAdmitShortcut(spec, receipt)
	if err == nil {
		admitted.Close()
		t.Fatal("receipt admitted a replacement shortcut directory")
	}
	if replacement.Receipt.Directory.ID == receipt.Directory.ID {
		t.Fatal("fixture did not replace the full native directory identity")
	}
	if data, err := os.ReadFile(filepath.Join(oldPath, "Harbor.lnk")); err != nil || len(data) == 0 {
		t.Fatalf("original fixture shortcut was not preserved: %v", err)
	}
}

// integrationNamespaceCreateDirectory authors protected disposable shell fixtures without a pathname permission gap.
func integrationNamespaceCreateDirectory(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(pointer, &attributes); err != nil {
		t.Fatal(err)
	}
}
