package ticketkey

import (
	"errors"
	"fmt"
)

var (
	// ErrStoreClosed reports use after the signing-key store released its rooted filesystem handle.
	ErrStoreClosed = errors.New("helper ticket key store is closed")
)

// CorruptionError identifies persisted signing-key material that cannot be trusted without exposing it.
type CorruptionError struct {
	// Component identifies the bounded store object that failed validation.
	Component string
	// Cause retains the parsing or filesystem-policy failure.
	Cause error
}

// Error describes the failed component without formatting signing-key bytes.
func (err *CorruptionError) Error() string {
	component := "material"
	if err != nil && err.Component != "" {
		component = err.Component
	}
	reason := "validation failed"
	if err != nil && err.Cause != nil {
		reason = err.Cause.Error()
	}
	return fmt.Sprintf("helper ticket key store %s is corrupt: %s", component, reason)
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
