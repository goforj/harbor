// Package trust observes and conditionally mutates Harbor's exact local CA
// trust projection without accepting caller-selected certificate stores.
//
// Native backends normalize bounded operating-system facts into Observation.
// Classification and fingerprinting remain platform-neutral so an elevated
// mutation can be bound to the exact facts admitted by an unprivileged caller.
// Trust ownership is identified by a versioned installation marker; entries
// without the exact marker remain foreign even when they contain the same CA.
package trust
