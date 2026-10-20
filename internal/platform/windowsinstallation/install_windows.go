//go:build windows

package windowsinstallation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Install writes only the complete embedded signed payload to Harbor's fixed protected machine location.
func Install(payload fs.FS) (*Installation, error) {
	if err := requireInstallerAuthority(); err != nil {
		return nil, err
	}
	current, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self, err := openRetained(current, false)
	if err != nil {
		return nil, err
	}
	trustErr := verifySignature(self, PublisherThumbprint)
	if err := errors.Join(trustErr, self.Close()); err != nil {
		return nil, err
	}
	root, err := MachineRoot()
	if err != nil {
		return nil, err
	}
	var installation *Installation
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = WithMachineTransaction(ctx, func() error {
		var installErr error
		installation, installErr = installAt(root, PublisherThumbprint, payload)
		return installErr
	})
	if err != nil && installation != nil {
		return nil, errors.Join(err, installation.Close())
	}
	return installation, err
}

// installAt is separately exercised with complete disposable signed fixtures without invoking installed product code.
func installAt(root, publisher string, payload fs.FS) (_ *Installation, result error) {
	manifestData, err := fs.ReadFile(payload, "release-manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := Decode(bytes.NewReader(manifestData), &manifest); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return nil, err
	}
	selection := Selection{SchemaVersion: SchemaVersion, ProductID: ProductID, InstallationID: hex.EncodeToString(identifier), UserSID: user.User.Sid.String(), ReleaseSequence: manifest.ReleaseSequence, BundleDigest: manifest.BundleDigest, SignerThumbprint: publisher}
	if err := ValidateManifest(manifest, selection); err != nil {
		return nil, err
	}
	if err := validateEmbeddedPublisher(payload, publisher); err != nil {
		return nil, err
	}
	parents := &Installation{root: root}
	defer func() { result = errors.Join(result, parents.Close()) }()
	vendor := filepath.Dir(root)
	if err := parents.retainDirectoryChain(filepath.Dir(vendor)); err != nil {
		return nil, err
	}
	if err := createMachineDirectory(vendor); err != nil {
		return nil, err
	}
	if err := parents.retainDirectoryChain(vendor); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		installed, admitErr := admitAt(root, publisher)
		if admitErr != nil {
			return nil, fmt.Errorf("preserve existing Harbor installation; an unsupported flat development layout or foreign state requires its original uninstaller: %w", admitErr)
		}
		if installed.selection.BundleDigest != manifest.BundleDigest || installed.selection.ReleaseSequence != manifest.ReleaseSequence || installed.selection.UserSID != selection.UserSID {
			return nil, errors.Join(errors.New("preserve different selected Harbor installation; stop and uninstall that exact package before installing this development package"), installed.Close())
		}
		return installed, nil
	}
	stage := filepath.Join(vendor, ".Harbor-stage-"+selection.InstallationID)
	if err := createMachineDirectoryNew(stage); err != nil {
		return nil, err
	}
	staged := &Installation{root: stage}
	defer func() { result = errors.Join(result, staged.Close()) }()
	if err := staged.retainDirectoryChain(stage); err != nil {
		return nil, err
	}
	stageDirectory, err := openRetained(stage, true)
	if err != nil {
		return nil, err
	}
	stageIdentity, identityErr := identifyFile(windows.Handle(stageDirectory.Fd()))
	if err := errors.Join(identityErr, stageDirectory.Close()); err != nil {
		return nil, err
	}
	releases := filepath.Join(stage, "releases")
	if err := createMachineDirectoryNew(releases); err != nil {
		return nil, err
	}
	release := filepath.Join(releases, strconv.FormatUint(manifest.ReleaseSequence, 10))
	if err := createMachineDirectoryNew(release); err != nil {
		return nil, err
	}
	if err := staged.retainDirectoryChain(release); err != nil {
		return nil, err
	}
	for _, component := range manifest.Components {
		source, err := payload.Open(component.Path)
		if err != nil {
			return nil, err
		}
		writeErr := writeMachineFile(filepath.Join(stage, filepath.FromSlash(component.Path)), source, component.Size, component.SHA256)
		if err := errors.Join(writeErr, source.Close()); err != nil {
			return nil, err
		}
		file, err := staged.retain(filepath.FromSlash(component.Path), false)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(component.Path, ".exe") || strings.HasSuffix(component.Path, ".ps1") {
			if err := verifySignature(file, publisher); err != nil {
				return nil, fmt.Errorf("admit installed %s before selection: %w", component.Role, err)
			}
		}
	}
	if err := writeMachineBytes(filepath.Join(release, "release-manifest.json"), manifestData); err != nil {
		return nil, err
	}
	selectionData, err := json.Marshal(selection)
	if err != nil {
		return nil, err
	}
	if err := writeMachineBytes(filepath.Join(stage, "installation.json"), selectionData); err != nil {
		return nil, err
	}
	complete, err := admitAt(stage, publisher)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(complete.Close(), staged.Close()); err != nil {
		return nil, err
	}
	if err := commitStagedDirectory(stage, root, stageIdentity); err != nil {
		return nil, err
	}
	return admitAt(root, publisher)
}

