//go:build windows

package windowsinstallation

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	getShellWindow           = windows.NewLazySystemDLL("user32.dll").NewProc("GetShellWindow")
	getWindowThreadProcessID = windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowThreadProcessId")
)

// VerifyInteractiveSession refuses service logons and elevation under a different account before machine installation writes.
func VerifyInteractiveSession() error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	var session uint32
	if err := tokenInteger(token, windows.TokenSessionId, &session); err != nil {
		return err
	}
	shell, err := retainInteractiveShell(user.User.Sid.String(), session)
	if err != nil {
		return err
	}
	return windows.CloseHandle(shell)
}

// retainInteractiveShell binds activation to the current desktop's actual Explorer token rather than a requested account.
func retainInteractiveShell(user string, session uint32) (_ windows.Handle, result error) {
	window, _, _ := getShellWindow.Call()
	if window == 0 {
		return 0, errors.New("installed task operation requires an interactive Explorer shell")
	}
	var pid uint32
	thread, _, _ := getWindowThreadProcessID.Call(window, uintptr(unsafe.Pointer(&pid)))
	if thread == 0 || pid == 0 {
		return 0, errors.New("cannot identify the interactive Explorer process")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, windows.CloseHandle(process))
		}
	}()
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, err
	}
	defer func() { result = errors.Join(result, token.Close()) }()
	owner, err := token.GetTokenUser()
	if err != nil {
		return 0, err
	}
	var shellSession uint32
	if err := tokenInteger(token, windows.TokenSessionId, &shellSession); err != nil {
		return 0, err
	}
	path := make([]uint16, 32768)
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(process, 0, &path[0], &size); err != nil {
		return 0, err
	}
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		return 0, err
	}
	if err := admitInteractiveShell(user, session, owner.User.Sid.String(), shellSession, windows.UTF16ToString(path[:size]), filepath.Join(system, "explorer.exe")); err != nil {
		return 0, err
	}
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return 0, errors.Join(errors.New("interactive Explorer process has settled"), err)
	}
	return process, nil
}

// admitInteractiveShell rejects over-the-shoulder elevation, another session, and substitute shell images.
func admitInteractiveShell(user string, session uint32, shellUser string, shellSession uint32, image, expected string) error {
	if !validUserSID(user) || user != shellUser || session == 0 || session != shellSession || !strings.EqualFold(image, expected) {
		return errors.New("installed task user or session differs from the interactive Explorer shell")
	}
	return nil
}
