//go:build windows

package windowsinstallation

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// integrationObject binds a machine projection to its retained native filesystem identity.
type integrationObject struct {
	Path    string `json:"path"`
	Volume  uint64 `json:"volume"`
	ID      string `json:"id"`
	SDDL    string `json:"sddl"`
	Created bool   `json:"created"`
}

// integrationFileID preserves the complete NTFS or ReFS identity when projections are retired.
type integrationFileID struct {
	Volume uint64
	ID     [16]byte
}

// integrationDirectory keeps deletion authority separate from pathname observations.
type integrationDirectory struct {
	Object integrationObject
	File   *os.File
}

// privilegedSpool retains every admitted ancestor until the complete machine operation settles.
type privilegedSpool struct {
	Directories []integrationDirectory
	Anchor      *os.File
}

// integrationSpoolPlan fixes the first-user helper topology without consulting caller environment values.
func integrationSpoolPlan(programData, userSID string) []integrationObject {
	machine := "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"
	shared := machine + "(A;OICI;0x1200a9;;;BU)"
	gateway := machine + "(A;OICI;0x1200a0;;;" + userSID + ")"
	pending := "O:" + userSID + "D:P(A;OICI;FA;;;" + userSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"
	base := filepath.Join(programData, "GoForj", "Harbor")
	root := filepath.Join(base, "Privileged")
	return []integrationObject{
		{Path: filepath.Join(programData, "GoForj"), SDDL: shared},
		{Path: base, SDDL: shared},
		{Path: root, SDDL: gateway},
		{Path: filepath.Join(root, "tickets"), SDDL: gateway},
		{Path: filepath.Join(root, "tickets", "pending"), SDDL: pending},
		{Path: filepath.Join(root, "tickets", "claims"), SDDL: machine},
		{Path: filepath.Join(root, "state"), SDDL: machine},
		{Path: filepath.Join(root, "state", "replay"), SDDL: machine},
	}
}

// preparePrivilegedSpool creates protected descriptors at birth and refuses an unclaimed privileged namespace.
func preparePrivilegedSpool(programData, userSID string) (_ *privilegedSpool, result error) {
	if !validUserSID(userSID) {
		return nil, errors.New("Windows privileged spool requires the intended account SID")
	}
	anchor, err := openIntegrationAnchor(programData)
	if err != nil {
		return nil, err
	}
	spool := &privilegedSpool{Anchor: anchor}
	defer func() {
		if result != nil {
			result = errors.Join(result, spool.rollback(), spool.Close())
		}
	}()
	if err := admitProgramDataAnchor(windows.Handle(anchor.Fd())); err != nil {
		return nil, err
	}
	for index, object := range integrationSpoolPlan(programData, userSID) {
		path, err := windows.UTF16PtrFromString(object.Path)
		if err != nil {
			return nil, err
		}
		descriptor, err := windows.SecurityDescriptorFromString(object.SDDL)
		if err != nil {
			return nil, err
		}
		attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
		err = windows.CreateDirectory(path, &attributes)
		object.Created = err == nil
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, fmt.Errorf("create protected spool directory: %w", err)
		}
		if !object.Created && index >= 2 {
			return nil, errors.New("preserve an unclaimed Windows privileged spool; only an exact installation receipt can admit repair")
		}
		file, err := openIntegrationDirectory(object.Path)
		if err != nil {
			return nil, err
		}
		// Keep a newly created object even when later admission fails, so rollback never guesses its replacement.
		spool.Directories = append(spool.Directories, integrationDirectory{Object: object, File: file})
		observed, err := inspectIntegrationObject(file, object)
		if err != nil {
			return nil, err
		}
		spool.Directories[len(spool.Directories)-1].Object = observed
	}
	return spool, nil
}

// admitPrivilegedSpool refuses descriptor or native-identity drift before a repair or removal uses the projection.
func admitPrivilegedSpool(programData, userSID string, objects []integrationObject) (_ *privilegedSpool, result error) {
	if !validUserSID(userSID) {
		return nil, errors.New("Windows privileged spool requires the intended account SID")
	}
	plan := integrationSpoolPlan(programData, userSID)
	if len(objects) != len(plan) {
		return nil, errors.New("Windows privileged spool receipt has a different topology")
	}
	anchor, err := openIntegrationAnchor(programData)
	if err != nil {
		return nil, err
	}
	spool := &privilegedSpool{Anchor: anchor}
	defer func() {
		if result != nil {
			result = errors.Join(result, spool.Close())
		}
	}()
	if err := admitProgramDataAnchor(windows.Handle(anchor.Fd())); err != nil {
		return nil, err
	}
	for index, expected := range objects {
		if expected.Path != plan[index].Path || expected.SDDL != plan[index].SDDL || expected.ID == "" {
			return nil, errors.New("Windows privileged spool receipt differs from its compiled projection")
		}
		file, err := openIntegrationDirectory(expected.Path)
		if err != nil {
			return nil, err
		}
		spool.Directories = append(spool.Directories, integrationDirectory{Object: expected, File: file})
		observed, err := inspectIntegrationObject(file, expected)
		if err != nil {
			return nil, err
		}
		if observed != expected {
			return nil, errors.New("Windows privileged spool directory native identity changed")
		}
	}
	return spool, nil
}

