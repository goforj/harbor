//go:build windows

package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/helperpath"
	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

const (
	windowsHelperPipePrefix           = `\\.\pipe\goforj-harbor-helper-`
	windowsHelperPipeRandomBytes      = 32
	windowsHelperAdministratorsSID    = "S-1-5-32-544"
	windowsHelperUsersSID             = "S-1-5-32-545"
	windowsHelperSystemSID            = "S-1-5-18"
	windowsHelperFileAllAccess        = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	windowsShellExecuteNoAsync        = 0x00000100
	windowsShellExecuteNoCloseProcess = 0x00000040
	windowsShellExecuteFlagNoUI       = 0x00000400
)

var shellExecuteExWindowsProcedure = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteWindowsInfo matches SHELLEXECUTEINFOW on supported pointer-width Windows targets.
type shellExecuteWindowsInfo struct {
	size          uint32
	mask          uint32
	window        windows.HWND
	verb          *uint16
	file          *uint16
	parameters    *uint16
	directory     *uint16
	show          int32
	instance      windows.Handle
	itemIDList    unsafe.Pointer
	class         *uint16
	classKey      windows.Handle
	hotKey        uint32
	iconOrMonitor windows.Handle
	process       windows.Handle
}

// nativeWindowsPipeListener adapts one go-winio listener to the transport's one-client boundary.
type nativeWindowsPipeListener struct {
	listener net.Listener
	name     string
}

// nativeWindowsPipeConnection exposes the kernel handle retained by go-winio.
type nativeWindowsPipeConnection struct {
	net.Conn
}

// nativeWindowsElevatedProcess retains one ShellExecuteEx process handle until exact wait completes.
type nativeWindowsElevatedProcess struct {
	handle windows.Handle
}

// newNativeTransport selects Windows's runas and authenticated named-pipe transport.
func newNativeTransport() Transport {
	helperExecutable := helperpath.Executable()
	if helperExecutable == "" {
		return unavailableNativeTransport{}
	}
	return newWindowsNativeTransport(
		helperExecutable,
		inspectInstalledWindowsHelper,
		newWindowsInvocationPipe,
		launchWindowsElevatedHelper,
	)
}

// inspectInstalledWindowsHelper retains and verifies the exact signed installer object before consent.
func inspectInstalledWindowsHelper(path string) (windowsHelperInspection, error) {
	if path == "" {
		return windowsHelperInspection{}, errors.New("installed Windows helper path is empty")
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windowsHelperInspection{}, fmt.Errorf("encode installed Windows helper path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windowsHelperInspection{}, fmt.Errorf("open installed Windows helper without following its leaf: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return windowsHelperInspection{}, errors.Join(errors.New("retain installed Windows helper"), windows.CloseHandle(handle))
	}

	metadata, err := inspectRetainedWindowsHelper(file, path, pathPointer)
	if err != nil {
		return windowsHelperInspection{}, errors.Join(err, file.Close())
	}
	return windowsHelperInspection{metadata: metadata, close: file.Close}, nil
}

// inspectRetainedWindowsHelper verifies object identity, mutation policy, and Authenticode trust on one handle.
func inspectRetainedWindowsHelper(file *os.File, path string, pathPointer *uint16) (windowsHelperMetadata, error) {
	information, err := file.Stat()
	if err != nil {
		return windowsHelperMetadata{}, fmt.Errorf("stat installed Windows helper: %w", err)
	}
	if !information.Mode().IsRegular() {
		return windowsHelperMetadata{}, errors.New("installed Windows helper is not a regular file")
	}

	var nativeInformation windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &nativeInformation); err != nil {
		return windowsHelperMetadata{}, fmt.Errorf("read installed Windows helper information: %w", err)
	}
	if nativeInformation.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return windowsHelperMetadata{}, errors.New("installed Windows helper is a reparse point")
	}
	if nativeInformation.NumberOfLinks != 1 {
		return windowsHelperMetadata{}, fmt.Errorf("installed Windows helper has %d hard links, want 1", nativeInformation.NumberOfLinks)
	}
	finalPath, err := windowsfile.FinalPath(windows.Handle(file.Fd()))
	if err != nil {
		return windowsHelperMetadata{}, fmt.Errorf("resolve installed Windows helper final path: %w", err)
	}
	if !strings.EqualFold(finalPath, extendedWindowsPath(path)) {
		return windowsHelperMetadata{}, fmt.Errorf("installed Windows helper resolves to %q, want %q", finalPath, extendedWindowsPath(path))
	}
	if err := validateInstalledWindowsHelperSecurity(windows.Handle(file.Fd())); err != nil {
		return windowsHelperMetadata{}, err
	}
	if err := verifyInstalledWindowsHelperSignature(windows.Handle(file.Fd()), pathPointer); err != nil {
		return windowsHelperMetadata{}, err
	}

	return windowsHelperMetadata{
		regular:          true,
		reparse:          false,
		linkCount:        1,
		finalPathMatches: true,
		ownerTrusted:     true,
		daclProtected:    true,
		daclRestricted:   true,
		signatureTrusted: true,
	}, nil
}

