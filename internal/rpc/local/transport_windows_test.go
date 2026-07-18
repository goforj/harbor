//go:build windows

package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsTestPipePath returns a collision-resistant local endpoint for parallel CI workers.
func windowsTestPipePath(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(t.Name())
	return fmt.Sprintf(`\\.\pipe\goforj-harbor-test-%d-%d-%s`, os.Getpid(), time.Now().UnixNano(), name)
}

// TestWindowsTransportAuthenticatesBothPeers verifies named-pipe PID and token admission in both directions.
func TestWindowsTransportAuthenticatesBothPeers(t *testing.T) {
	path := windowsTestPipePath(t)
	userID, err := currentWindowsUserID()
	if err != nil {
		t.Fatalf("read current Windows user: %v", err)
	}
	listener, err := listenWindows(path, userID, readWindowsClientIdentity)
	if err != nil {
		t.Fatalf("listen on Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	type acceptResult struct {
		connection Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := listener.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()

	client, err := dialWindows(context.Background(), path, userID, readWindowsServerIdentity)
	if err != nil {
		t.Fatalf("dial Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	assertWindowsPipeDACL(t, client, userID)

	result := <-accepted
	if result.err != nil {
		t.Fatalf("accept Windows connection: %v", result.err)
	}
	t.Cleanup(func() { _ = result.connection.Close() })

	for name, identity := range map[string]PeerIdentity{
		"client view of daemon": client.Peer(),
		"daemon view of client": result.connection.Peer(),
	} {
		if identity.UserID != userID {
			t.Errorf("%s user = %q, want %q", name, identity.UserID, userID)
		}
		if identity.ProcessID != uint32(os.Getpid()) {
			t.Errorf("%s process = %d, want %d", name, identity.ProcessID, os.Getpid())
		}
	}
}

// TestWindowsTransportWriteDeadline verifies authenticated pipes expose native timeout semantics to RPC writers.
func TestWindowsTransportWriteDeadline(t *testing.T) {
	path := windowsTestPipePath(t)
	userID, err := currentWindowsUserID()
	if err != nil {
		t.Fatalf("read current Windows user: %v", err)
	}
	listener, err := listenWindows(path, userID, readWindowsClientIdentity)
	if err != nil {
		t.Fatalf("listen on Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	type acceptResult struct {
		connection Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := listener.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()

	client, err := dialWindows(context.Background(), path, userID, readWindowsServerIdentity)
	if err != nil {
		t.Fatalf("dial Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	result := <-accepted
	if result.err != nil {
		t.Fatalf("accept Windows connection: %v", result.err)
	}
	t.Cleanup(func() { _ = result.connection.Close() })

	if err := client.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("set expired Windows write deadline: %v", err)
	}
	t.Cleanup(func() { _ = client.SetWriteDeadline(time.Time{}) })

	_, writeErr := client.Write([]byte("deadline"))
	if err := client.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("reset Windows write deadline: %v", err)
	}
	var timeoutError net.Error
	if !errors.As(writeErr, &timeoutError) {
		t.Fatalf("write error = %v, want net.Error", writeErr)
	}
	if !timeoutError.Timeout() {
		t.Fatalf("write error = %v, want timeout", writeErr)
	}
}

// assertWindowsPipeDACL verifies the live kernel object retains Harbor's protected two-principal ACL.
func assertWindowsPipeDACL(t *testing.T, connection Conn, userID string) {
	t.Helper()
	authenticated, ok := connection.(*authenticatedConn)
	if !ok {
		t.Fatalf("connection type = %T, want authenticated connection", connection)
	}
	handle, ok := authenticated.Conn.(pipeHandle)
	if !ok {
		t.Fatalf("underlying connection type = %T, want Windows pipe handle", authenticated.Conn)
	}

	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(handle.Fd()),
		windows.SE_KERNEL_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("read live named-pipe DACL: %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read live named-pipe DACL control: %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("live named-pipe DACL control = %#x, want protected DACL", control)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read live named-pipe access list: %v", err)
	}
	if acl == nil {
		t.Fatal("live named-pipe access list is empty")
	}
	if acl.AceCount != 2 {
		t.Fatalf("live named-pipe ACE count = %d, want 2", acl.AceCount)
	}

	wantUsers := map[string]bool{userID: false, windowsSystemUserID: false}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			t.Fatalf("read live named-pipe ACE %d: %v", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("live named-pipe ACE %d type = %d, want allow", index, ace.Header.AceType)
		}
		if ace.Mask != windows.GENERIC_ALL {
			t.Fatalf("live named-pipe ACE %d mask = %#x, want generic all", index, ace.Mask)
		}

		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := sid.String()
		seen, ok := wantUsers[principal]
		if !ok {
			t.Fatalf("live named-pipe ACE %d grants unexpected SID %q", index, principal)
		}
		if seen {
			t.Fatalf("live named-pipe DACL repeats SID %q", principal)
		}
		wantUsers[principal] = true
	}
	for user, seen := range wantUsers {
		if !seen {
			t.Errorf("live named-pipe DACL does not grant SID %q", user)
		}
	}
}

// TestWindowsTransportRejectsDifferentTokenUser verifies the DACL is reinforced by process-token admission.
func TestWindowsTransportRejectsDifferentTokenUser(t *testing.T) {
	path := windowsTestPipePath(t)
	userID, err := currentWindowsUserID()
	if err != nil {
		t.Fatalf("read current Windows user: %v", err)
	}
	listener, err := listenWindows(path, userID, func(net.Conn) (PeerIdentity, error) {
		return PeerIdentity{UserID: "S-1-5-21-1-2-3-4", ProcessID: 42}, nil
	})
	if err != nil {
		t.Fatalf("listen on Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()

	connection, err := dialWindows(context.Background(), path, userID, readWindowsServerIdentity)
	if err != nil {
		t.Fatalf("dial Windows endpoint: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	if err := <-accepted; !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("accept error = %v, want %v", err, ErrPeerUnauthorized)
	}
}

// TestWindowsSecurityDescriptorRestrictsPipe verifies no broad built-in group receives endpoint access.
func TestWindowsSecurityDescriptorRestrictsPipe(t *testing.T) {
	const userID = "S-1-5-21-100-200-300-400"
	descriptor, err := windowsSecurityDescriptor(userID)
	if err != nil {
		t.Fatalf("build Windows security descriptor: %v", err)
	}
	want := "D:P(A;;GA;;;" + userID + ")(A;;GA;;;" + windowsSystemUserID + ")"
	if descriptor != want {
		t.Fatalf("security descriptor = %q, want %q", descriptor, want)
	}
}

// TestWindowsSecurityDescriptorRejectsSystemOwner verifies Harbor cannot create a system-wide authority endpoint.
func TestWindowsSecurityDescriptorRejectsSystemOwner(t *testing.T) {
	if _, err := windowsSecurityDescriptor(windowsSystemUserID); err == nil {
		t.Fatal("SYSTEM identity unexpectedly accepted as Harbor owner")
	}
}

// TestWindowsDialHonorsCanceledContext verifies CLI cancellation reaches named-pipe connection attempts.
func TestWindowsDialHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	userID, err := currentWindowsUserID()
	if err != nil {
		t.Fatalf("read current Windows user: %v", err)
	}

	_, err = dialWindows(ctx, windowsTestPipePath(t), userID, readWindowsServerIdentity)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dial error = %v, want %v", err, context.Canceled)
	}
}
