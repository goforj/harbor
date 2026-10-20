//go:build windows

package windowsinstallation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const integrationShortcutDirectorySDDL = "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)"
const integrationShortcutFileSDDL = "O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FR;;;BU)"

// integrationShortcutSpec fixes all shell-visible fields without consulting per-user shell registration.
type integrationShortcutSpec struct{ Programs, Target, WorkingDirectory, Icon string }

// integrationShortcutReceipt binds both directory and shortcut identities and all shortcut bytes.
type integrationShortcutReceipt struct {
	Directory integrationObject `json:"directory"`
	File      integrationObject `json:"file"`
	SHA256    string            `json:"sha256"`
}

// integrationShortcut holds the exact projection objects until activation or retirement settles.
type integrationShortcut struct {
	Receipt   integrationShortcutReceipt
	Directory *os.File
	File      *os.File
	Anchors   []*os.File
}

// integrationShortcutPlan selects the common Start Menu and only admitted product components.
func integrationShortcutPlan(installation *Installation) (integrationShortcutSpec, error) {
	programs, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return integrationShortcutSpec{}, err
	}
	target, err := installation.Path("desktop-launcher")
	if err != nil {
		return integrationShortcutSpec{}, err
	}
	icon, err := installation.Path("desktop")
	if err != nil {
		return integrationShortcutSpec{}, err
	}
	return integrationShortcutSpec{programs, target, installation.root, icon}, nil
}

// integrationShellLink emits the bounded MS-SHLLINK structures directly, avoiding elevated COM activation.
func integrationShellLink(spec integrationShortcutSpec) ([]byte, error) {
	for _, path := range []string{spec.Target, spec.WorkingDirectory, spec.Icon} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, `\\`) || len(utf16.Encode([]rune(path))) > 259 || strings.ContainsAny(path, "\x00%") {
			return nil, errors.New("Windows shortcut path is not a bounded local absolute path")
		}
	}
	header := make([]byte, 76)
	binary.LittleEndian.PutUint32(header, 76)
	copy(header[4:20], []byte{1, 0x14, 2, 0, 0, 0, 0, 0, 0xc0, 0, 0, 0, 0, 0, 0, 0x46})
	// PreferEnvironmentPath loads the exact EXP_SZ_LINK target without PIDL tracking or resolution.
	binary.LittleEndian.PutUint32(header[20:], 0x020002d2)
	binary.LittleEndian.PutUint32(header[24:], windows.FILE_ATTRIBUTE_NORMAL)
	binary.LittleEndian.PutUint32(header[60:], 1)
	info := make([]byte, 36)
	binary.LittleEndian.PutUint32(info[4:], 36)
	binary.LittleEndian.PutUint32(info[8:], 1)
	binary.LittleEndian.PutUint32(info[12:], uint32(len(info)))
	// DRIVE_FIXED and an empty ANSI volume label provide no tracking identity or network fallback.
	volume := make([]byte, 17)
	binary.LittleEndian.PutUint32(volume, 17)
	binary.LittleEndian.PutUint32(volume[4:], 3)
	binary.LittleEndian.PutUint32(volume[12:], 16)
	info = append(info, volume...)
	binary.LittleEndian.PutUint32(info[16:], uint32(len(info)))
	for _, character := range spec.Target {
		if character > 127 {
			info = append(info, '?')
		} else {
			info = append(info, byte(character))
		}
	}
	info = append(info, 0)
	binary.LittleEndian.PutUint32(info[24:], uint32(len(info)))
	info = append(info, 0)
	binary.LittleEndian.PutUint32(info[28:], uint32(len(info)))
	info = append(info, integrationUTF16Bytes(spec.Target, true)...)
	binary.LittleEndian.PutUint32(info[32:], uint32(len(info)))
	info = append(info, 0, 0)
	binary.LittleEndian.PutUint32(info, uint32(len(info)))
	data := append(header, info...)
	for _, value := range []string{spec.WorkingDirectory, spec.Icon} {
		units := utf16.Encode([]rune(value))
		data = binary.LittleEndian.AppendUint16(data, uint16(len(units)))
		data = append(data, integrationUTF16Bytes(value, false)...)
	}
	// EXP_SZ_LINK stores an absolute path with no percent tokens, so expansion cannot redirect it.
	target := make([]byte, 0x314)
	binary.LittleEndian.PutUint32(target, 0x314)
	binary.LittleEndian.PutUint32(target[4:], 0xa0000001)
	ansi := target[8:268]
	index := 0
	for _, character := range spec.Target {
		if character > 127 {
			ansi[index] = '?'
		} else {
			ansi[index] = byte(character)
		}
		index++
	}
	copy(target[268:], integrationUTF16Bytes(spec.Target, true))
	data = append(data, target...)
	return append(data, 0, 0, 0, 0), nil
}

