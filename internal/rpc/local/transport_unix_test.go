//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/daemon"
)

// TestDefaultUnixTransportUsesRuntimeDiscovery verifies the public API joins path resolution, locking, and peer admission.
func TestDefaultUnixTransportUsesRuntimeDiscovery(t *testing.T) {
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	t.Setenv("TMPDIR", runtimeRoot)

	lock, err := daemon.AcquireProcessLock()
	if err != nil {
		t.Fatalf("acquire daemon authority: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	listener, err := Listen()
	if err != nil {
		t.Fatalf("listen on discovered Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		accepted <- err
	}()

	connection, err := Dial(nil)
	if err != nil {
		t.Fatalf("dial discovered Unix endpoint: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close discovered Unix connection: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("accept discovered Unix connection: %v", err)
	}
}

// TestUnixTransportAuthenticatesBothPeers verifies clients and servers receive kernel-backed identities.
func TestUnixTransportAuthenticatesBothPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "harbord.sock")
	userID := strconv.Itoa(os.Geteuid())
	listener, err := listenUnix(path, uint32(os.Geteuid()), readUnixPeerIdentity)
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close Unix listener: %v", err)
		}
	})

	type acceptResult struct {
		connection Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := listener.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()

	client, err := dialUnix(context.Background(), path, uint32(os.Geteuid()), readUnixPeerIdentity)
	if err != nil {
		t.Fatalf("dial Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	result := <-accepted
	if result.err != nil {
		t.Fatalf("accept Unix connection: %v", result.err)
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

// TestUnixTransportRejectsDifferentUser verifies admission fails before an RPC handshake can begin.
func TestUnixTransportRejectsDifferentUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := listenUnix(path, uint32(os.Geteuid()), func(*net.UnixConn) (PeerIdentity, error) {
		return PeerIdentity{UserID: "different-user", ProcessID: 42}, nil
	})
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()

	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	if err := <-accepted; !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("accept error = %v, want %v", err, ErrPeerUnauthorized)
	}
}

// TestUnixTransportRejectsInvalidCredentialResults verifies missing and unreadable kernel identity fails closed.
func TestUnixTransportRejectsInvalidCredentialResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := openUnixEndpoint(path)
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan *net.UnixConn, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			accepted <- nil
			return
		}
		accepted <- connection
	}()
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-accepted
	if server == nil {
		t.Fatal("accept Unix connection failed")
	}
	t.Cleanup(func() { _ = server.Close() })

	credentialErr := errors.New("credential failure")
	for _, testCase := range []struct {
		name string
		read unixCredentialReader
		want error
	}{
		{
			name: "read failure",
			read: func(*net.UnixConn) (PeerIdentity, error) {
				return PeerIdentity{}, credentialErr
			},
			want: credentialErr,
		},
		{
			name: "missing process",
			read: func(*net.UnixConn) (PeerIdentity, error) {
				return PeerIdentity{UserID: strconv.Itoa(os.Geteuid())}, nil
			},
			want: ErrPeerUnauthorized,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := authenticateUnixPeer(server, uint32(os.Geteuid()), testCase.read)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("authenticate error = %v, want %v", err, testCase.want)
			}
		})
	}

	pipeServer, pipeClient := net.Pipe()
	t.Cleanup(func() { _ = pipeServer.Close() })
	t.Cleanup(func() { _ = pipeClient.Close() })
	if _, err := authenticateUnixPeer(pipeServer, uint32(os.Geteuid()), readUnixPeerIdentity); err == nil {
		t.Fatal("in-memory connection unexpectedly accepted as Unix socket")
	}
}

// TestUnixTransportRemovesRefusedSocket verifies a crash artifact cannot prevent the lock-owning daemon from restarting.
func TestUnixTransportRemovesRefusedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	address := &net.UnixAddr{Name: path, Net: "unix"}
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("create stale Unix endpoint: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale Unix endpoint: %v", err)
	}

	listener, err := openUnixEndpoint(path)
	if err != nil {
		t.Fatalf("replace stale Unix endpoint: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close replacement Unix endpoint: %v", err)
	}
}

// TestUnixTransportPreservesLiveSocket verifies startup never unlinks an endpoint that accepts connections.
func TestUnixTransportPreservesLiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	address := &net.UnixAddr{Name: path, Net: "unix"}
	live, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("create live Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	listener, err := openUnixEndpoint(path)
	if err == nil || !strings.Contains(err.Error(), "already accepting") {
		t.Fatalf("second listener error = %v, want live endpoint rejection", err)
	}
	if listener != nil {
		t.Fatal("live endpoint returned a replacement listener")
	}
}

