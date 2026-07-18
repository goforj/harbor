package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maximumIdentifierBytes = 256

// Sequence orders authoritative Harbor snapshots and state events.
type Sequence uint64

// MaximumSequence keeps JSON ordering exact across Go and JavaScript clients.
const MaximumSequence Sequence = 1<<53 - 1

// ProjectID identifies one registered Harbor project independently of its path or slug.
type ProjectID string

// Validate reports whether the project ID is safe to use as a stable identity.
func (id ProjectID) Validate() error {
	return validateIdentifier("project ID", string(id))
}

// AppID identifies one GoForj App within a project.
type AppID string

// Validate reports whether the App ID is safe to use as a stable identity.
func (id AppID) Validate() error {
	return validateIdentifier("App ID", string(id))
}

// ServiceID identifies one service within a project.
type ServiceID string

// Validate reports whether the service ID is safe to use as a stable identity.
func (id ServiceID) Validate() error {
	return validateIdentifier("service ID", string(id))
}

// ResourceID identifies one resource within a project.
type ResourceID string

// Validate reports whether the resource ID is safe to use as a stable identity.
func (id ResourceID) Validate() error {
	return validateIdentifier("resource ID", string(id))
}

// OperationID identifies one daemon-owned operation.
type OperationID string

// Validate reports whether the operation ID is safe to use as a stable identity.
func (id OperationID) Validate() error {
	return validateIdentifier("operation ID", string(id))
}

// IntentID identifies one logical mutation across client retries and reconnects.
type IntentID string

// Validate reports whether the intent ID is safe to use as an idempotency identity.
func (id IntentID) Validate() error {
	return validateIdentifier("intent ID", string(id))
}

// validateIdentifier keeps the domain neutral about how IDs are generated while excluding values that cannot be represented safely in logs and protocol fixtures.
func validateIdentifier(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", kind)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", kind)
	}
	if len(value) > maximumIdentifierBytes {
		return fmt.Errorf("%s must not exceed %d bytes", kind, maximumIdentifierBytes)
	}
	if containsControlCharacter(value) {
		return fmt.Errorf("%s must not contain control characters", kind)
	}
	return nil
}

// containsControlCharacter reports whether text would make logs or protocol fixtures ambiguous.
func containsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