// extendedWindowsPath returns the canonical DOS-volume shape emitted by GetFinalPathNameByHandle.
func extendedWindowsPath(path string) string {
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(clean, `\\`)
	}
	return `\\?\` + clean
}

// validateInstalledWindowsHelperSecurity requires an Administrators-owned protected executable policy.
func validateInstalledWindowsHelperSecurity(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read installed Windows helper security: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read installed Windows helper DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("installed Windows helper DACL is not protected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read installed Windows helper owner: %w", err)
	}
	if owner == nil || owner.String() != windowsHelperAdministratorsSID {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("installed Windows helper owner is %q, want Administrators %q", got, windowsHelperAdministratorsSID)
	}
	return validateInstalledWindowsHelperDACL(descriptor)
}

// validateInstalledWindowsHelperDACL permits only machine mutation and ordinary read-and-execute access.
func validateInstalledWindowsHelperDACL(descriptor *windows.SECURITY_DESCRIPTOR) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read installed Windows helper access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 3 {
		return errors.New("installed Windows helper DACL must contain exactly three entries")
	}
	want := map[string]bool{
		windowsHelperAdministratorsSID: false,
		windowsHelperSystemSID:         false,
		windowsHelperUsersSID:          false,
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read installed Windows helper DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			return fmt.Errorf("installed Windows helper DACL entry %d is not a direct allow entry", index)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		seen, found := want[principal]
		if !found || seen {
			return fmt.Errorf("installed Windows helper DACL grants unexpected or duplicate SID %q", principal)
		}
		if !validInstalledWindowsHelperAccess(principal, uint32(ace.Mask)) {
			return fmt.Errorf("installed Windows helper DACL grants unexpected access %#x to %q", ace.Mask, principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("installed Windows helper DACL does not grant SID %q", principal)
		}
	}
	return nil
}

// validInstalledWindowsHelperAccess distinguishes machine mutation from user read-and-execute access.
func validInstalledWindowsHelperAccess(principal string, mask uint32) bool {
	if principal == windowsHelperUsersSID {
		generic := uint32(windows.GENERIC_READ | windows.GENERIC_EXECUTE)
		mapped := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
		return mask == generic || mask == mapped
	}
	return mask == windowsHelperFileAllAccess || mask == windows.GENERIC_ALL
}

// verifyInstalledWindowsHelperSignature validates the retained executable without UI or network retrieval.
func verifyInstalledWindowsHelperSignature(handle windows.Handle, path *uint16) error {
	fileInformation := &windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: path,
		File:     handle,
	}
	trust := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(fileInformation),
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		ProvFlags:                       windows.WTD_CACHE_ONLY_URL_RETRIEVAL | windows.WTD_DISABLE_MD2_MD4,
		UIContext:                       windows.WTD_UICONTEXT_EXECUTE,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, trust)
	trust.StateAction = windows.WTD_STATEACTION_CLOSE
	closeErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, trust)
	runtime.KeepAlive(fileInformation)
	runtime.KeepAlive(trust)
	if err := errors.Join(verifyErr, closeErr); err != nil {
		return fmt.Errorf("verify installed Windows helper Authenticode signature: %w", err)
	}
	return nil
}

// newWindowsInvocationPipe creates one unpredictable owner-and-SYSTEM message pipe before consent.
func newWindowsInvocationPipe() (windowsPipeListener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read Windows launcher token user: %w", err)
	}
	userID := user.User.Sid.String()
	if userID == windowsHelperSystemSID {
		return nil, errors.New("Windows helper launcher cannot run as LocalSystem")
	}
	randomBytes := make([]byte, windowsHelperPipeRandomBytes)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return nil, fmt.Errorf("generate Windows helper pipe route: %w", err)
	}
	name := windowsHelperPipePrefix + hex.EncodeToString(randomBytes)
	security := fmt.Sprintf("O:%sD:P(A;;GA;;;%s)(A;;GA;;;%s)", userID, userID, windowsHelperSystemSID)
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: security,
		MessageMode:        true,
		InputBufferSize:    int32(helper.MaxRequestBytes + 1),
		OutputBufferSize:   int32(helper.MaxResponseBytes + 1),
	})
	if err != nil {
		return nil, fmt.Errorf("listen on Windows helper invocation pipe: %w", err)
	}
	return &nativeWindowsPipeListener{listener: listener, name: name}, nil
}

// path returns the one non-authoritative route passed to the fixed helper.
func (listener *nativeWindowsPipeListener) path() string {
	return listener.name
}

// accept returns exactly one local message-pipe client.
func (listener *nativeWindowsPipeListener) accept() (windowsPipeConnection, error) {
	connection, err := listener.listener.Accept()
	if err != nil {
		return nil, err
	}
	return &nativeWindowsPipeConnection{Conn: connection}, nil
}

// close releases the one-shot listener and rejects any later client.
func (listener *nativeWindowsPipeListener) close() error {
	return listener.listener.Close()
}

// closeWrite sends go-winio's zero-message EOF marker while preserving the response direction.
func (connection *nativeWindowsPipeConnection) closeWrite() error {
	writer, ok := connection.Conn.(interface{ CloseWrite() error })
	if !ok {
		return fmt.Errorf("Windows helper pipe connection type %T does not support request half-close", connection.Conn)
	}
	return writer.CloseWrite()
}

// clientProcessID returns the kernel-authenticated process attached to the accepted pipe instance.
func (connection *nativeWindowsPipeConnection) clientProcessID() (uint32, error) {
	handle, ok := connection.Conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("Windows helper pipe connection type %T does not expose a kernel handle", connection.Conn)
	}
	var processID uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(handle.Fd()), &processID); err != nil {
		return 0, fmt.Errorf("read Windows helper pipe client process: %w", err)
	}
	return processID, nil
}

// launchWindowsElevatedHelper uses runas with only the fixed executable and random pipe route.
func launchWindowsElevatedHelper(executable string, pipeName string) (windowsLaunchOutcome, error) {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return windowsLaunchOutcome{}, err
	}
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return windowsLaunchOutcome{}, fmt.Errorf("encode Windows helper executable: %w", err)
	}
	parameters, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return windowsLaunchOutcome{}, fmt.Errorf("encode Windows helper pipe route: %w", err)
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return windowsLaunchOutcome{}, fmt.Errorf("encode Windows helper working directory: %w", err)
	}
	information := &shellExecuteWindowsInfo{
		size:       uint32(unsafe.Sizeof(shellExecuteWindowsInfo{})),
		mask:       windowsShellExecuteNoCloseProcess | windowsShellExecuteNoAsync | windowsShellExecuteFlagNoUI,
		verb:       verb,
		file:       file,
		parameters: parameters,
		directory:  directory,
		show:       windows.SW_HIDE,
	}
	result, _, callErr := shellExecuteExWindowsProcedure.Call(uintptr(unsafe.Pointer(information)))
	runtime.KeepAlive(verb)
	runtime.KeepAlive(file)
	runtime.KeepAlive(parameters)
	runtime.KeepAlive(directory)
	runtime.KeepAlive(information)
	if result == 0 {
		if information.process != 0 && information.process != windows.InvalidHandle {
			if errors.Is(callErr, windows.ERROR_SUCCESS) {
				callErr = errors.New("ShellExecuteExW returned failure after exposing a process handle")
			}
			return windowsLaunchOutcome{
				process: &nativeWindowsElevatedProcess{handle: information.process},
				created: true,
			}, fmt.Errorf("launch elevated Windows helper after process creation: %w", callErr)
		}
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return windowsLaunchOutcome{declined: true}, nil
		}
		if errors.Is(callErr, windows.ERROR_SUCCESS) {
			callErr = errors.New("ShellExecuteExW returned failure without a Windows error")
		}
		return windowsLaunchOutcome{}, fmt.Errorf("launch elevated Windows helper: %w", callErr)
	}
	if information.process == 0 || information.process == windows.InvalidHandle {
		return windowsLaunchOutcome{created: true}, errors.New("ShellExecuteExW omitted the created process handle")
	}
	return windowsLaunchOutcome{
		process: &nativeWindowsElevatedProcess{handle: information.process},
		created: true,
	}, nil
}

// processID returns the PID from the same retained object later used for exact wait.
func (process *nativeWindowsElevatedProcess) processID() (uint32, error) {
	processID, err := windows.GetProcessId(process.handle)
	if err != nil {
		return 0, fmt.Errorf("read elevated Windows helper process ID: %w", err)
	}
	return processID, nil
}

// wait blocks on the exact retained process and reads its final numeric exit code.
func (process *nativeWindowsElevatedProcess) wait() (int, error) {
	event, err := windows.WaitForSingleObject(process.handle, windows.INFINITE)
	if err != nil {
		return -1, fmt.Errorf("wait for elevated Windows helper: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return -1, fmt.Errorf("wait for elevated Windows helper returned event %#x", event)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.handle, &exitCode); err != nil {
		return -1, fmt.Errorf("read elevated Windows helper exit code: %w", err)
	}
	return int(exitCode), nil
}

// close releases the exact process object only after wait completes.
func (process *nativeWindowsElevatedProcess) close() error {
	return windows.CloseHandle(process.handle)
}

var _ windowsPipeListener = (*nativeWindowsPipeListener)(nil)
var _ windowsPipeConnection = (*nativeWindowsPipeConnection)(nil)
var _ windowsElevatedProcess = (*nativeWindowsElevatedProcess)(nil)