// TestUnixTransportRejectsAmbiguousEndpointTypes verifies stale cleanup cannot delete user files or links.
func TestUnixTransportRejectsAmbiguousEndpointTypes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		create func(string) error
	}{
		{
			name: "regular file",
			create: func(path string) error {
				return os.WriteFile(path, []byte("keep"), 0o600)
			},
		},
		{
			name: "symbolic link",
			create: func(path string) error {
				return os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "harbord.sock")
			if err := testCase.create(path); err != nil {
				t.Fatalf("create ambiguous endpoint: %v", err)
			}

			listener, err := openUnixEndpoint(path)
			if err == nil {
				t.Fatal("ambiguous endpoint unexpectedly accepted")
			}
			if listener != nil {
				t.Fatal("ambiguous endpoint returned a listener")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("ambiguous endpoint was removed: %v", err)
			}
		})
	}
}

// TestUnixTransportRejectsAmbiguousListenPaths verifies endpoint selection never inherits a working directory.
func TestUnixTransportRejectsAmbiguousListenPaths(t *testing.T) {
	for _, path := range []string{"", "harbord.sock", filepath.Join("relative", "harbord.sock")} {
		listener, err := openUnixEndpoint(path)
		if err == nil {
			t.Fatalf("openUnixEndpoint(%q) unexpectedly succeeded", path)
		}
		if listener != nil {
			t.Fatalf("openUnixEndpoint(%q) returned a listener", path)
		}
	}
}

// TestUnixTransportRejectsAmbiguousRuntimeLeaf verifies sockets cannot be created through a leaf symlink or file.
func TestUnixTransportRejectsAmbiguousRuntimeLeaf(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(root, "linked-runtime")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create runtime symlink: %v", err)
	}
	if err := prepareUnixRuntimeDirectory(link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink runtime error = %v, want symbolic-link rejection", err)
	}

	file := filepath.Join(root, "runtime-file")
	if err := os.WriteFile(file, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create runtime file: %v", err)
	}
	if err := prepareUnixRuntimeDirectory(file); err == nil {
		t.Fatal("runtime file unexpectedly accepted as directory")
	}
}

// TestUnixTransportRejectsForeignRuntimeOwner verifies permission repair cannot cross a user boundary.
func TestUnixTransportRejectsForeignRuntimeOwner(t *testing.T) {
	path := t.TempDir()
	err := prepareUnixRuntimeDirectoryForUser(path, uint32(os.Geteuid()+1), os.Chmod)
	if err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("foreign-owner error = %v, want ownership rejection", err)
	}
}

// TestUnixTransportReportsPermissionRepairFailure verifies a failed owner-only mode change stops startup.
func TestUnixTransportReportsPermissionRepairFailure(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	err := prepareUnixRuntimeDirectoryForUser(t.TempDir(), uint32(os.Geteuid()), func(string, os.FileMode) error {
		return chmodErr
	})
	if !errors.Is(err, chmodErr) {
		t.Fatalf("permission repair error = %v, want %v", err, chmodErr)
	}
}

// TestInspectOwnedUnixSocketRejectsWrongTypeAndOwner verifies post-bind inspection fails closed.
func TestInspectOwnedUnixSocketRejectsWrongTypeAndOwner(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create regular endpoint: %v", err)
	}
	if _, err := inspectOwnedUnixSocket(regular, uint32(os.Geteuid())); err == nil {
		t.Fatal("regular endpoint unexpectedly passed socket inspection")
	}

	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on inspected endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if _, err := inspectOwnedUnixSocket(path, uint32(os.Geteuid()+1)); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("foreign socket error = %v, want ownership rejection", err)
	}
}

// TestUnixTransportSecuresOnlyHarborLeaf verifies existing platform parents retain their chosen permissions.
func TestUnixTransportSecuresOnlyHarborLeaf(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "platform-runtime")
	if err := os.Mkdir(parent, 0o751); err != nil {
		t.Fatalf("create platform runtime parent: %v", err)
	}
	leaf := filepath.Join(parent, "harbor")
	path := filepath.Join(leaf, "harbord.sock")
	listener, err := openUnixEndpoint(path)
	if err != nil {
		t.Fatalf("listen below platform runtime parent: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("inspect platform runtime parent: %v", err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o751 {
		t.Errorf("platform parent mode = %o, want 751", got)
	}
	leafInfo, err := os.Stat(leaf)
	if err != nil {
		t.Fatalf("inspect Harbor runtime leaf: %v", err)
	}
	if got := leafInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("Harbor leaf mode = %o, want 700", got)
	}
	socketInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("inspect Harbor socket: %v", err)
	}
	if got := socketInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("Harbor socket mode = %o, want 600", got)
	}
}

