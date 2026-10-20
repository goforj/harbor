//go:build windows

package windowsinstallation

import (
	"bytes"
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
	return installAt(root, PublisherThumbprint, payload)
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
	if err := createMachineDirectoryNew(root); err != nil {
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
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
	if err := parents.retainDirectoryChain(root); err != nil {
		return nil, err
	}
	releases := filepath.Join(root, "releases")
	if err := createMachineDirectoryNew(releases); err != nil {
		return nil, err
	}
	release := filepath.Join(releases, strconv.FormatUint(manifest.ReleaseSequence, 10))
	if err := createMachineDirectoryNew(release); err != nil {
		return nil, err
	}
	if err := parents.retainDirectoryChain(release); err != nil {
		return nil, err
	}
	for _, component := range manifest.Components {
		source, err := payload.Open(component.Path)
		if err != nil {
			return nil, err
		}
		writeErr := writeMachineFile(filepath.Join(root, filepath.FromSlash(component.Path)), source, component.Size, component.SHA256)
		if err := errors.Join(writeErr, source.Close()); err != nil {
			return nil, err
		}
		file, err := parents.retain(filepath.FromSlash(component.Path), false)
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
	if err := writeMachineBytes(filepath.Join(root, "installation.json"), selectionData); err != nil {
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
