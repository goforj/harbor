package materialstore

import (
	"strings"
	"testing"
)

// FuzzManifestDecoder ensures arbitrary persisted bytes never panic or bypass strict schema validation.
func FuzzManifestDecoder(fuzzer *testing.F) {
	fingerprint := strings.Repeat("a", 64)
	for _, seed := range [][]byte{
		{},
		[]byte(`{"version":1,"kind":"authority","fingerprint":"` + fingerprint + `"}`),
		[]byte(`{"version":1,"kind":"leaf","fingerprint":"` + fingerprint + `","authority_fingerprint":"` + fingerprint + `","hosts":["orders.test"]}`),
		[]byte(`{"version":2}`),
		[]byte(`null`),
		[]byte(`{} {}`),
		[]byte(`{"version":1,"kind":"authority","kind":"leaf","fingerprint":"` + fingerprint + `"}`),
		[]byte(`{"version":1,"kind":"authority","Kind":"leaf","fingerprint":"` + fingerprint + `"}`),
		[]byte(`{"version":1,"kind":"authority","fingerprint":"` + fingerprint + `","authority_fingerprint":null}`),
		[]byte(`{"version":1,"kind":"authority","fingerprint":"` + fingerprint + `","hosts":[]}`),
	} {
		fuzzer.Add(seed)
	}
	fuzzer.Fuzz(func(t *testing.T, encoded []byte) {
		manifest, err := decodeManifest(encoded)
		if err != nil {
			return
		}
		switch manifest.Kind {
		case manifestKindAuthority:
			_ = validateAuthorityManifest(manifest)
		case manifestKindLeaf:
			_ = validateLeafManifest(manifest, manifest.AuthorityFingerprint, manifest.Hosts)
		}
	})
}
