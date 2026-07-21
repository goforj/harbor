//go:build darwin || linux

package local

import "github.com/goforj/harbor/internal/platform/runtimepath"

// endpointReference resolves the owner-scoped Unix socket used by Harbor's local transport.
func endpointReference() (string, error) {
	return runtimepath.SocketPath()
}
