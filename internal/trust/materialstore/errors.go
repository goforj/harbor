package materialstore

import (
	"errors"
	"fmt"
)

var (
	// ErrAuthorityNotInitialized reports that no active local CA has been persisted.
	ErrAuthorityNotInitialized = errors.New("local certificate authority is not initialized")
	// ErrAuthorityAlreadyInitialized reports that creation cannot replace an existing local CA identity.
	ErrAuthorityAlreadyInitialized = errors.New("local certificate authority is already initialized")
	// ErrLeafNotFound reports that no active certificate exists for one canonical host set.
	ErrLeafNotFound = errors.New("local certificate is not persisted")
	// ErrStoreClosed reports use after the material store released its rooted filesystem handle.
	ErrStoreClosed = errors.New("certificate material store is closed")
)

// CorruptionError identifies persisted material that cannot be trusted without exposing its contents.
type CorruptionError struct {
	// Component identifies the bounded store object that failed validation.
	Component string
	// Cause retains the parsing, filesystem, or certificate-policy failure.
	Cause error
}

// Error describes the failed component without formatting certificate or private-key bytes.
func (err *CorruptionError) Error() string {
	component := "material"
	if err != nil && err.Component != "" {
		component = err.Component
	}
	reason := "validation failed"
	if err != nil && err.Cause != nil {
		reason = err.Cause.Error()
	}
	return fmt.Sprintf("certificate material store %s is corrupt: %s", component, reason)
}

// Unwrap exposes the underlying validation failure for exact error classification.
func (err *CorruptionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// corrupt scopes a validation failure to one non-secret store component.
func corrupt(component string, cause error) error {
	return &CorruptionError{Component: component, Cause: cause}
}
