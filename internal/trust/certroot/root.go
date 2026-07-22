// Package certroot contains the public-only certificate authority shape shared across trust boundaries.
package certroot

import "time"

// Root is the public-only representation passed to platform trust installation.
type Root struct {
	// CertificatePEM contains one CA certificate and no private key.
	CertificatePEM []byte
	// Fingerprint is the lowercase SHA-256 digest of the CA certificate.
	Fingerprint string
	// NotBefore is the certificate's UTC activation time.
	NotBefore time.Time
	// NotAfter is the certificate's UTC expiration time.
	NotAfter time.Time
}
