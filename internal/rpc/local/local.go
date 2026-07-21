package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ErrPeerUnauthorized identifies a local process whose operating-system user does not own this Harbor session.
var ErrPeerUnauthorized = errors.New("local IPC peer is not authorized")

// PeerIdentity describes the operating-system identity authenticated for a local connection.
type PeerIdentity struct {
	// UserID is the platform-native user identifier authenticated by the transport.
	UserID string
	// ProcessID is the peer process identified by the operating system.
	ProcessID uint32
}

// Conn is a local connection paired with identity established by the operating system.
type Conn interface {
	net.Conn
	// Peer returns the identity authenticated for this connection.
	Peer() PeerIdentity
}

// Listener accepts only connections whose peer identity has been inspected by the operating system.
type Listener interface {
	// Accept returns the next connection after operating-system peer admission.
	Accept() (Conn, error)
	// Close releases the platform endpoint and safely tolerates repeated calls.
	Close() error
	// Addr returns the platform endpoint used by the listener.
	Addr() net.Addr
}

// authenticatedConn keeps identity immutable for the lifetime of its underlying connection.
type authenticatedConn struct {
	net.Conn
	peer PeerIdentity
}

// Peer returns the operating-system identity authenticated when the connection was established.
func (connection *authenticatedConn) Peer() PeerIdentity {
	return connection.peer
}

// authenticatingListener rejects a connection before application bytes are read when peer admission fails.
type authenticatingListener struct {
	listener     net.Listener
	authenticate func(net.Conn) (PeerIdentity, error)
	closeOnce    sync.Once
	closeErr     error
}

// Accept authenticates the next operating-system connection before returning it to the RPC server.
func (listener *authenticatingListener) Accept() (Conn, error) {
	connection, err := listener.listener.Accept()
	if err != nil {
		return nil, err
	}

	identity, err := listener.authenticate(connection)
	if err == nil {
		return &authenticatedConn{Conn: connection, peer: identity}, nil
	}

	if closeErr := connection.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close rejected local IPC peer: %w", closeErr))
	}
	return nil, err
}

// Close stops accepting local connections and releases the platform endpoint.
func (listener *authenticatingListener) Close() error {
	listener.closeOnce.Do(func() {
		listener.closeErr = listener.listener.Close()
	})

	return listener.closeErr
}

// Addr returns the platform endpoint used by the listener.
func (listener *authenticatingListener) Addr() net.Addr {
	return listener.listener.Addr()
}

// Dial connects to the current user's Harbor daemon and authenticates its operating-system identity.
func Dial(ctx context.Context) (Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	return dial(ctx)
}

// Listen creates the current user's authenticated Harbor endpoint.
//
// The caller must hold Harbor's daemon process lock until after the returned listener is closed. That
// authority makes stale endpoint removal safe and prevents two daemon processes from replacing each
// other's endpoint during startup.
func Listen() (Listener, error) {
	return listen()
}

// EndpointReference returns the current user's Harbor IPC endpoint without opening a connection.
func EndpointReference() (string, error) {
	return endpointReference()
}
