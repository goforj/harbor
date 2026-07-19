package materialstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
)

// rootedFilesystem confines every caller-independent store path beneath one verified private directory handle.
type rootedFilesystem struct {
	root          *os.Root
	syncDirectory func(string, *os.File) error
}

// openRootedFilesystem creates or verifies the private root before retaining a confined operating-system handle.
func openRootedFilesystem(directory string) (*rootedFilesystem, error) {
	return openRootedFilesystemWithHook(directory, nil)
}

// openRootedFilesystemWithHook keeps root-swap regression tests deterministic at the pathname-to-handle boundary.
func openRootedFilesystemWithHook(directory string, afterValidation func()) (*rootedFilesystem, error) {
	return openRootedFilesystemWithHooks(directory, afterValidation, nil)
}

// openRootedFilesystemWithHooks admits only package-test instrumentation while production always uses platform durability.
func openRootedFilesystemWithHooks(
	directory string,
	afterValidation func(),
	syncDirectory func(string, *os.File) error,
) (*rootedFilesystem, error) {
	if directory == "" {
		return nil, fmt.Errorf("open certificate material store: directory is empty")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("open certificate material store: directory %q is not absolute", directory)
	}
	directory = filepath.Clean(directory)
	if err := preparePlatformRoot(directory); err != nil {
		return nil, fmt.Errorf("prepare certificate material directory: %w", err)
	}
	validated, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, fmt.Errorf("retain validated certificate material directory: %w", err)
	}
	if err := validatePlatformFile(validated, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate retained certificate material directory: %w", err), validated.Close())
	}
	if afterValidation != nil {
		afterValidation()
	}

	current, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed before its rooted handle opened: %w", err), validated.Close())
	}
	same, err := platformSameFile(validated, current)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare certificate material directory handles: %w", err), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed before its rooted handle opened"), current.Close(), validated.Close())
	}
	if err := validatePlatformFile(current, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate current certificate material directory: %w", err), current.Close(), validated.Close())
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed before its rooted handle opened: %w", err), current.Close(), validated.Close())
	}
	filesystem := &rootedFilesystem{
		root: root,
		syncDirectory: func(_ string, directory *os.File) error {
			return platformSyncDirectory(directory)
		},
	}
	if syncDirectory != nil {
		filesystem.syncDirectory = syncDirectory
	}
	opened, err := filesystem.openDirectUnvalidated(".", true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect opened certificate material directory: %w", err), root.Close(), current.Close(), validated.Close())
	}
	same, err = platformSameFile(current, opened)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare certificate material directory handles: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed before its rooted handle opened"), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := filesystem.validateOpened(".", opened, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate opened certificate material directory: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	verified, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed after its rooted handle opened: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	same, err = platformSameFile(current, verified)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare verified certificate material directory handles: %w", err), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(fmt.Errorf("certificate material directory changed after its rooted handle opened"), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := validatePlatformFile(verified, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate verified certificate material directory: %w", err), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := errors.Join(verified.Close(), opened.Close(), current.Close(), validated.Close()); err != nil {
		return nil, errors.Join(fmt.Errorf("close certificate material directory verification handles: %w", err), root.Close())
	}
	return filesystem, nil
}

// Close releases the rooted filesystem handle without removing durable material.
func (filesystem *rootedFilesystem) Close() error {
	return filesystem.root.Close()
}

// ensureDirectory creates one private descendant and durably links it before returning.
func (filesystem *rootedFilesystem) ensureDirectory(path string) error {
	if err := validateRelativePath(path); err != nil {
		return err
	}
	err := filesystem.root.Mkdir(path, privateDirectoryMode)
	created := err == nil
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create private directory %q: %w", path, err)
	}

	directory, err := filesystem.openDirectUnvalidated(path, true)
	if err != nil {
		if created {
			return errors.Join(err, filesystem.root.Remove(path))
		}
		return err
	}
	defer directory.Close()
	if created {
		if err := platformSecureCreatedFile(directory, true); err != nil {
			return errors.Join(
				fmt.Errorf("secure private directory %q: %w", path, err),
				directory.Close(),
				filesystem.root.Remove(path),
			)
		}
	}
	if err := filesystem.validateOpened(path, directory, true); err != nil {
		return fmt.Errorf("validate private directory %q: %w", path, err)
	}
	if err := filesystem.syncDirectory(path, directory); err != nil {
		return fmt.Errorf("sync private directory %q: %w", path, err)
	}
	if err := filesystem.syncDirectoryPath(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync parent of private directory %q: %w", path, err)
	}
	return nil
}

