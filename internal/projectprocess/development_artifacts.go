package projectprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const developmentArtifactDirectoryName = "development-artifacts"

// developmentArtifactRoot retains a rooted parent handle so cleanup cannot be redirected after launch.
type developmentArtifactRoot struct {
	parent *os.Root
	name   string
	path   string
}

// resolveDevelopmentArtifactDirectory returns Harbor's per-user runtime root for generated development artifacts.
func resolveDevelopmentArtifactDirectory() string {
	directory, err := runtimepath.Directory()
	if err != nil {
		return ""
	}
	return directory
}

// developmentArtifactPath derives an opaque session path outside the registered checkout.
func developmentArtifactPath(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (string, string, error) {
	if err := projectID.Validate(); err != nil {
		return "", "", fmt.Errorf("validate development artifact project ID: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return "", "", fmt.Errorf("validate development artifact session ID: %w", err)
	}
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", "", fmt.Errorf("development artifact directory must be a clean absolute path")
	}
	digest := sha256.Sum256(append(append([]byte(string(projectID)), 0), []byte(sessionID)...))
	name := hex.EncodeToString(digest[:])
	return filepath.Join(directory, developmentArtifactDirectoryName, name), name, nil
}

// prepareDevelopmentArtifactRoot creates one owner-private session root and retains its parent authority.
func prepareDevelopmentArtifactRoot(directory string, projectID domain.ProjectID, sessionID domain.SessionID) (*developmentArtifactRoot, error) {
	path, name, err := developmentArtifactPath(directory, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	if information, inspectErr := os.Lstat(directory); inspectErr == nil {
		if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			return nil, fmt.Errorf("development artifact root is not a direct directory")
		}
	} else if !errors.Is(inspectErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect development artifact root: %w", inspectErr)
	}
	parentPath := filepath.Dir(path)
	if err := os.MkdirAll(parentPath, 0o700); err != nil {
		return nil, fmt.Errorf("create development artifact directory: %w", err)
	}
	information, err := os.Lstat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("inspect development artifact directory: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return nil, fmt.Errorf("development artifact directory is not an owner-private directory")
	}
	if err := os.Chmod(parentPath, 0o700); err != nil {
		return nil, fmt.Errorf("secure development artifact directory: %w", err)
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open development artifact directory: %w", err)
	}
	if err := parent.RemoveAll(name); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("retire interrupted development artifact root: %w", err)
	}
	if err := parent.Mkdir(name, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("create development artifact root: %w", err)
	}
	if err := parent.Chmod(name, 0o700); err != nil {
		_ = parent.RemoveAll(name)
		_ = parent.Close()
		return nil, fmt.Errorf("secure development artifact root: %w", err)
	}
	return &developmentArtifactRoot{
		parent: parent,
		name:   name,
		path:   path,
	}, nil
}

// remove deletes only this session's rooted artifact tree after its process scope has settled.
func (root *developmentArtifactRoot) remove() error {
	if root == nil || root.parent == nil {
		return nil
	}
	removeErr := root.parent.RemoveAll(root.name)
	closeErr := root.parent.Close()
	root.parent = nil
	if removeErr != nil {
		removeErr = fmt.Errorf("remove development artifact root: %w", removeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close development artifact directory: %w", closeErr)
	}
	return errors.Join(removeErr, closeErr)
}

// retain closes Harbor's directory authority while leaving uncertain live-process artifacts untouched.
func (root *developmentArtifactRoot) retain() error {
	if root == nil || root.parent == nil {
		return nil
	}
	err := root.parent.Close()
	root.parent = nil
	if err != nil {
		return fmt.Errorf("close retained development artifact directory: %w", err)
	}
	return nil
}