// integrationUTF16Bytes encodes exact Windows string units without consulting a process code page.
func integrationUTF16Bytes(value string, terminated bool) []byte {
	units := utf16.Encode([]rune(value))
	if terminated {
		units = append(units, 0)
	}
	data := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(data[index*2:], unit)
	}
	return data
}

// integrationCreateShortcut creates the protected directory and link without replacing existing objects.
func integrationCreateShortcut(spec integrationShortcutSpec) (_ *integrationShortcut, result error) {
	data, err := integrationShellLink(spec)
	if err != nil {
		return nil, err
	}
	shortcut, err := integrationOpenShortcutAnchors(spec.Programs)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, shortcut.retire(), shortcut.Close())
		}
	}()
	directory := integrationObject{Path: filepath.Join(spec.Programs, "GoForj"), SDDL: integrationShortcutDirectorySDDL}
	descriptor, err := windows.SecurityDescriptorFromString(directory.SDDL)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	pointer, _ := windows.UTF16PtrFromString(directory.Path)
	err = windows.CreateDirectory(pointer, &attributes)
	directory.Created = err == nil
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	shortcut.Receipt.Directory = directory
	shortcut.Directory, err = openIntegrationDirectory(directory.Path)
	if err != nil {
		return nil, err
	}
	shortcut.Receipt.Directory, err = inspectIntegrationObject(shortcut.Directory, directory)
	if err != nil {
		return nil, err
	}
	fileObject := integrationObject{Path: filepath.Join(directory.Path, "Harbor.lnk"), SDDL: integrationShortcutFileSDDL, Created: true}
	descriptor, err = windows.SecurityDescriptorFromString(fileObject.SDDL)
	if err != nil {
		return nil, err
	}
	attributes.SecurityDescriptor = descriptor
	pointer, _ = windows.UTF16PtrFromString(fileObject.Path)
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.DELETE, windows.FILE_SHARE_READ, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	shortcut.File = os.NewFile(uintptr(handle), fileObject.Path)
	shortcut.Receipt.File = fileObject
	if _, err := shortcut.File.Write(data); err != nil {
		return nil, err
	}
	if err := shortcut.File.Sync(); err != nil {
		return nil, err
	}
	shortcut.Receipt.File, err = integrationInspectShortcutFile(shortcut.File, fileObject)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	shortcut.Receipt.SHA256 = hex.EncodeToString(digest[:])
	return shortcut, nil
}

