package hostconflict

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"slices"
)

const observationFingerprintDomain = "goforj.harbor.host-conflict-observation.v3\x00"

// Fingerprint returns a deterministic digest over validated raw facts and their recomputed classifications.
//
// A matching digest is not an authorization decision. Call Classify and require
// StateSafe because conflict and indeterminate observations also have stable
// fingerprints for compare-and-swap evidence.
func (o Observation) Fingerprint() (string, error) {
	assessment, err := o.Classify()
	if err != nil {
		return "", err
	}
	payload := append([]byte(nil), observationFingerprintDomain...)
	payload = appendString(payload, string(o.Request.Purpose()))
	payload = appendAddress(payload, o.Request.Candidate())
	payload = appendRequirements(payload, o.Request.requirements)
	payload = appendScope(payload, o.Scope)
	payload = appendInterface(payload, o.Loopback.Interface)
	payload = appendString(payload, string(o.Loopback.Kind))
	payload = appendRoutes(payload, o.Routes)
	payload = appendSockets(payload, o.Sockets)
	payload = appendPolicy(payload, o.Policy)
	payload = appendString(payload, string(assessment.Routes))
	payload = appendString(payload, string(assessment.Sockets))
	payload = appendString(payload, string(assessment.Policy))
	payload = appendString(payload, string(assessment.State))
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// appendRequirements binds the canonical request without depending on caller slice ownership.
func appendRequirements(destination []byte, requirements []SocketRequirement) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(requirements)))
	for _, requirement := range requirements {
		destination = appendString(destination, string(requirement.Transport))
		destination = binary.AppendUvarint(destination, uint64(requirement.Port))
	}
	return destination
}

// appendScope binds platform scope identity so observations cannot cross namespaces or compartments.
func appendScope(destination []byte, scope NetworkScope) []byte {
	destination = appendString(destination, string(scope.Platform))
	destination = appendBool(destination, scope.LinuxNamespace != nil)
	if scope.LinuxNamespace != nil {
		destination = binary.AppendUvarint(destination, scope.LinuxNamespace.Device)
		destination = binary.AppendUvarint(destination, scope.LinuxNamespace.Inode)
	}
	destination = appendBool(destination, scope.WindowsCompartment != nil)
	if scope.WindowsCompartment != nil {
		destination = binary.AppendUvarint(destination, uint64(scope.WindowsCompartment.ID))
	}
	return destination
}

// appendRoutes canonicalizes matching-route enumeration while preserving multiplicity and selection.
func appendRoutes(destination []byte, snapshot RouteSnapshot) []byte {
	destination = appendBool(destination, snapshot.Complete)
	destination = appendBool(destination, snapshot.Truncated)
	destination = appendBool(destination, snapshot.Selected != nil)
	if snapshot.Selected != nil {
		encoded := encodeRoute(*snapshot.Selected)
		destination = appendBytes(destination, encoded)
	}
	routes := make([][]byte, 0, len(snapshot.Matching))
	for _, fact := range snapshot.Matching {
		routes = append(routes, encodeRoute(fact))
	}
	slices.SortFunc(routes, bytes.Compare)
	destination = binary.AppendUvarint(destination, uint64(len(routes)))
	for _, route := range routes {
		destination = appendBytes(destination, route)
	}
	return destination
}

// encodeRoute records every authority field in one sortable representation.
func encodeRoute(fact RouteFact) []byte {
	encoded := appendPrefix(nil, fact.Destination)
	encoded = appendInterface(encoded, fact.Interface)
	encoded = appendBool(encoded, fact.NativeLoopback)
	encoded = appendOptionalAddress(encoded, fact.Gateway)
	encoded = appendString(encoded, string(fact.Normalization))
	encoded = binary.AppendUvarint(encoded, fact.NativeFlags)
	return encoded
}

// appendSockets canonicalizes endpoint enumeration while preserving duplicate kernel facts.
func appendSockets(destination []byte, snapshot SocketSnapshot) []byte {
	destination = appendBool(destination, snapshot.Complete)
	destination = appendBool(destination, snapshot.Truncated)
	endpoints := make([][]byte, 0, len(snapshot.Endpoints))
	for _, fact := range snapshot.Endpoints {
		endpoints = append(endpoints, encodeSocket(fact))
	}
	slices.SortFunc(endpoints, bytes.Compare)
	destination = binary.AppendUvarint(destination, uint64(len(endpoints)))
	for _, endpoint := range endpoints {
		destination = appendBytes(destination, endpoint)
	}
	return destination
}

// encodeSocket records only authority facts and deliberately excludes process diagnostics.
func encodeSocket(fact SocketFact) []byte {
	encoded := appendString(nil, string(fact.Protocol))
	encoded = appendAddress(encoded, fact.Address)
	encoded = binary.AppendUvarint(encoded, uint64(fact.Port))
	encoded = appendBool(encoded, fact.TCPAccepting)
	encoded = appendString(encoded, string(fact.IPv6Only))
	return encoded
}

// appendPolicy binds Linux policy completeness and interface enumeration without platform diagnostics.
func appendPolicy(destination []byte, policy PolicyFacts) []byte {
	destination = appendBool(destination, policy.Linux != nil)
	if policy.Linux == nil {
		return destination
	}
	destination = appendBool(destination, policy.Linux.Complete)
	destination = appendBool(destination, policy.Linux.Truncated)
	destination = appendBool(destination, policy.Linux.IPNonlocalBind)
	facts := make([][]byte, 0, len(policy.Linux.RouteLocalnet))
	for _, fact := range policy.Linux.RouteLocalnet {
		encoded := appendInterface(nil, fact.Interface)
		encoded = appendBool(encoded, fact.Enabled)
		facts = append(facts, encoded)
	}
	slices.SortFunc(facts, bytes.Compare)
	destination = binary.AppendUvarint(destination, uint64(len(facts)))
	for _, fact := range facts {
		destination = appendBytes(destination, fact)
	}
	return destination
}

// appendInterface encodes the complete interface identity.
func appendInterface(destination []byte, identity InterfaceIdentity) []byte {
	destination = appendString(destination, identity.Name)
	destination = binary.AppendUvarint(destination, uint64(identity.Index))
	return binary.AppendUvarint(destination, identity.WindowsLUID)
}

// appendPrefix encodes an IPv4 prefix as its address plus bounded bit length.
func appendPrefix(destination []byte, prefix netip.Prefix) []byte {
	destination = appendAddress(destination, prefix.Addr())
	return binary.AppendUvarint(destination, uint64(prefix.Bits()))
}

// appendOptionalAddress distinguishes a normalized absent gateway from an address value.
func appendOptionalAddress(destination []byte, address netip.Addr) []byte {
	destination = appendBool(destination, address.IsValid())
	if !address.IsValid() {
		return destination
	}
	return appendAddress(destination, address)
}

// appendAddress records the family before canonical address bytes.
func appendAddress(destination []byte, address netip.Addr) []byte {
	if address.Is4() {
		destination = append(destination, 4)
		value := address.As4()
		return append(destination, value[:]...)
	}
	destination = append(destination, 6)
	value := address.As16()
	return append(destination, value[:]...)
}

// appendString length-prefixes text so adjacent fields cannot collide.
func appendString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBytes length-prefixes nested fact encodings before concatenation.
func appendBytes(destination []byte, value []byte) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBool records a bounded boolean byte.
func appendBool(destination []byte, value bool) []byte {
	if value {
		return append(destination, 1)
	}
	return append(destination, 0)
}
