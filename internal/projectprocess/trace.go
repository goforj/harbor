package projectprocess

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

const (
	maximumProjectLaunchTraceBytes = 4 << 20
	projectLaunchTraceDirectory    = "_data/harbor"
	projectLaunchTraceFilename     = "forj-dev.log"
	projectLaunchTraceTruncated    = "\n[Harbor launch trace truncated]\n"
)

// projectLaunchTrace keeps one launch diagnostic bounded while presenting ordinary writer semantics to the output relay.
type projectLaunchTrace struct {
	file      *os.File
	remaining int64
}

// openProjectLaunchTrace replaces the prior launch trace with a private record for the accepted project session.
func openProjectLaunchTrace(
	checkoutRoot string,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	startedAt time.Time,
) (io.WriteCloser, error) {
	directory := filepath.Join(checkoutRoot, filepath.FromSlash(projectLaunchTraceDirectory))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create project launch trace directory: %w", err)
	}
	path := filepath.Join(directory, projectLaunchTraceFilename)
	trace, err := newProjectLaunchTrace(path, maximumProjectLaunchTraceBytes)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf(
		"Harbor managed forj dev\nproject=%s\nsession=%s\nstarted=%s\ncheckout=%s\n\n",
		projectID,
		sessionID,
		startedAt.UTC().Format(time.RFC3339Nano),
		checkoutRoot,
	)
	if _, err := io.WriteString(trace, header); err != nil {
		return nil, errors.Join(fmt.Errorf("write project launch trace header: %w", err), trace.Close())
	}
	return trace, nil
}

// newProjectLaunchTrace opens one owner-private file and enforces the supplied byte ceiling across every write.
func newProjectLaunchTrace(path string, maximumBytes int64) (*projectLaunchTrace, error) {
	if maximumBytes <= int64(len(projectLaunchTraceTruncated)) {
		return nil, fmt.Errorf("project launch trace limit must exceed %d bytes", len(projectLaunchTraceTruncated))
	}
	information, err := os.Lstat(path)
	if err == nil && !information.Mode().IsRegular() {
		return nil, fmt.Errorf("project launch trace %q must be a direct regular file", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect project launch trace %q: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open project launch trace %q: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("secure project launch trace %q: %w", path, err), file.Close())
	}
	return &projectLaunchTrace{file: file, remaining: maximumBytes}, nil
}

// Write records complete launch output until the bounded file has emitted one visible truncation marker.
func (trace *projectLaunchTrace) Write(bytes []byte) (int, error) {
	length := len(bytes)
	if length == 0 || trace.remaining == 0 {
		return length, nil
	}
	if int64(length) <= trace.remaining {
		written, err := trace.file.Write(bytes)
		trace.remaining -= int64(written)
		return written, err
	}

	marker := []byte(projectLaunchTraceTruncated)
	prefixBytes := trace.remaining - int64(len(marker))
	if prefixBytes < 0 {
		prefixBytes = 0
	}
	if prefixBytes > 0 {
		written, err := trace.file.Write(bytes[:prefixBytes])
		trace.remaining -= int64(written)
		if err != nil || int64(written) != prefixBytes {
			if err == nil {
				err = io.ErrShortWrite
			}
			return written, err
		}
	}
	writtenMarker, err := trace.file.Write(marker)
	trace.remaining -= int64(writtenMarker)
	if err != nil || writtenMarker != len(marker) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return int(prefixBytes) + writtenMarker, err
	}
	trace.remaining = 0
	return length, nil
}

// Close flushes the latest diagnostics before releasing the project trace file.
func (trace *projectLaunchTrace) Close() error {
	return errors.Join(trace.file.Sync(), trace.file.Close())
}

// projectLaunchTracePath returns the stable ignored path used by runtime diagnostics and future desktop log views.
func projectLaunchTracePath(checkoutRoot string) string {
	return filepath.Join(checkoutRoot, filepath.FromSlash(projectLaunchTraceDirectory), projectLaunchTraceFilename)
}

// removeProjectLaunchTrace removes Harbor's settled-lifecycle diagnostic and empty Harbor-only parent directories.
func removeProjectLaunchTrace(checkoutRoot string) error {
	root, err := os.OpenRoot(checkoutRoot)
	if err != nil {
		return fmt.Errorf("open checkout root for project launch trace cleanup: %w", err)
	}
	defer root.Close()
	dataRoot, err := openDirectProjectLaunchTraceDirectory(root, "_data")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dataRoot.Close()
	harborRoot, err := openDirectProjectLaunchTraceDirectory(dataRoot, "harbor")
	if errors.Is(err, fs.ErrNotExist) {
		return removeEmptyProjectLaunchTraceDirectory(root, "_data", dataRoot)
	}
	if err != nil {
		return err
	}
	defer harborRoot.Close()
	information, err := harborRoot.Lstat(projectLaunchTraceFilename)
	if err == nil {
		if !information.Mode().IsRegular() {
			return fmt.Errorf("project launch trace %q must be a direct regular file", projectLaunchTraceFilename)
		}
		if err := harborRoot.Remove(projectLaunchTraceFilename); err != nil {
			return fmt.Errorf("remove project launch trace %q: %w", projectLaunchTraceFilename, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect project launch trace %q: %w", projectLaunchTraceFilename, err)
	}
	if err := removeEmptyProjectLaunchTraceDirectory(dataRoot, "harbor", harborRoot); err != nil {
		return err
	}
	return removeEmptyProjectLaunchTraceDirectory(root, "_data", dataRoot)
}

// openDirectProjectLaunchTraceDirectory retains one direct directory after proving its descriptor still names the observed entry.
func openDirectProjectLaunchTraceDirectory(root *os.Root, path string) (*os.Root, error) {
	information, err := root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("inspect project launch trace directory %q: %w", path, err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return nil, fmt.Errorf("project launch trace directory %q must be a direct directory", path)
	}
	directory, err := root.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open project launch trace directory %q: %w", path, err)
	}
	opened, err := directory.Open(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("retain project launch trace directory %q: %w", path, err), directory.Close())
	}
	openedInformation, statErr := opened.Stat()
	closeErr := opened.Close()
	current, currentErr := root.Lstat(path)
	if statErr != nil || closeErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(information, openedInformation) || !os.SameFile(openedInformation, current) {
		return nil, errors.Join(fmt.Errorf("project launch trace directory %q changed while opening", path), directory.Close())
	}
	return directory, nil
}

// removeEmptyProjectLaunchTraceDirectory removes only a direct empty directory, preserving project files and links.
func removeEmptyProjectLaunchTraceDirectory(parent *os.Root, path string, directory *os.Root) error {
	openedDirectory, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open project launch trace directory %q: %w", path, err)
	}
	opened, statErr := openedDirectory.Stat()
	current, currentErr := parent.Lstat(path)
	if statErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(opened, current) {
		return errors.Join(fmt.Errorf("project launch trace directory %q changed before removal", path), openedDirectory.Close())
	}
	_, readErr := openedDirectory.ReadDir(1)
	closeErr := openedDirectory.Close()
	if readErr == nil {
		return closeErr
	}
	if !errors.Is(readErr, io.EOF) {
		return errors.Join(fmt.Errorf("inspect project launch trace directory %q: %w", path, readErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := parent.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove empty project launch trace directory %q: %w", path, err)
	}
	return nil
}
