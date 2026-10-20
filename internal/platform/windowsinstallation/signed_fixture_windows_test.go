//go:build windows

package windowsinstallation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestNativeSignedInstallationAdmission uses only disposable CI-signed fixtures created on this same Windows worker.
func TestNativeSignedInstallationAdmission(t *testing.T) {
	root := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("disposable signed native fixture was not configured")
	}
	publisher := os.Getenv("HARBOR_ADMISSION_PUBLISHER")
	if !thumbprintPattern.MatchString(publisher) {
		t.Fatal("fixture publisher is absent or malformed")
	}
	payloads := make(map[string][]byte)
	for extension, variable := range map[string]string{".exe": "HARBOR_ADMISSION_SIGNED_PE", ".ps1": "HARBOR_ADMISSION_SIGNED_SCRIPT", ".cer": "HARBOR_ADMISSION_CERTIFICATE"} {
		data, err := os.ReadFile(os.Getenv(variable))
		if err != nil {
			t.Fatal(err)
		}
		payloads[extension] = data
	}
	payloads[".txt"] = []byte(publisher)
	for _, name := range []string{"exact", "digest", "publisher", "selection", "hardlink", "reparse", "acl", "owner"} {
		t.Run(name, func(t *testing.T) {
			expectedPublisher := publisher
			path := filepath.Join(root, name)
			manifest, selection := writeSignedFixture(t, path, publisher, payloads)
			component := filepath.Join(path, filepath.FromSlash(manifest.Components[3].Path))
			switch name {
			case "digest":
				if err := os.WriteFile(component, []byte("foreign content"), 0600); err != nil {
					t.Fatal(err)
				}
			case "publisher":
				expectedPublisher = strings.Repeat("B", 40)
			case "selection":
				selection.ReleaseSequence++
				writeFixtureJSON(t, filepath.Join(path, "installation.json"), selection)
			case "hardlink":
				if err := os.Link(component, filepath.Join(path, "foreign.exe")); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				if err := os.Rename(component, component+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(component+".original", component); err != nil {
					t.Fatal(err)
				}
			case "acl":
				setFixtureSecurity(t, component, "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GW;;;BU)")
			case "owner":
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				setFixtureSecurity(t, component, "O:"+user.User.Sid.String()+"D:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)")
			}
			installation, err := admitAt(path, expectedPublisher)
			if name != "exact" {
				if err == nil {
					installation.Close()
					t.Fatal("drifted native installation admitted")
				}
				t.Logf("refused %s: %v", name, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(component, component+".replacement"); err == nil {
				t.Fatal("admitted component allowed replacement")
			}
			if err := installation.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(component, component+".replacement"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeSignedFixture creates a protected complete payload without installing or executing its components.
func writeSignedFixture(t *testing.T, root, publisher string, payloads map[string][]byte) (Manifest, Selection) {
	t.Helper()
	manifest, selection := fixtureMetadata(t)
	manifest.SignerThumbprint, selection.SignerThumbprint = publisher, publisher
	for _, path := range []string{root, filepath.Join(root, "releases"), filepath.Join(root, "releases", "1")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		setFixtureSecurity(t, path, "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;GRGX;;;BU)")
	}
	for index := range manifest.Components {
		component := &manifest.Components[index]
		data := payloads[filepath.Ext(component.Path)]
		if len(data) == 0 {
			t.Fatalf("fixture lacks %s", component.Role)
		}
		path := filepath.Join(root, filepath.FromSlash(component.Path))
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		setFixtureSecurity(t, path, "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)")
		digest := sha256.Sum256(data)
		component.Size, component.SHA256 = int64(len(data)), hex.EncodeToString(digest[:])
	}
	digest, err := Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BundleDigest, selection.BundleDigest = digest, digest
	writeFixtureJSON(t, filepath.Join(root, "releases", "1", "release-manifest.json"), manifest)
	writeFixtureJSON(t, filepath.Join(root, "installation.json"), selection)
	return manifest, selection
}

// writeFixtureJSON preserves the production canonical representation before assigning the machine fixture owner.
func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	setFixtureSecurity(t, path, "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)")
}

// setFixtureSecurity authors fixture ACLs through the exact opened object rather than a shell command.
func setFixtureSecurity(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.WRITE_OWNER|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
	if err := errors.Join(err, windows.CloseHandle(handle)); err != nil {
		t.Fatal(err)
	}
}
