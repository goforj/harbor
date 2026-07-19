// Package launchdsocket acquires Harbor's fixed macOS ingress sockets from launchd.
//
// The package intentionally exposes no caller-selected launchd socket name. The
// root-owned service definition is the only authority that can select the two
// descriptors returned to Harbor's unprivileged ingress relay.
package launchdsocket
