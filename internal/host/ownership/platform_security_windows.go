//go:build windows

package ownership

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsAdministratorsSID = "S-1-5-32-544"
	windowsSystemSID         = "S-1-5-18"
	windowsFileAllAccess     = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

var reopenOwnershipFileProcedure = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// validatePlatformDirectory rejects reparse points and requires the elevated Administrators boundary plus LocalSystem.
func validatePlatformDirectory(path string, _ os.FileInfo) error {
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("directory is a Windows reparse point")
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	validateErr := validatePlatformFile(directory, nil, true)
	closeErr := directory.Close()
	return errors.Join(validateErr, closeErr)
}

// securePlatformFile replaces inherited access on the exact created handle before publication.
func securePlatformFile(file *os.File, directory bool) error {
	descriptor, owner, err := windowsOwnershipDescriptor(directory)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read machine ownership Windows DACL: %w", err)
	}
	currentDescriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read existing machine ownership Windows owner: %w", err)
	}
	currentOwner, _, err := currentDescriptor.Owner()
	if err != nil {
		return fmt.Errorf("decode existing machine ownership Windows owner: %w", err)
	}
	securityInformation, ownerAccess := windowsOwnershipSecurityUpdate(currentOwner, owner)
	securityHandle, err := reopenOwnershipSecurityHandle(file, directory, ownerAccess)
	if err != nil {
		return err
	}
	ownerToApply := owner
	if ownerAccess == 0 {
		ownerToApply = nil
	}
	err = windows.SetSecurityInfo(
		securityHandle,
		windows.SE_FILE_OBJECT,
		securityInformation,
		ownerToApply,
		nil,
		dacl,
		nil,
	)
	closeErr := windows.CloseHandle(securityHandle)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(file)
	if err := errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("apply machine ownership Windows DACL: %w", err)
	}
	return nil
}

// windowsOwnershipSecurityUpdate avoids privileged owner mutation when the object already has the machine owner.
func windowsOwnershipSecurityUpdate(currentOwner, desiredOwner *windows.SID) (windows.SECURITY_INFORMATION, uint32) {
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if currentOwner != nil && currentOwner.Equals(desiredOwner) {
		return information, 0
	}
	return information | windows.OWNER_SECURITY_INFORMATION, windows.WRITE_OWNER
}

// validatePlatformFile rejects reparse points, hard links, and grants beyond Administrators and LocalSystem.
func validatePlatformFile(file *os.File, _ os.FileInfo, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("opened machine ownership path has the wrong object type")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read machine ownership Windows file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("machine ownership path is a Windows reparse point")
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("machine ownership file has %d hard links, want 1", information.NumberOfLinks)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read machine ownership Windows security descriptor: %w", err)
	}
	return validateWindowsSecurityDescriptor(descriptor, directory)
}

// validateWindowsSecurityDescriptor requires an exact protected Administrators and LocalSystem boundary.
func validateWindowsSecurityDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read machine ownership Windows DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("machine ownership Windows DACL is not protected")
	}
	wantOwner, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		return fmt.Errorf("resolve machine ownership Windows Administrators SID: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read machine ownership Windows object owner: %w", err)
	}
	if owner == nil || owner.String() != wantOwner.String() {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("machine ownership Windows object owner is %q, want %q", got, wantOwner.String())
	}
	return validateWindowsDACL(descriptor, wantOwner.String(), directory)
}

// windowsOwnershipDescriptor keeps the filtered token for the same interactive SID outside privileged machine state.
func windowsOwnershipDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
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
		return nil, nil, fmt.Errorf("build machine ownership Windows security descriptor: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return nil, nil, fmt.Errorf("read machine ownership Windows descriptor owner: %w", err)
	}
	if owner == nil || owner.String() != windowsAdministratorsSID {
		return nil, nil, fmt.Errorf("machine ownership Windows descriptor owner is not Administrators")
	}
	return descriptor, owner, nil
}

// reopenOwnershipSecurityHandle derives only the security-authoring access required by the pending update.
func reopenOwnershipSecurityHandle(file *os.File, directory bool, ownerAccess uint32) (windows.Handle, error) {
	flags := uintptr(0)
	if directory {
		flags = windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, _, callErr := reopenOwnershipFileProcedure.Call(
		file.Fd(),
		uintptr(windows.READ_CONTROL|windows.WRITE_DAC|ownerAccess),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		flags,
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		return windows.InvalidHandle, fmt.Errorf("reopen machine ownership object for DACL protection: %w", callErr)
	}
	return windows.Handle(handle), nil
}

// validateWindowsDACL rejects inherited, additional, denied, duplicated, or weakened grants.
func validateWindowsDACL(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID string, directory bool) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read machine ownership Windows access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("machine ownership Windows DACL has %d entries, want 2", count)
	}
	want := map[string]bool{ownerSID: false, windowsSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read machine ownership Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("machine ownership Windows DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != windowsFileAllAccess {
			return fmt.Errorf("machine ownership Windows DACL entry %d has access mask %#x, want %#x", index, ace.Mask, windowsFileAllAccess)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return fmt.Errorf("machine ownership Windows DACL entry %d has flags %#x, want %#x", index, ace.Header.AceFlags, wantFlags)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := sid.String()
		seen, exists := want[principal]
		if !exists || seen {
			return fmt.Errorf("machine ownership Windows DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("machine ownership Windows DACL does not grant SID %q", principal)
		}
	}
	return nil
}
