package projectdiscovery

import (
	"errors"
	"io/fs"
)

// ProjectRootNotFoundError identifies a registered project whose checkout path no longer resolves.
type ProjectRootNotFoundError struct {
	Path  string
	cause error
}

// Error preserves a bounded diagnostic for daemon logs without misclassifying the project configuration.
func (err *ProjectRootNotFoundError) Error() string {
	if err == nil {
		return "project root was not found"
	}
	if err.Path == "" {
		return "project root was not found"
	}
	return "project root " + err.Path + " was not found"
}

// Unwrap retains the native filesystem cause for local diagnostics.
func (err *ProjectRootNotFoundError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// InvalidProjectError identifies a selected path or project metadata shape that the user can correct.
type InvalidProjectError struct {
	cause error
}

// Error preserves the concrete discovery diagnostic for daemon-local inspection.
func (err *InvalidProjectError) Error() string {
	if err == nil || err.cause == nil {
		return "invalid GoForj project selection"
	}
	return err.cause.Error()
}

// Unwrap keeps the underlying semantic error available to errors.Is and errors.As callers.
func (err *InvalidProjectError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// invalidProjectError marks only correctable selection and metadata failures for transport classification.
func invalidProjectError(err error) error {
	if err == nil {
		return nil
	}
	var invalid *InvalidProjectError
	if errors.As(err, &invalid) {
		return err
	}
	return &InvalidProjectError{cause: err}
}

// projectRootNotFoundError separates a missing checkout from malformed files inside an existing checkout.
func projectRootNotFoundError(path string, cause error) error {
	return &ProjectRootNotFoundError{Path: path, cause: cause}
}

// isInvalidProjectFilesystemError limits user classification to absent or unreadable selected files.
func isInvalidProjectFilesystemError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}
