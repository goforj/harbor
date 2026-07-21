//go:build windows

package local

import "github.com/goforj/harbor/internal/platform/runtimepath"

// endpointReference resolves the owner-scoped named pipe used by Harbor's local transport.
func endpointReference() (string, error) {
	return runtimepath.PipePath()
}
