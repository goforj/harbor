package rpc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

// failingWriter returns one stable transport error for writer state tests.
type failingWriter struct {
	err error
}

// Write returns the configured failure without accepting bytes.
func (w failingWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

// TestFrameRoundTripEnvelope verifies framing preserves one validated request.
func TestFrameRoundTripEnvelope(t *testing.T) {
	deadline := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	want, err := NewRequestEnvelope(Version{Major: 1}, "req-1", "projects.list", deadline, struct{}{})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	var transport bytes.Buffer
	writer, err := NewFrameWriter(&transport, 4096)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	if err := writer.WriteEnvelope(want); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	if declared := binary.BigEndian.Uint32(transport.Bytes()[:frameHeaderSize]); declared != uint32(transport.Len()-frameHeaderSize) {
		t.Fatalf("declared size = %d, body = %d", declared, transport.Len()-frameHeaderSize)
	}

	reader, err := NewFrameReader(&transport, 4096)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	got, err := reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envelope = %#v, want %#v", got, want)
	}
}

// TestFrameReaderRejectsOversizedDeclarationWithoutDraining verifies hostile
// length prefixes cannot force allocation or unbounded payload reads.
func TestFrameReaderRejectsOversizedDeclarationWithoutDraining(t *testing.T) {
	var transport bytes.Buffer
	var header [frameHeaderSize]byte
	binary.BigEndian.PutUint32(header[:], 65)
	transport.Write(header[:])
	transport.Write(bytes.Repeat([]byte{'x'}, 65))

	reader, err := NewFrameReader(&transport, 64)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	_, err = reader.ReadFrame()
	var sizeError FrameSizeError
	if !errors.As(err, &sizeError) || sizeError.Size != 65 || sizeError.Limit != 64 {
		t.Fatalf("error = %#v", err)
	}
	if transport.Len() != 65 {
		t.Fatalf("reader drained %d body bytes", 65-transport.Len())
	}
	if _, err := reader.ReadFrame(); !errors.Is(err, ErrFrameReaderUnusable) {
		t.Fatalf("second read error = %v", err)
	}
}

// TestFrameReaderRejectsEmptyAndPartialFrames verifies a lost frame boundary is
// terminal for the current transport.
func TestFrameReaderRejectsEmptyAndPartialFrames(t *testing.T) {
	for name, test := range map[string]struct {
		encoded    []byte
		firstError error
	}{
		"empty": {
			encoded:    []byte{0, 0, 0, 0},
			firstError: ErrEmptyFrame,
		},
		"partial header": {
			encoded:    []byte{0, 0},
			firstError: io.ErrUnexpectedEOF,
		},
		"partial body": {
			encoded:    []byte{0, 0, 0, 4, '{', '}'},
			firstError: io.ErrUnexpectedEOF,
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader, err := NewFrameReader(bytes.NewReader(test.encoded), 64)
			if err != nil {
				t.Fatalf("create reader: %v", err)
			}
			if _, err := reader.ReadFrame(); !errors.Is(err, test.firstError) {
				t.Fatalf("first read error = %v, want %v", err, test.firstError)
			}
			if _, err := reader.ReadFrame(); !errors.Is(err, ErrFrameReaderUnusable) {
				t.Fatalf("second read error = %v", err)
			}
		})
	}
}

// TestFrameReaderRecoversAfterMalformedJSON verifies a complete bad frame does
// not obscure the length boundary of the next frame.
func TestFrameReaderRecoversAfterMalformedJSON(t *testing.T) {
	var transport bytes.Buffer
	transport.Write([]byte{0, 0, 0, 1, '{'})
	transport.Write([]byte{0, 0, 0, 2, '{', '}'})
	reader, err := NewFrameReader(&transport, 64)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	if _, err := reader.ReadFrame(); !errors.Is(err, ErrInvalidFrameJSON) {
		t.Fatalf("first read error = %v", err)
	}
	payload, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if string(payload) != "{}" {
		t.Fatalf("payload = %q", payload)
	}
}

// TestFrameWriterEnforcesLimitBeforeWriting verifies rejected frames leave the
// transport untouched for a smaller valid frame.
func TestFrameWriterEnforcesLimitBeforeWriting(t *testing.T) {
	var transport bytes.Buffer
	writer, err := NewFrameWriter(&transport, 2)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	if err := writer.WriteFrame([]byte(`{"long":true}`)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if transport.Len() != 0 {
		t.Fatalf("transport has %d bytes", transport.Len())
	}
	if err := writer.WriteFrame([]byte(`{}`)); err != nil {
		t.Fatalf("write valid frame: %v", err)
	}
}

// TestFrameWriterBecomesUnusableAfterTransportFailure verifies no frame follows
// a prefix or payload whose delivery is uncertain.
func TestFrameWriterBecomesUnusableAfterTransportFailure(t *testing.T) {
	writer, err := NewFrameWriter(failingWriter{err: errors.New("transport failed")}, 64)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	if err := writer.WriteFrame([]byte(`{}`)); err == nil {
		t.Fatal("transport failure not returned")
	}
	if err := writer.WriteFrame([]byte(`{}`)); !errors.Is(err, ErrFrameWriterUnusable) {
		t.Fatalf("second write error = %v", err)
	}
}

// TestFrameLimitIsBoundedByProtocolPolicy verifies transports cannot silently
// opt out of the shared hard maximum.
func TestFrameLimitIsBoundedByProtocolPolicy(t *testing.T) {
	for _, limit := range []uint32{0, MaximumFrameSize + 1} {
		if _, err := NewFrameReader(bytes.NewReader(nil), limit); !errors.Is(err, ErrInvalidFrameLimit) {
			t.Fatalf("reader limit %d error = %v", limit, err)
		}
		if _, err := NewFrameWriter(io.Discard, limit); !errors.Is(err, ErrInvalidFrameLimit) {
			t.Fatalf("writer limit %d error = %v", limit, err)
		}
	}
}