// writeExclusiveFile publishes bytes only into a newly created private regular file handle.
func (filesystem *rootedFilesystem) writeExclusiveFile(path string, content []byte) (writeErr error) {
	if err := validateRelativePath(path); err != nil {
		return err
	}
	file, err := filesystem.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return fmt.Errorf("create private file %q: %w", path, err)
	}
	remove := true
	closed := false
	defer func() {
		if !closed {
			closeErr := file.Close()
			if closeErr != nil && writeErr == nil {
				writeErr = fmt.Errorf("close private file %q: %w", path, closeErr)
			}
		}
		if remove {
			removeErr := filesystem.root.Remove(path)
			if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				writeErr = errors.Join(writeErr, fmt.Errorf("remove incomplete private file %q: %w", path, removeErr))
			}
		}
	}()

	if err := platformSecureCreatedFile(file, false); err != nil {
		return fmt.Errorf("secure private file %q: %w", path, err)
	}
	if err := filesystem.validateOpened(path, file, false); err != nil {
		return fmt.Errorf("validate private file %q: %w", path, err)
	}
	if err := writeAll(file, content); err != nil {
		return fmt.Errorf("write private file %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private file %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close private file %q: %w", path, err)
	}
	closed = true
	remove = false
	return nil
}

// readBoundedFile rejects links, special files, and oversized content on the exact handle it reads.
func (filesystem *rootedFilesystem) readBoundedFile(path string, maximum int64) (content []byte, readErr error) {
	if err := validateRelativePath(path); err != nil {
		return nil, err
	}
	file, err := filesystem.openDirect(path, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			readErr = errors.Join(readErr, fmt.Errorf("close private file %q: %w", path, err))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect private file %q: %w", path, err)
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("private file %q exceeds %d bytes", path, maximum)
	}
	content, err = io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read private file %q: %w", path, err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("private file %q exceeds %d bytes", path, maximum)
	}
	return content, nil
}

// validateGenerationDirectory rejects partial pairs and unexpected durable files through one verified directory handle.
func (filesystem *rootedFilesystem) validateGenerationDirectory(path string) error {
	if err := validateRelativePath(path); err != nil {
		return err
	}
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return fmt.Errorf("open certificate generation %q: %w", path, err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(
			wrapError("read certificate generation", path, readErr),
			wrapError("close certificate generation", path, closeErr),
		)
	}
	if len(entries) != 2 {
		return fmt.Errorf("certificate generation %q contains %d entries, want 2", path, len(entries))
	}
	want := map[string]bool{certificateFilename: false, privateKeyFilename: false}
	for _, entry := range entries {
		seen, exists := want[entry.Name()]
		if !exists || seen || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("certificate generation %q contains unexpected entry %q", path, entry.Name())
		}
		want[entry.Name()] = true
	}
	return nil
}

