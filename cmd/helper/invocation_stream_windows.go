//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsInvocationSystemSID     = "S-1-5-18"
	windowsInvocationPipeAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

// windowsHelperPipeConnection preserves message continuation while treating the launcher's zero message as EOF.
type windowsHelperPipeConnection struct {
	*os.File
}

// openPlatformInvocation replaces non-inheritable standard streams with one authenticated local pipe.
func openPlatformInvocation(arguments []string, _ io.Reader, _ io.Writer) (invocationStreams, error) {
	return openWindowsInvocation(arguments, openWindowsHelperPipe)
}

// openWindowsHelperPipe connects synchronously and authenticates the launcher before durable authority opens.
func openWindowsHelperPipe(path string) (io.ReadWriteCloser, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode Windows helper invocation pipe: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.FILE_WRITE_ATTRIBUTES,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION,
		0,
	)
	if err != nil {
		return nil, err
	}
	closeOnError := func(openErr error) (io.ReadWriteCloser, error) {
		return nil, errors.Join(openErr, windows.CloseHandle(handle))
	}

	readMode := uint32(windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(handle, &readMode, nil, nil); err != nil {
		return closeOnError(fmt.Errorf("set Windows helper invocation pipe message mode: %w", err))
	}
	userID, err := currentWindowsInvocationUserID()
	if err != nil {
		return closeOnError(err)
	}
	if err := validateWindowsInvocationPipeSecurity(handle, userID); err != nil {
		return closeOnError(err)
	}
	if err := validateWindowsInvocationServer(handle, userID); err != nil {
		return closeOnError(err)
	}

	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return closeOnError(errors.New("retain Windows helper invocation pipe"))
	}
	return &windowsHelperPipeConnection{File: file}, nil
}

// Read hides ERROR_MORE_DATA so the bounded codec can assemble one request message across buffer growth.
func (connection *windowsHelperPipeConnection) Read(body []byte) (int, error) {
	written, err := connection.File.Read(body)
	if errors.Is(err, windows.ERROR_MORE_DATA) {
		err = nil
	}
	return written, err
}

// currentWindowsInvocationUserID returns the token user shared by filtered and elevated UAC tokens.
func currentWindowsInvocationUserID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read Windows helper token user: %w", err)
	}
	return user.User.Sid.String(), nil
}

// validateWindowsInvocationServer binds the random pipe route to a launcher running as the same token user.
func validateWindowsInvocationServer(pipe windows.Handle, expectedUserID string) error {
	var processID uint32
	if err := windows.GetNamedPipeServerProcessId(pipe, &processID); err != nil {
		return fmt.Errorf("read Windows helper pipe server process: %w", err)
	}
	if processID == 0 {
		return errors.New("Windows helper pipe server process ID is unavailable")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return fmt.Errorf("open Windows helper pipe server process %d: %w", processID, err)
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("open Windows helper pipe server process %d token: %w", processID, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read Windows helper pipe server process %d user: %w", processID, err)
	}
	if user.User.Sid.String() != expectedUserID {
		return fmt.Errorf("Windows helper pipe server user is %q, want %q", user.User.Sid.String(), expectedUserID)
	}
	return nil
}

// validateWindowsInvocationPipeSecurity requires a protected owner-and-SYSTEM kernel DACL on the live pipe.
func validateWindowsInvocationPipeSecurity(pipe windows.Handle, expectedUserID string) error {
	descriptor, err := windows.GetSecurityInfo(
		pipe,
		windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows helper invocation pipe security: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read Windows helper invocation pipe DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Windows helper invocation pipe DACL is not protected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read Windows helper invocation pipe owner: %w", err)
	}
	if owner == nil || owner.String() != expectedUserID {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("Windows helper invocation pipe owner is %q, want %q", got, expectedUserID)
	}

	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows helper invocation pipe access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		return errors.New("Windows helper invocation pipe DACL must contain exactly two entries")
	}
	want := map[string]bool{expectedUserID: false, windowsInvocationSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read Windows helper invocation pipe DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || !windowsInvocationPipeAccessIsFull(uint32(ace.Mask)) {
			return fmt.Errorf("Windows helper invocation pipe DACL entry %d is not an exact full-access grant", index)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		seen, found := want[principal]
		if !found || seen {
			return fmt.Errorf("Windows helper invocation pipe DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("Windows helper invocation pipe DACL does not grant SID %q", principal)
		}
	}
	return nil
}

// windowsInvocationPipeAccessIsFull accepts the generic grant and Windows's mapped kernel form.
func windowsInvocationPipeAccessIsFull(mask uint32) bool {
	return mask == uint32(windows.GENERIC_ALL) || mask == uint32(windowsInvocationPipeAllAccess)
}

var _ io.ReadWriteCloser = (*windowsHelperPipeConnection)(nil)
