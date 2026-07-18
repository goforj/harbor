package local

import (
	"errors"
	"net"
	"testing"
)

// testListener supplies controlled connections to the common admission wrapper.
type testListener struct {
	connection net.Conn
	acceptErr  error
	closeErr   error
}

// Accept returns the connection configured by the test.
func (listener *testListener) Accept() (net.Conn, error) {
	return listener.connection, listener.acceptErr
}

// Close is a no-op because the test owns both ends of the in-memory connection.
func (listener *testListener) Close() error {
	return listener.closeErr
}

// TestAuthenticatingListenerDelegatesLifecycle verifies transport-neutral address and idempotent closure behavior.
func TestAuthenticatingListenerDelegatesLifecycle(t *testing.T) {
	closeErr := errors.New("close failure")
	base := &testListener{closeErr: closeErr}
	listener := &authenticatingListener{listener: base}
	if got := listener.Addr().String(); got != "local-test" {
		t.Fatalf("listener address = %q, want local-test", got)
	}
	if err := listener.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close error = %v, want %v", err, closeErr)
	}
	if err := listener.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second close error = %v, want cached %v", err, closeErr)
	}
}

// TestAuthenticatingListenerPreservesAcceptFailure verifies endpoint errors are not mislabeled as peer rejection.
func TestAuthenticatingListenerPreservesAcceptFailure(t *testing.T) {
	acceptErr := errors.New("accept failure")
	listener := &authenticatingListener{listener: &testListener{acceptErr: acceptErr}}
	connection, err := listener.Accept()
	if !errors.Is(err, acceptErr) {
		t.Fatalf("accept error = %v, want %v", err, acceptErr)
	}
	if connection != nil {
		t.Fatal("failed accept returned a connection")
	}
}

// Addr returns an in-memory address suitable for common listener tests.
func (listener *testListener) Addr() net.Addr {
	return testAddress("local-test")
}

// testAddress gives the listener a deterministic address without coupling common tests to an operating system.
type testAddress string

// Network identifies the address as an in-memory test endpoint.
func (address testAddress) Network() string {
	return "test"
}

// String returns the deterministic test endpoint name.
func (address testAddress) String() string {
	return string(address)
}

// errorCloseConn supplies a deterministic close failure for rejected-peer cleanup tests.
type errorCloseConn struct {
	net.Conn
	closeErr error
}

// Close returns the configured failure after closing the underlying in-memory connection.
func (connection *errorCloseConn) Close() error {
	_ = connection.Conn.Close()
	return connection.closeErr
}

// TestAuthenticatingListenerReturnsImmutablePeer verifies admitted identity stays attached to the connection.
func TestAuthenticatingListenerReturnsImmutablePeer(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	want := PeerIdentity{UserID: "user-1000", ProcessID: 42}
	listener := &authenticatingListener{
		listener: &testListener{connection: server},
		authenticate: func(net.Conn) (PeerIdentity, error) {
			return want, nil
		},
	}

	connection, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept authenticated connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if got := connection.Peer(); got != want {
		t.Fatalf("peer = %+v, want %+v", got, want)
	}
}

// TestAuthenticatingListenerClosesRejectedPeer verifies untrusted processes cannot retain an admitted connection.
func TestAuthenticatingListenerClosesRejectedPeer(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	listener := &authenticatingListener{
		listener: &testListener{connection: server},
		authenticate: func(net.Conn) (PeerIdentity, error) {
			return PeerIdentity{}, ErrPeerUnauthorized
		},
	}

	connection, err := listener.Accept()
	if !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("accept error = %v, want %v", err, ErrPeerUnauthorized)
	}
	if connection != nil {
		t.Fatal("rejected peer returned a connection")
	}

	buffer := make([]byte, 1)
	if _, err := client.Read(buffer); err == nil {
		t.Fatal("rejected peer remained connected")
	}
}

// TestAuthenticatingListenerReportsRejectedPeerCloseFailure verifies cleanup errors remain observable with admission failure.
func TestAuthenticatingListenerReportsRejectedPeerCloseFailure(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	closeErr := errors.New("close failure")
	listener := &authenticatingListener{
		listener: &testListener{connection: &errorCloseConn{Conn: server, closeErr: closeErr}},
		authenticate: func(net.Conn) (PeerIdentity, error) {
			return PeerIdentity{}, ErrPeerUnauthorized
		},
	}

	_, err := listener.Accept()
	if !errors.Is(err, ErrPeerUnauthorized) || !errors.Is(err, closeErr) {
		t.Fatalf("accept error = %v, want admission and close errors", err)
	}
}
