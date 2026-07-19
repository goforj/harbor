package ticketauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/netip"
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

// TestEnvelopeCodecRoundTrip proves persistence preserves every authenticated field byte-for-byte.
func TestEnvelopeCodecRoundTrip(t *testing.T) {
	now := envelopeTestTime()
	_, privateKey := envelopeTestKey(t, 6)
	envelope, err := Sign(envelopeTestTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("sign fixture: %v", err)
	}
	first, err := Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	second, err := Encode(envelope)
	if err != nil {
		t.Fatalf("encode envelope again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("deterministic encodings differ:\n%s\n%s", first, second)
	}
	decoded, err := Decode(first)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !reflect.DeepEqual(decoded, envelope) {
		t.Fatalf("decoded envelope = %#v, want %#v", decoded, envelope)
	}
}

// TestEnvelopePoolCodecRoundTripAndBound proves one exact-eight authority remains canonical, verifiable, and within the v3 envelope limit.
func TestEnvelopePoolCodecRoundTripAndBound(t *testing.T) {
	now := envelopeTestTime()
	publicKey, privateKey := envelopeTestKey(t, 11)
	ticket := envelopeTestPoolTicket(now)
	envelope, err := Sign(ticket, privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if EnvelopeVersion != 3 || helper.ProtocolVersion != 3 {
		t.Fatalf("protocol constants = envelope %d, ticket %d, want v3", EnvelopeVersion, helper.ProtocolVersion)
	}
	if envelope.Version != EnvelopeVersion || envelope.Ticket.Version != helper.ProtocolVersion {
		t.Fatalf("pool envelope versions = envelope %d, ticket %d", envelope.Version, envelope.Ticket.Version)
	}

	encoded, err := Encode(envelope)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(encoded) == 0 || len(encoded) > MaxEnvelopeBytes {
		t.Fatalf("encoded pool envelope size = %d, want 1..%d", len(encoded), MaxEnvelopeBytes)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	verified, err := decoded.Verify(publicKey, now)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !reflect.DeepEqual(verified, ticket) {
		t.Fatalf("verified pool ticket = %#v, want %#v", verified, ticket)
	}
	bootstrapped, proposedKey, err := decoded.VerifyBootstrap(now)
	if err != nil {
		t.Fatalf("VerifyBootstrap() error = %v", err)
	}
	if !reflect.DeepEqual(bootstrapped, ticket) || !publicKey.Equal(proposedKey) {
		t.Fatalf("bootstrap pool ticket = %#v/%x, want %#v/%x", bootstrapped, proposedKey, ticket, publicKey)
	}
}

// TestEnvelopePoolVerifyRejectsIdentitySubstitution proves every exact-address authority field participates in the v3 signature.
func TestEnvelopePoolVerifyRejectsIdentitySubstitution(t *testing.T) {
	now := envelopeTestTime()
	publicKey, privateKey := envelopeTestKey(t, 12)
	valid, err := Sign(envelopeTestPoolTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*helper.Ticket)
	}{
		{name: "pool", mutate: func(ticket *helper.Ticket) { ticket.ApprovedPool = "127.77.0.16/29" }},
		{name: "address", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].Address = "127.77.0.9"
		}},
		{name: "observation state", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation.State = helper.ObservationOwned
		}},
		{name: "observation fingerprint", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation.Fingerprint = strings.Repeat("c", 64)
		}},
		{name: "pre-assignment fingerprint", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Fingerprint = strings.Repeat("d", 64)
		}},
		{name: "pre-assignment requirements", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Requirements = []helper.SocketRequirement{{
				Transport: helper.SocketTransportTCP4,
				Port:      443,
			}}
		}},
		{name: "identity count", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities = ticket.ExpectedLoopbackPool.Identities[:7]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneEnvelope(valid)
			test.mutate(&candidate.Ticket)
			if _, err := candidate.Verify(publicKey, now); err == nil || !strings.Contains(err.Error(), "signature is invalid") {
				t.Fatalf("Verify() error = %v, want signature rejection", err)
			}
		})
	}
}

