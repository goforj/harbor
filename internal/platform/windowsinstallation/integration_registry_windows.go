//go:build windows

package windowsinstallation

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const integrationUninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor`
const integrationRegistrySDDL = "O:BAD:P(A;;KA;;;BA)(A;;KA;;;SY)(A;;KR;;;BU)"

// integrationRegistryValue retains the exact native value type and bytes, including string terminators.
type integrationRegistryValue struct {
	Type uint32
	Data []byte
}

// integrationRegistry retains the admitted key so retirement cannot delete a pathname replacement.
type integrationRegistry struct{ Key registry.Key }

// integrationRegistryValues derives the entire allowlisted registration from admitted installation state.
func integrationRegistryValues(installation *Installation) (map[string]integrationRegistryValue, error) {
	uninstaller, err := installation.Path("uninstaller")
	if err != nil {
		return nil, err
	}
	icon, err := installation.Path("desktop")
	if err != nil {
		return nil, err
	}
	selection := installation.Selection()
	values := make(map[string]integrationRegistryValue)
	for name, value := range map[string]string{
		"DisplayName": "GoForj Harbor", "Publisher": "GoForj", "DisplayVersion": installation.Manifest().Version,
		"InstallLocation": installation.root, "DisplayIcon": `"` + icon + `",0`, "UninstallString": `"` + uninstaller + `"`,
		"InstallationID": selection.InstallationID, "UserSID": selection.UserSID, "BundleDigest": selection.BundleDigest,
	} {
		units, err := windows.UTF16FromString(value)
		if err != nil {
			return nil, err
		}
		data := make([]byte, len(units)*2)
		for index, unit := range units {
			binary.LittleEndian.PutUint16(data[index*2:], unit)
		}
		values[name] = integrationRegistryValue{registry.SZ, data}
	}
	for _, name := range []string{"NoModify", "NoRepair"} {
		values[name] = integrationRegistryValue{registry.DWORD, []byte{1, 0, 0, 0}}
	}
	return values, nil
}

// integrationCreateRegistry refuses an existing registration before writing any values.
func integrationCreateRegistry(values map[string]integrationRegistryValue) (*integrationRegistry, error) {
	const parentPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
	pointer, _ := windows.UTF16PtrFromString(parentPath)
	var handle windows.Handle
	if err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, pointer, 8, registry.CREATE_SUB_KEY|registry.READ|registry.WOW64_64KEY, &handle); err != nil {
		return nil, err
	}
	defer windows.RegCloseKey(handle)
	if err := integrationRegistryCheckNativeName(registry.Key(handle), `\REGISTRY\MACHINE\`+parentPath); err != nil {
		return nil, err
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if err := integrationRegistryAdmitParent(descriptor); err != nil {
		return nil, err
	}
	key, err := integrationCreateRegistryAt(registry.Key(handle), "GoForjHarbor", values)
	if err != nil {
		return nil, err
	}
	if err := integrationRegistryNativeName(key.Key); err != nil {
		return nil, errors.Join(err, key.retire(), key.Close())
	}
	return key, nil
}

// integrationCreateRegistryAt confines disposable tests to an explicitly supplied registry fixture.
func integrationCreateRegistryAt(parent registry.Key, path string, values map[string]integrationRegistryValue) (_ *integrationRegistry, result error) {
	descriptor, err := windows.SecurityDescriptorFromString(integrationRegistrySDDL)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var handle windows.Handle
	var disposition uint32
	status, _, _ := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyExW").Call(uintptr(parent), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, uintptr(registry.ALL_ACCESS|registry.WOW64_64KEY), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&disposition)))
	if status != 0 {
		return nil, windows.Errno(status)
	}
	key := &integrationRegistry{registry.Key(handle)}
	if disposition != 1 {
		return nil, errors.Join(errors.New("preserve existing Windows uninstall registration"), key.Close())
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, key.retire(), key.Close())
		}
	}()
	if parent == registry.LOCAL_MACHINE && path == integrationUninstallKey {
		if err := integrationRegistryNativeName(key.Key); err != nil {
			return nil, err
		}
	}
	for name, value := range values {
		if err := integrationSetRegistryValue(key.Key, name, value); err != nil {
			return nil, err
		}
	}
	if err := integrationInspectRegistry(key.Key, values); err != nil {
		return nil, err
	}
	return key, nil
}

// integrationAdmitRegistry opens the fixed machine view and validates every value before use.
func integrationAdmitRegistry(values map[string]integrationRegistryValue) (*integrationRegistry, error) {
	pointer, _ := windows.UTF16PtrFromString(integrationUninstallKey)
	var handle windows.Handle
	// REG_OPTION_OPEN_LINK ensures an unexpected symbolic key is inspected rather than followed.
	if err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, pointer, 8, registry.ALL_ACCESS|registry.WOW64_64KEY, &handle); err != nil {
		return nil, err
	}
	key := &integrationRegistry{registry.Key(handle)}
	if err := integrationRegistryNativeName(key.Key); err != nil {
		return nil, errors.Join(err, key.Close())
	}
	if err := integrationInspectRegistry(key.Key, values); err != nil {
		return nil, errors.Join(err, key.Close())
	}
	return key, nil
}

// integrationInspectRegistry rejects ACL drift, child keys, foreign values and native type differences.
func integrationInspectRegistry(key registry.Key, values map[string]integrationRegistryValue) error {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	expected, err := windows.SecurityDescriptorFromString(integrationRegistrySDDL)
	if err != nil {
		return err
	}
	if err := compareIntegrationSecurity(descriptor, expected); err != nil {
		return errors.New("Windows uninstall registration owner or ACL changed")
	}
	information, err := key.Stat()
	if err != nil {
		return err
	}
	if information.SubKeyCount != 0 || int(information.ValueCount) != len(values) {
		return errors.New("Windows uninstall registration contains foreign entries")
	}
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		expected, ok := values[name]
		if !ok {
			return fmt.Errorf("foreign uninstall value %q", name)
		}
		if len(expected.Data) > 65536 {
			return errors.New("Windows uninstall value exceeds bound")
		}
		data := make([]byte, len(expected.Data))
		length, kind, err := key.GetValue(name, data)
		if err != nil {
			return err
		}
		if kind != expected.Type || length != len(data) || !bytes.Equal(data, expected.Data) {
			return fmt.Errorf("Windows uninstall value %q differs", name)
		}
	}
	return nil
}

// retire deletes only the retained key and refuses nonempty child namespaces through NtDeleteKey.
func (key *integrationRegistry) retire() error {
	status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtDeleteKey").Call(uintptr(key.Key))
	if int32(status) < 0 {
		return windows.NTStatus(status)
	}
	return nil
}

// Close releases registry authority after the machine operation settles.
func (key *integrationRegistry) Close() error { return key.Key.Close() }

// integrationRegistryNativeName rejects redirected registry ancestors before machine registration is trusted.
func integrationRegistryNativeName(key registry.Key) error {
	return integrationRegistryCheckNativeName(key, `\REGISTRY\MACHINE\`+integrationUninstallKey)
}

// integrationRegistryCheckNativeName rejects registry symbolic-link or view redirection.
func integrationRegistryCheckNativeName(key registry.Key, expected string) error {
	buffer := make([]byte, 32768)
	var size uint32
	status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryKey").Call(uintptr(key), 3, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&size)))
	if int32(status) < 0 {
		return windows.NTStatus(status)
	}
	length := binary.LittleEndian.Uint32(buffer)
	if length%2 != 0 || length > uint32(len(buffer)-4) {
		return errors.New("invalid registry native identity")
	}
	units := make([]uint16, length/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(buffer[4+index*2:])
	}
	if !strings.EqualFold(windows.UTF16ToString(units), expected) {
		return errors.New("Windows uninstall registry native path differs")
	}
	return nil
}

// integrationSetRegistryValue writes exact native bytes without registry string normalization.
func integrationSetRegistryValue(key registry.Key, name string, value integrationRegistryValue) error {
	pointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if len(value.Data) == 0 || len(value.Data) > 65536 {
		return errors.New("Windows uninstall value outside bound")
	}
	status, _, _ := windows.NewLazySystemDLL("advapi32.dll").NewProc("RegSetValueExW").Call(uintptr(key), uintptr(unsafe.Pointer(pointer)), 0, uintptr(value.Type), uintptr(unsafe.Pointer(&value.Data[0])), uintptr(len(value.Data)))
	if status != 0 {
		return windows.Errno(status)
	}
	return nil
}

// integrationRegistryAdmitParent rejects ordinary-user mutation of the fixed machine registration parent.
func integrationRegistryAdmitParent(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || (owner.String() != administratorsSID && owner.String() != systemSID && owner.String() != "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464") {
		return errors.New("Windows uninstall parent has foreign ownership")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("Windows uninstall parent has a null DACL")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows uninstall parent has an unsupported ACL entry")
		}
		grant := integrationACE{SID: (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(), Mask: uint32(ace.Mask), Flags: ace.Header.AceFlags}
		if grant.Flags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if grant.SID == administratorsSID || grant.SID == systemSID || grant.SID == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" {
			continue
		}
		if grant.Mask&(windows.GENERIC_ALL|windows.GENERIC_WRITE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.DELETE|registry.SET_VALUE|registry.CREATE_SUB_KEY|registry.CREATE_LINK) != 0 {
			return errors.New("Windows uninstall parent allows foreign mutation")
		}
	}
	return nil
}
