//go:build windows

package windowsinstallation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

// TestInstalledDescriptorRefusesForeignMutationAndOwnership exercises native ACL parsing without changing machine state.
func TestInstalledDescriptorRefusesForeignMutationAndOwnership(t *testing.T) {
	for _, test := range []struct {
		name, sddl      string
		directory, want bool
	}{
		{"fixedfile", "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)", false, true},
		{"fixeddirectory", "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;GRGX;;;BU)", true, true},
		{"foreignowner", "O:BUD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)", false, false},
		{"writableusers", "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GW;;;BU)", false, false},
		{"deleteusers", "O:BAD:P(A;;FA;;;BA)(A;;SD;;;BU)", false, false},
		{"reaclusers", "O:BAD:P(A;;FA;;;BA)(A;;WD;;;BU)", false, false},
		{"nullacl", "O:BA", false, false},
		{"unsupporteddeny", "O:BAD:P(D;;GW;;;BU)(A;;FA;;;BA)", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if got := admitDescriptor(descriptor, test.directory); (got == nil) != test.want {
				t.Fatalf("admitDescriptor=%v wantallowed%v", got, test.want)
			}
		})
	}
}

// TestRetainedFileRefusesAliasesAndPinsReplacement verifies the kernel handle remains authoritative until close.
func TestRetainedFileRefusesAliasesAndPinsReplacement(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := windowsfile.FinalPath(windows.Handle(directory.Fd()))
	closeErr := directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	root = strings.TrimPrefix(canonical, `\\?\`)
	path := filepath.Join(root, "component.exe")
	if err := os.WriteFile(path, []byte("component"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openRetained(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".replacement"); err == nil {
		t.Fatal("retained file allowed rename")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "hardlink.exe")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if file, err := openRetained(path, false); err == nil {
		file.Close()
		t.Fatal("hard-linked component admitted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("native account cannot create symbolic links: %v", err)
	}
	if file, err := openRetained(alias, false); err == nil {
		file.Close()
		t.Fatal("reparse component admitted")
	}
}

// TestSignatureRefusesUnsignedPE ensures a trust failure cannot resemble a trusted installed publisher.
func TestSignatureRefusesUnsignedPE(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := verifySignature(file, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("unsigned test executable admitted")
	}
}
