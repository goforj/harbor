//go:build windows

package windowsinstallation

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// integrationTestAnchor isolates native machine writes beneath the hosted workflow's protected disposable fixture.
func integrationTestAnchor(t *testing.T) string {
	t.Helper()
	root := os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires the native admission workflow's disposable protected fixture")
	}
	root = filepath.Join(root, t.Name())
	descriptor, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	if err := windows.CreateDirectory(pointer, &attrs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

// TestNativePrivilegedSpool proves exact topology, repair admission, and handle-based empty retirement on NTFS.
func TestNativePrivilegedSpool(t *testing.T) {
	root := integrationTestAnchor(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	spool, err := preparePrivilegedSpool(root, sid)
	if err != nil {
		t.Fatal(err)
	}
	objects := make([]integrationObject, len(spool.Directories))
	for i, d := range spool.Directories {
		objects[i] = d.Object
		if d.Object.ID == "" || !d.Object.Created {
			t.Fatalf("uncommitted native identity: %#v", d.Object)
		}
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	if foreign, err := preparePrivilegedSpool(root, sid); err == nil {
		foreign.Close()
		t.Fatal("recreated unclaimed privileged state")
	}
	admitted, err := admitPrivilegedSpool(root, sid, objects)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitted.rollback(); err != nil {
		admitted.Close()
		t.Fatal(err)
	}
	if err := admitted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "GoForj")); !os.IsNotExist(err) {
		t.Fatalf("owned empty spool survived: %v", err)
	}
}

// TestNativePrivilegedSpoolPreservesForeignContent refuses recursive deletion of an unowned file.
func TestNativePrivilegedSpoolPreservesForeignContent(t *testing.T) {
	root := integrationTestAnchor(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	spool, err := preparePrivilegedSpool(root, user.User.Sid.String())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "GoForj", "Harbor", "Privileged", "tickets", "pending", "foreign.txt")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		spool.Close()
		t.Fatal(err)
	}
	if err := spool.rollback(); err == nil {
		spool.Close()
		t.Fatal("retired a nonempty unowned namespace")
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
		t.Fatalf("foreign content changed: %q, %v", data, err)
	}
}

// TestPrivilegedSpoolRejectsInvalidReceipt covers malformed identity and topology before machine mutation.
func TestPrivilegedSpoolRejectsInvalidReceipt(t *testing.T) {
	if spool, err := preparePrivilegedSpool(`C:\ProgramData`, "S-1-5-18"); err == nil {
		spool.Close()
		t.Fatal("accepted system as intended interactive account")
	}
	if spool, err := admitPrivilegedSpool(`C:\ProgramData`, "S-1-5-21-1-2-3-1001", nil); err == nil {
		spool.Close()
		t.Fatal("accepted incomplete receipt")
	}
}

// TestIntegrationSecurityRejectsDrift distinguishes descriptor bookkeeping from authority changes.
func TestIntegrationSecurityRejectsDrift(t *testing.T) {
	base := "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"
	want, err := windows.SecurityDescriptorFromString(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, sddl string
		ok         bool
	}{
		{"exact", base, true},
		{"reordered", "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"auto inherited bookkeeping", "O:BAD:PAI(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)", true},
		{"unprotected", "O:BAD:(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)", false},
		{"owner", "O:SYD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)", false},
		{"missing", "O:BAD:P(A;OICI;FA;;;BA)", false},
		{"extra", "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FR;;;BU)", false},
		{"duplicate", "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;FA;;;SY)", false},
		{"mask", "O:BAD:P(A;OICI;FR;;;BA)(A;OICI;FA;;;SY)", false},
		{"flags", "O:BAD:P(A;OI;FA;;;BA)(A;OICI;FA;;;SY)", false},
		{"deny", "O:BAD:P(D;OICI;FA;;;BA)(A;OICI;FA;;;SY)", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := windows.SecurityDescriptorFromString(c.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := compareIntegrationSecurity(got, want); (err == nil) != c.ok {
				t.Fatalf("comparison = %v, want accepted %v", err, c.ok)
			}
		})
	}
}
