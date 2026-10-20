//go:build windows

package windowsinstallation

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// logNativeDesktopAccess records actual token access without changing the hosted interactive station or desktop.
func logNativeDesktopAccess(t *testing.T) {
	t.Helper()
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		t.Logf("native user object group observation error: %v", err)
	} else {
		for _, group := range groups.AllGroups() {
			t.Logf("native user object token group SID=%s attributes=%#x", group.Sid.String(), group.Attributes)
		}
	}
	library := windows.NewLazySystemDLL("user32.dll")
	if err := library.Load(); err != nil {
		t.Logf("native user object API loading observation error: %v", err)
		return
	}
	for _, object := range []struct {
		name, open, close string
		rights            []uint32
	}{
		{"winsta0", "OpenWindowStationW", "CloseWindowStation", []uint32{windows.READ_CONTROL, 0xf037f, 1, 2, 4, 8, 0x10, 0x20, 0x40, 0x100, 0x200}},
		{"default", "OpenDesktopW", "CloseDesktop", []uint32{windows.READ_CONTROL, 0xf01ff, 1, 2, 4, 8, 0x10, 0x20, 0x40, 0x80, 0x100}},
	} {
		name := windows.StringToUTF16Ptr(object.name)
		for _, access := range object.rights {
			var handle uintptr
			var err error
			if object.name == "winsta0" {
				handle, _, err = library.NewProc(object.open).Call(uintptr(unsafe.Pointer(name)), 0, uintptr(access))
			} else {
				handle, _, err = library.NewProc(object.open).Call(uintptr(unsafe.Pointer(name)), 0, 0, uintptr(access))
			}
			if handle == 0 {
				t.Logf("native user object %s access=%#x denied: %v", object.name, access, err)
				continue
			}
			t.Logf("native user object %s access=%#x granted", object.name, access)
			if access == windows.READ_CONTROL {
				descriptor, err := windows.GetSecurityInfo(windows.Handle(handle), windows.SE_WINDOW_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
				if err == nil {
					t.Logf("native user object %s descriptor=%s", object.name, descriptor.String())
				} else {
					t.Logf("native user object %s descriptor observation error: %v", object.name, err)
				}
			}
			closed, _, closeErr := library.NewProc(object.close).Call(handle)
			if closed == 0 {
				t.Errorf("close observed user object %s: %v", object.name, closeErr)
			}
		}
	}
}
