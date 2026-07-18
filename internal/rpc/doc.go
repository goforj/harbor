// Package rpc defines Harbor's transport-neutral local IPC protocol.
//
// The package owns negotiation, envelopes, and bounded JSON framing. Platform
// packages provide the authenticated Unix-domain socket or Windows named-pipe
// transport separately.
package rpc
