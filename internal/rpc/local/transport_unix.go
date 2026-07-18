//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const staleEndpointProbeTimeout = 250 * time.Millisecond

// unixCredentialReader extracts credentials attached by the Unix socket implementation.
type unixCredentialReader func(*net.UnixConn) (PeerIdentity, error)

// unixEndpointListener removes only the socket inode created by this listener.
type unixEndpointListener struct {
	*net.UnixListener
	path      string
	endpoint  os.FileInfo
	closeOnce sync.Once
	closeErr  error
}

// listen creates the owner-only Unix socket after the daemon has acquired singleton authority.
func listen() (Listener, error) {
	path, err := runtimepath.SocketPath()
	if err != nil {
		return nil, fmt.Errorf("resolve local IPC socket: %w", err)
	}

	return listenUnix(path, uint32(os.Geteuid()), readUnixPeerIdentity)
}

// listenUnix owns Unix endpoint preparation and leaves the RPC protocol independent of socket details.
func listenUnix(path string, expectedUID uint32, readIdentity unixCredentialReader) (Listener, error) {
	endpoint, err := openUnixEndpoint(path)
	if err != nil {
		return nil, err
	}

	return &authenticatingListener{
		listener: endpoint,
		authenticate: func(connection net.Conn) (PeerIdentity, error) {
			return authenticateUnixPeer(connection, expectedUID, readIdentity)
		},
	}, nil
}

// openUnixEndpoint creates a private socket and records its inode so shutdown cannot unlink a replacement path.
func openUnixEndpoint(path string) (*unixEndpointListener, error) {
	if path == "" {
		return nil, errors.New("listen on local IPC socket: path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("listen on local IPC socket: path %q is not absolute", path)
	}
	path = filepath.Clean(path)

	if err := prepareUnixRuntimeDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("prepare local IPC runtime directory: %w", err)
	}
	if err := removeStaleUnixEndpoint(path, net.DialTimeout); err != nil {
		return nil, err
	}

	address := &net.UnixAddr{Name: path, Net: "unix"}
	base, err := net.ListenUnix("unix", address)
	if err != nil {
		return nil, fmt.Errorf("listen on local IPC socket %q: %w", path, err)
	}
	base.SetUnlinkOnClose(false)

	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(
			fmt.Errorf("secure local IPC socket %q: %w", path, err),
			closeAndRemoveUnixEndpoint(base, path),
		)
	}

	info, err := inspectOwnedUnixSocket(path, uint32(os.Geteuid()))
	if err != nil {
		return nil, errors.Join(err, closeAndRemoveUnixEndpoint(base, path))
	}

	return &unixEndpointListener{UnixListener: base, path: path, endpoint: info}, nil
}

// prepareUnixRuntimeDirectory secures Harbor's leaf without changing permissions on an existing platform parent.
func prepareUnixRuntimeDirectory(path string) error {
	return prepareUnixRuntimeDirectoryForUser(path, uint32(os.Geteuid()), os.Chmod)
}

// prepareUnixRuntimeDirectoryForUser keeps owner and permission failures testable without changing process identity.
func prepareUnixRuntimeDirectoryForUser(path string, expectedUID uint32, chmod func(string, os.FileMode) error) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime directory %q is a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime directory %q is not a directory", path)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("runtime directory %q has unsupported ownership metadata", path)
	}
	if stat.Uid != expectedUID {
		return fmt.Errorf("runtime directory %q is owned by uid %d, want %d", path, stat.Uid, expectedUID)
	}
	if err := chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure %q: %w", path, err)
	}

	return nil
}

