//go:build windows

package windowsinstallation

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestNativeIntegrationRegistryPreservesForeignEntries exercises only a disposable machine-key fixture.
func TestNativeIntegrationRegistryPreservesForeignEntries(t *testing.T) {
	root := integrationTestAnchor(t)
	path := `Software\HarborAdmission-` + filepath.Base(filepath.Dir(root)) + `-Registry`
	values := map[string]integrationRegistryValue{"DisplayName": {registry.SZ, integrationUTF16Bytes("GoForj Harbor", true)}, "NoModify": {registry.DWORD, []byte{1, 0, 0, 0}}}
	key, err := integrationCreateRegistryAt(registry.LOCAL_MACHINE, path, values)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := key.retire(); err != nil {
			t.Error(err)
		}
		key.Close()
	})
	if other, err := integrationCreateRegistryAt(registry.LOCAL_MACHINE, path, values); err == nil {
		other.Close()
		t.Fatal("replaced existing registry fixture")
	}
	if err := integrationInspectRegistry(key.Key, values); err != nil {
		t.Fatal(err)
	}
	if err := integrationSetRegistryValue(key.Key, "NoModify", integrationRegistryValue{registry.SZ, integrationUTF16Bytes("1", true)}); err != nil {
		t.Fatal(err)
	}
	if err := integrationInspectRegistry(key.Key, values); err == nil {
		t.Fatal("accepted wrong registry value type")
	}
	if err := integrationSetRegistryValue(key.Key, "NoModify", values["NoModify"]); err != nil {
		t.Fatal(err)
	}
	if err := integrationSetRegistryValue(key.Key, "Foreign", values["DisplayName"]); err != nil {
		t.Fatal(err)
	}
	if err := integrationInspectRegistry(key.Key, values); err == nil {
		t.Fatal("accepted extra registry value")
	}
	if err := key.Key.DeleteValue("Foreign"); err != nil {
		t.Fatal(err)
	}
	child, _, err := registry.CreateKey(key.Key, "ForeignChild", registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if err := integrationInspectRegistry(key.Key, values); err == nil {
		t.Fatal("accepted foreign registry child")
	}
	if err := key.retire(); err == nil {
		t.Fatal("deleted a key containing foreign child")
	}
	if err := (&integrationRegistry{child}).retire(); err != nil {
		t.Fatal(err)
	}
	child.Close()
}
