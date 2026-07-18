// Package loopback observes and mutates exact IPv4 loopback /32 assignments.
//
// The package deliberately reports host facts without inferring Harbor
// ownership. Admission, durable ownership, and elevated-helper policy belong to
// higher layers that can bind these narrow effects to an authorized operation.
// StateAbsent proves only that the exact address is unassigned; route, listener,
// and durable-ownership conflicts remain mandatory higher-layer preconditions.
//
// Windows effects are active assignments with infinite address lifetimes, not
// persistent configuration. IP Helper removes them when the adapter is reset or
// the machine restarts, so a higher-level reconciler must restore desired state.
// StateExact requires Windows to report the assignment Preferred after
// duplicate-address detection. The identity HostProber separately proves
// end-to-end socket readiness before a durable lease is committed.
package loopback
