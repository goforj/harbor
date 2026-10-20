//go:build windows

package windowsinstallation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

const (
	administratorsSID = "S-1-5-32-544"
	systemSID         = "S-1-5-18"
	usersSID          = "S-1-5-32-545"
	fileAllAccess     = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

// PublisherThumbprint is injected by the package build before every PE is signed with that publisher.
var PublisherThumbprint string

// Installation retains every admitted filesystem object until task activation or process creation has completed.
type Installation struct {
	selection Selection
	manifest  Manifest
	root      string
	handles   []*os.File
}

// MachineRoot obtains Program Files from Windows instead of an environment-controlled override.
func MachineRoot() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "GoForj", "Harbor"), nil
}

// Admit verifies the fixed protected installation and retains its exact objects against replacement.
func Admit() (*Installation, error) {
	root, err := MachineRoot()
	if err != nil {
		return nil, err
	}
	return admitAt(root, PublisherThumbprint)
}

// admitAt injects only the machine root and compiled publisher for native temporary-directory tests.
func admitAt(root, publisher string) (_ *Installation, result error) {
	if !thumbprintPattern.MatchString(publisher) {
		return nil, errors.New("Windows package has no compiled publisher identity")
	}
	installation := &Installation{root: root}
	defer func() {
		if result != nil {
			result = errors.Join(result, installation.Close())
		}
	}()
	if err := installation.retainDirectoryChain(root); err != nil {
		return nil, err
	}
	selectionFile, err := installation.retain("installation.json", false)
	if err != nil {
		return nil, err
	}
	if err := Decode(selectionFile, &installation.selection); err != nil {
		return nil, err
	}
	if err := ValidateSelection(installation.selection); err != nil {
		return nil, err
	}
	if installation.selection.SignerThumbprint != publisher {
		return nil, errors.New("Windows installation publisher differs from compiled launcher publisher")
	}
	release := filepath.Join(root, "releases", strconv.FormatUint(installation.selection.ReleaseSequence, 10))
	if err := installation.retainDirectoryChain(release); err != nil {
		return nil, err
	}
	manifestFile, err := installation.retain(filepath.Join("releases", strconv.FormatUint(installation.selection.ReleaseSequence, 10), "release-manifest.json"), false)
	if err != nil {
		return nil, err
	}
	if err := Decode(manifestFile, &installation.manifest); err != nil {
		return nil, err
	}
	if err := ValidateManifest(installation.manifest, installation.selection); err != nil {
		return nil, err
	}
	for _, component := range installation.manifest.Components {
		file, err := installation.retain(filepath.FromSlash(component.Path), false)
		if err != nil {
			return nil, err
		}
		information, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if information.Size() != component.Size {
			return nil, fmt.Errorf("Windows component %s size differs from manifest", component.Role)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return nil, err
		}
		if hex.EncodeToString(hash.Sum(nil)) != component.SHA256 {
			return nil, fmt.Errorf("Windows component %s digest differs from manifest", component.Role)
		}
		if strings.HasSuffix(component.Path, ".exe") || strings.HasSuffix(component.Path, ".ps1") {
			if err := verifySignature(file, publisher); err != nil {
				return nil, fmt.Errorf("admit Windows %s publisher: %w", component.Role, err)
			}
		}
	}
	return installation, nil
}

// Path resolves only a previously admitted role beneath the retained protected root.
func (installation *Installation) Path(role string) (string, error) {
	if len(installation.handles) == 0 {
		return "", errors.New("Windows installation admission has been closed")
	}
	relative, err := ComponentPath(installation.manifest, role)
	if err != nil {
		return "", err
	}
	return filepath.Join(installation.root, filepath.FromSlash(relative)), nil
}

// Selection returns a copy of the immutable admitted machine identity.
func (installation *Installation) Selection() Selection {
	return installation.selection
}

// Manifest returns a detached copy so callers cannot change admitted path authority.
func (installation *Installation) Manifest() Manifest {
	manifest := installation.manifest
	manifest.Components = append([]Component(nil), manifest.Components...)
	return manifest
}

