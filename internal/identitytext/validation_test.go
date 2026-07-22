package identitytext

import (
	"strings"
	"testing"
)

// TestValidateInstallationID covers every canonical installation identifier boundary.
func TestValidateInstallationID(t *testing.T) {
	valid := []string{
		"a",
		"A0._-z",
		strings.Repeat("a", MaximumInstallationIDLength),
	}
	for _, value := range valid {
		if err := ValidateInstallationID(value); err != nil {
			t.Fatalf("ValidateInstallationID(%q) error = %v", value, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", MaximumInstallationIDLength+1),
		".harbor",
		"harbor-",
		"harbor/local",
		"hárbor",
	}
	for _, value := range invalid {
		if err := ValidateInstallationID(value); err == nil {
			t.Fatalf("ValidateInstallationID(%q) error = nil", value)
		}
	}
}

// TestValidateRequesterIdentity covers the requester-specific length while retaining the shared alphabet.
func TestValidateRequesterIdentity(t *testing.T) {
	valid := []string{
		"501",
		"S-1-5-21",
		strings.Repeat("a", MaximumRequesterIdentityLength),
	}
	for _, value := range valid {
		if err := ValidateRequesterIdentity(value); err != nil {
			t.Fatalf("ValidateRequesterIdentity(%q) error = %v", value, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", MaximumRequesterIdentityLength+1),
		" 501",
		"501/502",
		"identity_",
	}
	for _, value := range invalid {
		if err := ValidateRequesterIdentity(value); err == nil {
			t.Fatalf("ValidateRequesterIdentity(%q) error = nil", value)
		}
	}
}
