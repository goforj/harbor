package ticketissuer

// PoolPrerequisiteMissingError reports that the installer-owned helper boundary is absent.
type PoolPrerequisiteMissingError struct {
	cause error
}

// newPoolPrerequisiteMissingError retains the local filesystem diagnostic without making it peer-facing.
func newPoolPrerequisiteMissingError(cause error) *PoolPrerequisiteMissingError {
	return &PoolPrerequisiteMissingError{cause: cause}
}

// Error returns the daemon-local prerequisite diagnostic.
func (e *PoolPrerequisiteMissingError) Error() string {
	if e == nil || e.cause == nil {
		return "helper pool prerequisite is missing"
	}

	return "helper pool prerequisite is missing: " + e.cause.Error()
}

// Unwrap preserves the filesystem cause for daemon diagnostics only.
func (e *PoolPrerequisiteMissingError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}
