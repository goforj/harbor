package helper

import "github.com/goforj/harbor/internal/identitytext"

// MaximumInstallationIDLength is the wire-safe byte limit shared by daemon identity planning and helper admission.
const MaximumInstallationIDLength = identitytext.MaximumInstallationIDLength

// MaximumRequesterIdentityLength is the byte limit shared by machine ownership and helper ticket admission.
const MaximumRequesterIdentityLength = identitytext.MaximumRequesterIdentityLength

// ValidateInstallationID rejects installation identities that cannot cross the privileged helper boundary canonically.
func ValidateInstallationID(value string) error {
	return identitytext.ValidateInstallationID(value)
}

// ValidateRequesterIdentity rejects identities that cannot cross the helper boundary canonically.
func ValidateRequesterIdentity(value string) error {
	return identitytext.ValidateRequesterIdentity(value)
}
