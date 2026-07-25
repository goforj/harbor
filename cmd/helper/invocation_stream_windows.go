//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"

	"github.com/goforj/harbor/internal/helper"
	"golang.org/x/sys/windows"
)

const (
	windowsInvocationSystemSID     = "S-1-5-18"
	windowsInvocationPipeAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

var (
	errWindowsInvocationPipeConnection = errors.New("Windows helper pipe connection failed")
	errWindowsInvocationMessageMode    = errors.New("Windows helper pipe message mode failed")
	errWindowsInvocationTokenIdentity  = errors.New("Windows helper token identity failed")
	errWindowsInvocationPipeSecurity   = errors.New("Windows helper pipe security failed")
	errWindowsInvocationServerIdentity = errors.New("Windows helper server identity failed")
	errWindowsInvocationRetain         = errors.New("Windows helper pipe retention failed")
)

// windowsHelperPipeConnection preserves message continuation and terminates the single-request stream at its message boundary.
type windowsHelperPipeConnection struct {
	*os.File
	requestComplete bool
}

// openPlatformInvocation replaces non-inheritable standard streams with one authenticated local pipe.
func openPlatformInvocation(arguments []string, _ io.Reader, _ io.Writer) (invocationStreams, error) {
	return openWindowsInvocation(arguments, openWindowsHelperPipe)
}

// openWindowsHelperPipe connects synchronously and authenticates the launcher before durable authority opens.
func openWindowsHelperPipe(path string) (io.ReadWriteCloser, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("%w: encode pipe path: %v", errWindowsInvocationPipeConnection, err)
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
		return nil, fmt.Errorf("%w: %v", errWindowsInvocationPipeConnection, err)
	}
	closeOnError := func(openErr error) (io.ReadWriteCloser, error) {
		return nil, errors.Join(openErr, windows.CloseHandle(handle))
	}

	readMode := uint32(windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(handle, &readMode, nil, nil); err != nil {
		return closeOnError(fmt.Errorf("%w: %v", errWindowsInvocationMessageMode, err))
	}
	userID, err := currentWindowsInvocationUserID()
	if err != nil {
		return closeOnError(fmt.Errorf("%w: %v", errWindowsInvocationTokenIdentity, err))
	}
	if err := validateWindowsInvocationPipeSecurity(handle, userID); err != nil {
		return closeOnError(fmt.Errorf("%w: %v", errWindowsInvocationPipeSecurity, err))
	}
	if err := validateWindowsInvocationServer(handle, userID); err != nil {
		return closeOnError(fmt.Errorf("%w: %v", errWindowsInvocationServerIdentity, err))
	}

	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return closeOnError(errWindowsInvocationRetain)
	}
	return &windowsHelperPipeConnection{File: file}, nil
}

// platformInvocationFailureExitCode maps only reviewed pre-dispatch admission stages to bounded process evidence.
func platformInvocationFailureExitCode(err error) int {
	switch {
	case errors.Is(err, errWindowsInvocationPipeConnection):
		return helper.WindowsInvocationExitPipeConnection
	case errors.Is(err, errWindowsInvocationMessageMode):
		return helper.WindowsInvocationExitMessageMode
	case errors.Is(err, errWindowsInvocationTokenIdentity):
		return helper.WindowsInvocationExitTokenIdentity
	case errors.Is(err, errWindowsInvocationPipeSecurity):
		return helper.WindowsInvocationExitPipeSecurity
	case errors.Is(err, errWindowsInvocationServerIdentity):
		return helper.WindowsInvocationExitServerIdentity
	case errors.Is(err, errWindowsInvocationRetain):
		return helper.WindowsInvocationExitRetainConnection
	default:
		return 1
	}
}

// platformRuntimeFailureExitCode maps only reviewed pre-serve startup stages to bounded process evidence.
func platformRuntimeFailureExitCode(err error) int {
	switch {
	case errors.Is(err, errRuntimeAuthorization):
		return helper.WindowsInvocationExitAuthorization
	case errors.Is(err, errRuntimeTicketStore):
		return helper.WindowsInvocationExitTicketStore
	case errors.Is(err, errRuntimeReplayStore):
		return helper.WindowsInvocationExitReplayStore
	default:
		return 1
	}
}

// Read hides ERROR_MORE_DATA so the bounded codec can assemble one request message across buffer growth.
func (connection *windowsHelperPipeConnection) Read(body []byte) (int, error) {
	if connection.requestComplete {
		return 0, io.EOF
	}
	written, err := connection.File.Read(body)
	if errors.Is(err, windows.ERROR_MORE_DATA) {
		err = nil
	} else if err == nil {
		connection.requestComplete = true
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
