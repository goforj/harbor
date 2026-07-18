package session

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

// sessionWriter serializes the write deadline with its frame so concurrent
// requests cannot clear or replace another request's timeout.
type sessionWriter struct {
	connection net.Conn
	frames     *rpc.FrameWriter
	timeout    time.Duration
	terminate  func(error)
	now        func() time.Time
	mutex      sync.Mutex
	failure    error
}

// newSessionWriter creates the post-handshake writer for one negotiated connection.
func newSessionWriter(
	connection net.Conn,
	frames *rpc.FrameWriter,
	timeout time.Duration,
	terminate func(error),
) *sessionWriter {
	return &sessionWriter{
		connection: connection,
		frames:     frames,
		timeout:    timeout,
		terminate:  terminate,
		now:        time.Now,
	}
}

// writeEnvelope gives each complete frame its own bounded transport lifetime.
func (w *sessionWriter) writeEnvelope(envelope rpc.Envelope) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.failure != nil {
		return w.failure
	}

	deadline := w.now().Add(w.timeout)
	if err := w.connection.SetWriteDeadline(deadline); err != nil {
		return w.failLocked(fmt.Errorf("set RPC session write deadline: %w", err))
	}
	if err := w.frames.WriteEnvelope(envelope); err != nil {
		return w.failLocked(fmt.Errorf(
			"write RPC session frame: %w",
			classifyWriteError(w.now(), deadline, err),
		))
	}
	if err := w.connection.SetWriteDeadline(noDeadline); err != nil {
		// A peer may close immediately after consuming the complete frame. The
		// stream is already terminal in that case, so the read side owns its
		// normal EOF instead of replacing it with deadline-cleanup noise.
		if isClosedConnectionError(err) {
			return nil
		}
		return w.failLocked(fmt.Errorf("clear RPC session write deadline: %w", err))
	}

	return nil
}

// failLocked preserves the first unusable-stream cause before closing the connection.
func (w *sessionWriter) failLocked(err error) error {
	if w.failure == nil {
		w.failure = err
		w.terminate(err)
	}

	return w.failure
}

// isClosedConnectionError recognizes streams whose lifetime already ended independently of deadline cleanup.
func isClosedConnectionError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// classifyWriteError exposes one portable timeout cause only after the configured deadline elapsed.
func classifyWriteError(now time.Time, deadline time.Time, err error) error {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() && !now.Before(deadline) {
		return fmt.Errorf("%w: %w", ErrWriteTimeout, err)
	}

	return err
}
