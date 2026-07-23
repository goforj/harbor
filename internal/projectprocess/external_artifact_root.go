package projectprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/userpaths"
)

const externalArtifactRootMarker = ".harbor-artifact-root"

// externalArtifactRootCapability retains the exact directory identity Harbor created or admitted for a session.
type externalArtifactRootCapability struct {
	path     string
	base     string
	name     string
	marker   []byte
	identity os.FileInfo
	created  bool
}

// expectedExternalArtifactRoot derives the sole artifact directory a project session may receive.
func expectedExternalArtifactRoot(projectID domain.ProjectID, sessionID domain.SessionID) (string, string, []byte, error) {
	dataDirectory, err := userpaths.DataDirectory()
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Harbor data directory: %w", err)
	}
	if !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return "", "", nil, errors.New("Harbor data directory must be an absolute clean path")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(projectID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(sessionID))
	name := hex.EncodeToString(hash.Sum(nil))
	base := filepath.Join(dataDirectory, "dev-artifacts")
	marker := []byte("harbor-artifact-root-v1\nproject=" + string(projectID) + "\nsession=" + string(sessionID) + "\ndigest=" + name + "\n")
	return filepath.Join(base, name), base, marker, nil
}

// prepareExternalArtifactRoot creates or reopens only Harbor's exact direct artifact directory.
func prepareExternalArtifactRoot(projectID domain.ProjectID, sessionID domain.SessionID, path string) (*externalArtifactRootCapability, error) {
	expected, base, marker, err := expectedExternalArtifactRoot(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	if path != expected {
		return nil, errors.New("external artifact root does not match the Harbor project session")
	}
	root, err := openOrCreateDirectDirectory(base)
	if err != nil {
		return nil, fmt.Errorf("open Harbor external artifact base: %w", err)
	}
	defer root.Close()
	name := filepath.Base(expected)
	information, err := root.Lstat(name)
	created := false
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(name, 0o700); err != nil {
			return nil, fmt.Errorf("create external development artifact root: %w", err)
		}
		created = true
		information, err = root.Lstat(name)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect external development artifact root: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return nil, errors.New("external development artifact root must be a direct directory")
	}
	directory, err := openRetainedDirectDirectory(root, name, information)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	if created {
		if err := writeExternalArtifactRootMarker(directory, marker); err != nil {
			return nil, err
		}
	}
	if err := verifyExternalArtifactRootMarker(directory, marker); err != nil {
		return nil, err
	}
	return &externalArtifactRootCapability{
		path:     expected,
		base:     base,
		name:     name,
		marker:   marker,
		identity: information,
		created:  created,
	}, nil
}

