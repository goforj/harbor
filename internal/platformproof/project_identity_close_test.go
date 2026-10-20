package platformproof

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// closingProofConnection blocks native close completion so proof retirement can be checked deterministically.
type closingProofConnection struct {
	net.Conn
	entered  chan struct{}
	release  chan struct{}
	writeErr error
}

// Write optionally reproduces a failed payload without bypassing socket retirement.
func (connection *closingProofConnection) Write(value []byte) (int, error) {
	if connection.writeErr != nil {
		return 0, connection.writeErr
	}
	return connection.Conn.Write(value)
}

// Close holds the socket open until the test permits native retirement to complete.
func (connection *closingProofConnection) Close() error {
	close(connection.entered)
	<-connection.release
	return connection.Conn.Close()
}

// proofConnectionListener exposes one accepted connection without opening host sockets.
type proofConnectionListener struct {
	connection net.Conn
}

// Accept returns the connection whose retirement must precede completion.
func (listener proofConnectionListener) Accept() (net.Conn, error) {
	return listener.connection, nil
}

// Addr identifies the fixture endpoint for bounded proof errors.
func (proofConnectionListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3306}
}

// Close satisfies the fixture listener's independently managed lifetime.
func (proofConnectionListener) Close() error { return nil }

// TestServePayloadClosesBeforeCompletion prevents alias cleanup from racing accepted native sockets.
func TestServePayloadClosesBeforeCompletion(t *testing.T) {
	for _, writeErr := range []error{nil, errors.New("payload write failed")} {
		t.Run("write="+fmtError(writeErr), func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			go func() { _, _ = io.Copy(io.Discard, client) }()
			connection := &closingProofConnection{Conn: server, entered: make(chan struct{}), release: make(chan struct{}), writeErr: writeErr}
			result := make(chan error, 1)
			t.Cleanup(func() {
				close(connection.release)
				select {
				case err := <-result:
					if !errors.Is(err, writeErr) {
						t.Errorf("proof result = %v, want %v", err, writeErr)
					}
				case <-time.After(5 * time.Second):
					t.Error("proof did not finish after socket retirement")
				}
			})
			go servePayload(context.Background(), proofConnectionListener{connection}, signedPayload{}, result)
			select {
			case <-connection.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("proof did not begin socket retirement")
			}
			select {
			case err := <-result:
				t.Fatalf("proof completed before socket retirement: %v", err)
			default:
			}
		})
	}
}

// fmtError gives the write path a stable subtest label.
func fmtError(err error) string {
	if err == nil {
		return "success"
	}
	return "failure"
}