// removeStaleUnixEndpoint unlinks only a refused socket; live, ambiguous, and non-socket paths fail closed.
func removeStaleUnixEndpoint(path string, probe func(string, string, time.Duration) (net.Conn, error)) error {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local IPC endpoint %q: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local IPC endpoint %q is a symbolic link", path)
	}
	if before.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("local IPC endpoint %q is not a Unix socket", path)
	}

	connection, probeErr := probe("unix", path, staleEndpointProbeTimeout)
	if probeErr == nil {
		closeErr := connection.Close()
		inUseErr := fmt.Errorf("local IPC endpoint %q is already accepting connections", path)
		if closeErr != nil {
			return errors.Join(inUseErr, fmt.Errorf("close endpoint probe: %w", closeErr))
		}
		return inUseErr
	}
	if !errors.Is(probeErr, syscall.ECONNREFUSED) && !errors.Is(probeErr, os.ErrNotExist) {
		return fmt.Errorf("probe existing local IPC endpoint %q: %w", path, probeErr)
	}

	after, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reinspect stale local IPC endpoint %q: %w", path, err)
	}
	if !os.SameFile(before, after) || after.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("local IPC endpoint %q changed while stale state was inspected", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale local IPC endpoint %q: %w", path, err)
	}

	return nil
}

// inspectOwnedUnixSocket verifies the endpoint did not change type or owner while it was being secured.
func inspectOwnedUnixSocket(path string, expectedUID uint32) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect local IPC socket %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("local IPC endpoint %q is not a direct Unix socket", path)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("local IPC socket %q has unsupported ownership metadata", path)
	}
	if stat.Uid != expectedUID {
		return nil, fmt.Errorf("local IPC socket %q is owned by uid %d, want %d", path, stat.Uid, expectedUID)
	}

	return info, nil
}

// closeAndRemoveUnixEndpoint handles construction failures before an inode can be retained by the listener wrapper.
func closeAndRemoveUnixEndpoint(listener *net.UnixListener, path string) error {
	closeErr := listener.Close()
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

// Close releases the socket and removes it only when the path still names the inode this listener created.
func (listener *unixEndpointListener) Close() error {
	listener.closeOnce.Do(func() {
		closeErr := listener.UnixListener.Close()
		current, statErr := os.Lstat(listener.path)
		if errors.Is(statErr, os.ErrNotExist) {
			statErr = nil
		} else if statErr == nil && os.SameFile(listener.endpoint, current) && current.Mode()&os.ModeSocket != 0 {
			statErr = os.Remove(listener.path)
		} else if statErr == nil {
			statErr = fmt.Errorf("local IPC endpoint %q changed before listener shutdown", listener.path)
		}

		listener.closeErr = errors.Join(closeErr, statErr)
	})

	return listener.closeErr
}

// dial connects to the private Unix socket and authenticates the daemon before exposing the connection.
func dial(ctx context.Context) (Conn, error) {
	path, err := runtimepath.SocketPath()
	if err != nil {
		return nil, fmt.Errorf("resolve local IPC socket: %w", err)
	}

	return dialUnix(ctx, path, uint32(os.Geteuid()), readUnixPeerIdentity)
}

// dialUnix keeps endpoint selection injectable for native transport tests.
func dialUnix(ctx context.Context, path string, expectedUID uint32, readIdentity unixCredentialReader) (Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("dial local IPC socket %q: %w", path, err)
	}

	identity, err := authenticateUnixPeer(connection, expectedUID, readIdentity)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}

	return &authenticatedConn{Conn: connection, peer: identity}, nil
}

// authenticateUnixPeer compares kernel credentials with the effective user that owns Harbor's runtime state.
func authenticateUnixPeer(connection net.Conn, expectedUID uint32, readIdentity unixCredentialReader) (PeerIdentity, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return PeerIdentity{}, fmt.Errorf("authenticate local IPC peer: connection type %T is not a Unix socket", connection)
	}

	identity, err := readIdentity(unixConnection)
	if err != nil {
		return PeerIdentity{}, fmt.Errorf("read local IPC peer credentials: %w", err)
	}
	wantUserID := strconv.FormatUint(uint64(expectedUID), 10)
	if identity.UserID != wantUserID {
		return PeerIdentity{}, fmt.Errorf("%w: peer user %q, want %q", ErrPeerUnauthorized, identity.UserID, wantUserID)
	}
	if identity.ProcessID == 0 {
		return PeerIdentity{}, fmt.Errorf("%w: peer process ID is unavailable", ErrPeerUnauthorized)
	}

	return identity, nil
}
