//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

const (
	projectRemovalIntentLockRetryInterval        = 10 * time.Millisecond
	projectRemovalIntentWindowsSystemSID         = "S-1-5-18"
	projectRemovalIntentWindowsAdministratorsSID = "S-1-5-32-544"
	projectRemovalIntentWindowsFileAllAccess     = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	projectRemovalIntentLegacyMaximumACEs        = 8
)

// projectRemovalIntentWindowsTokenOwner matches the TOKEN_OWNER result returned by GetTokenInformation.
type projectRemovalIntentWindowsTokenOwner struct {
	owner *windows.SID
}

// projectRemovalIntentWindowsCreateFile opens one path with the access needed to replace its security descriptor.
type projectRemovalIntentWindowsCreateFile func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error)

// acquireProjectRemovalIntentLock waits on a nonblocking byte-range lock so context cancellation stays responsive.
func acquireProjectRemovalIntentLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(projectRemovalIntentLockRetryInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		overlapped := new(windows.Overlapped)
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// releaseProjectRemovalIntentLock relinquishes the byte-range lock before its handle closes.
func releaseProjectRemovalIntentLock(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// prepareProjectRemovalIntentObject establishes TokenUser ownership and migrates only narrowly trusted legacy objects.
func prepareProjectRemovalIntentObject(file *os.File, directory bool, created bool) error {
	if err := validateProjectRemovalIntentWindowsShape(file, directory); err != nil {
		return err
	}
	descriptor, err := projectRemovalIntentWindowsSecurity(file)
	if err != nil {
		return err
	}
	user, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		return err
	}
	if validateProjectRemovalIntentWindowsDescriptor(descriptor, user, directory) == nil {
		return nil
	}
	if !created {
		owner, err := currentProjectRemovalIntentWindowsOwnerSID()
		if err != nil {
			return err
		}
		if err := validateLegacyProjectRemovalIntentWindowsDescriptor(descriptor, user, owner); err != nil {
			return fmt.Errorf(
				"legacy project removal intent permissions cannot be migrated safely: %w; remove Harbor's project-removal-intents directory after confirming no removal is in progress",
				err,
			)
		}
	}
	if err := secureProjectRemovalIntentWindowsObject(file, directory); err != nil {
		return err
	}
	secured, err := projectRemovalIntentWindowsSecurity(file)
	if err != nil {
		return err
	}
	if err := validateProjectRemovalIntentWindowsDescriptor(secured, user, directory); err != nil {
		return fmt.Errorf("verify secured project removal intent object: %w", err)
	}
	return nil
}

// secureProjectRemovalIntentWindowsObject replaces inherited policy through an identity-checked handle.
func secureProjectRemovalIntentWindowsObject(file *os.File, directory bool) error {
	descriptor, owner, err := projectRemovalIntentWindowsDescriptor(directory)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read project removal intent Windows DACL: %w", err)
	}
	handle, err := reopenProjectRemovalIntentWindowsSecurityHandle(file, directory)
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
		return fmt.Errorf("apply project removal intent Windows security: %w", err)
	}
	return nil
}

// reopenProjectRemovalIntentWindowsSecurityHandle verifies the path-derived handle still names the retained object.
func reopenProjectRemovalIntentWindowsSecurityHandle(file *os.File, directory bool) (windows.Handle, error) {
	return reopenProjectRemovalIntentWindowsSecurityHandleWith(file, directory, windows.CreateFile)
}

// reopenProjectRemovalIntentWindowsSecurityHandleWith rejects a path race before any security mutation.
func reopenProjectRemovalIntentWindowsSecurityHandleWith(
	file *os.File,
	directory bool,
	open projectRemovalIntentWindowsCreateFile,
) (windows.Handle, error) {
	path, err := windowsfile.FinalPath(windows.Handle(file.Fd()))
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("resolve project removal intent Windows path: %w", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("encode project removal intent Windows path: %w", err)
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
		return windows.InvalidHandle, fmt.Errorf("reopen project removal intent object for security: %w", err)
	}
	same, err := windowsfile.SameObject(windows.Handle(file.Fd()), handle)
	if err != nil {
		return windows.InvalidHandle, errors.Join(err, windows.CloseHandle(handle))
	}
	if !same {
		return windows.InvalidHandle, errors.Join(errors.New("project removal intent object changed before security update"), windows.CloseHandle(handle))
	}
	return handle, nil
}

// validateProjectRemovalIntentObject rejects reparse points, hard links, and policy beyond TokenUser and LocalSystem.
func validateProjectRemovalIntentObject(file *os.File, directory bool) error {
	if err := validateProjectRemovalIntentWindowsShape(file, directory); err != nil {
		return err
	}
	descriptor, err := projectRemovalIntentWindowsSecurity(file)
	if err != nil {
		return err
	}
	user, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		return err
	}
	return validateProjectRemovalIntentWindowsDescriptor(descriptor, user, directory)
}

// validateProjectRemovalIntentWindowsShape rejects object aliases before security is inspected or changed.
func validateProjectRemovalIntentWindowsShape(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() {
		return errors.New("project removal intent object is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("project removal intent object is not a regular file")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read project removal intent file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("project removal intent object is a Windows reparse point")
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("project removal intent file has %d links, want 1", information.NumberOfLinks)
	}
	return nil
}

// projectRemovalIntentWindowsSecurity reads the exact owner and DACL from a retained object handle.
func projectRemovalIntentWindowsSecurity(file *os.File) (*windows.SECURITY_DESCRIPTOR, error) {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return nil, fmt.Errorf("read project removal intent Windows security descriptor: %w", err)
	}
	return descriptor, nil
}