// Close releases retained objects in reverse order after their admitted operation has settled.
func (installation *Installation) Close() error {
	var result error
	for index := len(installation.handles) - 1; index >= 0; index-- {
		result = errors.Join(result, installation.handles[index].Close())
	}
	installation.handles = nil
	return result
}

// retainDirectoryChain prevents an ancestor rename or reparse substitution while selected components are used.
func (installation *Installation) retainDirectoryChain(target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("Windows installation root must be absolute and canonical")
	}
	volume := filepath.VolumeName(target) + string(filepath.Separator)
	remaining := strings.TrimPrefix(target, volume)
	current := volume
	for _, part := range strings.Split(remaining, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return errors.New("Windows installation ancestor is not canonical")
		}
		current = filepath.Join(current, part)
		file, err := openRetained(current, true)
		if err != nil {
			return err
		}
		installation.handles = append(installation.handles, file)
		// Program Files and every product ancestor must exclude ordinary-user mutation.
		if err := admitSecurity(windows.Handle(file.Fd()), true); err != nil {
			return fmt.Errorf("admit Windows installation ancestor %q: %w", current, err)
		}
	}
	return nil
}

// retain binds a compiled relative path to an exact regular object without permitting parent traversal.
func (installation *Installation) retain(relative string, directory bool) (*os.File, error) {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, "..") {
		return nil, errors.New("Windows installed path is not a compiled relative destination")
	}
	file, err := openRetained(filepath.Join(installation.root, relative), directory)
	if err != nil {
		return nil, err
	}
	if err := admitSecurity(windows.Handle(file.Fd()), directory); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	installation.handles = append(installation.handles, file)
	return file, nil
}

// openRetained denies write/delete sharing and rejects every native alias before returning an object.
func openRetained(path string, directory bool) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	access := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
		access = windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES
	}
	handle, err := windows.CreateFile(pointer, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("retain installed Windows path %q: %w", path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (!directory && information.NumberOfLinks != 1) || (information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return nil, errors.Join(errors.New("Windows installation path has a reparse, hardlink or object-type conflict"), file.Close())
	}
	final, err := windowsfile.FinalPath(handle)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !strings.EqualFold(final, `\\?\`+path) {
		return nil, errors.Join(fmt.Errorf("Windows installation path resolves to %q instead of %q", final, `\\?\`+path), file.Close())
	}
	return file, nil
}

// admitSecurity rejects foreign ownership or ordinary-user mutation of protected installed objects.
func admitSecurity(handle windows.Handle, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return admitDescriptor(descriptor, directory)
}

// admitDescriptor permits trusted machine owners and refuses every untrusted mutation grant.
func admitDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || (owner.String() != administratorsSID && owner.String() != systemSID && owner.String() != `S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464`) {
		return errors.New("Windows installed object is not owned by a trusted machine principal")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("Windows installed object has a null DACL")
	}
	mutation := uint32(windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES)
	if directory {
		mutation |= 0x40
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows installed object has an unsupported ACL entry")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if principal != administratorsSID && principal != systemSID && principal != `S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464` && uint32(ace.Mask)&mutation != 0 {
			return fmt.Errorf("Windows installed object permits mutation by %s", principal)
		}
	}
	return nil
}

// VerifyUninstallerCopy pins a retained staged copy to the exact admitted maintenance agent before execution or removal.
func (installation *Installation) VerifyUninstallerCopy(file *os.File) error {
	if _, err := installation.Path("uninstaller"); err != nil {
		return err
	}
	var expected Component
	for _, component := range installation.manifest.Components {
		if component.Role == "uninstaller" {
			expected = component
			break
		}
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || information.NumberOfLinks != 1 {
		return errors.New("uninstaller copy has a reparse, hardlink or type conflict")
	}
	final, err := windowsfile.FinalPath(windows.Handle(file.Fd()))
	if err != nil {
		return err
	}
	if !strings.EqualFold(final, `\\?\`+file.Name()) {
		return errors.New("uninstaller copy path is not canonical")
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() != expected.Size {
		return errors.New("uninstaller copy size differs from admitted component")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.New("uninstaller copy digest differs from admitted component")
	}
	return verifySignature(file, installation.selection.SignerThumbprint)
}