// TestEnvelopeCodecPreservesRequirementBounds keeps valid route-only and maximum tickets within persistent limits.
func TestEnvelopeCodecPreservesRequirementBounds(t *testing.T) {
	maximum := make([]helper.SocketRequirement, helper.MaximumSocketRequirements)
	for index := range maximum {
		maximum[index] = helper.SocketRequirement{Transport: helper.SocketTransportTCP4, Port: uint16(index + 1)}
	}
	for name, requirements := range map[string][]helper.SocketRequirement{
		"route only": {},
		"maximum":    maximum,
	} {
		t.Run(name, func(t *testing.T) {
			now := envelopeTestTime()
			publicKey, privateKey := envelopeTestKey(t, 10)
			ticket := envelopeTestTicket(now)
			ticket.ExpectedPreAssignment.Requirements = requirements
			envelope, err := Sign(ticket, privateKey, now)
			if err != nil {
				t.Fatalf("Sign() error = %v", err)
			}
			encoded, err := Encode(envelope)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			verified, err := decoded.Verify(publicKey, now)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if !reflect.DeepEqual(verified.ExpectedPreAssignment.Requirements, requirements) {
				t.Fatalf("requirements = %#v, want %#v", verified.ExpectedPreAssignment.Requirements, requirements)
			}
		})
	}
}

