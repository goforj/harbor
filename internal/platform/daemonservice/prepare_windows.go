//go:build windows

package daemonservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/runtimepath"
	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

// ErrDaemonActive preserves a daemon that already owns this user's singleton authority.
var ErrDaemonActive = errors.New("an existing Harbor daemon owns the user runtime")

// PrepareDatabase serializes the selected Windows release's migrations against the exact daemon singleton byte range.
func PrepareDatabase(migrate func() error) error {
	directory, err := runtimepath.Directory()
	if err != nil {
		return err
	}
	return prepareDatabaseAt(directory, migrate)
}

// prepareDatabaseAt retains every ancestor and refuses ambiguous runtime authority before opening user state.
func prepareDatabaseAt(directory string, migrate func() error) (result error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner := user.User.Sid.String()
	if owner == "S-1-5-18" {
		return errors.New("installed daemon migrations cannot run as LocalSystem")
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;SY)", owner, owner))
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	ancestors, err := retainRuntimeAncestors(directory, attributes)
	defer func() {
		for index := len(ancestors) - 1; index >= 0; index-- {
			result = errors.Join(result, ancestors[index].Close())
		}
		runtime.KeepAlive(descriptor)
	}()
	if err != nil {
		return err
	}
	if err := admitRuntimeObject(ancestors[len(ancestors)-1], owner, true); err != nil {
		return err
	}
	path := filepath.Join(directory, runtimepath.DaemonLockFilename)
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, attributes, windows.OPEN_ALWAYS, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	defer func() { result = errors.Join(result, file.Close()) }()
	if err := admitRuntimeObject(file, owner, false); err != nil {
		return err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrDaemonActive
		}
		return fmt.Errorf("lock installed Windows daemon preparation: %w", err)
	}
	defer func() { result = errors.Join(result, windows.UnlockFileEx(handle, 0, 1, 0, overlapped)) }()
	return migrate()
}

// retainRuntimeAncestors pins each existing or newly private directory before traversing its next child.
func retainRuntimeAncestors(path string, attributes *windows.SecurityAttributes) (files []*os.File, result error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("Windows runtime directory is not absolute and canonical")
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("Windows runtime directory must use a local drive")
	}
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return files, errors.New("Windows runtime ancestor is not canonical")
		}
		current = filepath.Join(current, part)
		pointer := windows.StringToUTF16Ptr(current)
		if err := windows.CreateDirectory(pointer, attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return files, err
		}
		handle, err := windows.CreateFile(pointer, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return files, err
		}
		file := os.NewFile(uintptr(handle), current)
		files = append(files, file)
		if err := admitRuntimeShape(file, true); err != nil {
			return files, err
		}
	}
	if len(files) == 0 {
		return files, errors.New("Windows runtime directory has no user leaf")
	}
	return files, nil
}

// admitRuntimeShape refuses redirected directories, nonregular locks and multiple native links while handles remain retained.
func admitRuntimeShape(file *os.File, directory bool) error {
	handle := windows.Handle(file.Fd())
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && information.NumberOfLinks != 1) {
		return errors.New("Windows runtime object has a reparse, hardlink or type conflict")
	}
	final, err := windowsfile.FinalPath(handle)
	if err != nil {
		return err
	}
	if !strings.EqualFold(final, `\\?\`+file.Name()) {
		return fmt.Errorf("Windows runtime object resolves to %q instead of %q", final, `\\?\`+file.Name())
	}
	return nil
}

// admitRuntimeObject accepts source-created private grants without repairing foreign ownership or permissions.
func admitRuntimeObject(file *os.File, owner string, directory bool) error {
	if err := admitRuntimeShape(file, directory); err != nil {
		return err
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	actualOwner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if actualOwner == nil || actualOwner.String() != owner {
		return errors.New("Windows runtime object is not owned by the current user SID")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount == 0 {
		return errors.New("Windows runtime object has no private DACL")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows runtime object has unsupported ACL entries")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if principal != owner && principal != "S-1-5-18" && principal != "S-1-5-32-544" {
			return fmt.Errorf("Windows runtime object grants access to foreign principal %s", principal)
		}
	}
	return nil
}
