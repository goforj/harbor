package ticketkey

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

// rootedFilesystem confines every store-generated path beneath one verified private directory handle.
type rootedFilesystem struct {
	root       *os.Root
	beforeOpen func(string)
}

// openRootedFilesystem creates or verifies the private root before retaining a confined operating-system handle.
func openRootedFilesystem(directory string, afterValidation func(), beforeOpen func(string)) (*rootedFilesystem, error) {
	if directory == "" {
		return nil, errors.New("open helper ticket key store: directory is empty")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("open helper ticket key store: directory %q is not absolute", directory)
	}
	directory = filepath.Clean(directory)
	if err := preparePlatformRoot(directory); err != nil {
		return nil, fmt.Errorf("prepare helper ticket key directory: %w", err)
	}
	validated, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, fmt.Errorf("retain validated helper ticket key directory: %w", err)
	}
	if err := validatePlatformFile(validated, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate retained helper ticket key directory: %w", err), validated.Close())
	}
	if afterValidation != nil {
		afterValidation()
	}

	current, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("helper ticket key directory changed before its rooted handle opened: %w", err), validated.Close())
	}
	same, err := platformSameFile(validated, current)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare helper ticket key directory handles: %w", err), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(errors.New("helper ticket key directory changed before its rooted handle opened"), current.Close(), validated.Close())
	}
	if err := validatePlatformFile(current, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate current helper ticket key directory: %w", err), current.Close(), validated.Close())
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("helper ticket key directory changed before its rooted handle opened: %w", err), current.Close(), validated.Close())
	}
	filesystem := &rootedFilesystem{root: root, beforeOpen: beforeOpen}
	opened, err := filesystem.openDirectUnvalidated(".", true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect opened helper ticket key directory: %w", err), root.Close(), current.Close(), validated.Close())
	}
	same, err = platformSameFile(current, opened)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare helper ticket key directory handles: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(errors.New("helper ticket key directory changed before its rooted handle opened"), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := filesystem.validateOpened(".", opened, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate opened helper ticket key directory: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	verified, err := openPlatformFileNoFollow(directory, true)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("helper ticket key directory changed after its rooted handle opened: %w", err), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	same, err = platformSameFile(current, verified)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare verified helper ticket key directory handles: %w", err), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if !same {
		return nil, errors.Join(errors.New("helper ticket key directory changed after its rooted handle opened"), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := validatePlatformFile(verified, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate verified helper ticket key directory: %w", err), verified.Close(), opened.Close(), root.Close(), current.Close(), validated.Close())
	}
	if err := errors.Join(verified.Close(), opened.Close(), current.Close(), validated.Close()); err != nil {
		return nil, errors.Join(fmt.Errorf("close helper ticket key directory verification handles: %w", err), root.Close())
	}
	return filesystem, nil
}

// Close releases the rooted filesystem handle without removing durable key material.
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
	if created {
		if err := platformSecureCreatedFile(directory, true); err != nil {
			return errors.Join(fmt.Errorf("secure private directory %q: %w", path, err), directory.Close(), filesystem.root.Remove(path))
		}
	}
	if err := filesystem.validateOpened(path, directory, true); err != nil {
		closeErr := directory.Close()
		if created {
			return errors.Join(fmt.Errorf("validate private directory %q: %w", path, err), closeErr, filesystem.root.Remove(path))
		}
		return errors.Join(fmt.Errorf("validate private directory %q: %w", path, err), closeErr)
	}
	syncErr := platformSyncDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync private directory %q: %w", path, err)
	}
	if err := filesystem.syncDirectory("."); err != nil {
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
			writeErr = errors.Join(writeErr, file.Close())
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

// validateDirectory requires one immutable regular file and rejects hidden or partial state.
func (filesystem *rootedFilesystem) validateDirectory(path string, expected string) error {
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return err
	}
	validationErr := validateDirectoryHandle(directory, path, expected)
	closeErr := directory.Close()
	return errors.Join(validationErr, closeErr)
}

// validateDirectoryHandle requires one immutable regular file while retaining the directory identity for its caller.
func validateDirectoryHandle(directory *os.File, path string, expected string) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) != 1 {
		return fmt.Errorf("private directory %q contains %d entries, want 1", path, len(entries))
	}
	entry := entries[0]
	if entry.Name() != expected || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("private directory %q contains unexpected entry %q", path, entry.Name())
	}
	return nil
}