// TestEnvelopeSignatureKnownVector fixes the v3 signed authority shape and domain separator.
func TestEnvelopeSignatureKnownVector(t *testing.T) {
	now := envelopeTestTime()
	_, privateKey := envelopeTestKey(t, 9)
	envelope, err := Sign(envelopeTestTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	const want = "sDfJXP3iZO1IoewDjB2tzGMSeUtyd3PnOo9sZUhdxR4OcOViZDPJCVxtMz9w2FbFC9hX8YTL9iy1wRWWSm8RBA=="
	if envelope.Signature != want {
		t.Fatalf("signature = %q, want %q", envelope.Signature, want)
	}
}

// TestEnvelopeDecodeRejectsNoncanonicalShapes covers ambiguity at every nested object boundary.
func TestEnvelopeDecodeRejectsNoncanonicalShapes(t *testing.T) {
	now := envelopeTestTime()
	_, privateKey := envelopeTestKey(t, 7)
	signedTicket := envelopeTestTicket(now)
	signedTicket.OwnershipSchemaVersion = 2
	signedTicket.NetworkPolicyFingerprint = strings.Repeat("d", 64)
	envelope, err := Sign(signedTicket, privateKey, now)
	if err != nil {
		t.Fatalf("sign fixture: %v", err)
	}
	canonical, err := Encode(envelope)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	var objects struct {
		Version   json.RawMessage `json:"version"`
		PublicKey json.RawMessage `json:"public_key"`
		Ticket    json.RawMessage `json:"ticket"`
		Signature json.RawMessage `json:"signature"`
	}
	if err := json.Unmarshal(canonical, &objects); err != nil {
		t.Fatalf("decode fixture objects: %v", err)
	}
	var ticketObjects struct {
		Version                  json.RawMessage `json:"version"`
		Operation                json.RawMessage `json:"operation"`
		InstallationID           json.RawMessage `json:"installation_id"`
		RequesterIdentity        json.RawMessage `json:"requester_identity"`
		OwnershipGeneration      json.RawMessage `json:"ownership_generation"`
		OwnershipSchemaVersion   json.RawMessage `json:"ownership_schema_version"`
		NetworkPolicyFingerprint json.RawMessage `json:"network_policy_fingerprint"`
		ApprovedPool             json.RawMessage `json:"approved_pool"`
		ApprovedAddress          json.RawMessage `json:"approved_address"`
		ExpectedObservation      json.RawMessage `json:"expected_observation"`
		ExpectedPreAssignment    json.RawMessage `json:"expected_pre_assignment"`
		Nonce                    json.RawMessage `json:"nonce"`
		ExpiresAt                json.RawMessage `json:"expires_at"`
	}
	if err := json.Unmarshal(objects.Ticket, &ticketObjects); err != nil {
		t.Fatalf("decode fixture ticket: %v", err)
	}
	valid := string(canonical)
	ticket := string(objects.Ticket)
	observation := string(ticketObjects.ExpectedObservation)
	preAssignment := string(ticketObjects.ExpectedPreAssignment)
	var preAssignmentObjects struct {
		Fingerprint  json.RawMessage `json:"fingerprint"`
		Requirements json.RawMessage `json:"requirements"`
	}
	if err := json.Unmarshal(ticketObjects.ExpectedPreAssignment, &preAssignmentObjects); err != nil {
		t.Fatalf("decode fixture pre-assignment: %v", err)
	}
	var requirements []json.RawMessage
	if err := json.Unmarshal(preAssignmentObjects.Requirements, &requirements); err != nil || len(requirements) == 0 {
		t.Fatalf("decode fixture requirements = %d, %v", len(requirements), err)
	}
	requirement := string(requirements[0])
	var requirementObjects struct {
		Transport json.RawMessage `json:"transport"`
		Port      json.RawMessage `json:"port"`
	}
	if err := json.Unmarshal(requirements[0], &requirementObjects); err != nil {
		t.Fatalf("decode fixture requirement: %v", err)
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "null", body: "null"},
		{name: "array", body: "[]"},
		{name: "whitespace", body: " " + valid},
		{name: "trailing whitespace", body: valid + "\n"},
		{name: "trailing value", body: valid + "{}"},
		{name: "missing envelope field", body: strings.Replace(valid, `,"signature":`+string(objects.Signature), "", 1)},
		{name: "unknown envelope field", body: strings.TrimSuffix(valid, "}") + `,"unknown":true}`},
		{name: "case alias envelope field", body: strings.Replace(valid, `"version":`, `"Version":`, 1)},
		{name: "duplicate envelope field", body: strings.Replace(valid, `"version":`, `"version":3,"version":`, 1)},
		{name: "escaped envelope field", body: strings.Replace(valid, `"version":`, `"\u0076ersion":`, 1)},
		{name: "missing ticket field", body: strings.Replace(valid, ticket, strings.Replace(ticket, `,"nonce":`+string(ticketObjects.Nonce), "", 1), 1)},
		{name: "unknown ticket field", body: strings.Replace(valid, ticket, strings.TrimSuffix(ticket, "}")+`,"unknown":true}`, 1)},
		{name: "case alias ticket field", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"approved_pool":`, `"Approved_Pool":`, 1), 1)},
		{name: "duplicate ticket field", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"nonce":`, `"nonce":"duplicate","nonce":`, 1), 1)},
		{name: "missing ownership schema", body: strings.Replace(valid, ticket, strings.Replace(ticket, `,"ownership_schema_version":`+string(ticketObjects.OwnershipSchemaVersion), "", 1), 1)},
		{name: "case alias ownership schema", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"ownership_schema_version":`, `"Ownership_Schema_Version":`, 1), 1)},
		{name: "duplicate ownership schema", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"ownership_schema_version":`, `"ownership_schema_version":2,"ownership_schema_version":`, 1), 1)},
		{name: "empty network policy fingerprint", body: strings.Replace(valid, ticket, strings.Replace(ticket, string(ticketObjects.NetworkPolicyFingerprint), `""`, 1), 1)},
		{name: "case alias network policy fingerprint", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"network_policy_fingerprint":`, `"Network_Policy_Fingerprint":`, 1), 1)},
		{name: "duplicate network policy fingerprint", body: strings.Replace(valid, ticket, strings.Replace(ticket, `"network_policy_fingerprint":`, `"network_policy_fingerprint":"duplicate","network_policy_fingerprint":`, 1), 1)},
		{name: "missing observation field", body: strings.Replace(valid, observation, `{"state":"absent"}`, 1)},
		{name: "unknown observation field", body: strings.Replace(valid, observation, strings.TrimSuffix(observation, "}")+`,"unknown":true}`, 1)},
		{name: "case alias observation field", body: strings.Replace(valid, observation, strings.Replace(observation, `"state":`, `"State":`, 1), 1)},
		{name: "duplicate observation field", body: strings.Replace(valid, observation, strings.Replace(observation, `"state":`, `"state":"absent","state":`, 1), 1)},
		{name: "null pre-assignment", body: strings.Replace(valid, preAssignment, "null", 1)},
		{name: "missing pre-assignment fingerprint", body: strings.Replace(valid, preAssignment, strings.Replace(preAssignment, `"fingerprint":`+string(preAssignmentObjects.Fingerprint)+`,`, "", 1), 1)},
		{name: "missing pre-assignment requirements", body: strings.Replace(valid, preAssignment, strings.Replace(preAssignment, `,"requirements":`+string(preAssignmentObjects.Requirements), "", 1), 1)},
		{name: "unknown pre-assignment field", body: strings.Replace(valid, preAssignment, strings.TrimSuffix(preAssignment, "}")+`,"unknown":true}`, 1)},
		{name: "case alias pre-assignment field", body: strings.Replace(valid, preAssignment, strings.Replace(preAssignment, `"fingerprint":`, `"Fingerprint":`, 1), 1)},
		{name: "duplicate pre-assignment field", body: strings.Replace(valid, preAssignment, strings.Replace(preAssignment, `"fingerprint":`, `"fingerprint":"duplicate","fingerprint":`, 1), 1)},
		{name: "missing requirement transport", body: strings.Replace(valid, requirement, strings.Replace(requirement, `"transport":`+string(requirementObjects.Transport)+`,`, "", 1), 1)},
		{name: "missing requirement port", body: strings.Replace(valid, requirement, strings.Replace(requirement, `,"port":`+string(requirementObjects.Port), "", 1), 1)},
		{name: "unknown requirement field", body: strings.Replace(valid, requirement, strings.TrimSuffix(requirement, "}")+`,"unknown":true}`, 1)},
		{name: "case alias requirement field", body: strings.Replace(valid, requirement, strings.Replace(requirement, `"transport":`, `"Transport":`, 1), 1)},
		{name: "duplicate requirement field", body: strings.Replace(valid, requirement, strings.Replace(requirement, `"port":`, `"port":1,"port":`, 1), 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode([]byte(test.body)); err == nil {
				t.Fatal("Decode() accepted a noncanonical envelope")
			}
		})
	}
}

// TestEnvelopeCodecBounds rejects allocations beyond the persistent format limit.
func TestEnvelopeCodecBounds(t *testing.T) {
	if _, err := Decode(bytes.Repeat([]byte{'x'}, MaxEnvelopeBytes+1)); err == nil {
		t.Fatal("Decode() accepted an oversized envelope")
	}
	oversized := Envelope{PublicKey: strings.Repeat("x", MaxEnvelopeBytes)}
	if _, err := Encode(oversized); err == nil {
		t.Fatal("Encode() accepted an oversized envelope")
	}
	invalidTime := Envelope{Ticket: helper.Ticket{ExpiresAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)}}
	if _, err := Encode(invalidTime); err == nil {
		t.Fatal("Encode() accepted a time outside JSON's canonical range")
	}
	expanding := []byte(`{"version":3,"public_key":"` + strings.Repeat("<", 3000) + `","ticket":{},"signature":""}`)
	if len(expanding) > MaxEnvelopeBytes {
		t.Fatal("expanding decode fixture unexpectedly exceeds the input bound")
	}
	if _, err := Decode(expanding); err == nil {
		t.Fatal("Decode() accepted content whose canonical form exceeds the bound")
	}
}

// TestEnvelopeBootstrapRejectsInvalidInputs covers self-contained key and envelope validation paths.
func TestEnvelopeBootstrapRejectsInvalidInputs(t *testing.T) {
	now := envelopeTestTime()
	_, privateKey := envelopeTestKey(t, 8)
	valid, err := Sign(envelopeTestTicket(now), privateKey, now)
	if err != nil {
		t.Fatalf("sign fixture: %v", err)
	}
	invalidKey := valid
	invalidKey.PublicKey = "invalid"
	if _, _, err := invalidKey.VerifyBootstrap(now); err == nil {
		t.Fatal("VerifyBootstrap() accepted an invalid public key")
	}
	invalidEnvelope := valid
	invalidEnvelope.Version = EnvelopeVersion - 1
	if _, _, err := invalidEnvelope.VerifyBootstrap(now); err == nil {
		t.Fatal("VerifyBootstrap() accepted an invalid envelope")
	}
	invalidPayload := valid
	invalidPayload.Ticket.ExpiresAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := invalidPayload.VerifyBootstrap(now); err == nil {
		t.Fatal("VerifyBootstrap() accepted an unencodable signed payload")
	}
}

// TestSignaturePayloadRejectsUnencodableTime proves persistence and signatures share JSON's time boundary.
func TestSignaturePayloadRejectsUnencodableTime(t *testing.T) {
	ticket := envelopeTestTicket(envelopeTestTime())
	ticket.ExpiresAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := signaturePayload(ticket); err == nil {
		t.Fatal("signaturePayload() accepted an unencodable time")
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
	unboundTicket := envelopeTestTicket(now)
	unboundTicket.ExpectedPreAssignment = nil
	if _, err := Sign(unboundTicket, privateKey, now); err == nil {
		t.Fatal("Sign() accepted an absent ensure without pre-assignment evidence")
	}
	misplacedTicket := envelopeTestTicket(now)
	misplacedTicket.ExpectedObservation.State = helper.ObservationOwned
	if _, err := Sign(misplacedTicket, privateKey, now); err == nil {
		t.Fatal("Sign() accepted pre-assignment evidence on an owned ensure")
	}
	if _, err := Sign(envelopeTestTicket(now), ed25519.PrivateKey("short"), now); err == nil {
		t.Fatal("Sign() accepted an invalid private key")
	}
	boundaryNow := time.Date(9999, time.December, 31, 23, 59, 0, 0, time.UTC)
	unencodable := envelopeTestTicket(boundaryNow)
	if _, err := Sign(unencodable, privateKey, boundaryNow); err == nil {
		t.Fatal("Sign() accepted a ticket outside JSON's time range")
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
		{name: "ticket version", key: publicKey, mutate: func(value *Envelope) { value.Ticket.Version++ }},
		{name: "ticket operation", key: publicKey, mutate: func(value *Envelope) {
			value.Ticket.Operation = helper.OperationReleaseLoopbackIdentity
			value.Ticket.ExpectedObservation.State = helper.ObservationOwned
		}},
		{name: "ticket installation", key: publicKey, mutate: func(value *Envelope) { value.Ticket.InstallationID = "other-installation" }},
		{name: "ticket requester", key: publicKey, mutate: func(value *Envelope) { value.Ticket.RequesterIdentity = "2000" }},
		{name: "ticket generation", key: publicKey, mutate: func(value *Envelope) { value.Ticket.OwnershipGeneration++ }},
		{name: "ticket ownership schema", key: publicKey, mutate: func(value *Envelope) { value.Ticket.OwnershipSchemaVersion = 2 }},
		{name: "ticket network policy fingerprint", key: publicKey, mutate: func(value *Envelope) {
			value.Ticket.NetworkPolicyFingerprint = strings.Repeat("e", 64)
		}},
		{name: "ticket pool", key: publicKey, mutate: func(value *Envelope) {
			value.Ticket.ApprovedPool = "127.78.0.0/24"
			value.Ticket.ApprovedAddress = "127.78.0.10"
		}},
		{name: "ticket address", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ApprovedAddress = "127.77.0.11" }},
		{name: "ticket observation state", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpectedObservation.State = helper.ObservationOwned }},
		{name: "ticket observation", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpectedObservation.Fingerprint = strings.Repeat("b", 64) }},
		{name: "ticket pre-assignment presence", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpectedPreAssignment = nil }},
		{name: "ticket pre-assignment fingerprint", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpectedPreAssignment.Fingerprint = strings.Repeat("c", 64) }},
		{name: "ticket pre-assignment transport", key: publicKey, mutate: func(value *Envelope) {
			value.Ticket.ExpectedPreAssignment.Requirements[0].Transport = helper.SocketTransportUDP4
		}},
		{name: "ticket pre-assignment port", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpectedPreAssignment.Requirements[0].Port++ }},
		{name: "ticket pre-assignment requirement count", key: publicKey, mutate: func(value *Envelope) {
			value.Ticket.ExpectedPreAssignment.Requirements = append(value.Ticket.ExpectedPreAssignment.Requirements, helper.SocketRequirement{Transport: helper.SocketTransportUDP4, Port: 123})
		}},
		{name: "ticket nonce", key: publicKey, mutate: func(value *Envelope) { value.Ticket.Nonce = strings.Repeat("z", 32) }},
		{name: "ticket expiry", key: publicKey, mutate: func(value *Envelope) { value.Ticket.ExpiresAt = value.Ticket.ExpiresAt.Add(time.Second) }},
		{name: "signature", key: publicKey, mutate: func(value *Envelope) {
			value.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}},
		{name: "short signature", key: publicKey, mutate: func(value *Envelope) { value.Signature = base64.StdEncoding.EncodeToString([]byte("short")) }},
		{name: "noncanonical signature", key: publicKey, mutate: func(value *Envelope) { value.Signature = strings.TrimRight(value.Signature, "=") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneEnvelope(valid)
			test.mutate(&candidate)
			_, err := candidate.Verify(test.key, now)
			if err == nil {
				t.Fatal("Verify() accepted substituted authority")
			}
			if strings.HasPrefix(test.name, "ticket ") && !strings.Contains(err.Error(), "signature is invalid") {
				t.Fatalf("Verify() ticket substitution error = %v, want signature rejection", err)
			}
		})
	}
}

// cloneEnvelope isolates optional signed authority slices before substitution tests mutate them.
func cloneEnvelope(envelope Envelope) Envelope {
	clone := envelope
	if envelope.Ticket.ExpectedPreAssignment != nil {
		expected := *envelope.Ticket.ExpectedPreAssignment
		expected.Requirements = append([]helper.SocketRequirement{}, expected.Requirements...)
		clone.Ticket.ExpectedPreAssignment = &expected
	}
	if envelope.Ticket.ExpectedLoopbackPool != nil {
		expectedPool := *envelope.Ticket.ExpectedLoopbackPool
		expectedPool.Identities = append([]helper.ExpectedLoopbackIdentity(nil), expectedPool.Identities...)
		for index := range expectedPool.Identities {
			preAssignment := expectedPool.Identities[index].ExpectedPreAssignment
			if preAssignment == nil {
				continue
			}
			clonedPreAssignment := *preAssignment
			clonedPreAssignment.Requirements = append([]helper.SocketRequirement{}, preAssignment.Requirements...)
			expectedPool.Identities[index].ExpectedPreAssignment = &clonedPreAssignment
		}
		clone.Ticket.ExpectedLoopbackPool = &expectedPool
	}
	return clone
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
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackIdentity,
		InstallationID:         "harbor-test-installation",
		RequesterIdentity:      "1000",
		OwnershipGeneration:    1,
		OwnershipSchemaVersion: 1,
		ApprovedPool:           "127.77.0.0/24",
		ApprovedAddress:        "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("a", 64),
		},
		ExpectedPreAssignment: &helper.ExpectedPreAssignment{
			Fingerprint: strings.Repeat("b", 64),
			Requirements: []helper.SocketRequirement{
				{Transport: helper.SocketTransportTCP4, Port: 443},
				{Transport: helper.SocketTransportUDP4, Port: 53},
			},
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// envelopeTestPoolTicket returns one complete route-only /29 authority under the v3 signing domain.
func envelopeTestPoolTicket(now time.Time) helper.Ticket {
	pool := netip.MustParsePrefix("127.77.0.8/29")
	identities := make([]helper.ExpectedLoopbackIdentity, 0, 8)
	address := pool.Addr()
	for range 8 {
		identities = append(identities, helper.ExpectedLoopbackIdentity{
			Address: address.String(),
			ExpectedObservation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: strings.Repeat("a", 64),
			},
			ExpectedPreAssignment: &helper.ExpectedPreAssignment{
				Fingerprint:  strings.Repeat("b", 64),
				Requirements: []helper.SocketRequirement{},
			},
		})
		address = address.Next()
	}
	return helper.Ticket{
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackPool,
		InstallationID:         "harbor-test-installation",
		RequesterIdentity:      "1000",
		OwnershipGeneration:    1,
		OwnershipSchemaVersion: 1,
		ApprovedPool:           pool.String(),
		ExpectedLoopbackPool: &helper.ExpectedLoopbackPool{
			Identities: identities,
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// envelopeTestTime supplies the canonical UTC instant shared by signature tests.
func envelopeTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}
