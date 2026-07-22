package lowport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
)

const observationFingerprintDomain = "goforj.harbor.lowport-observation.v2\x00"

// Fingerprint returns canonical compare-and-swap evidence for a validated observation.
func (o Observation) Fingerprint() (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	return fingerprintValidated(o), nil
}

// fingerprintValidated binds every request field and native fact after validation has succeeded.
func fingerprintValidated(o Observation) string {
	payload := append([]byte(nil), observationFingerprintDomain...)
	payload = appendString(payload, o.Request.installationID)
	payload = binary.AppendUvarint(payload, uint64(o.Request.ownerUID))
	payload = appendString(payload, o.Request.policyFingerprint)
	payload = appendString(payload, o.Request.httpUpstream.String())
	payload = appendString(payload, o.Request.httpsUpstream.String())
	payload = appendBool(payload, o.Complete)

	artifacts := make([][]byte, 0, len(o.Artifacts))
	for _, artifact := range o.Artifacts {
		encoded := appendString(nil, string(artifact.Kind))
		encoded = appendBool(encoded, artifact.Present)
		encoded = appendBool(encoded, artifact.Owned)
		encoded = appendBool(encoded, artifact.Exact)
		encoded = appendBool(encoded, artifact.Ambiguous)
		encoded = appendString(encoded, artifact.Fingerprint)
		artifacts = append(artifacts, encoded)
	}
	slices.SortFunc(artifacts, bytes.Compare)
	payload = binary.AppendUvarint(payload, uint64(len(artifacts)))
	for _, artifact := range artifacts {
		payload = appendBytes(payload, artifact)
	}
	payload = appendString(payload, string(classifyValidated(o)))
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// classifyValidated classifies native facts after their bounded representation has been proven valid.
func classifyValidated(o Observation) State {
	if !o.Complete {
		return StateIndeterminate
	}
	kinds := make(map[ArtifactKind]int, 2)
	present := 0
	exact := 0
	for _, artifact := range o.Artifacts {
		kinds[artifact.Kind]++
		if artifact.Ambiguous {
			return StateAmbiguous
		}
		if !artifact.Present {
			continue
		}
		present++
		if !artifact.Owned {
			return StateForeign
		}
		if artifact.Exact {
			exact++
		}
	}
	if kinds[ArtifactKindPlist] != 1 || kinds[ArtifactKindService] != 1 {
		return StateAmbiguous
	}
	if present == 0 {
		return StateAbsent
	}
	if present == 2 && exact == 2 {
		return StateExact
	}
	return StateOwnedDrifted
}

// appendString length-prefixes text so adjacent request or artifact fields cannot collide.
func appendString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendBytes length-prefixes encoded facts before hashing them.
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
