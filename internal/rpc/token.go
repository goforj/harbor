package rpc

import (
	"fmt"
	"unicode"
)

const (
	maxCapabilityLength = 128
	maxRequestIDLength  = 128
	maxMethodLength     = 128
	maxEventNameLength  = 128
	maxVersionLength    = 128
)

// validateWireToken rejects ambiguous or control-bearing values before they
// reach dispatch, logs, or diagnostics.
func validateWireToken(name string, value string, maxLength int) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maxLength)
	}
	for _, character := range value {
		if character > unicode.MaxASCII || !isWireTokenCharacter(byte(character)) {
			return fmt.Errorf("%s contains an unsupported character", name)
		}
	}

	return nil
}

// isWireTokenCharacter keeps identifiers portable across logs, shells, and
// platform transports without assigning semantics to future token values.
func isWireTokenCharacter(character byte) bool {
	if character >= 'a' && character <= 'z' {
		return true
	}
	if character >= 'A' && character <= 'Z' {
		return true
	}
	if character >= '0' && character <= '9' {
		return true
	}

	switch character {
	case '.', '_', '-', ':':
		return true
	default:
		return false
	}
}
