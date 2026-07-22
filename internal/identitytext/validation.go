// Package identitytext validates canonical installation and requester identifiers.
package identitytext

import "fmt"

const (
	// MaximumInstallationIDLength bounds one canonical installation identifier in bytes.
	MaximumInstallationIDLength = 128
	// MaximumRequesterIdentityLength bounds one canonical requester identity in bytes.
	MaximumRequesterIdentityLength = 256
)

// ValidateInstallationID rejects installation identities outside the canonical cross-process text shape.
func ValidateInstallationID(value string) error {
	return validate(value, MaximumInstallationIDLength, "installation ID")
}

// ValidateRequesterIdentity rejects requester identities outside the canonical cross-process text shape.
func ValidateRequesterIdentity(value string) error {
	return validate(value, MaximumRequesterIdentityLength, "requester identity")
}

// validate applies the shared canonical identifier alphabet without admitting path-like punctuation.
func validate(value string, maximumLength int, name string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximumLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumLength)
	}
	if !alphanumeric(value[0]) || !alphanumeric(value[len(value)-1]) {
		return fmt.Errorf("%s must start and end with an ASCII letter or digit", name)
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if alphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("%s contains a character outside ASCII letters, digits, dots, underscores, and hyphens", name)
	}
	return nil
}

// alphanumeric keeps identifier validation independent from path-like punctuation.
func alphanumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}
