package ticketauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxEnvelopeBytes bounds one canonical signed ticket before durable storage or JSON decoding.
const MaxEnvelopeBytes = 16 * 1024

// Encode returns the deterministic JSON representation used for signed-ticket persistence.
func Encode(envelope Envelope) ([]byte, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode helper ticket envelope: %w", err)
	}
	if len(encoded) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("encode helper ticket envelope: content exceeds %d bytes", MaxEnvelopeBytes)
	}
	return encoded, nil
}

// Decode accepts exactly one bounded envelope in the authoritative deterministic representation.
func Decode(encoded []byte) (Envelope, error) {
	if len(encoded) == 0 {
		return Envelope{}, errors.New("decode helper ticket envelope: content is empty")
	}
	if len(encoded) > MaxEnvelopeBytes {
		return Envelope{}, fmt.Errorf("decode helper ticket envelope: content exceeds %d bytes", MaxEnvelopeBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode helper ticket envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("decode helper ticket envelope: content must contain exactly one JSON value")
	}
	canonical, err := Encode(envelope)
	if err != nil {
		return Envelope{}, fmt.Errorf("decode helper ticket envelope: %w", err)
	}
	// Exact bytes make field names, counts, ordering, and nesting unambiguous without a second permissive schema.
	if !bytes.Equal(encoded, canonical) {
		return Envelope{}, errors.New("decode helper ticket envelope: content is not canonical")
	}
	return envelope, nil
}
