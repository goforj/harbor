//go:build windows

package ticketredeemer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsAdministratorsSID = "S-1-5-32-544"
	windowsSystemSID         = "S-1-5-18"
	windowsFileAllAccess     = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	windowsFileExecuteAccess = windows.STANDARD_RIGHTS_EXECUTE | windows.SYNCHRONIZE | windows.FILE_READ_ATTRIBUTES | windows.FILE_EXECUTE
)

// windowsFileRenameInformation is the variable-width handle-relative rename record accepted by NtSetInformationFile.
type windowsFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// windowsACEPolicy defines one exact principal grant in a protected DACL.
type windowsACEPolicy struct {
	mask  windows.ACCESS_MASK
	flags uint8
}

// validatePlatformProcessAdmission requires a high UAC token with active Administrators membership.
func validatePlatformProcessAdmission() error {
	token := windows.GetCurrentProcessToken()
	if !token.IsElevated() {
		return errors.New("privileged helper Windows token is not elevated")
	}
	administrators, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		return fmt.Errorf("resolve Windows Administrators SID: %w", err)
	}
	member, err := token.IsMember(administrators)
	if err != nil {
		return fmt.Errorf("inspect Windows Administrators membership: %w", err)
	}
	if !member {
		return errors.New("privileged helper Windows token is not an active Administrators member")
	}
	return nil
}

// openPlatformRootDirectory opens the fixed absolute root as a reparse point rather than traversing one.
func openPlatformRootDirectory(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// openPlatformDirectory resolves one direct directory through the retained parent handle.
func openPlatformDirectory(parent *os.File, parentPath string, name string) (*os.File, error) {
	return openWindowsRelative(
		parent,
		parentPath,
		name,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_DIRECTORY_FILE,
	)
}

// openPlatformFile resolves one direct file with the security and rename rights needed after claim.
func openPlatformFile(parent *os.File, parentPath string, name string) (*os.File, error) {
	return openWindowsRelative(
		parent,
		parentPath,
		name,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_NON_DIRECTORY_FILE,
	)
}

// openWindowsRelative performs one non-traversing NT open against a retained directory handle.
func openWindowsRelative(parent *os.File, parentPath string, name string, access uint32, kindOptions uint32) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT|kindOptions,
		0,
		0,
	)
	runtime.KeepAlive(objectName)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(parentPath, name), Err: windowsNativeError(err)}
	}
	return os.NewFile(uintptr(handle), name), nil
}

// platformEntryExists opens the direct object itself so a reparse point cannot redirect classification.
func platformEntryExists(parent *os.File, parentPath string, name string) (bool, error) {
	file, err := openWindowsRelative(
		parent,
		parentPath,
		name,
		windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES,
		0,
	)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, nil
}

// validatePlatformGatewayDirectory requires machine ownership and only traversal for the admitted interactive SID.
func validatePlatformGatewayDirectory(file *os.File, requesterIdentity string) error {
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	return validateWindowsObject(file, true, windowsAdministratorsSID, map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
		windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
		requesterIdentity:        {mask: windowsFileExecuteAccess, flags: inherit},
	})
}

// platformPendingIdentity authenticates the same linked-token user through the pending directory owner and DACL.
func platformPendingIdentity(file *os.File) (string, error) {
	requester, err := currentWindowsUserSID()
	if err != nil {
		return "", err
	}
	identity := requester.String()
	if identity == windowsAdministratorsSID || identity == windowsSystemSID {
		return "", errors.New("pending ticket owner must be a distinct interactive Windows SID")
	}
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	err = validateWindowsObject(file, true, identity, map[string]windowsACEPolicy{
		identity:                 {mask: windowsFileAllAccess, flags: inherit},
		windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
		windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
	})
	if err != nil {
		return "", err
	}
	return identity, nil
}

// validatePlatformMachineDirectory requires the exact Administrators and LocalSystem policy.
func validatePlatformMachineDirectory(file *os.File) error {
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	return validateWindowsObject(file, true, windowsAdministratorsSID, map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
		windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
	})
}

// validatePlatformPendingFile binds a direct regular object to the admitted interactive SID policy.
func validatePlatformPendingFile(file *os.File, requesterIdentity string) error {
	return validateWindowsObject(file, false, requesterIdentity, map[string]windowsACEPolicy{
		requesterIdentity:        {mask: windowsFileAllAccess},
		windowsAdministratorsSID: {mask: windowsFileAllAccess},
		windowsSystemSID:         {mask: windowsFileAllAccess},
	})
}

// validatePlatformMachineFile requires the claimed file to exclude the interactive SID completely.
func validatePlatformMachineFile(file *os.File) error {
	return validateWindowsObject(file, false, windowsAdministratorsSID, map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess},
		windowsSystemSID:         {mask: windowsFileAllAccess},
	})
}

// securePlatformClaim applies the exact machine descriptor through the already opened claimed handle.
// Existing same-SID handles survive DACL replacement, so later signature checks limit that race to failure or another daemon-signed ticket.
func securePlatformClaim(file *os.File) error {
	descriptor, owner, err := windowsMachineDescriptor(false)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read claimed ticket machine DACL: %w", err)
	}
	err = windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return fmt.Errorf("apply claimed ticket machine DACL: %w", err)
	}
	return nil
}

