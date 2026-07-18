package projectdiscovery

import (
	"errors"
	"io/fs"
)

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

// isInvalidProjectFilesystemError limits user classification to absent or unreadable selected files.
func isInvalidProjectFilesystemError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}
