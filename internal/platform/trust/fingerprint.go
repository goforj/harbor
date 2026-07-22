package trust

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
)

// Fingerprint returns a stable digest over validated native trust facts and their recomputed classification.
//
// Entry order does not affect the digest. Duplicate facts remain distinct and
// therefore invalidate compare-and-swap evidence rather than being silently
// collapsed.
func (observation Observation) Fingerprint() (string, error) {
	if err := observation.Validate(); err != nil {
		return "", err
	}
	return fingerprintValidated(observation), nil
}

// fingerprintValidated hashes an observation after its bounded representation has already been proven valid.
func fingerprintValidated(observation Observation) string {
	assessment := classifyValidated(observation)
	payload := append([]byte(nil), observationFingerprintDomain...)
	payload = appendRequest(payload, observation.Request)
	payload = appendBool(payload, observation.Complete)
	payload = appendBool(payload, observation.Truncated)

	entries := make([][]byte, 0, len(observation.Entries))
	for _, entry := range observation.Entries {
		entries = append(entries, encodeEntry(entry))
	}
	slices.SortFunc(entries, bytes.Compare)
	payload = binary.AppendUvarint(payload, uint64(len(entries)))
	for _, entry := range entries {
		payload = appendBytes(payload, entry)
	}

	payload = appendString(payload, string(assessment.State))
	payload = appendString(payload, string(assessment.Owned))
	payload = binary.AppendUvarint(payload, uint64(assessment.ForeignCount))
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// appendRequest binds the complete immutable trust authority to native observation facts.
func appendRequest(destination []byte, request Request) []byte {
	destination = appendString(destination, request.installationID)
	destination = appendString(destination, request.requesterIdentity)
	destination = appendString(destination, string(request.mechanism))
	destination = appendString(destination, request.root.Fingerprint)
	destination = appendString(destination, request.root.NotBefore.UTC().Round(0).String())
	destination = appendString(destination, request.root.NotAfter.UTC().Round(0).String())
	return appendBytes(destination, request.root.CertificatePEM)
}

// encodeEntry records every normalized native trust field in one sortable representation.
func encodeEntry(entry Entry) []byte {
	encoded := appendString(nil, string(entry.Mechanism))
	encoded = appendString(encoded, entry.NativeID)
	encoded = appendString(encoded, entry.CertificateFingerprint)
	encoded = appendBool(encoded, entry.NativeExact)
	encoded = appendString(encoded, entry.NativeAttributesSHA256)
	encoded = appendBool(encoded, entry.Owner != nil)
	if entry.Owner != nil {
		encoded = binary.AppendUvarint(encoded, uint64(entry.Owner.Version))
		encoded = appendString(encoded, entry.Owner.InstallationID)
		encoded = appendString(encoded, entry.Owner.RequesterIdentity)
		encoded = appendString(encoded, string(entry.Owner.Mechanism))
		encoded = appendString(encoded, entry.Owner.AuthorityFingerprint)
	}
	return encoded
}

// appendString length-prefixes text so adjacent fields cannot collide.
func appendString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBytes length-prefixes encoded bytes so sorted fact boundaries remain explicit.
func appendBytes(destination, value []byte) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBool records a bounded boolean without text-format dependencies.
func appendBool(destination []byte, value bool) []byte {
	if value {
		return append(destination, 1)
	}
	return append(destination, 0)
}