// validateEmbeddedPublisher binds the exported signer material to the compiled publisher before machine writes.
func validateEmbeddedPublisher(payload fs.FS, publisher string) error {
	if !thumbprintPattern.MatchString(publisher) {
		return errors.New("embedded setup has no compiled publisher")
	}
	data, err := fs.ReadFile(payload, "harbor-helper-signing.cer")
	if err != nil {
		return err
	}
	certificate, err := x509.ParseCertificate(data)
	if err != nil {
		return err
	}
	digest := sha1.Sum(certificate.Raw)
	if strings.ToUpper(hex.EncodeToString(digest[:])) != publisher {
		return errors.New("embedded certificate differs from compiled publisher")
	}
	thumbprint, err := fs.ReadFile(payload, "harbor-helper-signing-thumbprint.txt")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(thumbprint)) != publisher {
		return errors.New("embedded publisher receipt differs from compiled publisher")
	}
	return nil
}

// requireInstallerAuthority rejects a different elevated account before any machine installation operation.
func requireInstallerAuthority() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("Harbor setup requires explicit same-user administrator elevation")
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return err
	}
	administrator := false
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == administratorsSID && group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
			administrator = true
		}
	}
	if !administrator {
		return errors.New("Harbor setup requires effective administrator authority")
	}
	return VerifyInteractiveSession()
}

// machineDescriptor authors the trusted machine boundary at creation rather than repairing inherited user grants.
func machineDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	return windows.SecurityDescriptorFromString(fmt.Sprintf("O:BAD:P(A;%s;FA;;;BA)(A;%s;FA;;;SY)(A;%s;GRGX;;;BU)", inheritance, inheritance, inheritance))
}

// createMachineDirectory allows only an already protected vendor ancestor to be shared with other installed products.
func createMachineDirectory(path string) error {
	if err := createMachineDirectoryNew(path); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	file, err := openRetained(path, true)
	if err != nil {
		return err
	}
	return errors.Join(admitSecurity(windows.Handle(file.Fd()), true), file.Close())
}

// createMachineDirectoryNew never adopts an existing product directory or rewrites its security descriptor.
func createMachineDirectoryNew(path string) error {
	descriptor, err := machineDescriptor(true)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	err = windows.CreateDirectory(windows.StringToUTF16Ptr(path), &attributes)
	// SecurityAttributes points into the live descriptor through the native call.
	runtime.KeepAlive(descriptor)
	return err
}

// writeMachineBytes commits bounded canonical metadata without replacing an existing entry.
func writeMachineBytes(path string, data []byte) error {
	digest := sha256.Sum256(data)
	return writeMachineFile(path, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(digest[:]))
}

// writeMachineFile creates one exact signed component with a protected DACL and checks copied bytes before close.
func writeMachineFile(path string, source io.Reader, size int64, digest string) (result error) {
	if size < 0 || size == int64(^uint64(0)>>1) {
		return errors.New("invalid sealed component size")
	}
	descriptor, err := machineDescriptor(false)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_WRITE|windows.READ_CONTROL, windows.FILE_SHARE_READ, &attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	defer func() { result = errors.Join(result, file.Close()) }()
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(source, size+1))
	if err != nil {
		return err
	}
	if copied != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("embedded component size or digest differs from sealed manifest")
	}
	return file.Sync()
}

// commitStagedDirectory atomically selects only the exact fully validated staged directory without replacing any existing root.
func commitStagedDirectory(stage, destination string, identity fileIdentity) (result error) {
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(stage), windows.DELETE|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(handle)) }()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("preserve aliased staging directory")
	}
	actual, err := identifyFile(handle)
	if err != nil {
		return err
	}
	if actual != identity {
		return errors.New("preserve replaced staging directory")
	}
	if err := admitSecurity(handle, true); err != nil {
		return err
	}
	name, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	// FILE_RENAME_INFO has an eight-byte aligned root handle followed by byte length and UTF-16 name.
	type renameInformation struct {
		flags  uint32
		root   windows.Handle
		length uint32
		name   [1]uint16
	}
	offset := unsafe.Offsetof(renameInformation{}.name)
	buffer := make([]byte, int(offset)+len(name)*2)
	informationHeader := (*renameInformation)(unsafe.Pointer(&buffer[0]))
	informationHeader.length = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(name)), name)
	return windows.SetFileInformationByHandle(handle, windows.FileRenameInfo, &buffer[0], uint32(len(buffer)))
}
