//go:build windows

package windowsinstallation

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestInteractiveShellAdmission covers each authority mismatch without depending on a desktop session.
func TestInteractiveShellAdmission(t *testing.T) {
	user := "S-1-5-21-1-2-3-1001"
	image := `C:\Windows\explorer.exe`
	if err := admitInteractiveShell(user, 1, user, 1, image, image); err != nil {
		t.Fatal(err)
	}
	if err := admitInteractiveShell(user, 1, user, 1, `c:\WINDOWS\EXPLORER.EXE`, image); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		user         string
		session      uint32
		shellUser    string
		shellSession uint32
		image        string
	}{
		{user, 1, "S-1-5-21-1-2-3-1002", 1, image},
		{user, 0, user, 0, image},
		{user, 1, user, 2, image},
		{systemSID, 1, systemSID, 1, image},
		{user, 1, user, 1, `C:\foreign\explorer.exe`},
		{user, 1, user, 1, `C:\Windows\cmd.exe`},
	} {
		if err := admitInteractiveShell(fixture.user, fixture.session, fixture.shellUser, fixture.shellSession, fixture.image, image); err == nil {
			t.Fatalf("admitted interactive shell fixture %+v", fixture)
		}
	}
}

// TestNativeInteractiveShellBinding proves a native desktop observation or the explicit refusal of a headless session.
func TestNativeInteractiveShellBinding(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var session uint32
	if err := tokenInteger(token, windows.TokenSessionId, &session); err != nil {
		t.Fatal(err)
	}
	window, _, _ := getShellWindow.Call()
	verification := VerifyInteractiveSession()
	if (window == 0 || session == 0) && verification == nil {
		t.Fatal("public interactive verification admitted a headless session")
	}
	if window != 0 && session != 0 && verification != nil {
		t.Fatal(verification)
	}
	process, err := retainInteractiveShell(user.User.Sid.String(), session)
	if window == 0 || session == 0 {
		if err == nil {
			windows.CloseHandle(process)
			t.Fatal("headless session admitted an interactive shell")
		}
		t.Logf("native headless session refused: %v", err)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CloseHandle(process); err != nil {
		t.Fatal(err)
	}
	foreign, err := retainInteractiveShell("S-1-5-21-1-2-3-1002", session)
	if err == nil {
		windows.CloseHandle(foreign)
		t.Fatal("native shell admitted a foreign account")
	}
}