// rename moves one confined object after proving every participating handle remains in the retained root.
func (filesystem *rootedFilesystem) rename(oldPath, newPath string, replace bool) error {
	if err := validateRelativePath(oldPath); err != nil {
		return err
	}
	if err := validateRelativePath(newPath); err != nil {
		return err
	}
	sourceInfo, err := filesystem.root.Lstat(oldPath)
	if err != nil {
		return fmt.Errorf("inspect rename source %q: %w", oldPath, err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() && !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("rename source %q is not a direct file or directory", oldPath)
	}
	source, err := filesystem.openDirect(oldPath, sourceInfo.IsDir())
	if err != nil {
		return fmt.Errorf("validate rename source %q: %w", oldPath, err)
	}
	defer source.Close()
	if !replace {
		if !sourceInfo.IsDir() {
			return fmt.Errorf("no-replace publication requires a nonempty directory source")
		}
		entries, err := source.ReadDir(1)
		if err != nil || len(entries) == 0 {
			return fmt.Errorf("no-replace publication requires a nonempty directory source: %w", err)
		}
	}

	parent, err := filesystem.openDirect(filepath.Dir(newPath), true)
	if err != nil {
		return fmt.Errorf("validate rename destination parent %q: %w", filepath.Dir(newPath), err)
	}
	defer parent.Close()
	if destinationInfo, err := filesystem.root.Lstat(newPath); err == nil {
		if !replace {
			return fs.ErrExist
		}
		if destinationInfo.Mode()&os.ModeSymlink != 0 || destinationInfo.IsDir() != sourceInfo.IsDir() || !destinationInfo.IsDir() && !destinationInfo.Mode().IsRegular() {
			return fmt.Errorf("rename destination %q is not the expected direct object type", newPath)
		}
		destination, openErr := filesystem.openDirect(newPath, destinationInfo.IsDir())
		if openErr != nil {
			return fmt.Errorf("validate rename destination %q: %w", newPath, openErr)
		}
		if closeErr := destination.Close(); closeErr != nil {
			return fmt.Errorf("close rename destination %q: %w", newPath, closeErr)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect rename destination %q: %w", newPath, err)
	}
	if err := filesystem.root.Rename(oldPath, newPath); err != nil {
		if !replace {
			if _, destinationErr := filesystem.root.Lstat(newPath); destinationErr == nil {
				return fs.ErrExist
			}
		}
		return err
	}
	sourceOpened, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect renamed source handle %q: %w", oldPath, err)
	}
	published, err := filesystem.openDirectHandle(newPath, sourceOpened.IsDir())
	if err != nil {
		return fmt.Errorf("inspect renamed destination %q: %w", newPath, err)
	}
	same, err := platformSameFile(source, published)
	closeErr := published.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("compare renamed destination %q: %w", newPath, err), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if !same {
		return fmt.Errorf("renamed destination %q does not match its verified source handle", newPath)
	}
	return nil
}

// syncDirectoryPath commits preceding child creation or rename metadata through one verified directory handle.
func (filesystem *rootedFilesystem) syncDirectoryPath(path string) error {
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return fmt.Errorf("open sync directory %q: %w", path, err)
	}
	syncErr := filesystem.syncDirectory(path, directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// removeAll removes only an internally generated staging subtree beneath the confined root.
func (filesystem *rootedFilesystem) removeAll(path string) error {
	if err := validateRelativePath(path); err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(path), ".staging-") {
		return fmt.Errorf("refuse to remove non-staging certificate path %q", path)
	}
	info, err := filesystem.root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect staging path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("staging path %q is not a direct file or directory", path)
	}
	handle, err := filesystem.openDirect(path, info.IsDir())
	if err != nil {
		return fmt.Errorf("validate staging path %q: %w", path, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close staging path %q: %w", path, err)
	}
	return filesystem.root.RemoveAll(path)
}

// lstat inspects one confined path without following its final component.
func (filesystem *rootedFilesystem) lstat(path string) (os.FileInfo, error) {
	if err := validateRelativePath(path); err != nil {
		return nil, err
	}
	return filesystem.root.Lstat(path)
}

// readDirectory returns deterministic caller-owned entry metadata from one verified directory handle.
func (filesystem *rootedFilesystem) readDirectory(path string) ([]os.DirEntry, error) {
	if err := validateRelativePath(path); err != nil {
		return nil, err
	}
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return entries, nil
}

// openDirect opens one direct child object and proves its pathname observation matches the retained handle.
func (filesystem *rootedFilesystem) openDirect(path string, directory bool) (*os.File, error) {
	file, err := filesystem.openDirectUnvalidated(path, directory)
	if err != nil {
		return nil, err
	}
	if err := filesystem.validateOpened(path, file, directory); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// openDirectUnvalidated rejects a link before opening and binds the result to the same direct object.
func (filesystem *rootedFilesystem) openDirectUnvalidated(path string, directory bool) (*os.File, error) {
	return filesystem.openDirectHandle(path, directory)
}

// openDirectHandle retains one direct object and confirms its rooted name still selects the same full identity.
func (filesystem *rootedFilesystem) openDirectHandle(path string, directory bool) (*os.File, error) {
	if path != "." {
		if err := validateRelativePath(path); err != nil {
			return nil, err
		}
	}
	observed, err := filesystem.root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if observed.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private path %q is not a direct object", path)
	}
	if directory && !observed.IsDir() {
		return nil, fmt.Errorf("private path %q is not a direct directory", path)
	}
	if !directory && !observed.Mode().IsRegular() {
		return nil, fmt.Errorf("private path %q is not a direct regular file", path)
	}
	file, err := filesystem.root.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if directory && !opened.IsDir() || !directory && !opened.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("private path %q changed to the wrong object type", path), file.Close())
	}
	reobserved, err := filesystem.root.Lstat(path)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if reobserved.Mode()&os.ModeSymlink != 0 || directory && !reobserved.IsDir() || !directory && !reobserved.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("private path %q changed while its handle opened", path), file.Close())
	}
	confirmed, err := filesystem.root.Open(path)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	same, err := platformSameFile(file, confirmed)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare private path %q handles: %w", path, err), confirmed.Close(), file.Close())
	}
	if !same {
		return nil, errors.Join(fmt.Errorf("private path %q changed while its handle opened", path), confirmed.Close(), file.Close())
	}
	final, err := filesystem.root.Lstat(path)
	if err != nil {
		return nil, errors.Join(err, confirmed.Close(), file.Close())
	}
	if final.Mode()&os.ModeSymlink != 0 || directory && !final.IsDir() || !directory && !final.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("private path %q changed while its handle opened", path), confirmed.Close(), file.Close())
	}
	if err := confirmed.Close(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// validateOpened applies platform policy to a handle and proves the rooted name still selects that object.
func (filesystem *rootedFilesystem) validateOpened(path string, file *os.File, directory bool) error {
	if err := validatePlatformFile(file, directory); err != nil {
		return err
	}
	current, err := filesystem.openDirectHandle(path, directory)
	if err != nil {
		return fmt.Errorf("private path %q changed after its handle opened: %w", path, err)
	}
	same, err := platformSameFile(file, current)
	closeErr := current.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("compare private path %q after its handle opened: %w", path, err), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if !same {
		return fmt.Errorf("private path %q changed after its handle opened", path)
	}
	return nil
}

// validateRelativePath rejects absolute, parent, volume, and empty paths before any rooted operation.
func validateRelativePath(path string) error {
	if path == "" || path == "." || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return fmt.Errorf("certificate material path %q is not a clean relative path", path)
	}
	return nil
}

// wrapError adds an object path only when an operation actually failed.
func wrapError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %q: %w", operation, path, err)
}

// writeAll treats an unexplained zero-byte write as a filesystem failure instead of spinning forever.
func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(content) {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}
