//go:build windows

package replaystore

import (
	"errors"
	"fmt"
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
)

var reopenReplayFileProcedure = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// createPlatformFile applies the machine DACL in FILE_CREATE so no interactive-owner window exists.
func createPlatformFile(root *os.Root, directoryPath string, name string) (*os.File, error) {
	descriptor, _, err := replayMachineDescriptor(false)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open retained replay directory: %w", err)
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("encode replay tombstone name: %w", err), directory.Close())
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(directory.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_EA,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		0,
		0,
	)
	closeErr := directory.Close()
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(objectName)
	if err != nil {
		return nil, errors.Join(
			&os.PathError{Op: "open", Path: filepath.Join(directoryPath, name), Err: windowsCreateError(err)},
			closeErr,
		)
	}
	if closeErr != nil {
		return nil, errors.Join(fmt.Errorf("close retained replay directory: %w", closeErr), windows.CloseHandle(handle))
	}
	return os.NewFile(uintptr(handle), name), nil
}

// windowsCreateError preserves ordinary exclusive-create classifications across the native API boundary.
func windowsCreateError(err error) error {
	status, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	if status == windows.STATUS_OBJECT_NAME_COLLISION {
		return windows.ERROR_FILE_EXISTS
	}
	return status.Errno()
}

// validatePlatformDirectory requires the machine-wide Administrators and LocalSystem boundary.
func validatePlatformDirectory(path string, _ os.FileInfo, _ uint32) error {
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

// validatePlatformRoot proves the retained handle itself has the machine boundary after all path lookups finish.
func validatePlatformRoot(root *os.Root, _ uint32) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	validateErr := validateWindowsObject(directory, true)
	closeErr := directory.Close()
	return errors.Join(validateErr, closeErr)
}

// securePlatformFile excludes the interactive SID before a tombstone can authorize privileged mutation.
func securePlatformFile(file *os.File) error {
	descriptor, owner, err := replayMachineDescriptor(false)
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

// validatePlatformFile rejects reparse points, hard links, and access outside the machine principals.
func validatePlatformFile(file *os.File, _ os.FileInfo, _ uint32) error {
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
	return validateWindowsDescriptor(descriptor, directory)
}

// replayMachineDescriptor grants full access only to Administrators and LocalSystem.
// A machine principal owns the object because a UAC-filtered process must not recover WRITE_DAC through its stable user SID.
func replayMachineDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	owner, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve Windows Administrators SID: %w", err)
	}
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
		return nil, nil, fmt.Errorf("build machine replay security descriptor: %w", err)
	}
	return descriptor, owner, nil
}

// validateWindowsDescriptor requires a protected machine owner before inspecting individual grants.
func validateWindowsDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read replay DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("replay Windows DACL is not protected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read replay object owner: %w", err)
	}
	if owner == nil || owner.String() != windowsAdministratorsSID {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("replay object owner is %q, want Windows Administrators %q", got, windowsAdministratorsSID)
	}
	return validateWindowsDACL(descriptor, directory)
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
func validateWindowsDACL(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read replay access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		return fmt.Errorf("replay Windows DACL must contain exactly two entries")
	}
	want := map[string]bool{windowsAdministratorsSID: false, windowsSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read replay DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("replay DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != windowsFileAllAccess {
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