// syncPlatformDirectory accepts only the documented Windows directory-flush limitations.
func syncPlatformDirectory(directory *os.File) error {
	err := windows.FlushFileBuffers(windows.Handle(directory.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}

// validatePlatformTopology requires the atomic claim directories to live on one Windows volume.
func validatePlatformTopology(_ *os.File, pending *os.File, claims *os.File) error {
	pendingInfo, err := windowsHandleInformation(pending)
	if err != nil {
		return err
	}
	claimsInfo, err := windowsHandleInformation(claims)
	if err != nil {
		return err
	}
	if pendingInfo.VolumeSerialNumber != claimsInfo.VolumeSerialNumber {
		return errors.New("pending and claimed ticket directories are on different volumes")
	}
	return nil
}

// renamePlatformNoReplace moves the already opened source handle relative to protected claims.
func renamePlatformNoReplace(_ *os.File, claims *os.File, sourceFile *os.File, _ string, destination string) (bool, error) {
	renameBuffer, err := windowsRenameBuffer(windows.Handle(claims.Fd()), destination)
	if err != nil {
		return false, err
	}
	err = windows.NtSetInformationFile(
		windows.Handle(sourceFile.Fd()),
		&windows.IO_STATUS_BLOCK{},
		&renameBuffer[0],
		uint32(len(renameBuffer)),
		windows.FileRenameInformation,
	)
	runtime.KeepAlive(renameBuffer)
	if err != nil {
		err = windowsNativeError(err)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return false, fs.ErrExist
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, fs.ErrNotExist
		}
		return false, err
	}
	return true, nil
}

// validateWindowsObject proves type, reparse, link, owner, and protected-DACL policy from one retained handle.
func validateWindowsObject(file *os.File, directory bool, ownerSID string, policy map[string]windowsACEPolicy) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("opened ticket spool object has the wrong type")
	}
	nativeInfo, err := windowsHandleInformation(file)
	if err != nil {
		return err
	}
	if nativeInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("ticket spool object is a Windows reparse point")
	}
	if !directory && nativeInfo.NumberOfLinks != 1 {
		return fmt.Errorf("ticket spool file has %d hard links, want 1", nativeInfo.NumberOfLinks)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read ticket spool security descriptor: %w", err)
	}
	return validateWindowsDescriptor(descriptor, ownerSID, policy)
}

// validateWindowsDescriptor rejects inheritance, aliases, extra principals, and weakened exact grants.
func validateWindowsDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID string, policy map[string]windowsACEPolicy) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read ticket spool DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("ticket spool Windows DACL is not protected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read ticket spool object owner: %w", err)
	}
	if owner == nil || owner.String() != ownerSID {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("ticket spool Windows owner is %q, want %q", got, ownerSID)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read ticket spool Windows access list: %w", err)
	}
	if dacl == nil || int(dacl.AceCount) != len(policy) {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("ticket spool Windows DACL has %d entries, want %d", count, len(policy))
	}
	seen := make(map[string]bool, len(policy))
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read ticket spool Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("ticket spool Windows DACL entry %d is not an allow entry", index)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		want, exists := policy[principal]
		if !exists || seen[principal] {
			return fmt.Errorf("ticket spool Windows DACL grants unexpected or duplicate SID %q", principal)
		}
		if ace.Mask != want.mask || ace.Header.AceFlags != want.flags {
			return fmt.Errorf(
				"ticket spool Windows DACL entry %d for %q has mask %#x flags %#x, want mask %#x flags %#x",
				index,
				principal,
				ace.Mask,
				ace.Header.AceFlags,
				want.mask,
				want.flags,
			)
		}
		seen[principal] = true
	}
	for principal := range policy {
		if !seen[principal] {
			return fmt.Errorf("ticket spool Windows DACL does not grant SID %q", principal)
		}
	}
	return nil
}

// windowsMachineDescriptor builds the exact policy that permanently retains consumed claims.
func windowsMachineDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%sD:P(A;%s;FA;;;%s)(A;%s;FA;;;%s)",
		windowsAdministratorsSID,
		inheritance,
		windowsAdministratorsSID,
		inheritance,
		windowsSystemSID,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build claimed ticket machine descriptor: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return nil, nil, fmt.Errorf("read claimed ticket machine owner: %w", err)
	}
	return descriptor, owner, nil
}

// currentWindowsUserSID returns the linked-token user whose owner-only pending directory carries admission.
func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// windowsHandleInformation returns stable volume, link, and reparse metadata for one opened object.
func windowsHandleInformation(file *os.File) (windows.ByHandleFileInformation, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return windows.ByHandleFileInformation{}, fmt.Errorf("read ticket spool file information: %w", err)
	}
	return information, nil
}

// windowsRenameBuffer encodes one canonical reference relative to the retained claims handle.
func windowsRenameBuffer(directory windows.Handle, destination string) ([]byte, error) {
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return nil, err
	}
	name = name[:len(name)-1]
	var layout windowsFileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.FileName))+len(name)*2)
	information := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.RootDirectory = directory
	information.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&information.FileName[0], len(name)), name)
	return buffer, nil
}

// windowsNativeError converts native statuses into errors.Is-compatible filesystem outcomes.
func windowsNativeError(err error) error {
	status, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	switch status {
	case windows.STATUS_OBJECT_NAME_COLLISION, windows.STATUS_OBJECT_NAME_EXISTS:
		return windows.ERROR_FILE_EXISTS
	case windows.STATUS_OBJECT_NAME_NOT_FOUND:
		return windows.ERROR_FILE_NOT_FOUND
	case windows.STATUS_OBJECT_PATH_NOT_FOUND:
		return windows.ERROR_PATH_NOT_FOUND
	default:
		return status.Errno()
	}
}