// renameDirectoryNoReplace atomically publishes one nonempty directory without replacing a concurrent winner.
func (filesystem *rootedFilesystem) renameDirectoryNoReplace(oldPath string, newPath string) error {
	if err := validateRelativePath(oldPath); err != nil {
		return err
	}
	if err := validateRelativePath(newPath); err != nil {
		return err
	}
	source, err := filesystem.openDirect(oldPath, true)
	if err != nil {
		return fmt.Errorf("validate publication source %q: %w", oldPath, err)
	}
	defer source.Close()
	entries, err := source.ReadDir(1)
	if err != nil || len(entries) == 0 {
		return fmt.Errorf("publication source %q is empty: %w", oldPath, err)
	}
	if _, err := filesystem.root.Lstat(newPath); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect publication destination %q: %w", newPath, err)
	}
	if err := filesystem.root.Rename(oldPath, newPath); err != nil {
		if _, destinationErr := filesystem.root.Lstat(newPath); destinationErr == nil {
			return fs.ErrExist
		}
		return err
	}
	sourceInfo, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect published source handle %q: %w", oldPath, err)
	}
	published, err := filesystem.openDirectHandle(newPath, sourceInfo.IsDir())
	if err != nil {
		return fmt.Errorf("inspect published destination %q: %w", newPath, err)
	}
	same, err := platformSameFile(source, published)
	closeErr := published.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("compare published destination %q: %w", newPath, err), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if !same {
		return fmt.Errorf("published destination %q does not match its verified source handle", newPath)
	}
	return filesystem.syncDirectory(".")
}

// removeStaging removes only the caller-generated incomplete subtree beneath the confined root.
func (filesystem *rootedFilesystem) removeStaging(path string) error {
	if err := validateRelativePath(path); err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(path), ".staging-") {
		return fmt.Errorf("refuse to remove non-staging helper ticket key path %q", path)
	}
	info, err := filesystem.root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("staging path %q is not a direct directory", path)
	}
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	if err := filesystem.root.RemoveAll(path); err != nil {
		return err
	}
	return filesystem.syncDirectory(".")
}

// lstat inspects one confined path without following its final component.
func (filesystem *rootedFilesystem) lstat(path string) (os.FileInfo, error) {
	if err := validateRelativePath(path); err != nil {
		return nil, err
	}
	return filesystem.root.Lstat(path)
}

// syncDirectory commits metadata through one verified directory handle.
func (filesystem *rootedFilesystem) syncDirectory(path string) error {
	directory, err := filesystem.openDirect(path, true)
	if err != nil {
		return err
	}
	syncErr := platformSyncDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// openDirect opens one direct object and proves its pathname observation matches the retained handle.
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

// openDirectUnvalidated binds one direct pathname to a retained handle before platform policy is inspected.
func (filesystem *rootedFilesystem) openDirectUnvalidated(path string, directory bool) (*os.File, error) {
	observed, err := filesystem.openDirectHandle(path, directory)
	if err != nil {
		return nil, err
	}
	if filesystem.beforeOpen != nil {
		filesystem.beforeOpen(path)
	}
	file, err := filesystem.openDirectHandle(path, directory)
	if err != nil {
		return nil, errors.Join(err, observed.Close())
	}
	same, err := platformSameFile(observed, file)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compare private path %q handles: %w", path, err), file.Close(), observed.Close())
	}
	if !same {
		return nil, errors.Join(fmt.Errorf("private path %q changed while its handle opened", path), file.Close(), observed.Close())
	}
	if err := observed.Close(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
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

// validateOpened applies platform policy and proves the rooted name still selects the opened object.
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

// validateRelativePath rejects absolute, parent, volume, and empty paths before rooted operations.
func validateRelativePath(path string) error {
	if path == "" || path == "." || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return fmt.Errorf("helper ticket key path %q is not a clean relative path", path)
	}
	return nil
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
