//go:build windows

package local

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/goforj/harbor/internal/platform/runtimepath"
	"golang.org/x/sys/windows"
)

const (
	windowsSystemUserID  = "S-1-5-18"
	windowsPipeAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

// windowsPeerReader extracts the client or server process identity attached to a named pipe.
type windowsPeerReader func(net.Conn) (PeerIdentity, error)

// pipeHandle exposes the Windows handle retained by go-winio connections for kernel identity queries.
type pipeHandle interface {
	Fd() uintptr
}

// listen creates a named pipe whose endpoint name, DACL, and accepted clients are scoped to one Windows user.
func listen() (Listener, error) {
	path, err := runtimepath.PipePath()
	if err != nil {
		return nil, fmt.Errorf("resolve local IPC named pipe: %w", err)
	}
	userID, err := currentWindowsUserID()
	if err != nil {
		return nil, err
	}

	return listenWindows(path, userID, readWindowsClientIdentity)
}

// listenWindows creates a byte-stream pipe with remote clients rejected by go-winio and a protected owner DACL.
func listenWindows(path, expectedUserID string, readIdentity windowsPeerReader) (Listener, error) {
	securityDescriptor, err := windowsSecurityDescriptor(expectedUserID)
	if err != nil {
		return nil, err
	}

	base, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: securityDescriptor})
	if err != nil {
		return nil, fmt.Errorf("listen on local IPC named pipe %q: %w", path, err)
	}

	return &authenticatingListener{
		listener: base,
		authenticate: func(connection net.Conn) (PeerIdentity, error) {
			return authenticateWindowsPeer(connection, expectedUserID, readIdentity)
		},
	}, nil
}

// windowsSecurityDescriptor grants full pipe access only to the owning user and Windows SYSTEM.
func windowsSecurityDescriptor(userID string) (string, error) {
	sid, err := windows.StringToSid(userID)
	if err != nil {
		return "", fmt.Errorf("build local IPC pipe security: parse user SID %q: %w", userID, err)
	}
	canonicalUserID := sid.String()
	if canonicalUserID == windowsSystemUserID {
		return "", fmt.Errorf("build local IPC pipe security: Harbor cannot run as Windows SYSTEM")
	}

	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;%s)", canonicalUserID, windowsSystemUserID), nil
}

// windowsPipeAccessIsFull accepts the generic grant and the object-specific mask Windows materializes from it.
func windowsPipeAccessIsFull(mask uint32) bool {
	return mask == uint32(windows.GENERIC_ALL) || mask == uint32(windowsPipeAllAccess)
}

// dial connects to the current user's named pipe and verifies that its server process has the same SID.
func dial(ctx context.Context) (Conn, error) {
	path, err := runtimepath.PipePath()
	if err != nil {
		return nil, fmt.Errorf("resolve local IPC named pipe: %w", err)
	}
	userID, err := currentWindowsUserID()
	if err != nil {
		return nil, err
	}

	return dialWindows(ctx, path, userID, readWindowsServerIdentity)
}

// dialWindows keeps named-pipe endpoint and identity admission injectable for Windows-native tests.
func dialWindows(ctx context.Context, path, expectedUserID string, readIdentity windowsPeerReader) (Conn, error) {
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE)
	connection, err := winio.DialPipeAccessImpLevel(ctx, path, access, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, fmt.Errorf("dial local IPC named pipe %q: %w", path, err)
	}

	identity, err := authenticateWindowsPeer(connection, expectedUserID, readIdentity)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}

	return &authenticatedConn{Conn: connection, peer: identity}, nil
}

// authenticateWindowsPeer requires both endpoint ACL admission and a matching process-token SID.
func authenticateWindowsPeer(connection net.Conn, expectedUserID string, readIdentity windowsPeerReader) (PeerIdentity, error) {
	identity, err := readIdentity(connection)
	if err != nil {
		return PeerIdentity{}, fmt.Errorf("read local IPC peer credentials: %w", err)
	}
	if identity.UserID != expectedUserID {
		return PeerIdentity{}, fmt.Errorf("%w: peer user %q, want %q", ErrPeerUnauthorized, identity.UserID, expectedUserID)
	}
	if identity.ProcessID == 0 {
		return PeerIdentity{}, fmt.Errorf("%w: peer process ID is unavailable", ErrPeerUnauthorized)
	}

	return identity, nil
}

// readWindowsClientIdentity resolves the accepted named-pipe client's PID and primary token SID.
func readWindowsClientIdentity(connection net.Conn) (PeerIdentity, error) {
	return readWindowsPeerIdentity(connection, windows.GetNamedPipeClientProcessId)
}

// readWindowsServerIdentity resolves the connected named-pipe server's PID and primary token SID.
func readWindowsServerIdentity(connection net.Conn) (PeerIdentity, error) {
	return readWindowsPeerIdentity(connection, windows.GetNamedPipeServerProcessId)
}

// readWindowsPeerIdentity binds a named-pipe kernel process ID to the user SID on that process token.
func readWindowsPeerIdentity(connection net.Conn, readProcessID func(windows.Handle, *uint32) error) (PeerIdentity, error) {
	handle, ok := connection.(pipeHandle)
	if !ok {
		return PeerIdentity{}, fmt.Errorf("connection type %T does not expose a Windows pipe handle", connection)
	}

	var processID uint32
	if err := readProcessID(windows.Handle(handle.Fd()), &processID); err != nil {
		return PeerIdentity{}, fmt.Errorf("read named-pipe peer process ID: %w", err)
	}
	if processID == 0 {
		return PeerIdentity{}, errors.New("read named-pipe peer process ID: operating system returned zero")
	}

	userID, err := windowsProcessUserID(processID)
	if err != nil {
		return PeerIdentity{}, err
	}
	return PeerIdentity{UserID: userID, ProcessID: processID}, nil
}

// currentWindowsUserID returns the canonical SID owning Harbor's user session.
func currentWindowsUserID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current Windows user SID: %w", err)
	}

	return user.User.Sid.String(), nil
}

// windowsProcessUserID resolves a process token after the named-pipe kernel identifies its PID.
func windowsProcessUserID(processID uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", fmt.Errorf("open local IPC peer process %d: %w", processID, err)
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open local IPC peer process %d token: %w", processID, err)
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read local IPC peer process %d user SID: %w", processID, err)
	}

	return user.User.Sid.String(), nil
}
