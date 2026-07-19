// Package ingressrelay couples Harbor's fixed HTTP and HTTPS low-port relays.
//
// The operating-system adapter acquires both advertised listeners before this
// package receives them. Runtime then gates admission until both relays are
// running, so Harbor never exposes a one-sided ingress generation.
package ingressrelay