// writeExternalArtifactRootMarker creates the marker once so a link cannot redirect an ownership write.
func writeExternalArtifactRootMarker(directory *os.Root, marker []byte) error {
	file, err := directory.OpenFile(externalArtifactRootMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create external development artifact root marker: %w", err)
	}
	written, writeErr := file.Write(marker)
	if writeErr == nil && written != len(marker) {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(writeErr, file.Sync(), file.Close())
}

// removeExternalArtifactRoot retires only the exact direct directory and marker admitted at launch.
func removeExternalArtifactRoot(capability *externalArtifactRootCapability) error {
	return removeExternalArtifactRootWithHook(capability, nil)
}

// removeExternalArtifactRootWithHook exposes the post-content race boundary without mutable package-global test state.
func removeExternalArtifactRootWithHook(capability *externalArtifactRootCapability, afterContentsRemoved func()) error {
	if capability == nil {
		return nil
	}
	root, err := openDirectDirectory(capability.base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open Harbor external artifact base for cleanup: %w", err)
	}
	defer root.Close()
	information, err := root.Lstat(capability.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect external development artifact root for cleanup: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() || !os.SameFile(capability.identity, information) {
		return errors.New("external development artifact root changed before cleanup")
	}
	directory, err := openRetainedDirectDirectory(root, capability.name, information)
	if err != nil {
		return err
	}
	if err := verifyExternalArtifactRootMarker(directory, capability.marker); err != nil {
		_ = directory.Close()
		return err
	}
	if err := removeExternalArtifactRootContents(directory); err != nil {
		_ = directory.Close()
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	if afterContentsRemoved != nil {
		afterContentsRemoved()
	}
	current, err := root.Lstat(capability.name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(capability.identity, current) {
		if err != nil {
			return fmt.Errorf("inspect external development artifact root before removal: %w", err)
		}
		return errors.New("external development artifact root changed before removal")
	}
	if err := root.Remove(capability.name); err != nil {
		return fmt.Errorf("remove external development artifact root: %w", err)
	}
	return nil
}

// removeExternalArtifactRootContents recursively removes artifacts only through the retained owned-directory descriptor.
func removeExternalArtifactRootContents(directory *os.Root) error {
	opened, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open external development artifact root contents: %w", err)
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	for _, entry := range entries {
		if err := directory.RemoveAll(entry.Name()); err != nil {
			return fmt.Errorf("remove external development artifact %q: %w", entry.Name(), err)
		}
	}
	return nil
}

// verifyExternalArtifactRootMarker rejects a directory whose session ownership cannot be proved directly.
func verifyExternalArtifactRootMarker(directory *os.Root, want []byte) error {
	information, err := directory.Lstat(externalArtifactRootMarker)
	if err != nil {
		return fmt.Errorf("inspect external development artifact root marker: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return errors.New("external development artifact root marker must be a direct regular file")
	}
	got, err := directory.ReadFile(externalArtifactRootMarker)
	if err != nil {
		return fmt.Errorf("read external development artifact root marker: %w", err)
	}
	if string(got) != string(want) {
		return errors.New("external development artifact root marker does not match the Harbor project session")
	}
	return nil
}

// openOrCreateDirectDirectory walks from the volume root so no path component can redirect Harbor through a symbolic link.
func openOrCreateDirectDirectory(path string) (*os.Root, error) {
	rootPath, segments, err := artifactPathSegments(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		information, statErr := root.Lstat(segment)
		if errors.Is(statErr, fs.ErrNotExist) {
			if err := root.Mkdir(segment, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				root.Close()
				return nil, err
			}
			information, statErr = root.Lstat(segment)
		}
		if statErr != nil || information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			root.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("directory component %q is not a direct directory", segment)
		}
		next, openErr := openRetainedDirectDirectory(root, segment, information)
		root.Close()
		if openErr != nil {
			return nil, openErr
		}
		root = next
	}
	return root, nil
}

// openDirectDirectory opens an existing absolute directory through only direct components.
func openDirectDirectory(path string) (*os.Root, error) {
	rootPath, segments, err := artifactPathSegments(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		information, statErr := root.Lstat(segment)
		if statErr != nil || information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			root.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("directory component %q is not a direct directory", segment)
		}
		next, openErr := openRetainedDirectDirectory(root, segment, information)
		root.Close()
		if openErr != nil {
			return nil, openErr
		}
		root = next
	}
	return root, nil
}

// openRetainedDirectDirectory keeps a directory descriptor only after confirming the parent entry did not change.
func openRetainedDirectDirectory(parent *os.Root, name string, expected os.FileInfo) (*os.Root, error) {
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := directory.Open(".")
	if err != nil {
		directory.Close()
		return nil, err
	}
	openedInformation, statErr := opened.Stat()
	closeErr := opened.Close()
	current, currentErr := parent.Lstat(name)
	if statErr != nil || closeErr != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(expected, openedInformation) || !os.SameFile(openedInformation, current) {
		directory.Close()
		return nil, fmt.Errorf("directory %q changed while opening", name)
	}
	return directory, nil
}

// artifactPathSegments converts one clean absolute path into direct filesystem components.
func artifactPathSegments(path string) (string, []string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, errors.New("artifact path must be an absolute clean path")
	}
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(path[len(volume):], string(filepath.Separator))
	if relative == "" {
		return root, nil, nil
	}
	return root, strings.Split(relative, string(filepath.Separator)), nil
}
