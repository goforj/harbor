//go:build windows

package windowsinstallation

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestNativeIntegrationRegistryAncestorBoundary proves a writable ancestor cannot rename or delete its protected subtree.
func TestNativeIntegrationRegistryAncestorBoundary(t *testing.T) {
	fixture := integrationTestAnchor(t)
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	user, err := source.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	selectedSID := user.User.Sid.String()
	// Mirror the observed explicit Terminal Server Users rights on an enabled SID so the boundary is nonvacuous.
	const parentMask = 0x3001f
	sddl := "O:BAD:P(A;;KA;;;BA)(A;;KA;;;SY)(A;;0x3001f;;;" + selectedSID + ")"
	outerPath := `Software\HarborAdmission-` + filepath.Base(filepath.Dir(fixture)) + `-RegistryBoundary`
	outer := integrationBoundaryCreateKey(t, registry.LOCAL_MACHINE, outerPath, sddl)
	defer func() {
		if err := outer.retire(); err != nil {
			t.Error(err)
		}
		if err := outer.Close(); err != nil {
			t.Error(err)
		}
	}()
	ancestor := integrationBoundaryCreateKey(t, outer.Key, "Ancestor", sddl)
	defer func() {
		if err := ancestor.retire(); err != nil {
			t.Error(err)
		}
		if err := ancestor.Close(); err != nil {
			t.Error(err)
		}
	}()
	values := map[string]integrationRegistryValue{"InstallationID": {registry.SZ, integrationUTF16Bytes("owned-boundary-fixture", true)}}
	leaf, err := integrationCreateRegistryAt(ancestor.Key, "GoForjHarbor", values)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := leaf.retire(); err != nil {
			t.Error(err)
		}
		if err := leaf.Close(); err != nil {
			t.Error(err)
		}
	}()
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
	if facts.Elevation != 0 || facts.UserSID != selectedSID {
		t.Fatalf("wrong effective fixture authority: %+v", facts)
	}
	t.Logf("effective restricted registry boundary token: %+v", facts)
	lowAncestor, err := registry.OpenKey(registry.LOCAL_MACHINE, outerPath+`\Ancestor`, parentMask|registry.WOW64_64KEY)
	if err != nil {
		t.Fatal(err)
	}
	defer lowAncestor.Close()
	if err := lowAncestor.SetStringValue("PositiveWrite", "allowed"); err != nil {
		t.Fatalf("parent SET_VALUE positive control: %v", err)
	}
	positive, existing, err := registry.CreateKey(lowAncestor, "PositiveChild", registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		t.Fatalf("parent CREATE_SUB_KEY positive control: %v", err)
	}
	if existing {
		positive.Close()
		t.Fatal("unexpected preexisting positive-control child")
	}
	if err := (&integrationRegistry{positive}).retire(); err != nil {
		positive.Close()
		t.Fatal(err)
	}
	if err := positive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lowAncestor.DeleteValue("PositiveWrite"); err != nil {
		t.Fatal(err)
	}
	lowLeaf, err := registry.OpenKey(lowAncestor, "GoForjHarbor", registry.READ|registry.WOW64_64KEY)
	if err != nil {
		t.Fatalf("protected leaf read positive control: %v", err)
	}
	defer lowLeaf.Close()
	if value, _, err := lowLeaf.GetStringValue("InstallationID"); err != nil || value != "owned-boundary-fixture" {
		t.Fatalf("protected leaf identity: %q %v", value, err)
	}
	for _, access := range []uint32{registry.SET_VALUE, windows.DELETE} {
		handle, err := registry.OpenKey(lowAncestor, "GoForjHarbor", access|registry.WOW64_64KEY)
		if err == nil {
			handle.Close()
			t.Errorf("protected leaf allowed access %#x", access)
		} else if err != windows.ERROR_ACCESS_DENIED {
			t.Errorf("protected leaf access %#x returned unexpected error: %v", access, err)
		}
	}
	if err := lowLeaf.SetStringValue("InstallationID", "tampered"); err != windows.ERROR_ACCESS_DENIED {
		t.Errorf("protected leaf SET_VALUE expected access denied, got %v", err)
	}
	if err := (&integrationRegistry{lowLeaf}).retire(); err != windows.STATUS_ACCESS_DENIED {
		t.Errorf("protected leaf NtDeleteKey expected access denied, got %v", err)
	}
	rename := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegRenameKey")
	newName, _ := windows.UTF16PtrFromString("UnexpectedWin32Rename")
	status, _, _ := rename.Call(uintptr(lowAncestor), 0, uintptr(unsafe.Pointer(newName)))
	if status == 0 {
		integrationBoundaryRestoreName(t, ancestor.Key)
		t.Error("restricted thread renamed ancestor with RegRenameKey")
	} else if status != uintptr(windows.ERROR_ACCESS_DENIED) {
		t.Errorf("RegRenameKey expected access denied, got %#x", status)
	} else {
		t.Logf("RegRenameKey protected ancestor rejected: %#x", status)
	}
	nativeName, err := windows.NewNTUnicodeString("UnexpectedNativeRename")
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtRenameKey").Call(uintptr(lowAncestor), uintptr(unsafe.Pointer(nativeName)))
	if int32(status) >= 0 {
		integrationBoundaryRestoreName(t, ancestor.Key)
		t.Error("restricted thread renamed ancestor with NtRenameKey")
	} else if windows.NTStatus(status) != windows.STATUS_ACCESS_DENIED {
		t.Errorf("NtRenameKey expected access denied, got %#x", status)
	} else {
		t.Logf("NtRenameKey protected ancestor rejected: %#x", status)
	}
	if err := (&integrationRegistry{lowAncestor}).retire(); err == nil {
		t.Error("restricted thread deleted nonempty protected ancestor")
	} else if err != windows.STATUS_CANNOT_DELETE && err != windows.STATUS_ACCESS_DENIED {
		t.Errorf("NtDeleteKey unexpected rejection: %v", err)
	} else {
		t.Logf("NtDeleteKey nonempty protected ancestor rejected: %v", err)
	}
	if err := integrationInspectRegistry(leaf.Key, values); err != nil {
		t.Fatalf("protected leaf changed: %v", err)
	}
}

// integrationBoundaryCreateKey creates only a new disposable fixture namespace with its descriptor present at birth.
func integrationBoundaryCreateKey(t *testing.T, parent registry.Key, name, sddl string) *integrationRegistry {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	var handle windows.Handle
	var disposition uint32
	status, _, _ := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyExW").Call(uintptr(parent), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, uintptr(registry.ALL_ACCESS|registry.WOW64_64KEY), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&disposition)))
	if status != 0 {
		t.Fatal(windows.Errno(status))
	}
	key := &integrationRegistry{registry.Key(handle)}
	if disposition != 1 {
		key.Close()
		t.Fatal("preserve preexisting registry boundary fixture")
	}
	return key
}

// integrationBoundaryRestoreName restores only the retained fixture after an unexpected successful low rename.
func integrationBoundaryRestoreName(t *testing.T, key registry.Key) {
	t.Helper()
	if err := windows.RevertToSelf(); err != nil {
		t.Fatalf("restore high thread for owned fixture recovery: %v", err)
	}
	name, _ := windows.UTF16PtrFromString("Ancestor")
	status, _, _ := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegRenameKey").Call(uintptr(key), 0, uintptr(unsafe.Pointer(name)))
	if status != 0 {
		t.Fatal(fmt.Errorf("restore retained fixture ancestor: %w", windows.Errno(status)))
	}
	t.Fatal("restored unexpected ancestor rename; effective low boundary failed")
}
