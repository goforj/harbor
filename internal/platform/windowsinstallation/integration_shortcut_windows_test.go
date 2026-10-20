//go:build windows

package windowsinstallation

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestIntegrationShellLinkBounds rejects caller-shaped aliases before touching machine state.
func TestIntegrationShellLinkBounds(t *testing.T) {
	spec := integrationShortcutSpec{Target: `C:\Program Files\Harbor\desktop-launcher.exe`, WorkingDirectory: `C:\Program Files\Harbor`, Icon: `C:\Program Files\Harbor\releases\1\harbor-desktop.exe`}
	first, err := integrationShellLink(spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := integrationShellLink(spec)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("shortcut encoding is not deterministic")
	}
	if flags := binary.LittleEndian.Uint32(first[20:]); flags&0x20 != 0 || flags&0xd2 != 0xd2 {
		t.Fatalf("unexpected shortcut flags %#x", flags)
	}
	for _, bad := range []string{`relative.exe`, `C:\a\..\launcher.exe`, `\\server\share\launcher.exe`, "C:\\nul\x00.exe"} {
		changed := spec
		changed.Target = bad
		if _, err := integrationShellLink(changed); err == nil {
			t.Fatalf("accepted target %q", bad)
		}
	}
}

// TestNativeIntegrationShortcut proves native shell parsing and exact receipt retirement in a disposable fixture.
func TestNativeIntegrationShortcut(t *testing.T) {
	root := integrationTestAnchor(t)
	spec := integrationShortcutSpec{Programs: root, Target: filepath.Join(root, "desktop-launcher.exe"), WorkingDirectory: root, Icon: filepath.Join(root, "harbor-desktop.exe")}
	shortcut, err := integrationCreateShortcut(spec)
	if err != nil {
		t.Fatal(err)
	}
	receipt := shortcut.Receipt
	if len(receipt.File.ID) != 32 || receipt.SHA256 == "" {
		t.Fatal("shortcut lacks full native identity and digest")
	}
	if err := shortcut.Close(); err != nil {
		t.Fatal(err)
	}
	integrationTestShellParser(t, receipt.File.Path, spec)
	if other, err := integrationCreateShortcut(spec); err == nil {
		other.Close()
		t.Fatal("overwrote existing shortcut")
	}
	admitted, err := integrationAdmitShortcut(spec, receipt)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(receipt.Directory.Path, "Foreign.lnk")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := admitted.retire(); err != nil {
		t.Fatalf("failed to preserve shared shortcut directory: %v", err)
	}
	if err := admitted.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatalf("foreign shortcut changed: %q, %v", data, err)
	}
	if _, err := os.Stat(receipt.File.Path); !os.IsNotExist(err) {
		t.Fatalf("owned shortcut survived: %v", err)
	}
}

// integrationTestCOMCall invokes only interfaces returned by the explicit system Shell Link server.
func integrationTestCOMCall(object uintptr, index int, args ...uintptr) uintptr {
	table := *(*uintptr)(unsafe.Pointer(object))
	method := *(*uintptr)(unsafe.Pointer(table + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	result, _, _ := syscall.SyscallN(method, append([]uintptr{object}, args...)...)
	return result
}

// integrationTestShellParser loads the fixture without Resolve or user-controlled COM class registration.
func integrationTestShellParser(t *testing.T, path string, spec integrationShortcutSpec) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := windows.NewLazySystemDLL("ole32.dll")
	status, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if int32(status) < 0 {
		t.Fatalf("CoInitializeEx: %#x", status)
	}
	defer ole.NewProc("CoUninitialize").Call()
	class, _ := windows.GUIDFromString("{00021401-0000-0000-C000-000000000046}")
	factoryID, _ := windows.GUIDFromString("{00000001-0000-0000-C000-000000000046}")
	linkID, _ := windows.GUIDFromString("{000214F9-0000-0000-C000-000000000046}")
	persistID, _ := windows.GUIDFromString("{0000010B-0000-0000-C000-000000000046}")
	var factory, link, persist uintptr
	factoryProcedure := integrationTestShellFactory(t)
	status, _, _ = syscall.SyscallN(factoryProcedure, uintptr(unsafe.Pointer(&class)), uintptr(unsafe.Pointer(&factoryID)), uintptr(unsafe.Pointer(&factory)))
	if int32(status) < 0 {
		t.Fatalf("system Shell Link factory: %#x", status)
	}
	defer integrationTestCOMCall(factory, 2)
	if status := integrationTestCOMCall(factory, 3, 0, uintptr(unsafe.Pointer(&linkID)), uintptr(unsafe.Pointer(&link))); int32(status) < 0 {
		t.Fatalf("Shell Link instance: %#x", status)
	}
	defer integrationTestCOMCall(link, 2)
	if status := integrationTestCOMCall(link, 0, uintptr(unsafe.Pointer(&persistID)), uintptr(unsafe.Pointer(&persist))); int32(status) < 0 {
		t.Fatalf("Shell Link persistence: %#x", status)
	}
	defer integrationTestCOMCall(persist, 2)
	pointer, _ := windows.UTF16PtrFromString(path)
	if status := integrationTestCOMCall(persist, 5, uintptr(unsafe.Pointer(pointer)), 0); int32(status) < 0 {
		t.Fatalf("Shell Link Load: %#x", status)
	}
	for _, check := range []struct {
		method   int
		expected string
		extra    []uintptr
	}{{3, spec.Target, []uintptr{0, 4}}, {8, spec.WorkingDirectory, nil}, {10, "", nil}} {
		buffer := make([]uint16, 32768)
		arguments := []uintptr{uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))}
		arguments = append(arguments, check.extra...)
		if status := integrationTestCOMCall(link, check.method, arguments...); int32(status) < 0 {
			t.Fatalf("Shell Link getter %d: %#x", check.method, status)
		}
		if actual := windows.UTF16ToString(buffer); actual != check.expected {
			t.Fatalf("native Shell Link field %d = %q, want %q", check.method, actual, check.expected)
		}
	}
	buffer := make([]uint16, 32768)
	var iconIndex int32
	if status := integrationTestCOMCall(link, 16, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&iconIndex))); int32(status) < 0 {
		t.Fatalf("Shell Link GetIconLocation: %#x", status)
	}
	if actual := windows.UTF16ToString(buffer); actual != spec.Icon || iconIndex != 0 {
		t.Fatalf("native icon = %q,%d", actual, iconIndex)
	}
}

