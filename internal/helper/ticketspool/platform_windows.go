//go:build windows

package ticketspool

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
)

// windowsFileRenameInformation is the variable-width no-replace rename buffer accepted by NtSetInformationFile.
type windowsFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// createPlatformFile applies the final user, Administrators, and LocalSystem DACL in FILE_CREATE itself.
func createPlatformFile(_ *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
	descriptor, _, err := windowsPendingDescriptor(false)
	if err != nil {
		return nil, err
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, fmt.Errorf("encode staged helper ticket name: %w", err)
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
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH,
		0,
		0,
	)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(objectName)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(directoryPath, name), Err: windowsCreateError(err)}
	}
	return os.NewFile(uintptr(handle), name), nil
}

// reopenPlatformFile obtains read and delete authority on the exact direct child without following reparse points.
func reopenPlatformFile(_ *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, fmt.Errorf("encode staged helper ticket name: %w", err)
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH,
		0,
		0,
	)
	runtime.KeepAlive(objectName)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(directoryPath, name), Err: windowsNativeError(err)}
	}
	return os.NewFile(uintptr(handle), name), nil
}

// validatePlatformDirectory rejects reparse-point aliases before the retained handle is compared and validated.
func validatePlatformDirectory(path string, _ os.FileInfo) error {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(pathPointer)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("directory is a Windows reparse point")
	}
	return nil
}

// validatePlatformObject rejects reparse points, hard links, and any grant beyond the exact pending-spool principals.
func validatePlatformObject(file *os.File, _ os.FileInfo, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("opened helper ticket spool path has the wrong object type")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read helper ticket spool file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("helper ticket spool path is a Windows reparse point")
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("helper ticket spool file has %d hard links, want 1", information.NumberOfLinks)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read helper ticket spool security descriptor: %w", err)
	}
	return validateWindowsDescriptor(descriptor, directory)
}

// renamePlatformNoReplace renames the already validated handle relative to the retained directory and flushes it afterward.
func renamePlatformNoReplace(_ *os.Root, directory *os.File, sourceFile *os.File, _ string, destination string) (bool, error) {
	renameBuffer, err := windowsRenameBuffer(windows.Handle(directory.Fd()), destination)
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
		return false, err
	}
	return true, windows.FlushFileBuffers(windows.Handle(sourceFile.Fd()))
}

// syncPlatformDirectory needs no path-based fallback because the handle-relative rename source is write-through and flushed.
func syncPlatformDirectory(_ *os.File) error {
	return nil
}

// windowsPendingDescriptor builds the exact protected policy installed on pending directories and their files.
func windowsPendingDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	owner, err := currentWindowsUserSID()
	if err != nil {
		return nil, nil, err
	}
	if owner.String() == windowsSystemSID || owner.String() == windowsAdministratorsSID {
		return nil, nil, errors.New("helper ticket spool requires a distinct interactive Windows user")
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%sD:P(A;%s;FA;;;%s)(A;%s;FA;;;%s)(A;%s;FA;;;%s)",
		owner.String(),
		inheritance,
		owner.String(),
		inheritance,
		windowsAdministratorsSID,
		inheritance,
		windowsSystemSID,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build helper ticket spool Windows security descriptor: %w", err)
	}
	return descriptor, owner, nil
}

// validateWindowsDescriptor requires the current user to own one protected exact three-principal DACL.
func validateWindowsDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read helper ticket spool DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("helper ticket spool Windows DACL is not protected")
	}
	wantOwner, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read helper ticket spool Windows owner: %w", err)
	}
	if owner == nil || owner.String() != wantOwner.String() {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("helper ticket spool Windows owner is %q, want %q", got, wantOwner.String())
	}
	return validateWindowsDACL(descriptor, wantOwner.String(), directory)
}

// validateWindowsDACL rejects inherited, additional, denied, duplicate, or weakened pending-spool grants.
func validateWindowsDACL(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID string, directory bool) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read helper ticket spool Windows access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 3 {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("helper ticket spool Windows DACL has %d entries, want 3", count)
	}
	want := map[string]bool{
		ownerSID:                 false,
		windowsAdministratorsSID: false,
		windowsSystemSID:         false,
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read helper ticket spool Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("helper ticket spool Windows DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != windowsFileAllAccess {
			return fmt.Errorf("helper ticket spool Windows DACL entry %d has access mask %#x", index, ace.Mask)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return fmt.Errorf("helper ticket spool Windows DACL entry %d has flags %#x, want %#x", index, ace.Header.AceFlags, wantFlags)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := sid.String()
		seen, exists := want[principal]
		if !exists || seen {
			return fmt.Errorf("helper ticket spool Windows DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("helper ticket spool Windows DACL does not grant SID %q", principal)
		}
	}
	return nil
}

// currentWindowsUserSID resolves the daemon process identity whose tickets the helper may consume.
func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// windowsRenameBuffer encodes one destination name relative to the retained pending-directory handle.
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

// windowsCreateError preserves exclusive-create collision classification across the native API boundary.
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

// windowsNativeError converts NTSTATUS outcomes into classifications understood by errors.Is.
func windowsNativeError(err error) error {
	status, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	if status == windows.STATUS_OBJECT_NAME_COLLISION || status == windows.STATUS_OBJECT_NAME_EXISTS {
		return windows.ERROR_FILE_EXISTS
	}
	return status.Errno()
}
