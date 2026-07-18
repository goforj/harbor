// Package hostconflict classifies bounded host-network facts before Harbor
// claims an IPv4 loopback identity.
//
// The package is deliberately independent of operating-system observation and
// mutation. Native adapters populate the fact model; this package validates,
// classifies, and fingerprints those facts without opening sockets or trusting
// a copied classification.
package hostconflict
