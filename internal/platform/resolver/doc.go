// Package resolver observes and conditionally mutates Harbor's exact .test
// resolver route without accepting caller-selected host resources.
//
// Native backends normalize bounded operating-system facts into Observation.
// Classification and fingerprinting remain platform-neutral so an elevated
// mutation can be bound to the exact facts admitted by an unprivileged caller.
// Resolver ownership is identified by a versioned installation marker; facts
// without the exact marker remain foreign even when they route to the same DNS
// endpoint.
package resolver
