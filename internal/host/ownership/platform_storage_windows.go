//go:build windows

package ownership

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

// windowsFileRenameInformation is the variable-length buffer accepted by FileRenameInformation.
type windowsFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

// flushOwnershipFile remains replaceable in Windows tests so failed rename confirmation can be exercised deterministically.
var flushOwnershipFile = windows.FlushFileBuffers

// createPlatformFile applies the protected machine descriptor in FILE_CREATE so no inherited-access window exists.
func createPlatformFile(root *os.Root, directoryPath string, name string) (*os.File, error) {
	descriptor, _, err := windowsOwnershipDescriptor(false)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open retained machine ownership directory: %w", err)
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("encode machine ownership file name: %w", err), directory.Close())
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
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.READ_CONTROL,
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
		closeFileErr := windows.CloseHandle(handle)
		removeErr := root.Remove(name)
		if errors.Is(removeErr, fs.ErrNotExist) {
			removeErr = nil
		}
		return nil, errors.Join(
			fmt.Errorf("close retained machine ownership directory after creating %q: %w", filepath.Join(directoryPath, name), closeErr),
			wrapStoreError("close newly created machine ownership file", filepath.Join(directoryPath, name), closeFileErr),
			wrapStoreError("remove newly created machine ownership file", filepath.Join(directoryPath, name), removeErr),
		)
	}
	return os.NewFile(uintptr(handle), name), nil
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

// platformRenameNoReplace keeps both names relative to the retained directory because ancestor replacement can redirect absolute paths.
func platformRenameNoReplace(root *os.Root, _ string, source string, destination string) (bool, error) {
	sourceHandle, directory, err := openWindowsRelativeFile(
		root,
		source,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_WRITE|windows.DELETE,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH,
	)
	if err != nil {
		return false, err
	}

	renameBuffer, err := windowsRenameBuffer(windows.Handle(directory.Fd()), destination)
	if err != nil {
		return false, errors.Join(err, windows.CloseHandle(sourceHandle), directory.Close())
	}
	err = windows.NtSetInformationFile(
		sourceHandle,
		&windows.IO_STATUS_BLOCK{},
		&renameBuffer[0],
		uint32(len(renameBuffer)),
		windows.FileRenameInformation,
	)
	runtime.KeepAlive(renameBuffer)
	if err != nil {
		err = windowsNativeError(err)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			err = fs.ErrExist
		}
		return false, errors.Join(err, windows.CloseHandle(sourceHandle), directory.Close())
	}
	flushErr := flushOwnershipFile(sourceHandle)
	closeSourceErr := windows.CloseHandle(sourceHandle)
	closeDirectoryErr := directory.Close()
	return true, errors.Join(flushErr, closeSourceErr, closeDirectoryErr)
}

// openWindowsRelativeFile prevents ancestor path replacement from redirecting protected entry access.
func openWindowsRelativeFile(root *os.Root, name string, access uint32, options uint32) (windows.Handle, *os.File, error) {
	directory, err := root.Open(".")
	if err != nil {
		return windows.InvalidHandle, nil, err
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, nil, errors.Join(err, directory.Close())
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
		access,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	runtime.KeepAlive(objectName)
	if err != nil {
		return windows.InvalidHandle, nil, errors.Join(windowsNativeError(err), directory.Close())
	}
	return handle, directory, nil
}

// windowsRenameBuffer encodes a no-replace destination relative to the retained directory handle.
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

// windowsNativeError converts NTSTATUS values into Win32 classifications understood by errors.Is.
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

// platformConfirmEntry reopens the active record handle-relative and flushes it before replay grants authority.
func platformConfirmEntry(root *os.Root, directoryPath string, name string) error {
	handle, directory, err := openWindowsRelativeFile(
		root,
		name,
		windows.SYNCHRONIZE|windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_WRITE_THROUGH,
	)
	if err != nil {
		return fmt.Errorf("open machine ownership entry %q for confirmation: %w", filepath.Join(directoryPath, name), err)
	}
	file := os.NewFile(uintptr(handle), name)
	validateErr := validateOpenedEntry(root, name, file)
	var flushErr error
	if validateErr == nil {
		flushErr = flushOwnershipFile(handle)
	}
	closeFileErr := file.Close()
	closeDirectoryErr := directory.Close()
	return errors.Join(
		validateErr,
		wrapStoreError("flush machine ownership entry", filepath.Join(directoryPath, name), flushErr),
		wrapStoreError("close confirmed machine ownership entry", filepath.Join(directoryPath, name), closeFileErr),
		wrapStoreError("close retained machine ownership directory", directoryPath, closeDirectoryErr),
	)
}

// platformConfirmCleanup needs no Windows directory flush because the earlier release rename is the durable boundary.
func platformConfirmCleanup(_ *os.Root) error {
	return nil
}
