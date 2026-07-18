package rpc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// FuzzFrameReaderBoundaries verifies arbitrary prefixes never bypass the frame
// limit or return an incomplete JSON value as a successful frame.
func FuzzFrameReaderBoundaries(f *testing.F) {
	f.Add(uint32(2), []byte(`{}`))
	f.Add(uint32(0), []byte{})
	f.Add(uint32(65), bytes.Repeat([]byte{'x'}, 65))
	f.Fuzz(func(t *testing.T, declared uint32, body []byte) {
		var encoded bytes.Buffer
		var header [frameHeaderSize]byte
		binary.BigEndian.PutUint32(header[:], declared)
		encoded.Write(header[:])
		encoded.Write(body)

		reader, err := NewFrameReader(&encoded, 64)
		if err != nil {
			t.Fatalf("create reader: %v", err)
		}
		payload, err := reader.ReadFrame()
		if err != nil {
			return
		}
		if len(payload) > 64 {
			t.Fatalf("payload length = %d", len(payload))
		}
		if uint32(len(payload)) != declared {
			t.Fatalf("payload length = %d, declared = %d", len(payload), declared)
		}
		if !json.Valid(payload) {
			t.Fatalf("reader returned invalid JSON %q", payload)
		}
	})
}

// FuzzEnvelopeDecoding verifies arbitrary additive or malformed JSON cannot
// panic the stable envelope decoder and valid messages remain frameable.
func FuzzEnvelopeDecoding(f *testing.F) {
	f.Add([]byte(`{"kind":"cancel","protocol":{"major":1,"minor":0},"request_id":"req-1"}`))
	f.Add([]byte(`{"kind":"request","future":true}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		var envelope Envelope
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return
		}
		if err := envelope.Validate(); err != nil {
			return
		}

		var transport bytes.Buffer
		writer := NewDefaultFrameWriter(&transport)
		if err := writer.WriteEnvelope(envelope); err != nil {
			t.Fatalf("write validated envelope: %v", err)
		}
		reader := NewDefaultFrameReader(&transport)
		if _, err := reader.ReadEnvelope(); err != nil {
			t.Fatalf("read validated envelope: %v", err)
		}
	})
}