// TestNativeIntegrationShortcutRejectsDrift proves bytes, hardlink counts and retained identities all gate repair.
func TestNativeIntegrationShortcutRejectsDrift(t *testing.T) {
	root := integrationTestAnchor(t)
	spec := integrationShortcutSpec{Programs: root, Target: filepath.Join(root, "desktop-launcher.exe"), WorkingDirectory: root, Icon: filepath.Join(root, "harbor-desktop.exe")}
	shortcut, err := integrationCreateShortcut(spec)
	if err != nil {
		t.Fatal(err)
	}
	receipt := shortcut.Receipt
	if err := shortcut.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt.File.Path, []byte("foreign shortcut"), 0600); err != nil {
		t.Fatal(err)
	}
	if admitted, err := integrationAdmitShortcut(spec, receipt); err == nil {
		admitted.Close()
		t.Fatal("accepted changed shortcut bytes")
	}
	original, err := integrationShellLink(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt.File.Path, original, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(receipt.Directory.Path, "alias.lnk")
	if err := os.Link(receipt.File.Path, link); err != nil {
		t.Fatal(err)
	}
	if admitted, err := integrationAdmitShortcut(spec, receipt); err == nil {
		admitted.Close()
		t.Fatal("accepted hardlinked shortcut")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	changed := receipt
	changed.File.ID = "00000000000000000000000000000000"
	if admitted, err := integrationAdmitShortcut(spec, changed); err == nil {
		admitted.Close()
		t.Fatal("accepted different native file identity")
	}
	changed = receipt
	changed.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if admitted, err := integrationAdmitShortcut(spec, changed); err == nil {
		admitted.Close()
		t.Fatal("accepted different planned digest")
	}
	admitted, err := integrationAdmitShortcut(spec, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitted.retire(); err != nil {
		t.Fatal(err)
	}
	if err := admitted.Close(); err != nil {
		t.Fatal(err)
	}
}

// integrationTestShellFactory admits the machine-registered system server before bypassing COM registration lookup.
func integrationTestShellFactory(t *testing.T) uintptr {
	t.Helper()
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Classes\CLSID\{00021401-0000-0000-C000-000000000046}\InprocServer32`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	registered, kind, err := key.GetStringValue("")
	if err != nil {
		t.Fatal(err)
	}
	if kind != registry.SZ && kind != registry.EXPAND_SZ {
		t.Fatalf("unexpected Shell Link registration type %d", kind)
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	// Expand only the OS-defined root, never process environment values.
	if strings.HasPrefix(strings.ToLower(registered), `%systemroot%\`) {
		registered = windowsDirectory + registered[len("%systemroot%"):]
	}
	var server string
	for _, name := range []string{"shell32.dll", "windows.storage.dll"} {
		candidate := filepath.Join(systemDirectory, name)
		if strings.EqualFold(registered, candidate) {
			server = candidate
			break
		}
	}
	if server == "" {
		t.Fatalf("Shell Link registration is not an allowlisted System32 server: %q", registered)
	}
	retained := &Installation{}
	if err := retained.retainDirectoryChain(systemDirectory); err != nil {
		retained.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := retained.Close(); err != nil {
			t.Error(err)
		}
	})
	pointer, err := windows.UTF16PtrFromString(server)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), server)
	retained.handles = append(retained.handles, file)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		t.Fatal(err)
	}
	// Windows servicing intentionally hardlinks system DLLs into WinSxS; trusted ACLs bind those aliases.
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		t.Fatal("Shell Link server has a reparse or type conflict")
	}
	final, err := finalIntegrationPath(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(final, `\\?\`+server) {
		t.Fatal("Shell Link server native path differs")
	}
	if err := admitSecurity(handle, false); err != nil {
		t.Fatal(err)
	}
	module, err := windows.LoadLibraryEx(server, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.FreeLibrary(module); err != nil {
			t.Error(err)
		}
	})
	procedure, err := windows.GetProcAddress(module, "DllGetClassObject")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("admitted native Shell Link parser server: %s", server)
	return procedure
}
