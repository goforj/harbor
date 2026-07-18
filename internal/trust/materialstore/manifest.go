package materialstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/goforj/harbor/internal/trust/localca"
)

const (
	manifestVersion       = 1
	manifestKindAuthority = "authority"
	manifestKindLeaf      = "leaf"
	maximumManifestBytes  = 16 << 10
	fingerprintBytes      = sha256.Size
)

// activeManifest selects one immutable, fully validated certificate generation.
type activeManifest struct {
	Version               int      `json:"version"`
	Kind                  string   `json:"kind"`
	Fingerprint           string   `json:"fingerprint"`
	AuthorityFingerprint  string   `json:"authority_fingerprint,omitempty"`
	Hosts                 []string `json:"hosts,omitempty"`
	authorityFieldPresent bool
	hostsFieldPresent     bool
}

// authorityManifest constructs the only valid manifest shape for a current root identity.
func authorityManifest(fingerprint string) activeManifest {
	return activeManifest{
		Version:     manifestVersion,
		Kind:        manifestKindAuthority,
		Fingerprint: fingerprint,
	}
}

// leafManifest constructs the only valid manifest shape for one exact canonical SAN set.
func leafManifest(authorityFingerprint, fingerprint string, hosts []string) activeManifest {
	return activeManifest{
		Version:               manifestVersion,
		Kind:                  manifestKindLeaf,
		Fingerprint:           fingerprint,
		AuthorityFingerprint:  authorityFingerprint,
		Hosts:                 append([]string(nil), hosts...),
		authorityFieldPresent: true,
		hostsFieldPresent:     true,
	}
}

// encodeManifest emits deterministic JSON so repeated publication has one stable representation.
func encodeManifest(manifest activeManifest) ([]byte, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode active certificate manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumManifestBytes {
		return nil, fmt.Errorf("active certificate manifest exceeds %d bytes", maximumManifestBytes)
	}
	return encoded, nil
}

// decodeManifest rejects extensions and concatenated values until a schema version explicitly defines them.
func decodeManifest(encoded []byte) (activeManifest, error) {
	if len(encoded) == 0 {
		return activeManifest{}, fmt.Errorf("active certificate manifest is empty")
	}
	if len(encoded) > maximumManifestBytes {
		return activeManifest{}, fmt.Errorf("active certificate manifest exceeds %d bytes", maximumManifestBytes)
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return activeManifest{}, fmt.Errorf("active certificate manifest must be a JSON object")
	}
	fields, err := inspectManifestFields(encoded)
	if err != nil {
		return activeManifest{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest activeManifest
	if err := decoder.Decode(&manifest); err != nil {
		return activeManifest{}, fmt.Errorf("decode active certificate manifest: %w", err)
	}
	if err := requireManifestEOF(decoder); err != nil {
		return activeManifest{}, err
	}
	_, manifest.authorityFieldPresent = fields["authority_fingerprint"]
	_, manifest.hostsFieldPresent = fields["hosts"]
	return manifest, nil
}

// inspectManifestFields prevents duplicate identities and retains optional-field presence for exact shape validation.
func inspectManifestFields(encoded []byte) (map[string]struct{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode active certificate manifest: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("active certificate manifest must be a JSON object")
	}
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode active certificate manifest field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return nil, fmt.Errorf("active certificate manifest contains a non-string field name")
		}
		if !knownManifestField(field) {
			return nil, fmt.Errorf("active certificate manifest contains unknown field %q", field)
		}
		if _, exists := seen[field]; exists {
			return nil, fmt.Errorf("active certificate manifest contains duplicate field %q", field)
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode active certificate manifest field %q: %w", field, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode active certificate manifest: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("active certificate manifest must end with a JSON object delimiter")
	}
	if err := requireManifestEOF(decoder); err != nil {
		return nil, err
	}
	return seen, nil
}

// knownManifestField requires exact schema spelling because encoding/json otherwise accepts case-insensitive aliases.
func knownManifestField(field string) bool {
	switch field {
	case "version", "kind", "fingerprint", "authority_fingerprint", "hosts":
		return true
	default:
		return false
	}
}

// requireManifestEOF prevents a valid first JSON value from hiding trailing material.
func requireManifestEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("active certificate manifest contains multiple JSON values")
	}
	return fmt.Errorf("active certificate manifest contains trailing data: %w", err)
}

// validateAuthorityManifest proves the active pointer can name only one root generation.
func validateAuthorityManifest(manifest activeManifest) error {
	if manifest.Version != manifestVersion {
		return fmt.Errorf("authority manifest version = %d, want %d", manifest.Version, manifestVersion)
	}
	if manifest.Kind != manifestKindAuthority {
		return fmt.Errorf("authority manifest kind = %q, want %q", manifest.Kind, manifestKindAuthority)
	}
	if err := validateFingerprint(manifest.Fingerprint); err != nil {
		return fmt.Errorf("authority manifest fingerprint: %w", err)
	}
	if manifest.authorityFieldPresent || manifest.hostsFieldPresent || manifest.AuthorityFingerprint != "" || len(manifest.Hosts) != 0 {
		return fmt.Errorf("authority manifest contains leaf-only fields")
	}
	return nil
}

// validateLeafManifest binds an active leaf to the exact CA and canonical host-set path selected by its caller.
func validateLeafManifest(manifest activeManifest, authorityFingerprint string, hosts []string) error {
	if manifest.Version != manifestVersion {
		return fmt.Errorf("leaf manifest version = %d, want %d", manifest.Version, manifestVersion)
	}
	if manifest.Kind != manifestKindLeaf {
		return fmt.Errorf("leaf manifest kind = %q, want %q", manifest.Kind, manifestKindLeaf)
	}
	if !manifest.authorityFieldPresent || !manifest.hostsFieldPresent {
		return fmt.Errorf("leaf manifest omits required leaf fields")
	}
	if err := validateFingerprint(manifest.Fingerprint); err != nil {
		return fmt.Errorf("leaf manifest fingerprint: %w", err)
	}
	if err := validateFingerprint(manifest.AuthorityFingerprint); err != nil {
		return fmt.Errorf("leaf manifest authority fingerprint: %w", err)
	}
	if manifest.AuthorityFingerprint != authorityFingerprint {
		return fmt.Errorf("leaf manifest authority fingerprint does not match its directory")
	}
	canonical, err := localca.CanonicalHosts(manifest.Hosts)
	if err != nil {
		return fmt.Errorf("leaf manifest hosts: %w", err)
	}
	if !slices.Equal(canonical, manifest.Hosts) {
		return fmt.Errorf("leaf manifest hosts are not in canonical order")
	}
	if !slices.Equal(canonical, hosts) {
		return fmt.Errorf("leaf manifest hosts do not match their host-set directory")
	}
	return nil
}

// validateFingerprint limits every generation path to a lowercase SHA-256 identity.
func validateFingerprint(fingerprint string) error {
	if len(fingerprint) != fingerprintBytes*2 {
		return fmt.Errorf("fingerprint must contain %d lowercase hexadecimal characters", fingerprintBytes*2)
	}
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || hex.EncodeToString(decoded) != fingerprint {
		return fmt.Errorf("fingerprint must contain %d lowercase hexadecimal characters", fingerprintBytes*2)
	}
	return nil
}

// hostSetID gives canonical SAN collections an unambiguous, path-safe identity.
func hostSetID(hosts []string) string {
	digest := sha256.New()
	var length [4]byte
	for _, host := range hosts {
		binary.BigEndian.PutUint32(length[:], uint32(len(host)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(host))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
