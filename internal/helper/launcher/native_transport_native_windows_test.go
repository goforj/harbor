//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// TestNativeWindowsInvocationPipeRoundTrip verifies the native listener's kernel security, identity, and message EOF boundary.
func TestNativeWindowsInvocationPipeRoundTrip(t *testing.T) {
	listener, err := newWindowsInvocationPipe()
	if err != nil {
		t.Fatalf("create native Windows invocation pipe: %v", err)
	}
	t.Cleanup(func() { _ = listener.close() })

	accepts := acceptWindowsPipe(listener)
	timeout := 10 * time.Second
	client, err := winio.DialPipe(listener.path(), &timeout)
	if err != nil {
		t.Fatalf("connect native Windows invocation pipe: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	accepted := <-accepts
	if accepted.err != nil {
		t.Fatalf("accept native Windows invocation pipe: %v", accepted.err)
	}
	if accepted.connection == nil {
		t.Fatal("accepted native Windows invocation pipe connection is nil")
	}
	t.Cleanup(func() { _ = accepted.connection.Close() })
	if err := listener.close(); err != nil {
		t.Fatalf("close native Windows invocation pipe listener: %v", err)
	}

	deadline := time.Now().Add(timeout)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatalf("set native Windows invocation client deadline: %v", err)
	}
	server, ok := accepted.connection.(net.Conn)
	if !ok {
		t.Fatalf("accepted connection type = %T, want net.Conn", accepted.connection)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatalf("set native Windows invocation server deadline: %v", err)
	}

	clientHandle := nativeWindowsTestPipeHandle(t, client)
	serverHandle := nativeWindowsTestPipeHandle(t, server)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("read native Windows test token user: %v", err)
	}
	userID := user.User.Sid.String()
	assertNativeWindowsInvocationPipeSecurity(t, serverHandle, userID)

	clientProcessID, err := accepted.connection.clientProcessID()
	if err != nil {
		t.Fatalf("read native Windows invocation client process: %v", err)
	}
	var serverProcessID uint32
	if err := windows.GetNamedPipeServerProcessId(clientHandle, &serverProcessID); err != nil {
		t.Fatalf("read native Windows invocation server process: %v", err)
	}
	for role, processID := range map[string]uint32{"client": clientProcessID, "server": serverProcessID} {
		if processID != uint32(os.Getpid()) {
			t.Errorf("native Windows invocation %s process = %d, want %d", role, processID, os.Getpid())
		}
		processUserID, err := nativeWindowsTestProcessUserID(processID)
		if err != nil {
			t.Fatalf("read native Windows invocation %s process user: %v", role, err)
		}
		if processUserID != userID {
			t.Errorf("native Windows invocation %s process user = %q, want %q", role, processUserID, userID)
		}
	}

	request := strings.Repeat("native-request-", 512)
	response := strings.Repeat("native-response-", 384)
	type clientResult struct {
		request string
		err     error
	}
	clientResults := make(chan clientResult, 1)
	go func() {
		body, readErr := io.ReadAll(client)
		if readErr != nil {
			clientResults <- clientResult{err: fmt.Errorf("read request through native Windows invocation pipe: %w", readErr)}
			return
		}
		written, writeErr := io.WriteString(client, response)
		if writeErr != nil || written != len(response) {
			clientResults <- clientResult{err: errors.Join(writeErr, io.ErrShortWrite)}
			return
		}
		closeWriter, ok := client.(interface{ CloseWrite() error })
		if !ok {
			clientResults <- clientResult{err: fmt.Errorf("native Windows invocation client type %T does not support CloseWrite", client)}
			return
		}
		if err := closeWriter.CloseWrite(); err != nil {
			clientResults <- clientResult{err: fmt.Errorf("finish native Windows invocation response: %w", err)}
			return
		}
		clientResults <- clientResult{request: string(body)}
	}()

	captured, err := exchangeWindowsHelper(accepted.connection, strings.NewReader(request))
	if err != nil {
		t.Fatalf("exchange over native Windows invocation pipe: %v", err)
	}
	clientResultValue := <-clientResults
	if clientResultValue.err != nil {
		t.Fatal(clientResultValue.err)
	}
	if clientResultValue.request != request {
		t.Fatalf("native Windows invocation request length = %d, want %d", len(clientResultValue.request), len(request))
	}
	if string(captured.Bytes()) != response {
		t.Fatalf("native Windows invocation response length = %d, want %d", len(captured.Bytes()), len(response))
	}
}

