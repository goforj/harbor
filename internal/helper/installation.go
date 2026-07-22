package helper

import "fmt"

// MaximumInstallationIDLength is the wire-safe byte limit shared by daemon identity planning and helper admission.
const MaximumInstallationIDLength = 128

// ValidateInstallationID rejects installation identities that cannot cross the privileged helper boundary canonically.
func ValidateInstallationID(value string) error {
	if value == "" {
		return fmt.Errorf("installation ID is required")
	}
	if len(value) > MaximumInstallationIDLength {
		return fmt.Errorf("installation ID exceeds %d bytes", MaximumInstallationIDLength)
	}
	if !installationIDAlphanumeric(value[0]) || !installationIDAlphanumeric(value[len(value)-1]) {
		return fmt.Errorf("installation ID must start and end with an ASCII letter or digit")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if installationIDAlphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("installation ID contains a character outside ASCII letters, digits, dots, underscores, and hyphens")
	}
	return nil
}

// ValidateRequesterIdentity rejects identities that cannot cross the helper boundary canonically.
func ValidateRequesterIdentity(value string) error {
	if value == "" {
		return fmt.Errorf("requester identity is required")
	}
	if len(value) > MaximumRequesterIdentityLength {
		return fmt.Errorf("requester identity exceeds %d bytes", MaximumRequesterIdentityLength)
	}
	if !installationIDAlphanumeric(value[0]) || !installationIDAlphanumeric(value[len(value)-1]) {
		return fmt.Errorf("requester identity must start and end with an ASCII letter or digit")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if installationIDAlphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("requester identity contains a character outside ASCII letters, digits, dots, underscores, and hyphens")
	}
	return nil
}

// installationIDAlphanumeric keeps installation identity boundaries independent from path-like punctuation.
func installationIDAlphanumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}
