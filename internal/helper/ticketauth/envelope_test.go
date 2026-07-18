package ticketauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// TestEnvelopeSignAndVerify proves pinned and bootstrap verification return the exact authorized ticket.
func TestEnvelopeSignAndVerify(t *testing.T) {
	now := envelopeTestTime()
	publicKey, privateKey := envelopeTestKey(t, 1)
	ticket := envelopeTestTicket(now)
	envelope, err := Sign(ticket, privateKey, now)
	if err != nil {
		t.Fatalf("sign ticket: %v", err)
	}

	verified, err := envelope.Verify(publicKey, now)
	if err != nil {
		t.Fatalf("verify pinned ticket: %v", err)
	}
	if !reflect.DeepEqual(verified, ticket) {
		t.Fatalf("verified ticket = %#v, want %#v", verified, ticket)
	}
	bootstrapped, proposedKey, err := envelope.VerifyBootstrap(now)
	if err != nil {
		t.Fatalf("verify bootstrap ticket: %v", err)
	}
	if !reflect.DeepEqual(bootstrapped, ticket) || !publicKey.Equal(proposedKey) {
		t.Fatalf("bootstrap result = %#v/%x, want %#v/%x", bootstrapped, proposedKey, ticket, publicKey)
	}
	proposedKey[0] ^= 0xff
	if publicKey.Equal(proposedKey) {
		t.Fatal("bootstrap verification exposed its retained public key")
	}
}

// TestEnvelopeSignRejectsInvalidInputs keeps malformed authority out of the signed transport.
func TestEnvelopeSignRejectsInvalidInputs(t *testing.T) {
	now := envelopeTestTime()
	_, privateKey := envelopeTestKey(t, 2)
	invalidTicket := envelopeTestTicket(now)
	invalidTicket.ApprovedAddress = "192.0.2.1"
	if _, err := Sign(invalidTicket, privateKey, now); err == nil {
		t.Fatal("Sign() accepted an invalid ticket")
	}
	if _, err := Sign(envelopeTestTicket(now), ed25519.PrivateKey("short"), now); err == nil {
		t.Fatal("Sign() accepted an invalid private key")
	}
}

// TestEnvelopeVerifyRejectsSubstitution covers every authenticated envelope boundary.
func TestEnvelopeVerifyRejectsSubstitution(t *testing.T) {
	now := envelopeTestTime()
	publicKey, privateKey := envelopeTestKey(t, 3)
	otherPublicKey, _ := envelopeTestKey(t, 4)
	valid, err := Sign(envelopeTestTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("sign fixture: %v", err)
	}
	tests := []struct {
		name   string
		key    ed25519.PublicKey
		mutate func(*Envelope)
	}{
		{name: "wrong pinned key", key: otherPublicKey, mutate: func(*Envelope) {}},
		{name: "short pinned key", key: ed25519.PublicKey("short"), mutate: func(*Envelope) {}},
		{name: "version", key: publicKey, mutate: func(value *Envelope) { value.Version++ }},
		{name: "public key", key: publicKey, mutate: func(value *Envelope) { value.PublicKey = base64.StdEncoding.EncodeToString(otherPublicKey) }},
		{name: "noncanonical public key", key: publicKey, mutate: func(value *Envelope) { value.PublicKey = strings.TrimRight(value.PublicKey, "=") }},
		{name: "ticket", key: publicKey, mutate: func(value *Envelope) { value.Ticket.Nonce = strings.Repeat("z", 32) }},
		{name: "signature", key: publicKey, mutate: func(value *Envelope) {
			value.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}},
		{name: "short signature", key: publicKey, mutate: func(value *Envelope) { value.Signature = base64.StdEncoding.EncodeToString([]byte("short")) }},
		{name: "noncanonical signature", key: publicKey, mutate: func(value *Envelope) { value.Signature = strings.TrimRight(value.Signature, "=") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := candidate.Verify(test.key, now); err == nil {
				t.Fatal("Verify() accepted substituted authority")
			}
		})
	}
}

// TestEnvelopeVerifyRejectsSignedTicketOutsideItsTimeWindow proves signatures do not override admission time.
func TestEnvelopeVerifyRejectsSignedTicketOutsideItsTimeWindow(t *testing.T) {
	now := envelopeTestTime()
	publicKey, privateKey := envelopeTestKey(t, 5)
	envelope, err := Sign(envelopeTestTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("sign fixture: %v", err)
	}
	if _, err := envelope.Verify(publicKey, now.Add(2*time.Minute)); err == nil {
		t.Fatal("Verify() accepted an expired signed ticket")
	}
}

// envelopeTestKey derives a deterministic test-only key without retaining random fixtures.
func envelopeTestKey(t *testing.T, marker byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = marker + byte(index)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("Ed25519 private key returned an unexpected public key type")
	}
	return publicKey, privateKey
}

// envelopeTestTicket returns one valid ticket whose every field participates in the signature.
func envelopeTestTicket(now time.Time) helper.Ticket {
	return helper.Ticket{
		Version:             helper.ProtocolVersion,
		Operation:           helper.OperationEnsureLoopbackIdentity,
		DaemonIdentity:      "harbord-test-daemon",
		InstallationID:      "harbor-test-installation",
		RequesterIdentity:   "1000",
		OwnershipGeneration: 1,
		ApprovedPool:        "127.77.0.0/24",
		ApprovedAddress:     "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("a", 64),
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// envelopeTestTime supplies the canonical UTC instant shared by signature tests.
func envelopeTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}