// projectRemovalIntentWindowsDescriptor builds the protected TokenUser and LocalSystem policy.
func projectRemovalIntentWindowsDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	owner, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		return nil, nil, err
	}
	if owner.String() == projectRemovalIntentWindowsSystemSID {
		return nil, nil, errors.New("project removal intents cannot be owned by Windows LocalSystem")
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
		projectRemovalIntentWindowsSystemSID,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build project removal intent Windows security descriptor: %w", err)
	}
	return descriptor, owner, nil
}

// validateProjectRemovalIntentWindowsDescriptor requires the exact protected TokenUser and LocalSystem policy.
func validateProjectRemovalIntentWindowsDescriptor(
	descriptor *windows.SECURITY_DESCRIPTOR,
	owner *windows.SID,
	directory bool,
) error {
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read project removal intent Windows DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("project removal intent Windows DACL is not protected")
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("decode project removal intent Windows owner: %w", err)
	}
	if actualOwner == nil || !actualOwner.Equals(owner) {
		got := ""
		if actualOwner != nil {
			got = actualOwner.String()
		}
		return fmt.Errorf("project removal intent owner is %q, want TokenUser %q", got, owner.String())
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read project removal intent Windows access list: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("project removal intent Windows DACL has %d entries, want 2", count)
	}
	want := map[string]bool{owner.String(): false, projectRemovalIntentWindowsSystemSID: false}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read project removal intent Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("project removal intent Windows DACL entry %d is not an allow entry", index)
		}
		if ace.Mask != projectRemovalIntentWindowsFileAllAccess {
			return fmt.Errorf("project removal intent Windows DACL entry %d has access mask %#x, want %#x", index, ace.Mask, projectRemovalIntentWindowsFileAllAccess)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return fmt.Errorf("project removal intent Windows DACL entry %d has flags %#x, want %#x", index, ace.Header.AceFlags, wantFlags)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		seen, exists := want[principal]
		if !exists || seen {
			return fmt.Errorf("project removal intent Windows DACL grants unexpected or duplicate SID %q", principal)
		}
		want[principal] = true
	}
	for principal, seen := range want {
		if !seen {
			return fmt.Errorf("project removal intent Windows DACL does not grant SID %q", principal)
		}
	}
	return nil
}

// validateLegacyProjectRemovalIntentWindowsDescriptor admits only default-owner objects with narrow system principals and effective owner authority.
func validateLegacyProjectRemovalIntentWindowsDescriptor(
	descriptor *windows.SECURITY_DESCRIPTOR,
	user *windows.SID,
	defaultOwner *windows.SID,
) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("decode legacy Windows owner: %w", err)
	}
	if owner == nil || !owner.Equals(user) && !owner.Equals(defaultOwner) {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("owner %q is neither TokenUser %q nor current TokenOwner %q", got, user.String(), defaultOwner.String())
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read legacy Windows access list: %w", err)
	}
	if dacl == nil || dacl.AceCount == 0 || dacl.AceCount > projectRemovalIntentLegacyMaximumACEs {
		count := uint16(0)
		if dacl != nil {
			count = dacl.AceCount
		}
		return fmt.Errorf("legacy Windows DACL has unsafe entry count %d", count)
	}
	allowed := map[string]bool{
		user.String():                                true,
		defaultOwner.String():                        true,
		projectRemovalIntentWindowsSystemSID:         true,
		projectRemovalIntentWindowsAdministratorsSID: true,
	}
	authority := owner.String()
	authorityHasFullAccess := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read legacy Windows DACL entry %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("legacy Windows DACL entry %d is not an allow entry", index)
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !allowed[principal] {
			return fmt.Errorf("legacy Windows DACL grants unexpected SID %q", principal)
		}
		if principal == authority && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 &&
			(ace.Mask == windows.GENERIC_ALL || ace.Mask&projectRemovalIntentWindowsFileAllAccess == projectRemovalIntentWindowsFileAllAccess) {
			authorityHasFullAccess = true
		}
	}
	if !authorityHasFullAccess {
		return fmt.Errorf("legacy Windows owner %q lacks an effective full-access grant", authority)
	}
	return nil
}

// currentProjectRemovalIntentWindowsUserSID resolves the stable per-user identity shared across UAC contexts.
func currentProjectRemovalIntentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// currentProjectRemovalIntentWindowsOwnerSID resolves the default owner used only to identify migratable legacy objects.
func currentProjectRemovalIntentWindowsOwnerSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, fmt.Errorf("size current Windows token owner: %w", err)
	}
	if size < uint32(unsafe.Sizeof(projectRemovalIntentWindowsTokenOwner{})) {
		return nil, fmt.Errorf("current Windows token owner has invalid size %d", size)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size); err != nil {
		return nil, fmt.Errorf("read current Windows token owner: %w", err)
	}
	owner := (*projectRemovalIntentWindowsTokenOwner)(unsafe.Pointer(&buffer[0])).owner
	if owner == nil {
		return nil, errors.New("current Windows token owner is missing")
	}
	copy, err := owner.Copy()
	if err != nil {
		return nil, fmt.Errorf("copy current Windows token owner: %w", err)
	}
	return copy, nil
}

// syncProjectRemovalIntentDirectory requests the strongest directory flush Windows exposes when supported.
func syncProjectRemovalIntentDirectory(directory *os.File) error {
	err := windows.FlushFileBuffers(windows.Handle(directory.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}
