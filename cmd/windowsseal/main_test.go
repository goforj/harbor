package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
)

// TestSealCommitsExactCompletePayload ensures wrappers and duplicated uninstaller bytes bind the authoritative role plan.
func TestSealCommitsExactCompletePayload(t *testing.T) {
	components := t.TempDir()
	input := options{components: components, output: filepath.Join(t.TempDir(), "payload"), version: "0.1.0-dev.1", revision: strings.Repeat("a", 40), publisher: strings.Repeat("A", 40), sequence: 7}
	for _, component := range windowsinstallation.Plans(input.sequence) {
		name := componentSource(component.Role, component.Path)
		if err := os.WriteFile(filepath.Join(components, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := seal(input); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(input.output, "release-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var manifest windowsinstallation.Manifest
	if err := windowsinstallation.Decode(file, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, component := range manifest.Components {
		data, err := os.ReadFile(filepath.Join(input.output, filepath.FromSlash(component.Path)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if string(data) != componentSource(component.Role, component.Path) || int64(len(data)) != component.Size || hex.EncodeToString(digest[:]) != component.SHA256 {
			t.Fatalf("payload mismatch %s", component.Role)
		}
	}
	if err := seal(input); err == nil {
		t.Fatal("existing sealed output overwritten")
	}
}

// TestSealRejectsInvalidIdentityBeforeWriting prevents partial output from malformed build selections.
func TestSealRejectsInvalidIdentityBeforeWriting(t *testing.T) {
	for _, input := range []options{{}, {components: t.TempDir(), output: filepath.Join(t.TempDir(), "payload"), version: "stable", sequence: 1}} {
		if err := seal(input); err == nil {
			t.Fatal("invalid identity admitted")
		}
		if input.output != "" {
			if _, err := os.Stat(input.output); !os.IsNotExist(err) {
				t.Fatal("invalid identity created output")
			}
		}
	}
}

// TestSealRejectsMissingEmptyAndNonregularInputs prevents a manifest from committing an incomplete component set.
func TestSealRejectsMissingEmptyAndNonregularInputs(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			components := t.TempDir()
			input := options{components: components, output: filepath.Join(t.TempDir(), "payload"), version: "0.1.0-dev.1", revision: strings.Repeat("a", 40), publisher: strings.Repeat("A", 40), sequence: 7}
			path := filepath.Join(components, "desktop-launcher.exe")
			var err error
			switch kind {
			case "empty":
				err = os.WriteFile(path, nil, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				target := filepath.Join(components, "target.exe")
				if err = os.WriteFile(target, []byte("signed"), 0600); err == nil {
					err = os.Symlink(target, path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := seal(input); err == nil {
				t.Fatal("invalid component admitted")
			}
			if _, err := os.Stat(filepath.Join(input.output, "release-manifest.json")); !os.IsNotExist(err) {
				t.Fatal("incomplete payload committed a manifest")
			}
		})
	}
}
