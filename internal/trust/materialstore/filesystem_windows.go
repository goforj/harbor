//go:build windows

package materialstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

const (
	windowsSystemSID     = "S-1-5-18"
	windowsFileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

// windowsCreateFile opens one Win32 path with explicit security-authoring access.
type windowsCreateFile func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error)

// openPlatformFileNoFollow retains the final Windows directory entry itself so a reparse swap cannot be mistaken for its target.
func openPlatformFileNoFollow(path string, directory bool) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private Windows object path: %w", err)
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(fmt.Errorf("retain private Windows object handle"), windows.CloseHandle(handle))
	}
	return file, nil
}

// preparePlatformRoot creates the private leaf with a protected DACL in the creation syscall itself.
func preparePlatformRoot(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), privateDirectoryMode); err != nil {
		return fmt.Errorf("create certificate material parent: %w", err)
	}
	descriptor, _, err := windowsPrivateDescriptor(true)
	if err != nil {
		return err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode certificate material root path: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	err = windows.CreateDirectory(pathPointer, attributes)
	runtime.KeepAlive(descriptor)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create certificate material root: %w", err)
	}
	return nil
}

// platformSecureCreatedFile protects the already opened child from later ancestor-policy changes.
func platformSecureCreatedFile(file *os.File, directory bool) error {
	descriptor, owner, err := windowsPrivateDescriptor(directory)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private Windows DACL: %w", err)
	}
	securityHandle, err := reopenWindowsSecurityHandle(file, directory)
	if err != nil {
		return err
	}
	// Children inherit full access for TokenUser, so the checked reopen can
	// restore that explicit owner even when TokenOwner differs under elevation.
	err = windows.SetSecurityInfo(
		securityHandle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
	closeErr := windows.CloseHandle(securityHandle)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(file)
	if err := errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("apply private Windows DACL: %w", err)
	}
	return nil
}

// reopenWindowsSecurityHandle verifies a path-derived security handle still names the exact retained object.
func reopenWindowsSecurityHandle(file *os.File, directory bool) (windows.Handle, error) {
	return reopenWindowsSecurityHandleWith(file, directory, windows.CreateFile)
}

// reopenWindowsSecurityHandleWith rejects a path race before returning a security-authoring handle.
func reopenWindowsSecurityHandleWith(file *os.File, directory bool, open windowsCreateFile) (windows.Handle, error) {
	path, err := windowsfile.FinalPath(windows.Handle(file.Fd()))
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("resolve private Windows object path: %w", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("encode private Windows object path: %w", err)
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := open(
		pathPointer,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("reopen private Windows object for DACL protection: %w", err)
	}
	same, err := windowsfile.SameObject(windows.Handle(file.Fd()), handle)
	if err != nil {
		return windows.InvalidHandle, errors.Join(err, windows.CloseHandle(handle))
	}
	if !same {
		return windows.InvalidHandle, errors.Join(fmt.Errorf("private Windows object changed before DACL protection"), windows.CloseHandle(handle))
	}
	return handle, nil
}

// validatePlatformPath rejects reparse points and requires exactly the owner and LocalSystem protected-DACL grants.
func validatePlatformPath(path string, directory bool) error {
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("path is a Windows reparse point")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	validateErr := validatePlatformFile(file, directory)
	closeErr := file.Close()
	return errors.Join(validateErr, closeErr)
}

// validatePlatformFile rejects reparse points and requires exact protected grants on the opened object.
func validatePlatformFile(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read Windows file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("path is a Windows reparse point")
	}

	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows security descriptor: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read Windows DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("Windows DACL is not protected")
	}
	wantOwner, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read Windows object owner: %w", err)
	}
	if owner == nil || owner.String() != wantOwner.String() {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("Windows object owner is %q, want %q", got, wantOwner.String())
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("private file has %d hard links, want 1", information.NumberOfLinks)
	}
	return validateWindowsDACL(descriptor, wantOwner.String(), directory)
}

// platformSameFile compares retained Windows handles without narrowing modern 128-bit file identities.
func platformSameFile(first *os.File, second *os.File) (bool, error) {
	same, err := windowsfile.SameObject(windows.Handle(first.Fd()), windows.Handle(second.Fd()))
	runtime.KeepAlive(first)
	runtime.KeepAlive(second)
	if err != nil {
		return false, fmt.Errorf("compare private Windows object identity: %w", err)
	}
	return same, nil
}

// platformSyncDirectory requests the strongest directory flush Windows exposes and admits filesystems that reject it.
func platformSyncDirectory(directory *os.File) error {
	err := windows.FlushFileBuffers(windows.Handle(directory.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}

// windowsPrivateDescriptor constructs the exact owner and LocalSystem filesystem boundary used from first creation.
func windowsPrivateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	owner, err := currentWindowsUserSID()
	if err != nil {
		return nil, nil, err
	}
	if owner.String() == windowsSystemSID {
		return nil, nil, fmt.Errorf("Harbor certificate material cannot be owned by Windows LocalSystem")
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%sD:P(A;%s;FA;;;%s)(A;%s;FA;;;%s)",
		owner.String(),
		inheritance,
		owner.String(),
		inheritance,
		windowsSystemSID,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build private Windows security descriptor: %w", err)
	}
	return descriptor, owner, nil
}

// currentWindowsUserSID resolves the interactive process identity retained across elevation contexts.
func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// validateWindowsDACL rejects inherited, additional, denied, or weakened grants on private material.
func validateWindowsDACL(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID string, directory bool) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("Windows DACL has %d entries, want 2", count)
	}
	want := map[string]bool{ownerSID: false, windowsSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("Windows DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != windowsFileAllAccess && ace.Mask != windows.GENERIC_ALL {
			return fmt.Errorf("Windows DACL entry %d has access mask %#x", index, ace.Mask)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return fmt.Errorf("Windows DACL entry %d has flags %#x, want %#x", index, ace.Header.AceFlags, wantFlags)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := sid.String()
		seen, exists := want[principal]
		if !exists || seen {
			return fmt.Errorf("Windows DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("Windows DACL does not grant SID %q", principal)
		}
	}
	return nil
}
