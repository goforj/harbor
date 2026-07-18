package rpc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// MaximumFrameSize is the absolute JSON payload limit for one IPC frame.
	// Large streams such as logs must be split into ordered event chunks.
	MaximumFrameSize uint32 = 1 << 20
	frameHeaderSize         = 4
)

var (
	// ErrInvalidFrameLimit reports a zero or above-policy frame limit.
	ErrInvalidFrameLimit = errors.New("invalid frame limit")
	// ErrEmptyFrame reports a zero-length frame, which cannot contain JSON.
	ErrEmptyFrame = errors.New("empty frame")
	// ErrInvalidFrameJSON reports a complete frame that is not one JSON value.
	ErrInvalidFrameJSON = errors.New("frame is not valid JSON")
	// ErrFrameReaderUnusable reports that a declared length or partial read left
	// the stream boundary unsafe for another frame.
	ErrFrameReaderUnusable = errors.New("frame reader is unusable")
	// ErrFrameWriterUnusable reports that a partial write may have corrupted the stream.
	ErrFrameWriterUnusable = errors.New("frame writer is unusable")
)

// FrameSizeError reports a declared or encoded frame that exceeds policy.
type FrameSizeError struct {
	Size  uint32
	Limit uint32
}

// Error returns a bounded diagnostic containing only frame lengths.
func (e FrameSizeError) Error() string {
	return fmt.Sprintf("frame size %d exceeds limit %d", e.Size, e.Limit)
}

// FrameReader reads four-byte big-endian length-prefixed JSON frames.
type FrameReader struct {
	reader   io.Reader
	limit    uint32
	unusable bool
}

// NewFrameReader creates a reader with a caller-selected limit at or below the
// protocol hard maximum.
func NewFrameReader(reader io.Reader, limit uint32) (*FrameReader, error) {
	if err := validateFrameLimit(limit); err != nil {
		return nil, err
	}

	return &FrameReader{reader: reader, limit: limit}, nil
}

// NewDefaultFrameReader creates a reader using the protocol hard maximum.
func NewDefaultFrameReader(reader io.Reader) *FrameReader {
	return &FrameReader{reader: reader, limit: MaximumFrameSize}
}

// ReadFrame returns one complete JSON payload. An oversized declaration is not
// drained because doing so would let an unauthenticated peer force unbounded IO;
// the caller must close the transport instead.
func (r *FrameReader) ReadFrame() ([]byte, error) {
	if r.unusable {
		return nil, ErrFrameReaderUnusable
	}

	var header [frameHeaderSize]byte
	read, err := io.ReadFull(r.reader, header[:])
	if err != nil {
		if read > 0 {
			r.unusable = true
		}
		return nil, err
	}

	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		r.unusable = true
		return nil, ErrEmptyFrame
	}
	if size > r.limit {
		r.unusable = true
		return nil, FrameSizeError{Size: size, Limit: r.limit}
	}

	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r.reader, payload); err != nil {
		r.unusable = true
		return nil, err
	}
	if !json.Valid(payload) {
		return nil, ErrInvalidFrameJSON
	}

	return payload, nil
}

// ReadEnvelope decodes and validates one envelope while tolerating unknown
// additive fields in both the envelope and its typed payload.
func (r *FrameReader) ReadEnvelope() (Envelope, error) {
	payload, err := r.ReadFrame()
	if err != nil {
		return Envelope{}, err
	}

	var envelope Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, fmt.Errorf("validate envelope: %w", err)
	}

	return envelope, nil
}

// FrameWriter writes serialized JSON frames and prevents concurrent writes from
// interleaving on stream transports.
type FrameWriter struct {
	writer   io.Writer
	limit    uint32
	mutex    sync.Mutex
	unusable bool
}

// NewFrameWriter creates a writer with a caller-selected limit at or below the
// protocol hard maximum.
func NewFrameWriter(writer io.Writer, limit uint32) (*FrameWriter, error) {
	if err := validateFrameLimit(limit); err != nil {
		return nil, err
	}

	return &FrameWriter{writer: writer, limit: limit}, nil
}

// NewDefaultFrameWriter creates a writer using the protocol hard maximum.
func NewDefaultFrameWriter(writer io.Writer) *FrameWriter {
	return &FrameWriter{writer: writer, limit: MaximumFrameSize}
}

// WriteFrame writes one already-encoded JSON value with its length prefix.
func (w *FrameWriter) WriteFrame(payload []byte) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.unusable {
		return ErrFrameWriterUnusable
	}
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	if uint64(len(payload)) > uint64(w.limit) {
		size := uint32(len(payload))
		if uint64(len(payload)) > uint64(^uint32(0)) {
			size = ^uint32(0)
		}
		return FrameSizeError{Size: size, Limit: w.limit}
	}
	if !json.Valid(payload) {
		return ErrInvalidFrameJSON
	}

	frame := make([]byte, frameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(frame[:frameHeaderSize], uint32(len(payload)))
	copy(frame[frameHeaderSize:], payload)
	if err := writeAll(w.writer, frame); err != nil {
		w.unusable = true
		return err
	}

	return nil
}

// WriteEnvelope validates and serializes one envelope through the deterministic
// standard JSON encoder before framing it.
func (w *FrameWriter) WriteEnvelope(envelope Envelope) error {
	if err := envelope.Validate(); err != nil {
		return fmt.Errorf("validate envelope: %w", err)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}

	return w.WriteFrame(payload)
}

// validateFrameLimit enforces one compile-time protocol ceiling across every transport.
func validateFrameLimit(limit uint32) error {
	if limit == 0 || limit > MaximumFrameSize {
		return fmt.Errorf("%w: must be between 1 and %d bytes", ErrInvalidFrameLimit, MaximumFrameSize)
	}

	return nil
}

// writeAll treats a short or stalled write as terminal because another frame
// cannot safely follow a partially written prefix or payload.
func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		payload = payload[written:]
	}

	return nil
}
