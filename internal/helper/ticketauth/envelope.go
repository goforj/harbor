package ticketauth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

const (
	// EnvelopeVersion is the only signed helper-ticket representation this build accepts.
	EnvelopeVersion       uint16 = 2
	ticketSignatureDomain        = "goforj.harbor/helper-ticket:v2\x00"
)

// Envelope carries one signed ticket and the verifier proposed during first-install ownership claim.
type Envelope struct {
	Version   uint16        `json:"version"`
	PublicKey string        `json:"public_key"`
	Ticket    helper.Ticket `json:"ticket"`
	Signature string        `json:"signature"`
}

// Sign validates and signs one ticket with the installation's private helper-ticket key.
func Sign(ticket helper.Ticket, privateKey ed25519.PrivateKey, now time.Time) (Envelope, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("sign helper ticket: Ed25519 private key is invalid")
	}
	if err := ticket.Validate(now); err != nil {
		return Envelope{}, fmt.Errorf("sign helper ticket: %w", err)
	}
	payload, err := signaturePayload(ticket)
	if err != nil {
		return Envelope{}, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return Envelope{
		Version:   EnvelopeVersion,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		Ticket:    ticket,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}, nil
}

// Verify authenticates an envelope against the public key pinned by machine ownership.
func (envelope Envelope) Verify(expectedKey ed25519.PublicKey, now time.Time) (helper.Ticket, error) {
	if len(expectedKey) != ed25519.PublicKeySize {
		return helper.Ticket{}, errors.New("verify helper ticket: expected Ed25519 public key is invalid")
	}
	publicKey, err := envelope.verifierKey()
	if err != nil {
		return helper.Ticket{}, err
	}
	if subtle.ConstantTimeCompare(publicKey, expectedKey) != 1 {
		return helper.Ticket{}, errors.New("verify helper ticket: verifier key does not match machine ownership")
	}
	return envelope.verifyWithKey(publicKey, now)
}

// VerifyBootstrap authenticates the self-contained envelope used by an interactive first ownership claim.
func (envelope Envelope) VerifyBootstrap(now time.Time) (helper.Ticket, ed25519.PublicKey, error) {
	publicKey, err := envelope.verifierKey()
	if err != nil {
		return helper.Ticket{}, nil, err
	}
	ticket, err := envelope.verifyWithKey(publicKey, now)
	if err != nil {
		return helper.Ticket{}, nil, err
	}
	return ticket, append(ed25519.PublicKey(nil), publicKey...), nil
}

// verifyWithKey proves the signature before exposing semantic ticket failures to the caller.
func (envelope Envelope) verifyWithKey(publicKey ed25519.PublicKey, now time.Time) (helper.Ticket, error) {
	if envelope.Version != EnvelopeVersion {
		return helper.Ticket{}, errors.New("verify helper ticket: envelope version is unsupported")
	}
	signature, err := decodeCanonicalBase64("signature", envelope.Signature, ed25519.SignatureSize)
	if err != nil {
		return helper.Ticket{}, err
	}
	payload, err := signaturePayload(envelope.Ticket)
	if err != nil {
		return helper.Ticket{}, err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return helper.Ticket{}, errors.New("verify helper ticket: signature is invalid")
	}
	if err := envelope.Ticket.Validate(now); err != nil {
		return helper.Ticket{}, fmt.Errorf("verify helper ticket: %w", err)
	}
	return envelope.Ticket, nil
}

// verifierKey decodes only the canonical fixed-width key representation accepted by ownership storage.
func (envelope Envelope) verifierKey() (ed25519.PublicKey, error) {
	key, err := decodeCanonicalBase64("public key", envelope.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(key), nil
}

// signaturePayload binds the signature to this protocol use and the deterministic ticket object shape.
func signaturePayload(ticket helper.Ticket) ([]byte, error) {
	encoded, err := json.Marshal(ticket)
	if err != nil {
		return nil, fmt.Errorf("encode helper ticket signature payload: %w", err)
	}
	payload := make([]byte, 0, len(ticketSignatureDomain)+len(encoded))
	payload = append(payload, ticketSignatureDomain...)
	payload = append(payload, encoded...)
	return payload, nil
}

// decodeCanonicalBase64 rejects alternate encodings before keys or signatures become durable identities.
func decodeCanonicalBase64(label string, value string, size int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, fmt.Errorf("verify helper ticket: %s is invalid", label)
	}
	return decoded, nil
}
