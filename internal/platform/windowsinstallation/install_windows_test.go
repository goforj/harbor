//go:build windows

package windowsinstallation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/windows"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// signedInstallPayload reuses this worker's signed PE, signed policy and exported certificate without executing them.
func signedInstallPayload(t *testing.T) (string, fstest.MapFS) {
	t.Helper()
	publisher := os.Getenv("HARBOR_ADMISSION_PUBLISHER")
	payloads := make(map[string][]byte)
	for extension, variable := range map[string]string{".exe": "HARBOR_ADMISSION_SIGNED_PE", ".ps1": "HARBOR_ADMISSION_SIGNED_SCRIPT", ".cer": "HARBOR_ADMISSION_CERTIFICATE"} {
		data, err := os.ReadFile(os.Getenv(variable))
		if err != nil {
			t.Fatal(err)
		}
		payloads[extension] = data
	}
	payloads[".txt"] = []byte(publisher)
	manifest, _ := fixtureMetadata(t)
	manifest.SignerThumbprint = publisher
	payload := make(fstest.MapFS)
	for index := range manifest.Components {
		component := &manifest.Components[index]
		data := payloads[filepath.Ext(component.Path)]
		digest := sha256.Sum256(data)
		component.Size, component.SHA256 = int64(len(data)), hex.EncodeToString(digest[:])
		payload[component.Path] = &fstest.MapFile{Data: data, Mode: 0400}
	}
	digest, err := Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BundleDigest = digest
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	payload["release-manifest.json"] = &fstest.MapFile{Data: data, Mode: 0400}
	return publisher, payload
}

// TestNativeInstallTransaction verifies complete creation, exact reuse and foreign state refusal on a disposable protected parent.
func TestNativeInstallTransaction(t *testing.T) {
	base := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if base == "" {
		t.Skip("disposable signed native fixture was not configured")
	}
	publisher, payload := signedInstallPayload(t)
	for _, name := range []string{"complete", "foreign", "digest", "missing", "publisher"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(base, "install-"+name, "Harbor")
			candidate := make(fstest.MapFS)
			for path, file := range payload {
				candidate[path] = &fstest.MapFile{Data: append([]byte(nil), file.Data...), Mode: file.Mode}
			}
			if name == "foreign" {
				if err := createMachineDirectoryNew(filepath.Dir(root)); err != nil {
					t.Fatal(err)
				}
				if err := createMachineDirectoryNew(root); err != nil {
					t.Fatal(err)
				}
				if err := writeMachineBytes(filepath.Join(root, "foreign.txt"), []byte("preserve")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "digest" {
				candidate["desktop-launcher.exe"].Data = []byte("changed")
			}
			if name == "missing" {
				delete(candidate, "desktop-launcher.exe")
			}
			if name == "publisher" {
				candidate["harbor-helper-signing-thumbprint.txt"].Data = []byte("wrong")
			}
			installation, err := installAt(root, publisher, candidate)
			if name != "complete" {
				if err == nil {
					installation.Close()
					t.Fatal("invalid payload or foreign root admitted")
				}
				if name == "foreign" {
					if data, err := os.ReadFile(filepath.Join(root, "foreign.txt")); err != nil || string(data) != "preserve" {
						t.Fatalf("foreign file changed: %q %v", data, err)
					}
				}
				if name == "publisher" {
					if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("publisher failure created machine state")
					}
				}
				if name == "digest" || name == "missing" {
					if _, err := os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("failed payload committed fixed root")
					}
					retry, err := installAt(root, publisher, payload)
					if err != nil {
						t.Fatalf("valid retry after failed staging: %v", err)
					}
					if err := retry.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			identity := installation.Selection()
			if err := installation.Close(); err != nil {
				t.Fatal(err)
			}
			reused, err := installAt(root, publisher, payload)
			if err != nil {
				t.Fatal(err)
			}
			if reused.Selection() != identity {
				t.Fatal("repair changed stable installation identity")
			}
			if err := reused.Close(); err != nil {
				t.Fatal(err)
			}
			for path := range payload {
				if path == "release-manifest.json" {
					continue
				}
				if _, err := fs.Stat(os.DirFS(root), path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestNativeStageCommitPreservesReplacementAndExistingDestination exercises handle identity and no-replace rename before a selection can become canonical.
func TestNativeStageCommitPreservesReplacementAndExistingDestination(t *testing.T) {
	base := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if base == "" {
		t.Skip("disposable signed native fixture was not configured")
	}
	for _, name := range []string{"replaced", "existing"} {
		t.Run(name, func(t *testing.T) {
			stage, destination := filepath.Join(base, "stage-"+name), filepath.Join(base, "destination-"+name)
			if err := createMachineDirectoryNew(stage); err != nil {
				t.Fatal(err)
			}
			file, err := openRetained(stage, true)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := identifyFile(windows.Handle(file.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if name == "replaced" {
				if err := os.Rename(stage, stage+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := createMachineDirectoryNew(stage); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := createMachineDirectoryNew(destination); err != nil {
					t.Fatal(err)
				}
				if err := writeMachineBytes(filepath.Join(destination, "foreign.txt"), []byte("preserve")); err != nil {
					t.Fatal(err)
				}
			}
			if err := commitStagedDirectory(stage, destination, identity); err == nil {
				t.Fatal("replacement or existing destination admitted")
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatal("source stage changed")
			}
			if name == "existing" {
				if data, err := os.ReadFile(filepath.Join(destination, "foreign.txt")); err != nil || string(data) != "preserve" {
					t.Fatalf("foreign destination changed: %q %v", data, err)
				}
			}
		})
	}
}