// TestVerifyInstalledWindowsHelperSignatureRejectsUnsignedTestBinary verifies trust fails closed without production signing material.
func TestVerifyInstalledWindowsHelperSignatureRejectsUnsignedTestBinary(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve unsigned Windows test executable: %v", err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatalf("open unsigned Windows test executable: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	path, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		t.Fatalf("encode unsigned Windows test executable path: %v", err)
	}

	if err := verifyInstalledWindowsHelperSignature(windows.Handle(file.Fd()), path); err == nil {
		t.Fatal("go test's unsigned Windows executable unexpectedly passed Authenticode verification")
	}
}

// TestShellExecuteWindowsInfoLayout guards the documented 64-bit SHELLEXECUTEINFOW ABI used by Windows amd64 and arm64.
func TestShellExecuteWindowsInfoLayout(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("SHELLEXECUTEINFOW layout assertion is limited to documented 64-bit targets, not %s", runtime.GOARCH)
	}

	information := shellExecuteWindowsInfo{}
	if got := unsafe.Sizeof(information); got != 112 {
		t.Fatalf("shellExecuteWindowsInfo size = %d, want 112", got)
	}
	fields := []struct {
		name   string
		offset uintptr
		want   uintptr
	}{
		{name: "size", offset: unsafe.Offsetof(information.size), want: 0},
		{name: "mask", offset: unsafe.Offsetof(information.mask), want: 4},
		{name: "window", offset: unsafe.Offsetof(information.window), want: 8},
		{name: "verb", offset: unsafe.Offsetof(information.verb), want: 16},
		{name: "file", offset: unsafe.Offsetof(information.file), want: 24},
		{name: "parameters", offset: unsafe.Offsetof(information.parameters), want: 32},
		{name: "directory", offset: unsafe.Offsetof(information.directory), want: 40},
		{name: "show", offset: unsafe.Offsetof(information.show), want: 48},
		{name: "instance", offset: unsafe.Offsetof(information.instance), want: 56},
		{name: "itemIDList", offset: unsafe.Offsetof(information.itemIDList), want: 64},
		{name: "class", offset: unsafe.Offsetof(information.class), want: 72},
		{name: "classKey", offset: unsafe.Offsetof(information.classKey), want: 80},
		{name: "hotKey", offset: unsafe.Offsetof(information.hotKey), want: 88},
		{name: "iconOrMonitor", offset: unsafe.Offsetof(information.iconOrMonitor), want: 96},
		{name: "process", offset: unsafe.Offsetof(information.process), want: 104},
	}
	for _, field := range fields {
		if field.offset != field.want {
			t.Errorf("shellExecuteWindowsInfo.%s offset = %d, want %d", field.name, field.offset, field.want)
		}
	}
}

// nativeWindowsTestPipeHandle returns the kernel handle retained by one go-winio connection.
func nativeWindowsTestPipeHandle(t *testing.T, connection any) windows.Handle {
	t.Helper()
	if wrapped, ok := connection.(*nativeWindowsPipeConnection); ok {
		connection = wrapped.Conn
	}
	handle, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("native Windows invocation connection type = %T, want kernel handle", connection)
	}
	return windows.Handle(handle.Fd())
}

// nativeWindowsTestProcessUserID resolves the token SID attached to a kernel-reported pipe peer process.
func nativeWindowsTestProcessUserID(processID uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// assertNativeWindowsInvocationPipeSecurity verifies the listener materialized only protected owner-and-SYSTEM grants.
func assertNativeWindowsInvocationPipeSecurity(t *testing.T, handle windows.Handle, userID string) {
	t.Helper()
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("read native Windows invocation pipe security: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read native Windows invocation pipe DACL control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("native Windows invocation pipe DACL control = %#x, want protected DACL", control)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatalf("read native Windows invocation pipe owner: %v", err)
	}
	if owner == nil || owner.String() != userID {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		t.Fatalf("native Windows invocation pipe owner = %q, want %q", got, userID)
	}

	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read native Windows invocation pipe access list: %v", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		t.Fatalf("native Windows invocation pipe ACE count = %d, want 2", nativeWindowsTestACECount(dacl))
	}
	want := map[string]bool{userID: false, windowsHelperSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatalf("read native Windows invocation pipe ACE %d: %v", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || !validWindowsInvocationPipeAccess(uint32(ace.Mask)) {
			t.Fatalf("native Windows invocation pipe ACE %d is not an exact direct full-access grant", index)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		seen, ok := want[principal]
		if !ok || seen {
			t.Fatalf("native Windows invocation pipe grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			t.Errorf("native Windows invocation pipe does not grant SID %q", principal)
		}
	}
}

// nativeWindowsTestACECount returns zero for an absent DACL so assertion failures remain diagnostic.
func nativeWindowsTestACECount(dacl *windows.ACL) uint16 {
	if dacl == nil {
		return 0
	}
	return dacl.AceCount
}
