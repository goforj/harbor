package ticketkey

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	keyDocumentVersion      uint16 = 1
	keyDocumentAlgorithm           = "ed25519"
	maximumKeyDocumentBytes        = 256
)

// keyDocument is intentionally seed-based so the durable representation has one fixed-width secret value.
type keyDocument struct {
	Version   uint16 `json:"version"`
	Algorithm string `json:"algorithm"`
	Seed      string `json:"seed"`
}

// encodePrivateKey returns the only durable byte representation accepted by this store version.
func encodePrivateKey(privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("Ed25519 private key has an invalid size")
	}
	document := keyDocument{
		Version:   keyDocumentVersion,
		Algorithm: keyDocumentAlgorithm,
		Seed:      base64.StdEncoding.EncodeToString(privateKey.Seed()),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode helper ticket signing key: %w", err)
	}
	return encoded, nil
}

// decodePrivateKey admits only the exact versioned bytes emitted by encodePrivateKey.
func decodePrivateKey(encoded []byte) (ed25519.PrivateKey, error) {
	if len(encoded) == 0 || len(encoded) > maximumKeyDocumentBytes {
		return nil, fmt.Errorf("key document size is outside the accepted bounds")
	}
	var document keyDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode key document: %w", err)
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("re-encode key document: %w", err)
	}
	if !bytes.Equal(encoded, canonical) {
		return nil, errors.New("key document is not canonically encoded")
	}
	if document.Version != keyDocumentVersion {
		return nil, fmt.Errorf("key document version %d is unsupported", document.Version)
	}
	if document.Algorithm != keyDocumentAlgorithm {
		return nil, fmt.Errorf("key document algorithm %q is unsupported", document.Algorithm)
	}
	seed, err := base64.StdEncoding.DecodeString(document.Seed)
	if err != nil || len(seed) != ed25519.SeedSize || base64.StdEncoding.EncodeToString(seed) != document.Seed {
		return nil, errors.New("key document seed is invalid")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