// TestUnixTransportDoesNotRemoveReplacementPath verifies shutdown cannot unlink a path substituted after bind.
func TestUnixTransportDoesNotRemoveReplacementPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := openUnixEndpoint(path)
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("unlink active endpoint for replacement test: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("create replacement path: %v", err)
	}

	if err := listener.Close(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("close error = %v, want changed endpoint warning", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement path: %v", err)
	}
	if string(contents) != "replacement" {
		t.Fatalf("replacement contents = %q, want preserved contents", contents)
	}
}

// TestUnixTransportCloseToleratesMissingEndpoint verifies external cleanup does not turn orderly shutdown into failure.
func TestUnixTransportCloseToleratesMissingEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := openUnixEndpoint(path)
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove endpoint before shutdown: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener without endpoint: %v", err)
	}
}

// TestCloseAndRemoveUnixEndpointReleasesConstructionArtifact verifies failed setup does not retain a socket path.
func TestCloseAndRemoveUnixEndpointReleasesConstructionArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on construction artifact: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := closeAndRemoveUnixEndpoint(listener, path); err != nil {
		t.Fatalf("close and remove construction artifact: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("construction artifact still exists: %v", err)
	}
}

// TestCloseAndRemoveUnixEndpointToleratesAutomaticUnlink verifies cleanup accepts a socket already removed by Go.
func TestCloseAndRemoveUnixEndpointToleratesAutomaticUnlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on automatically unlinked endpoint: %v", err)
	}
	if err := closeAndRemoveUnixEndpoint(listener, path); err != nil {
		t.Fatalf("clean automatically unlinked endpoint: %v", err)
	}
}

// TestUnixDialRejectsUnexpectedDaemonUser verifies the client closes a connection that fails kernel admission.
func TestUnixDialRejectsUnexpectedDaemonUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on Unix endpoint: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		connection, _ := listener.AcceptUnix()
		accepted <- connection
	}()

	connection, err := dialUnix(context.Background(), path, uint32(os.Geteuid()), func(*net.UnixConn) (PeerIdentity, error) {
		return PeerIdentity{UserID: "different-user", ProcessID: 42}, nil
	})
	if !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("dial error = %v, want %v", err, ErrPeerUnauthorized)
	}
	if connection != nil {
		t.Fatal("unauthorized daemon returned a connection")
	}
	server := <-accepted
	if server != nil {
		_ = server.Close()
	}
}

// TestUnixDialHonorsCanceledContext verifies command cancellation reaches the operating-system dial immediately.
func TestUnixDialHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := filepath.Join(t.TempDir(), "missing.sock")
	_, err := dialUnix(ctx, path, uint32(os.Geteuid()), readUnixPeerIdentity)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dial error = %v, want %v", err, context.Canceled)
	}
}

// TestRemoveStaleUnixEndpointRejectsUncertainProbe verifies timeouts and permission errors never authorize removal.
func TestRemoveStaleUnixEndpointRejectsUncertainProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	address := &net.UnixAddr{Name: path, Net: "unix"}
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("create Unix endpoint: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close Unix endpoint: %v", err)
	}

	probeError := &net.OpError{Op: "dial", Net: "unix", Err: os.ErrPermission}
	err = removeStaleUnixEndpoint(path, func(string, string, time.Duration) (net.Conn, error) {
		return nil, probeError
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("stale probe error = %v, want permission error", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("uncertain endpoint was removed: %v", err)
	}
}

// TestRemoveStaleUnixEndpointDetectsProbeRace verifies a replaced inode cannot inherit stale-removal authority.
func TestRemoveStaleUnixEndpointDetectsProbeRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbord.sock")
	address := &net.UnixAddr{Name: path, Net: "unix"}
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("create Unix endpoint: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close Unix endpoint: %v", err)
	}

	err = removeStaleUnixEndpoint(path, func(string, string, time.Duration) (net.Conn, error) {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove stale endpoint in probe: %v", err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatalf("replace endpoint in probe: %v", err)
		}
		return nil, syscall.ECONNREFUSED
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("probe race error = %v, want changed endpoint rejection", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement path was removed: %v", err)
	}
}
