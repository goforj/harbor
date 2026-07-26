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
	for _, plan := range darwinComponentPlans(sequence) {
		source := plan.source
		if source == "" {
			source = plan.component.Destination
		}
		path := filepath.Join(root, filepath.FromSlash(source[1:]))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(plan.component.Role), 0o755); err != nil {
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
	if manifest.BundleDigest == "" || len(manifest.Components) != 8 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.ControlProtocol != 1 ||
		manifest.SnapshotSchema != domain.SnapshotSchemaVersion ||
		manifest.HelperProtocol != helper.ProtocolVersion {
		t.Fatalf("manifest protocol identity = %#v", manifest)
	}
	if manifest.Components[6].Mode != "4755" || manifest.Components[7].Mode != "0755" {
		t.Fatalf("manifest privileged component modes = %#v", manifest.Components[6:])
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

// TestNativeModeRetainsPrivilegedExecutableBits keeps helper admission facts in the sealed manifest.
func TestNativeModeRetainsPrivilegedExecutableBits(t *testing.T) {
	if got := nativeMode(0o755 | os.ModeSetuid); got != 0o4755 {
		t.Fatalf("nativeMode() = %04o, want 4755", got)
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
