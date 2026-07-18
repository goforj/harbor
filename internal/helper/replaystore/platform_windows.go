//go:build windows

package replaystore

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsSystemSID     = "S-1-5-18"
	windowsFileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

var reopenReplayFileProcedure = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// validatePlatformDirectory requires the exact elevated user and LocalSystem protected DACL.
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
	validateErr := validateWindowsObject(directory, true)
	closeErr := directory.Close()
	return errors.Join(validateErr, closeErr)
}

// securePlatformFile replaces inherited access with the exact private helper DACL on the opened tombstone.
func securePlatformFile(file *os.File) error {
	descriptor, owner, err := replayPrivateDescriptor()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private replay DACL: %w", err)
	}
	handle, err := reopenReplaySecurityHandle(file)
	if err != nil {
		return err
	}
	err = windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
	closeErr := windows.CloseHandle(handle)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(file)
	if err := errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("apply private replay DACL: %w", err)
	}
	return nil
}

// validatePlatformFile rejects reparse points, hard links, and grants beyond the helper user and LocalSystem.
func validatePlatformFile(file *os.File, _ os.FileInfo) error {
	return validateWindowsObject(file, false)
}

// platformSyncDirectory admits the two expected Windows directory-flush limitations after file durability succeeds.
func platformSyncDirectory(directory *os.File) error {
	err := windows.FlushFileBuffers(windows.Handle(directory.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}

// validateWindowsObject proves an opened object retains Harbor's exact protected filesystem boundary.
func validateWindowsObject(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("opened replay path has the wrong object type")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read replay file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("replay path is a Windows reparse point")
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("replay tombstone has %d hard links, want 1", information.NumberOfLinks)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read replay security descriptor: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read replay DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("replay Windows DACL is not protected")
	}
	wantOwner, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read replay object owner: %w", err)
	}
	if owner == nil || owner.String() != wantOwner.String() {
		return fmt.Errorf("replay object owner does not match the helper user")
	}
	return validateWindowsDACL(descriptor, wantOwner.String(), directory)
}

// replayPrivateDescriptor grants full access only to the elevated interactive user and LocalSystem.
func replayPrivateDescriptor() (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	owner, err := currentWindowsUserSID()
	if err != nil {
		return nil, nil, err
	}
	if owner.String() == windowsSystemSID {
		return nil, nil, fmt.Errorf("helper replay storage cannot be owned by Windows LocalSystem")
	}
	sddl := fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)", owner.String(), owner.String(), windowsSystemSID)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build private replay security descriptor: %w", err)
	}
	return descriptor, owner, nil
}

// currentWindowsUserSID resolves the elevated interactive identity that owns replay protection.
func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// reopenReplaySecurityHandle derives security-authoring access from the exact created tombstone.
func reopenReplaySecurityHandle(file *os.File) (windows.Handle, error) {
	handle, _, callErr := reopenReplayFileProcedure.Call(
		file.Fd(),
		uintptr(windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		0,
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		return windows.InvalidHandle, fmt.Errorf("reopen replay tombstone for DACL protection: %w", callErr)
	}
	return windows.Handle(handle), nil
}

// validateWindowsDACL rejects inherited, additional, denied, or weakened replay-store grants.
func validateWindowsDACL(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID string, directory bool) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read replay access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		return fmt.Errorf("replay Windows DACL must contain exactly two entries")
	}
	want := map[string]bool{ownerSID: false, windowsSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read replay DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("replay DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != windowsFileAllAccess && ace.Mask != windows.GENERIC_ALL {
			return fmt.Errorf("replay DACL entry %d has access mask %#x", index, ace.Mask)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return fmt.Errorf("replay DACL entry %d has flags %#x, want %#x", index, ace.Header.AceFlags, wantFlags)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := sid.String()
		seen, exists := want[principal]
		if !exists || seen {
			return fmt.Errorf("replay DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("replay DACL does not grant SID %q", principal)
		}
	}
	return nil
}
