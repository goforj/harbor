package windowsinstallation

import (
	"encoding/json"
	"strings"
	"testing"
)

// fixtureMetadata returns a complete deterministic Windows release identity without accessing host state.
func fixtureMetadata(t *testing.T) (Manifest, Selection) {
	t.Helper()
	manifest := Manifest{Architecture: "amd64", SchemaVersion: 1, ProductID: ProductID, Version: "0.1.0-dev.1", SourceRevision: strings.Repeat("a", 40), ReleaseSequence: 1, SignerThumbprint: strings.Repeat("A", 40), Components: Plans(1)}
	for index := range manifest.Components {
		manifest.Components[index].Size = 1
		manifest.Components[index].SHA256 = strings.Repeat("a", 64)
	}
	digest, err := Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BundleDigest = digest
	return manifest, Selection{SchemaVersion: 1, ProductID: ProductID, InstallationID: strings.Repeat("b", 32), UserSID: "S-1-5-21-1-2-3-1000", ReleaseSequence: 1, BundleDigest: digest, SignerThumbprint: manifest.SignerThumbprint}
}

// TestMetadataSelectionAndManifestRejectDrift keeps every authority-bearing installation field fail closed.
func TestMetadataSelectionAndManifestRejectDrift(t *testing.T) {
	manifest, selection := fixtureMetadata(t)
	if err := ValidateManifest(manifest, selection); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Selection){func(s *Selection) { s.UserSID = "S-1-5-18" }, func(s *Selection) { s.UserSID = "S-1-5-21-1-2-3-4294967296" }, func(s *Selection) { s.UserSID = "S-1-5-21-01-2-3-1000" }, func(s *Selection) { s.SchemaVersion++ }, func(s *Selection) { s.ProductID = "foreign" }, func(s *Selection) { s.InstallationID = "../escape" }, func(s *Selection) { s.ReleaseSequence = 0 }, func(s *Selection) { s.BundleDigest = strings.Repeat("B", 64) }, func(s *Selection) { s.SignerThumbprint = "" }} {
		changed := selection
		mutate(&changed)
		if ValidateManifest(manifest, changed) == nil {
			t.Fatal("drifted selection admitted")
		}
	}
	for _, mutate := range []func(*Manifest){func(m *Manifest) { m.Architecture = "foreign" }, func(m *Manifest) { m.SchemaVersion++ }, func(m *Manifest) { m.ProductID = "foreign" }, func(m *Manifest) { m.ReleaseSequence++ }, func(m *Manifest) { m.SignerThumbprint = strings.Repeat("B", 40) }, func(m *Manifest) { m.Version = "stable" }, func(m *Manifest) { m.SourceRevision = "HEAD" }, func(m *Manifest) { m.Components = m.Components[:1] }, func(m *Manifest) { m.Components[0].Path = "../foreign.exe" }, func(m *Manifest) { m.Components[0].Role = "foreign" }, func(m *Manifest) { m.Components[0].Size = 0 }, func(m *Manifest) { m.Components[0].SHA256 = "invalid" }, func(m *Manifest) { m.Components[0].SHA256 = strings.Repeat("b", 64) }} {
		changed := manifest
		changed.Components = append([]Component(nil), manifest.Components...)
		mutate(&changed)
		if ValidateManifest(changed, selection) == nil {
			t.Fatal("drifted manifest admitted")
		}
	}
	if _, err := ComponentPath(manifest, "daemon"); err != nil {
		t.Fatal(err)
	}
	if _, err := ComponentPath(manifest, "foreign"); err == nil {
		t.Fatal("unknown role admitted")
	}
}

// TestDecodeRejectsAmbiguousMetadata denies duplicate keys, unknown fields, trailing input and oversized records.
func TestDecodeRejectsAmbiguousMetadata(t *testing.T) {
	_, selection := fixtureMetadata(t)
	data, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Selection
	if err := Decode(strings.NewReader(string(data)+"\n"), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", string(data) + "{}", strings.Replace(string(data), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), strings.Replace(string(data), `"schema_version":1`, `"foreign":1,"schema_version":1`, 1), strings.Repeat(" ", MetadataLimit+1)} {
		if Decode(strings.NewReader(value), &decoded) == nil {
			t.Fatal("ambiguous metadata admitted")
		}
	}
}
