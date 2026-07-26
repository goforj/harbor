package releasebundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestSealDarwinDevelopmentPayloadWritesOneCompleteIdentity proves the fixed component set seals deterministically.
func TestSealDarwinDevelopmentPayloadWritesOneCompleteIdentity(t *testing.T) {
	root := t.TempDir()
	const sequence = uint64(42)
	for _, component := range darwinComponents(sequence) {
		path := filepath.Join(root, filepath.FromSlash(component.Destination[1:]))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(component.Role), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	manifest, err := SealDarwinDevelopmentPayload(root, DarwinConfig{
		Version:         "0.1.0-dev.42",
		ReleaseSequence: sequence,
		SourceRevision:  "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatalf("SealDarwinDevelopmentPayload() error = %v", err)
	}
	if manifest.BundleDigest == "" || len(manifest.Components) != 7 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.ControlProtocol != 1 ||
		manifest.SnapshotSchema != domain.SnapshotSchemaVersion ||
		manifest.HelperProtocol != helper.ProtocolVersion {
		t.Fatalf("manifest protocol identity = %#v", manifest)
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if digest != manifest.BundleDigest {
		t.Fatalf("bundle digest = %q, want %q", manifest.BundleDigest, digest)
	}
	appManifest := filepath.Join(root, "Applications", "Harbor.app", "Contents", "Library", "Harbor", "release-manifest.json")
	releaseManifest := filepath.Join(root, "Library", "Application Support", "GoForj", "Harbor", "releases", strconv.FormatUint(sequence, 10), "release-manifest.json")
	appContent, err := os.ReadFile(appManifest)
	if err != nil {
		t.Fatal(err)
	}
	releaseContent, err := os.ReadFile(releaseManifest)
	if err != nil {
		t.Fatal(err)
	}
	if string(appContent) != string(releaseContent) {
		t.Fatal("application and release manifests differ")
	}
	var decoded Manifest
	if err := json.Unmarshal(appContent, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BundleDigest != manifest.BundleDigest {
		t.Fatalf("written bundle digest = %q", decoded.BundleDigest)
	}
}

// TestSealDarwinDevelopmentPayloadRejectsMissingComponents prevents partial packages from acquiring a manifest.
func TestSealDarwinDevelopmentPayloadRejectsMissingComponents(t *testing.T) {
	_, err := SealDarwinDevelopmentPayload(t.TempDir(), DarwinConfig{
		Version:         "0.1.0-dev.1",
		ReleaseSequence: 1,
		SourceRevision:  "0123456789abcdef0123456789abcdef01234567",
	})
	if err == nil {
		t.Fatal("SealDarwinDevelopmentPayload() error = nil")
	}
}