// openIntegrationAnchor retains the system directory without requesting deletion or blocking unrelated writes.
func openIntegrationAnchor(path string) (*os.File, error) {
	return openIntegrationDirectoryWithRights(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
}

// openIntegrationDirectory retains deletion rights without allowing another opener to replace the directory.
func openIntegrationDirectory(path string) (*os.File, error) {
	return openIntegrationDirectoryWithRights(path, windows.DELETE|windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ)
}

// openIntegrationDirectoryWithRights rejects filesystem aliases before a retained machine projection is used.
func openIntegrationDirectoryWithRights(path string, access, sharing uint32) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("Windows machine projection path is not canonical")
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, access,
		sharing, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, fmt.Errorf("retain Windows machine directory: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, errors.Join(errors.New("Windows machine directory has an alias or type conflict"), file.Close())
	}
	final, err := finalIntegrationPath(handle)
	if err != nil || !strings.EqualFold(final, `\\?\`+path) {
		return nil, errors.Join(errors.New("Windows machine directory native path differs"), err, file.Close())
	}
	return file, nil
}

// finalIntegrationPath bounds native path observations without admitting short-name or reparse aliases.
func finalIntegrationPath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("Windows machine projection path exceeds its bound")
	}
	return windows.UTF16ToString(buffer[:length]), nil
}

// inspectIntegrationObject commits all native identity bits and the exact descriptor selected at creation.
func inspectIntegrationObject(file *os.File, object integrationObject) (integrationObject, error) {
	handle := windows.Handle(file.Fd())
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return object, err
	}
	expected, err := windows.SecurityDescriptorFromString(object.SDDL)
	if err != nil {
		return object, err
	}
	if err := compareIntegrationSecurity(descriptor, expected); err != nil {
		return object, fmt.Errorf("Windows machine directory owner or ACL differs from its exact plan %s: %w", object.Path, err)
	}
	var identity integrationFileID
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&identity)), uint32(unsafe.Sizeof(identity))); err != nil {
		return object, err
	}
	object.Volume = identity.Volume
	object.ID = hex.EncodeToString(identity.ID[:])
	return object, nil
}

// admitProgramDataAnchor permits ordinary child creation but denies replacement of machine-owned children.
func admitProgramDataAnchor(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || (owner.String() != administratorsSID && owner.String() != systemSID && owner.String() != "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464") {
		return errors.New("Windows ProgramData anchor has foreign ownership")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("Windows ProgramData anchor has a null DACL")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows ProgramData anchor has an unsupported ACL entry")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid == administratorsSID || sid == systemSID || sid == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" {
			continue
		}
		if ace.Mask&(windows.GENERIC_ALL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.DELETE|0x40) != 0 {
			return errors.New("Windows ProgramData anchor permits foreign child replacement")
		}
	}
	return nil
}

// rollback retires only created exact handles, preserving preexisting shared ancestors and nonempty directories.
func (spool *privilegedSpool) rollback() error {
	var result error
	for index := len(spool.Directories) - 1; index >= 0; index-- {
		directory := &spool.Directories[index]
		if !directory.Object.Created {
			continue
		}
		if err := retireIntegrationFile(directory.File); err != nil {
			result = errors.Join(result, err)
			continue
		}
		result = errors.Join(result, directory.File.Close())
		directory.File = nil
	}
	return result
}

// retireIntegrationFile requests deletion through the held identity and never recursively removes contents.
func retireIntegrationFile(file *os.File) error {
	remove := byte(1)
	return windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, &remove, 1)
}

// Close releases held projections without deleting admitted state.
func (spool *privilegedSpool) Close() error {
	var result error
	for index := len(spool.Directories) - 1; index >= 0; index-- {
		if spool.Directories[index].File != nil {
			result = errors.Join(result, spool.Directories[index].File.Close())
		}
	}
	return errors.Join(result, spool.Anchor.Close())
}

// integrationACE keeps only native access semantics, independent of descriptor string serialization.
type integrationACE struct {
	SID   string
	Mask  uint32
	Flags uint8
}

// compareIntegrationSecurity tolerates descriptor bookkeeping but requires exact owner and protected explicit grants.
func compareIntegrationSecurity(actual, expected *windows.SECURITY_DESCRIPTOR) error {
	actualOwner, _, err := actual.Owner()
	if err != nil {
		return err
	}
	expectedOwner, _, err := expected.Owner()
	if err != nil {
		return err
	}
	if actualOwner == nil || expectedOwner == nil || actualOwner.String() != expectedOwner.String() {
		return errors.New("foreign machine projection owner")
	}
	control, _, err := actual.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("machine projection DACL is not protected")
	}
	want, err := integrationACEs(expected)
	if err != nil {
		return err
	}
	got, err := integrationACEs(actual)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return errors.New("machine projection has additional or missing grants")
	}
	for grant := range want {
		if _, ok := got[grant]; !ok {
			return errors.New("machine projection grant differs")
		}
	}
	return nil
}

// integrationACEs rejects non-allow entries and duplicates before exact grant comparison.
func integrationACEs(descriptor *windows.SECURITY_DESCRIPTOR) (map[integrationACE]struct{}, error) {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, err
	}
	if dacl == nil {
		return nil, errors.New("machine projection has a null DACL")
	}
	result := make(map[integrationACE]struct{}, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return nil, errors.New("machine projection has a non-allow grant")
		}
		grant := integrationACE{SID: (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(), Mask: ace.Mask, Flags: ace.Header.AceFlags}
		if _, exists := result[grant]; exists {
			return nil, errors.New("machine projection has duplicate grants")
		}
		result[grant] = struct{}{}
	}
	return result, nil
}