// integrationAdmitShortcut binds a receipt to the compiled fields before granting deletion authority.
func integrationAdmitShortcut(spec integrationShortcutSpec, receipt integrationShortcutReceipt) (_ *integrationShortcut, result error) {
	data, err := integrationShellLink(spec)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if receipt.Directory.Path != filepath.Join(spec.Programs, "GoForj") || receipt.Directory.SDDL != integrationShortcutDirectorySDDL || receipt.File.Path != filepath.Join(spec.Programs, "GoForj", "Harbor.lnk") || receipt.File.SDDL != integrationShortcutFileSDDL || receipt.SHA256 != hex.EncodeToString(digest[:]) || receipt.File.ID == "" || receipt.Directory.ID == "" {
		return nil, errors.New("Windows shortcut receipt differs from its compiled projection")
	}
	shortcut, err := integrationOpenShortcutAnchors(spec.Programs)
	if err != nil {
		return nil, err
	}
	shortcut.Receipt = receipt
	defer func() {
		if result != nil {
			result = errors.Join(result, shortcut.Close())
		}
	}()
	shortcut.Directory, err = openIntegrationDirectory(receipt.Directory.Path)
	if err != nil {
		return nil, err
	}
	observed, err := inspectIntegrationObject(shortcut.Directory, receipt.Directory)
	if err != nil {
		return nil, err
	}
	if observed != receipt.Directory {
		return nil, errors.New("Windows shortcut directory identity changed")
	}
	pointer, _ := windows.UTF16PtrFromString(receipt.File.Path)
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	shortcut.File = os.NewFile(uintptr(handle), receipt.File.Path)
	observed, err = integrationInspectShortcutFile(shortcut.File, receipt.File)
	if err != nil {
		return nil, err
	}
	if observed != receipt.File {
		return nil, errors.New("Windows shortcut native identity changed")
	}
	actual, err := io.ReadAll(io.LimitReader(shortcut.File, int64(len(data))+1))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(actual, data) {
		return nil, errors.New("Windows shortcut bytes changed")
	}
	return shortcut, nil
}

// integrationInspectShortcutFile rejects hardlinks, aliases and descriptor drift before recording native identity.
func integrationInspectShortcutFile(file *os.File, object integrationObject) (integrationObject, error) {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return object, err
	}
	if information.NumberOfLinks != 1 || information.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return object, errors.New("Windows shortcut has a hardlink, alias or type conflict")
	}
	final, err := finalIntegrationPath(windows.Handle(file.Fd()))
	if err != nil {
		return object, err
	}
	if !strings.EqualFold(final, `\\?\`+object.Path) {
		return object, errors.New("Windows shortcut native path differs")
	}
	return inspectIntegrationObject(file, object)
}

// integrationOpenShortcutAnchors prevents common Start Menu ancestor replacement during projection changes.
func integrationOpenShortcutAnchors(programs string) (_ *integrationShortcut, result error) {
	if !filepath.IsAbs(programs) || filepath.Clean(programs) != programs || strings.HasPrefix(programs, `\\`) {
		return nil, errors.New("Windows common Programs path is not canonical")
	}
	shortcut := &integrationShortcut{}
	defer func() {
		if result != nil {
			result = errors.Join(result, shortcut.Close())
		}
	}()
	current := filepath.VolumeName(programs) + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(programs, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		file, err := openIntegrationAnchor(current)
		if err != nil {
			return nil, err
		}
		shortcut.Anchors = append(shortcut.Anchors, file)
		if err := admitProgramDataAnchor(windows.Handle(file.Fd())); err != nil {
			return nil, err
		}
	}
	return shortcut, nil
}

// retire removes exact created projections, preserving neighboring files and preexisting directories.
func (shortcut *integrationShortcut) retire() error {
	return shortcut.retireChecked(func() error { return nil })
}

// retireChecked revalidates the required runtime lease separately for link and shared-directory retirement.
func (shortcut *integrationShortcut) retireChecked(check func() error) error {
	var result error
	if shortcut.File != nil && shortcut.Receipt.File.Created {
		if err := check(); err != nil {
			return err
		}
		if err := retireIntegrationFile(shortcut.File); err != nil {
			return err
		}
		result = shortcut.File.Close()
		shortcut.File = nil
	}
	if shortcut.Directory != nil && shortcut.Receipt.Directory.Created {
		if err := check(); err != nil {
			return err
		}
		err := retireIntegrationFile(shortcut.Directory)
		if !errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Close releases retained shortcut objects without changing the shared Start Menu namespace.
func (shortcut *integrationShortcut) Close() error {
	var result error
	if shortcut.File != nil {
		result = errors.Join(result, shortcut.File.Close())
		shortcut.File = nil
	}
	if shortcut.Directory != nil {
		result = errors.Join(result, shortcut.Directory.Close())
		shortcut.Directory = nil
	}
	for index := len(shortcut.Anchors) - 1; index >= 0; index-- {
		result = errors.Join(result, shortcut.Anchors[index].Close())
	}
	shortcut.Anchors = nil
	return result
}
