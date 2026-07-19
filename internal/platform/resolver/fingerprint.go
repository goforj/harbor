package resolver

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"slices"
)

const observationFingerprintDomain = "goforj.harbor.resolver-observation.v1\x00"

// Fingerprint returns a stable digest over validated raw facts and their recomputed classification.
//
// Rule order and nil-versus-empty slices do not affect the digest. Duplicate
// facts remain distinct so multiplicity changes invalidate compare-and-swap
// evidence.
func (o Observation) Fingerprint() (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	return fingerprintValidated(o), nil
}

// fingerprintValidated hashes an observation after its bounded representation has already been proven valid.
func fingerprintValidated(o Observation) string {
	assessment := classifyValidated(o)
	payload := append([]byte(nil), observationFingerprintDomain...)
	payload = appendRequest(payload, o.Request)
	payload = appendBool(payload, o.Complete)
	payload = appendBool(payload, o.Truncated)

	rules := make([][]byte, 0, len(o.Rules))
	for _, rule := range o.Rules {
		rules = append(rules, encodeRule(rule))
	}
	slices.SortFunc(rules, bytes.Compare)
	payload = binary.AppendUvarint(payload, uint64(len(rules)))
	for _, rule := range rules {
		payload = appendBytes(payload, rule)
	}

	payload = appendString(payload, string(assessment.State))
	payload = appendString(payload, string(assessment.Owned))
	payload = binary.AppendUvarint(payload, uint64(assessment.ForeignCount))
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// appendRequest binds the complete immutable resolver authority to its native observation.
func appendRequest(destination []byte, request Request) []byte {
	destination = appendString(destination, string(request.InstallationID()))
	destination = appendString(destination, request.PolicyFingerprint())
	destination = appendString(destination, string(request.Mechanism()))
	destination = appendString(destination, request.Suffix())
	return appendServer(destination, request.Endpoint())
}

// encodeRule records every normalized rule field in one sortable representation.
func encodeRule(rule RuleFact) []byte {
	encoded := appendString(nil, string(rule.Mechanism))
	encoded = appendString(encoded, rule.NativeID)
	encoded = appendString(encoded, rule.Namespace)
	servers := make([][]byte, 0, len(rule.Servers))
	for _, server := range rule.Servers {
		servers = append(servers, appendServer(nil, server))
	}
	slices.SortFunc(servers, bytes.Compare)
	encoded = binary.AppendUvarint(encoded, uint64(len(servers)))
	for _, server := range servers {
		encoded = appendBytes(encoded, server)
	}
	encoded = appendBool(encoded, rule.RouteOnly)
	encoded = appendBool(encoded, rule.NativeExact)
	encoded = appendString(encoded, rule.NativeAttributesSHA256)
	encoded = appendBool(encoded, rule.Owner != nil)
	if rule.Owner != nil {
		encoded = binary.AppendUvarint(encoded, uint64(rule.Owner.Version))
		encoded = appendString(encoded, rule.Owner.InstallationID)
		encoded = appendString(encoded, rule.Owner.PolicyFingerprint)
	}
	return encoded
}

// appendServer records address family, bytes, scope, and port without relying on text formatting.
func appendServer(destination []byte, server netip.AddrPort) []byte {
	address := server.Addr()
	if address.Is4() {
		destination = append(destination, 4)
		value := address.As4()
		destination = append(destination, value[:]...)
	} else {
		destination = append(destination, 6)
		value := address.As16()
		destination = append(destination, value[:]...)
	}
	destination = appendString(destination, address.Zone())
	return binary.AppendUvarint(destination, uint64(server.Port()))
}

// appendString length-prefixes text so adjacent fields cannot collide.
func appendString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBytes length-prefixes a nested encoding so sorted fact boundaries remain explicit.
func appendBytes(destination []byte, value []byte) []byte {
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
